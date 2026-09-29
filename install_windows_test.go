//go:build windows

package main

import (
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"
)

// exitedProcessId 是一个已经结束的进程的 PID，删除用的批处理不用等它。
func exitedProcessId(t *testing.T) int {
	t.Helper()
	command := exec.Command(filepath.Join(systemDir(), "cmd.exe"), "/d", "/c", "exit 0")
	if err := command.Run(); err != nil {
		t.Fatal(err)
	}
	return command.ProcessState.Pid()
}

func createTestFile(t *testing.T, path string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte("test"), 0o644); err != nil {
		t.Fatal(err)
	}
}

func waitUntilGone(paths ...string) bool {
	deadline := time.Now().Add(30 * time.Second)
	for time.Now().Before(deadline) {
		remaining := false
		for _, path := range paths {
			if _, err := os.Stat(path); err == nil {
				remaining = true
			}
		}
		if !remaining {
			return true
		}
		time.Sleep(300 * time.Millisecond)
	}
	return false
}

func TestRemovalPlanDeletesProgramAndData(t *testing.T) {
	// 路径里有中文、空格、& 和括号：批处理按系统代码页解析，路径经环境变量传入才不会出错。
	root := filepath.Join(t.TempDir(), "卸载 测试 & (1)")
	installDir := filepath.Join(root, "Programs", "ProxySwitch")
	executable := filepath.Join(installDir, "ProxySwitch.exe")
	for _, name := range []string{"ProxySwitch.exe", "ProxySwitch.exe.old", "ProxySwitch.exe.1727000000.old", "ProxySwitch.exe.download"} {
		createTestFile(t, filepath.Join(installDir, name))
	}
	data := filepath.Join(root, "数据", "ProxySwitch")
	webData := filepath.Join(root, "浏览器数据", "ProxySwitch")
	createTestFile(t, filepath.Join(data, "core", "config.yaml"))
	createTestFile(t, filepath.Join(data, "config.jsonc"))
	createTestFile(t, filepath.Join(webData, "WebView", "Local State"))
	scripts, _ := filepath.Glob(filepath.Join(os.TempDir(), "proxyswitch-uninstall-*.cmd"))

	plan := removalPlan{executable: executable, data: []string{data, webData}, waitFor: exitedProcessId(t)}
	if err := plan.start(); err != nil {
		t.Fatal(err)
	}
	if !waitUntilGone(installDir, data, webData) {
		entries, _ := os.ReadDir(installDir)
		t.Fatalf("批处理没有删干净，安装目录里还有 %d 个文件", len(entries))
	}
	// 批处理最后删掉自己。
	deadline := time.Now().Add(10 * time.Second)
	for {
		after, _ := filepath.Glob(filepath.Join(os.TempDir(), "proxyswitch-uninstall-*.cmd"))
		if len(after) <= len(scripts) {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("批处理没有删掉自己：%v", after)
		}
		time.Sleep(300 * time.Millisecond)
	}
}

func TestRemovalPlanKeepsOtherFiles(t *testing.T) {
	installDir := filepath.Join(t.TempDir(), "ProxySwitch")
	executable := filepath.Join(installDir, "ProxySwitch.exe")
	createTestFile(t, executable)
	other := filepath.Join(installDir, "说明.txt")
	createTestFile(t, other)

	plan := removalPlan{executable: executable, waitFor: exitedProcessId(t)}
	if err := plan.start(); err != nil {
		t.Fatal(err)
	}
	if !waitUntilGone(executable) {
		t.Fatal("程序文件没有删掉")
	}
	time.Sleep(time.Second)
	if _, err := os.Stat(other); err != nil {
		t.Fatal("安装目录里别的文件不应该删")
	}
}

func TestInstalledCopyAndLinks(t *testing.T) {
	requireRegistryTests(t)
	if readRegistryString(hkeyCurrentUser, uninstallKey, "InstallLocation") != "" {
		t.Skip("这台电脑装了 ProxySwitch，不动它的卸载项")
	}
	installDir := filepath.Join(t.TempDir(), "Programs", "ProxySwitch")
	key, err := createRegistryKey(hkeyCurrentUser, uninstallKey)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = deleteRegistryTree(hkeyCurrentUser, uninstallKey) })
	if err := key.SetString("InstallLocation", installDir+`\`); err != nil {
		t.Fatal(err)
	}
	key.Close()
	if !isInstalledCopy(filepath.Join(installDir, "ProxySwitch.exe")) {
		t.Error("安装目录里的程序应该是安装版")
	}
	if isInstalledCopy(filepath.Join(t.TempDir(), "ProxySwitch.exe")) {
		t.Error("别处的程序不是安装版")
	}

	// 链接登记的是原来位置的 ProxySwitch-arm64.exe：也要取消；登记给别的程序的不动。
	ours, others := "proxyswitch-uninstall-test", "proxyswitch-uninstall-test-other"
	t.Cleanup(func() {
		_ = deleteRegistryTree(hkeyCurrentUser, classesKey+ours)
		_ = deleteRegistryTree(hkeyCurrentUser, classesKey+others)
	})
	if err := registerLink(ours, "ProxySwitch", `D:\下载\ProxySwitch-arm64.exe`); err != nil {
		t.Fatal(err)
	}
	if err := registerLink(others, "clash", `C:\Program Files\Clash Verge\clash-verge.exe`); err != nil {
		t.Fatal(err)
	}
	if err := unregisterProxySwitchLink(ours); err != nil {
		t.Fatal(err)
	}
	if err := unregisterProxySwitchLink(others); err != nil {
		t.Fatal(err)
	}
	if readRegistryString(hkeyCurrentUser, classesKey+ours+`\shell\open\command`, "") != "" {
		t.Error("ProxySwitch 的链接登记应该取消")
	}
	if readRegistryString(hkeyCurrentUser, classesKey+others+`\shell\open\command`, "") == "" {
		t.Error("别的程序的链接登记不应该动")
	}
}
