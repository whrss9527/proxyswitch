package main

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// elevatedTestProcess 假装是以管理员身份运行的内核：测试里直接启动。
type elevatedTestProcess struct {
	*localCoreProcess
}

func (elevatedTestProcess) Elevated() bool {
	return true
}

// useTestTunDevice 让内核建不出虚拟网卡（名字太长），只记一条日志：测试时不能真的改这台电脑的路由。
func useTestTunDevice(t *testing.T) {
	original := coreTunDevice
	coreTunDevice = "ProxySwitchTestNameTooLong"
	t.Cleanup(func() { coreTunDevice = original })
}

func TestCoreConfigTun(t *testing.T) {
	settings := CoreSettings{Port: 17890, Active: "p1", Mode: "rule", Subscriptions: []CoreSubscription{{Id: "p1"}}}
	parse := func(text []byte) map[string]any {
		t.Helper()
		var config map[string]any
		if err := json.Unmarshal(text, &config); err != nil {
			t.Fatal(err)
		}
		return config
	}
	if config := parse(coreConfigText(settings, "127.0.0.1:9090", "secret")); config["tun"] != nil || config["dns"] != nil {
		t.Error("没开 TUN 时不应有 tun 和 dns")
	}
	settings.Tun = true
	text := coreConfigText(settings, "127.0.0.1:9090", "secret")
	config := parse(text)
	tun, _ := config["tun"].(map[string]any)
	dns, _ := config["dns"].(map[string]any)
	if tun["enable"] != true || tun["auto-route"] != true || tun["stack"] != "gvisor" || tun["device"] != "ProxySwitch" {
		t.Errorf("TUN 设置不对：%v", tun)
	}
	if dns["enable"] != true || dns["enhanced-mode"] != "fake-ip" || !strings.Contains(string(text), "msftconnecttest.com") {
		t.Errorf("DNS 设置不对：%v", dns)
	}

	// 内核认得这份配置（-t 只检查，不建虚拟网卡）。
	binary := os.Getenv("PROXYSWITCH_CORE")
	if binary == "" {
		t.Skip("设置 PROXYSWITCH_CORE 为 mihomo 程序的路径才用内核检查配置")
	}
	dir := t.TempDir()
	if err := writeSubscriptionFile(dir, "p1", []byte("proxies:\n  - {name: a, type: http, server: 127.0.0.1, port: 1}\n")); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, coreConfigName)
	if err := os.WriteFile(path, text, 0o600); err != nil {
		t.Fatal(err)
	}
	if output, err := exec.Command(binary, "-t", "-d", dir, "-f", path).CombinedOutput(); err != nil || !strings.Contains(string(output), "successful") {
		t.Errorf("内核不认这份 TUN 配置：%v\n%s", err, output)
	}
}

// TUN 模式要以管理员身份运行内核：没同意时内核照常运行但不开 TUN，同样的设定不再问，TUN 关掉再打开时再问；
// 已经以管理员身份运行时，开关 TUN 只重新加载配置。测试里换掉以管理员身份启动的方式。
func TestCoreTunElevation(t *testing.T) {
	binary := requireCoreBinary(t)
	useTestTunDevice(t)
	website := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		_, _ = io.WriteString(writer, "hello")
	}))
	defer website.Close()
	node := startCountingProxy(t, strings.TrimPrefix(website.URL, "http://"))
	dir := t.TempDir()
	if err := writeSubscriptionFile(dir, "pa", []byte(nodesYaml(map[string]*countingProxy{"节点": node}, "节点"))); err != nil {
		t.Fatal(err)
	}
	originalSupported, originalStart := coreTunSupported, startElevatedCore
	t.Cleanup(func() { coreTunSupported, startElevatedCore = originalSupported, originalStart })
	coreTunSupported = true
	asked, decline := 0, true
	startElevatedCore = func(binary, dir, configPath, logPath string) (coreProcess, error) {
		asked++
		if decline {
			return nil, errTunDeclined
		}
		logFile, err := os.OpenFile(logPath, os.O_WRONLY|os.O_APPEND|os.O_CREATE, 0o644)
		if err != nil {
			return nil, err
		}
		command := exec.Command(binary, "-d", dir, "-f", configPath)
		command.Stdout, command.Stderr = logFile, logFile
		prepareCoreCommand(command)
		if err := command.Start(); err != nil {
			logFile.Close()
			return nil, err
		}
		return elevatedTestProcess{&localCoreProcess{command: command, logFile: logFile}}, nil
	}
	core := newCore(nil)
	defer core.Stop()
	port, _ := freeLocalPort()
	settings := CoreSettings{
		Binary: binary, Dir: dir, Port: port, TestUrl: "http://" + coreTestHost + "/", Active: "pa", Mode: "rule",
		Subscriptions: []CoreSubscription{{Id: "pa", Revision: "1"}}, Tun: true,
	}
	apply := func(tun bool) CoreStatus {
		t.Helper()
		settings.Tun = tun
		if err := core.Wait(core.Sync(settings), 30*time.Second); err != nil {
			t.Fatalf("内核没能启动：%v", err)
		}
		return core.Status()
	}
	configHasTun := func() bool {
		data, _ := os.ReadFile(filepath.Join(dir, coreConfigName))
		return strings.Contains(string(data), `"tun"`)
	}

	// 没同意：内核照常运行，TUN 没开，说明原因。
	if status := apply(true); !status.Running || status.Tun.Active || status.Tun.Error != errTunDeclined.Error() || asked != 1 || configHasTun() {
		t.Fatalf("没同意时应照常运行、不开 TUN：%+v 问了 %d 次", status, asked)
	}
	if body := getThroughCore(t, port); body != "hello" {
		t.Errorf("不开 TUN 时照常经节点访问：%q", body)
	}
	if apply(true); asked != 1 {
		t.Error("同样的设定不应再问")
	}
	if status := apply(false); status.Tun.Error != "" || asked != 1 {
		t.Errorf("关掉 TUN 后不再有错误：%+v", status)
	}

	// 再打开时再问，这次同意：内核以管理员身份重新启动，配置里有 TUN。
	decline = false
	status := apply(true)
	if !status.Running || !status.Tun.Active || status.Tun.Error != "" || asked != 2 || !configHasTun() || !core.runningElevated() {
		t.Fatalf("同意后应以管理员身份运行并开启 TUN：%+v 问了 %d 次", status, asked)
	}
	pid := corePid(core)

	// 已经以管理员身份运行：关掉和打开 TUN 只重新加载配置，不重启，也不再问。
	if status := apply(false); status.Tun.Active || configHasTun() || corePid(core) != pid {
		t.Errorf("关掉 TUN 只应重新加载：%+v", status)
	}
	if status := apply(true); !status.Tun.Active || !configHasTun() || corePid(core) != pid || asked != 2 {
		t.Errorf("再打开 TUN 不用重启也不用再问：%+v 问了 %d 次", status, asked)
	}
	if body := getThroughCore(t, port); body != "hello" {
		t.Errorf("TUN 模式下本机代理端口照常可用：%q", body)
	}

	// 其他平台开不了 TUN。
	coreTunSupported = false
	if status := apply(true); status.Tun.Active || !strings.Contains(status.Tun.Error, "Windows") {
		t.Errorf("不支持时应说明：%+v", status)
	}
}
