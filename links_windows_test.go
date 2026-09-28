//go:build windows

package main

import (
	"fmt"
	"os"
	"testing"
)

func TestCommandProgram(t *testing.T) {
	for command, want := range map[string]string{
		`"C:\Program Files\Clash Verge\clash-verge.exe" "%1"`: `C:\Program Files\Clash Verge\clash-verge.exe`,
		`C:\Tools\clash.exe %1`:                               `C:\Tools\clash.exe`,
		`"C:\Tools\ProxySwitch.exe",0`:                        `C:\Tools\ProxySwitch.exe`,
		``:                                                    ``,
	} {
		if got := commandProgram(command); got != want {
			t.Errorf("%s 的程序应是 %q：%q", command, want, got)
		}
	}
	if !isOurLinkCommand(`"c:\tools\proxyswitch.EXE" "%1"`, `C:\Tools\ProxySwitch.exe`) || isOurLinkCommand(`"C:\Tools\clash.exe" "%1"`, `C:\Tools\ProxySwitch.exe`) {
		t.Error("应按程序路径（不分大小写）判断是不是这个程序")
	}
	if handlerName(`"C:\Program Files\Clash Verge\clash-verge.exe" "%1"`) != "clash-verge" {
		t.Error("应取出程序的名字")
	}
}

// 在当前用户下登记一个测试用的链接协议：登记、接管别的程序、交还。
func TestLinkRegistration(t *testing.T) {
	requireRegistryTests(t)
	scheme := fmt.Sprintf("proxyswitch-test-%d", os.Getpid())
	root := classesKey + scheme
	t.Cleanup(func() { _ = deleteRegistryTree(hkeyCurrentUser, root) })
	ours := `C:\Tools\ProxySwitch.exe`
	other := `"C:\Program Files\Clash Verge\clash-verge.exe" "%1"`
	otherIcon := `"C:\Program Files\Clash Verge\clash-verge.exe",0`

	// 没有程序登记过：登记后指向这个程序，取消后整个键删掉。
	if err := registerLink(scheme, "测试", ours); err != nil {
		t.Fatal(err)
	}
	if command, isOurs := linkHandler(scheme, ours); !isOurs || command != `"C:\Tools\ProxySwitch.exe" "%1"` {
		t.Errorf("登记后应由这个程序处理：%q", command)
	}
	key, err := openRegistryKey(hkeyCurrentUser, root, keyRead)
	if err != nil {
		t.Fatal(err)
	}
	description, _ := key.String("")
	_, protocolErr := key.String("URL Protocol")
	key.Close()
	if description != "URL:测试" || protocolErr != nil {
		t.Errorf("应写上协议的说明和 URL Protocol：%q %v", description, protocolErr)
	}
	if err := unregisterLink(scheme, ours); err != nil {
		t.Fatal(err)
	}
	if key, err := openRegistryKey(hkeyCurrentUser, root, keyRead); err == nil {
		key.Close()
		t.Error("取消登记后应删除整个键")
	}

	// 别的程序登记过：接管时备份它的命令和图标，交还时恢复。
	for path, value := range map[string]string{root + `\shell\open\command`: other, root + `\DefaultIcon`: otherIcon} {
		key, err := createRegistryKey(hkeyCurrentUser, path)
		if err != nil {
			t.Fatal(err)
		}
		_ = key.SetString("", value)
		key.Close()
	}
	if command, isOurs := linkHandler(scheme, ours); isOurs || command != other {
		t.Fatalf("应读到别的程序的登记：%q", command)
	}
	for range 2 {
		// 登记两次，第二次不能把备份换成自己的命令。
		if err := registerLink(scheme, "测试", ours); err != nil {
			t.Fatal(err)
		}
	}
	if _, isOurs := linkHandler(scheme, ours); !isOurs {
		t.Error("接管后应由这个程序处理")
	}
	if backup := readRegistryString(hkeyCurrentUser, root+`\shell\open\command`, linkBackupValue); backup != other {
		t.Errorf("应备份原来的命令：%q", backup)
	}
	if err := unregisterLink(scheme, ours); err != nil {
		t.Fatal(err)
	}
	command := readRegistryString(hkeyCurrentUser, root+`\shell\open\command`, "")
	icon := readRegistryString(hkeyCurrentUser, root+`\DefaultIcon`, "")
	backup := readRegistryString(hkeyCurrentUser, root+`\shell\open\command`, linkBackupValue)
	if command != other || icon != otherIcon || backup != "" {
		t.Errorf("交还后应恢复原来的程序并删掉备份：%q %q %q", command, icon, backup)
	}

	// 现在由别的程序处理：取消登记不动它。
	if err := unregisterLink(scheme, ours); err != nil || readRegistryString(hkeyCurrentUser, root+`\shell\open\command`, "") != other {
		t.Errorf("不是这个程序的登记不应改动：%v", err)
	}
}
