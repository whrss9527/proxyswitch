//go:build windows

package main

import (
	"os"
	"strings"
)

// 开机自启：写 HKCU\...\Run，不需要管理员权限。

const (
	runKey       = `Software\Microsoft\Windows\CurrentVersion\Run`
	runValueName = "ProxySwitch"
)

func autostartCommand() (string, error) {
	executable, err := os.Executable()
	if err != nil {
		return "", err
	}
	return `"` + executable + `" --autostart`, nil
}

func isAutostartEnabled() bool {
	return strings.TrimSpace(readRegistryString(hkeyCurrentUser, runKey, runValueName)) != ""
}

// refreshAutostartPath 在已开启自启、但记录的不是当前 exe 路径时（exe 被挪动或更新到别处）改成当前路径。
func refreshAutostartPath() {
	current := strings.TrimSpace(readRegistryString(hkeyCurrentUser, runKey, runValueName))
	command, err := autostartCommand()
	if current == "" || err != nil || strings.EqualFold(current, command) {
		return
	}
	_ = setAutostart(true)
}

func setAutostart(enabled bool) error {
	key, err := createRegistryKey(hkeyCurrentUser, runKey)
	if err != nil {
		return err
	}
	defer key.Close()
	if !enabled {
		return key.DeleteValue(runValueName)
	}
	command, err := autostartCommand()
	if err != nil {
		return err
	}
	return key.SetString(runValueName, command)
}
