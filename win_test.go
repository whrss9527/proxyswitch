//go:build windows

package main

import (
	"os"
	"testing"
	"unsafe"
)

// 这些测试在 Windows（或 Wine）上运行：校验结构体布局，并对注册表读写做一次往返。

func TestStructSizes(t *testing.T) {
	if s := unsafe.Sizeof(wndClassExW{}); s != 80 {
		t.Errorf("WNDCLASSEXW 大小 %d，期望 80", s)
	}
	if s := unsafe.Sizeof(msgW{}); s != 48 {
		t.Errorf("MSG 大小 %d，期望 48", s)
	}
	if s := unsafe.Sizeof(notifyIconDataW{}); s != 976 {
		t.Errorf("NOTIFYICONDATAW 大小 %d，期望 976", s)
	}
	if s := unsafe.Sizeof(point{}); s != 8 {
		t.Errorf("POINT 大小 %d，期望 8", s)
	}
	var nid notifyIconDataW
	if off := unsafe.Offsetof(nid.hIcon); off != 32 {
		t.Errorf("hIcon 偏移 %d，期望 32", off)
	}
	if off := unsafe.Offsetof(nid.szTip); off != 40 {
		t.Errorf("szTip 偏移 %d，期望 40", off)
	}
	if off := unsafe.Offsetof(nid.szInfo); off != 304 {
		t.Errorf("szInfo 偏移 %d，期望 304", off)
	}
	if off := unsafe.Offsetof(nid.szInfoTitle); off != 820 {
		t.Errorf("szInfoTitle 偏移 %d，期望 820", off)
	}
	if off := unsafe.Offsetof(nid.hBalloonIcon); off != 968 {
		t.Errorf("hBalloonIcon 偏移 %d，期望 968", off)
	}
}

func TestSystemProxyRoundTrip(t *testing.T) {
	if os.Getenv("PROXYSWITCH_TEST_REGISTRY") == "" {
		t.Skip("设置 PROXYSWITCH_TEST_REGISTRY=1 才会真正改注册表")
	}
	orig, err := readSystemProxy()
	if err != nil {
		t.Fatalf("读取系统代理失败: %v", err)
	}
	t.Logf("原始状态: %+v", orig)
	defer func() {
		// 尽量恢复
		if orig.Active() {
			_ = setSystemProxy(orig.Server, orig.Bypass, orig.PAC)
		} else {
			_ = disableSystemProxy()
		}
	}()

	if err := setSystemProxy("127.0.0.1:7890", "localhost;<local>", ""); err != nil {
		t.Fatalf("setSystemProxy: %v", err)
	}
	st, _ := readSystemProxy()
	if !st.Enabled || st.Server != "127.0.0.1:7890" || st.Bypass != "localhost;<local>" || st.PAC != "" {
		t.Fatalf("手动代理写入后读回不对: %+v", st)
	}

	if err := setSystemProxy("", "", "http://pac.example.com/p.pac"); err != nil {
		t.Fatalf("setSystemProxy(pac): %v", err)
	}
	st, _ = readSystemProxy()
	if st.Enabled || st.PAC != "http://pac.example.com/p.pac" || !st.Active() {
		t.Fatalf("PAC 写入后读回不对: %+v", st)
	}

	if err := setSystemProxy("1.2.3.4:8080", "<local>", "http://pac.example.com/p.pac"); err != nil {
		t.Fatalf("setSystemProxy(both): %v", err)
	}
	st, _ = readSystemProxy()
	if !st.Enabled || st.PAC == "" || st.Server != "1.2.3.4:8080" {
		t.Fatalf("PAC+手动 写入后读回不对: %+v", st)
	}

	if err := disableSystemProxy(); err != nil {
		t.Fatalf("disableSystemProxy: %v", err)
	}
	st, _ = readSystemProxy()
	if st.Active() || st.PAC != "" {
		t.Fatalf("关闭后仍然是开启状态: %+v", st)
	}
	if st.Server != "1.2.3.4:8080" {
		t.Fatalf("关闭后应保留 ProxyServer 供系统设置回显: %+v", st)
	}
}

func TestUserEnvProxyRoundTrip(t *testing.T) {
	if os.Getenv("PROXYSWITCH_TEST_REGISTRY") == "" {
		t.Skip("设置 PROXYSWITCH_TEST_REGISTRY=1 才会真正改注册表")
	}
	if err := setUserEnvProxy("http://127.0.0.1:7890", "localhost,127.0.0.1"); err != nil {
		t.Fatalf("setUserEnvProxy: %v", err)
	}
	if got := readUserEnvProxy(); got != "http://127.0.0.1:7890" {
		t.Fatalf("读回 %q", got)
	}
	k, err := regOpen(hkeyCurrentUser, envRegKey, keyRead)
	if err != nil {
		t.Fatal(err)
	}
	np, err := k.getString("NO_PROXY")
	k.close()
	if err != nil || np != "localhost,127.0.0.1" {
		t.Fatalf("NO_PROXY = %q, err=%v", np, err)
	}
	if err := clearUserEnvProxy(); err != nil {
		t.Fatalf("clearUserEnvProxy: %v", err)
	}
	if got := readUserEnvProxy(); got != "" {
		t.Fatalf("清理后仍有 %q", got)
	}
	// 再清一次不应报错（值已不存在）
	if err := clearUserEnvProxy(); err != nil {
		t.Fatalf("重复清理报错: %v", err)
	}
}

func TestAutostartRoundTrip(t *testing.T) {
	if os.Getenv("PROXYSWITCH_TEST_REGISTRY") == "" {
		t.Skip("设置 PROXYSWITCH_TEST_REGISTRY=1 才会真正改注册表")
	}
	was := isAutostartEnabled()
	defer func() { _ = setAutostart(was) }()
	if err := setAutostart(true); err != nil {
		t.Fatalf("setAutostart(true): %v", err)
	}
	if !isAutostartEnabled() {
		t.Fatal("开启后 isAutostartEnabled 仍为 false")
	}
	if err := setAutostart(false); err != nil {
		t.Fatalf("setAutostart(false): %v", err)
	}
	if isAutostartEnabled() {
		t.Fatal("关闭后 isAutostartEnabled 仍为 true")
	}
}

func TestIconFromICOBytes(t *testing.T) {
	for _, ico := range [][]byte{iconOnICO, iconOffICO} {
		h, err := iconFromICOBytes(ico, 16, 16)
		if err != nil {
			t.Fatalf("iconFromICOBytes: %v", err)
		}
		if h == 0 {
			t.Fatal("HICON 为 0")
		}
		procDestroyIcon.Call(h)
		h, err = iconFromICOBytes(ico, 32, 32)
		if err != nil || h == 0 {
			t.Fatalf("iconFromICOBytes(32): %v", err)
		}
		procDestroyIcon.Call(h)
	}
	if _, err := iconFromICOBytes([]byte{1, 2, 3}, 16, 16); err == nil {
		t.Fatal("损坏的 ico 应当报错")
	}
}

func TestRegistryStringHelpers(t *testing.T) {
	if os.Getenv("PROXYSWITCH_TEST_REGISTRY") == "" {
		t.Skip("设置 PROXYSWITCH_TEST_REGISTRY=1 才会真正改注册表")
	}
	k, err := regCreate(hkeyCurrentUser, `Software\ProxySwitchTest`)
	if err != nil {
		t.Fatal(err)
	}
	defer k.close()
	if err := k.setString("s", "中文 值"); err != nil {
		t.Fatal(err)
	}
	if v, err := k.getString("s"); err != nil || v != "中文 值" {
		t.Fatalf("getString = %q, %v", v, err)
	}
	if err := k.setDWORD("d", 0x12345678); err != nil {
		t.Fatal(err)
	}
	if v, err := k.getDWORD("d"); err != nil || v != 0x12345678 {
		t.Fatalf("getDWORD = %#x, %v", v, err)
	}
	if _, err := k.getString("nope"); err != errNotFound {
		t.Fatalf("不存在的值应返回 errNotFound，得到 %v", err)
	}
	if err := k.deleteValue("s"); err != nil {
		t.Fatal(err)
	}
	if err := k.deleteValue("s"); err != nil {
		t.Fatalf("重复删除应无错误: %v", err)
	}
	_ = k.deleteValue("d")
}
