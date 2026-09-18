//go:build windows

package main

import (
	"os"
	"strings"
)

// 开机自启：写 HKCU\Software\Microsoft\Windows\CurrentVersion\Run，不需要管理员权限。

const (
	runRegKey   = `Software\Microsoft\Windows\CurrentVersion\Run`
	runRegValue = "ProxySwitch"
)

func autostartCommand() (string, error) {
	exe, err := os.Executable()
	if err != nil {
		return "", err
	}
	return `"` + exe + `"`, nil
}

func isAutostartEnabled() bool {
	k, err := regOpen(hkeyCurrentUser, runRegKey, keyRead)
	if err != nil {
		return false
	}
	defer k.close()
	v, err := k.getString(runRegValue)
	if err != nil {
		return false
	}
	// 只要有值就算已开启；路径与当前 exe 不一致（比如 exe 被挪走）时，
	// 启动时会由 refreshAutostartPath 自动改成新路径。
	return strings.TrimSpace(v) != ""
}

// refreshAutostartPath 在自启已开启但记录的路径不是当前 exe 时，更新为当前路径。
func refreshAutostartPath() {
	k, err := regOpen(hkeyCurrentUser, runRegKey, keyRead|keySetValue)
	if err != nil {
		return
	}
	defer k.close()
	v, err := k.getString(runRegValue)
	if err != nil || strings.TrimSpace(v) == "" {
		return
	}
	cmd, err := autostartCommand()
	if err != nil || strings.EqualFold(strings.TrimSpace(v), cmd) {
		return
	}
	_ = k.setString(runRegValue, cmd)
}

func setAutostart(enable bool) error {
	k, err := regCreate(hkeyCurrentUser, runRegKey)
	if err != nil {
		return err
	}
	defer k.close()
	if !enable {
		return k.deleteValue(runRegValue)
	}
	cmd, err := autostartCommand()
	if err != nil {
		return err
	}
	return k.setString(runRegValue, cmd)
}
