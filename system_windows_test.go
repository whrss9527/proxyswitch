//go:build windows

package main

import (
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"unsafe"
)

// 修改真实系统设置的测试只在设置 PROXYSWITCH_TEST_REGISTRY=1 时运行（CI 和测试虚拟机里），结束后恢复原值。
func requireRegistryTests(t *testing.T) {
	if os.Getenv("PROXYSWITCH_TEST_REGISTRY") != "1" {
		t.Skip("设置 PROXYSWITCH_TEST_REGISTRY=1 才运行会修改系统设置的测试")
	}
}

func TestStructSizes(t *testing.T) {
	sizes := []struct {
		name   string
		actual uintptr
		wanted uintptr
	}{
		{"NOTIFYICONDATAW", unsafe.Sizeof(notifyIconDataW{}), 976},
		{"WNDCLASSEXW", unsafe.Sizeof(wndClassExW{}), 80},
		{"INTERNET_PER_CONN_OPTIONW", unsafe.Sizeof(perConnectionOption{}), 16},
		{"INTERNET_PER_CONN_OPTION_LISTW", unsafe.Sizeof(perConnectionOptionList{}), 32},
		{"MENUITEMINFOW", unsafe.Sizeof(menuItemInfoW{}), 80},
		{"COPYDATASTRUCT", unsafe.Sizeof(copyDataStruct{}), 24},
		{"ICONINFO", unsafe.Sizeof(iconInfo{}), 32},
		{"BITMAPINFOHEADER", unsafe.Sizeof(bitmapInfoHeader{}), 40},
		{"MSG", unsafe.Sizeof(message{}), 48},
	}
	for _, size := range sizes {
		if size.actual != size.wanted {
			t.Errorf("%s 大小为 %d，应为 %d", size.name, size.actual, size.wanted)
		}
	}
}

func TestSystemProxyRoundTrip(t *testing.T) {
	requireRegistryTests(t)
	original, source, err := readSystemProxy()
	if err != nil {
		t.Fatal(err)
	}
	t.Logf("读取方式：%s，原值：%+v", source, original)
	defer func() {
		if err := writeSystemProxy(original); err != nil {
			t.Errorf("恢复原设置失败：%v", err)
		}
	}()

	manual := SystemProxyState{ProxyEnabled: true, Server: "127.0.0.1:7890", Bypass: "localhost;<local>"}
	if err := writeSystemProxy(manual); err != nil {
		t.Fatal(err)
	}
	if state, _, err := readSystemProxy(); err != nil || state != manual {
		t.Errorf("写入手动代理后读回不一致：%+v %v", state, err)
	}
	if value, _ := readRegistryDword(hkeyCurrentUser, internetSettingsKey, "ProxyEnable"); value != 1 {
		t.Errorf("注册表 ProxyEnable 应为 1，得到 %d", value)
	}

	pac := SystemProxyState{PacEnabled: true, Pac: "http://127.0.0.1:9/proxy.pac", Server: "127.0.0.1:7890", Bypass: "<local>", AutoDetect: true}
	if err := writeSystemProxy(pac); err != nil {
		t.Fatal(err)
	}
	if state, _, _ := readSystemProxy(); !state.PacEnabled || state.Pac != pac.Pac || state.ProxyEnabled {
		t.Errorf("写入 PAC 后读回不对：%+v", state)
	}

	direct := SystemProxyState{Server: "127.0.0.1:7890", Bypass: "<local>"}
	if err := writeSystemProxy(direct); err != nil {
		t.Fatal(err)
	}
	if state, _, _ := readSystemProxy(); state.Active() || state.Server != "127.0.0.1:7890" {
		t.Errorf("直连后应关闭代理并保留地址：%+v", state)
	}
}

func TestEnvironmentProxyRoundTrip(t *testing.T) {
	requireRegistryTests(t)
	original := readEnvironmentProxy()
	defer func() {
		if original["HTTP_PROXY"] == "" {
			_ = clearEnvironmentProxy()
			return
		}
		_ = setEnvironmentProxy(original["HTTP_PROXY"], original["NO_PROXY"])
	}()
	if err := setEnvironmentProxy("http://127.0.0.1:7890", "localhost"); err != nil {
		t.Fatal(err)
	}
	values := readEnvironmentProxy()
	if values["HTTPS_PROXY"] != "http://127.0.0.1:7890" || values["NO_PROXY"] != "localhost" {
		t.Errorf("环境变量读回不对：%v", values)
	}
	if err := clearEnvironmentProxy(); err != nil {
		t.Fatal(err)
	}
	if values := readEnvironmentProxy(); values["HTTP_PROXY"] != "" || values["NO_PROXY"] != "" {
		t.Errorf("清除后应为空：%v", values)
	}
}

func TestEngineWithWindowsSystem(t *testing.T) {
	requireRegistryTests(t)
	original, _, _ := readSystemProxy()
	defer func() { _ = writeSystemProxy(original) }()
	paths := pathsIn(t.TempDir(), false)
	config, _ := parseConfig(`{"profiles": [{"name": "本机", "server": "127.0.0.1:7890"}, {"name": "PAC", "pac": "http://127.0.0.1:9/p.pac"}]}`)
	if err := writeConfigFile(paths.Config, config); err != nil {
		t.Fatal(err)
	}
	var notices []Notice
	engine := newEngine(windowsSystem{}, paths, func(notice Notice) { notices = append(notices, notice) })
	if _, err := engine.LoadConfig(); err != nil {
		t.Fatal(err)
	}
	if err := engine.UseProfile("本机"); err != nil {
		t.Fatal(err)
	}
	if status := engine.Status(); status.State != statusOn || status.Profile.Name != "本机" {
		t.Errorf("开启后状态不对：%+v", status)
	}
	if err := engine.UseProfile("PAC"); err != nil {
		t.Fatal(err)
	}
	if status := engine.Status(); status.State != statusOn || status.Profile.Name != "PAC" {
		t.Errorf("切到 PAC 后状态不对：%+v", status)
	}
	if err := engine.TurnOff(); err != nil {
		t.Fatal(err)
	}
	if status := engine.Status(); status.State != statusOff {
		t.Errorf("关闭后状态不对：%+v", status)
	}
}

func TestListTcpListeners(t *testing.T) {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	port := listener.Addr().(*net.TCPAddr).Port
	executable, _ := os.Executable()
	for _, item := range listTcpListeners() {
		if item.Port == port {
			if item.Pid == 0 {
				t.Log("系统没有提供监听端口的进程号（兼容层的限制）")
				return
			}
			if item.Pid != uint32(os.Getpid()) || !strings.EqualFold(item.Process, filepath.Base(executable)) {
				t.Errorf("进程信息不对：%+v", item)
			}
			return
		}
	}
	t.Errorf("监听列表里没有端口 %d", port)
}

func TestReadNetworkInfo(t *testing.T) {
	info := readNetworkInfo()
	t.Logf("网络：%s，特征：%s", info.Describe(), info.Signature())
	for _, adapter := range info.Adapters {
		if adapter.Name == "" || adapter.Gateway == "" {
			t.Errorf("网卡信息不完整：%+v", adapter)
		}
	}
}

func TestCreateIcons(t *testing.T) {
	for _, size := range []int{16, 24, 32} {
		icon, err := createIcon(renderToggleIcon(size, trayIconStyle(iconStateOn, "#2563eb")))
		if err != nil || icon == 0 {
			t.Fatalf("%dpx 图标创建失败：%v", size, err)
		}
		procDestroyIcon.Call(icon)
	}
	bitmap, err := createMenuBitmap(renderDot(16, iconRed))
	if err != nil || bitmap == 0 {
		t.Fatalf("菜单位图创建失败：%v", err)
	}
	procDeleteObject.Call(bitmap)
}

func TestSystemHelpers(t *testing.T) {
	if build := windowsBuild(); build == 0 {
		t.Error("读不到 Windows 版本号")
	}
	if accent := systemAccentColor(); !hexColorPattern.MatchString(accent) {
		t.Errorf("强调色格式不对：%q", accent)
	}
	_ = rasEntryNames()
	_ = machineWideProxy()
	_ = findAppBrowser()
}
