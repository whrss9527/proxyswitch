//go:build windows

package main

import (
	"errors"
	"fmt"
	"io/fs"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
)

// WSL 在 Windows 上的部分：从注册表读出装了哪些发行版，读写用户目录下的 .wslconfig，wsl --shutdown 重启 WSL。

const lxssKey = `Software\Microsoft\Windows\CurrentVersion\Lxss`

func wslConfigPath() string {
	home, err := os.UserHomeDir()
	if err != nil {
		return ""
	}
	return filepath.Join(home, ".wslconfig")
}

// wslDistros 列出当前用户装的 WSL 发行版，不算 Docker Desktop 自己用的发行版。
func wslDistros() []string {
	key, err := openRegistryKey(hkeyCurrentUser, lxssKey, keyRead)
	if err != nil {
		return nil
	}
	defer key.Close()
	var names []string
	for _, id := range key.SubkeyNames() {
		name := readRegistryString(hkeyCurrentUser, lxssKey+`\`+id, "DistributionName")
		if name != "" && !strings.HasPrefix(strings.ToLower(name), "docker-desktop") {
			names = append(names, name)
		}
	}
	return names
}

func readWslInfo() WslInfo {
	info := WslInfo{Distros: wslDistros(), Supported: windowsBuild() >= wslMirroredMinBuild, Config: wslConfigPath()}
	info.Installed = len(info.Distros) > 0 && fileExists(filepath.Join(systemDir(), "wsl.exe"))
	if info.Config != "" {
		data, _ := os.ReadFile(info.Config)
		info.Mirrored, info.AutoProxy = wslConfigValues(string(data))
	}
	return info
}

// WslInfo 是 WSL 的情况，给系统集成页显示。
func (app *App) WslInfo() WslInfo {
	info := readWslInfo()
	info.Restart = app.wslRestart.Load()
	return info
}

// SetupWsl 把 WSL 设置成使用本机的代理（镜像网络 + 自动代理），重启 WSL 后生效。
func (app *App) SetupWsl() (WslInfo, error) {
	if windowsBuild() < wslMirroredMinBuild {
		return app.WslInfo(), errors.New("这个 Windows 版本的 WSL 不支持镜像网络，需要 Windows 11 22H2 或更新的版本")
	}
	return app.editWslConfig(setWslProxy, "已设置 WSL 使用本机的代理")
}

// ResetWsl 撤销 SetupWsl 的设置，恢复 WSL 默认的 NAT 网络，重启 WSL 后生效。
func (app *App) ResetWsl() (WslInfo, error) {
	return app.editWslConfig(resetWslProxy, "已恢复 WSL 默认的网络设置")
}

// editWslConfig 用 edit 修改 .wslconfig，内容有变化时写回并记下要重启 WSL。
func (app *App) editWslConfig(edit func(string) string, message string) (WslInfo, error) {
	path := wslConfigPath()
	if path == "" {
		return app.WslInfo(), errors.New("找不到用户目录")
	}
	data, err := os.ReadFile(path)
	if err != nil && !errors.Is(err, fs.ErrNotExist) {
		return app.WslInfo(), fmt.Errorf("读不了 %s：%v", path, err)
	}
	if updated := edit(string(data)); updated != string(data) {
		if err := os.WriteFile(path, []byte(updated), 0o644); err != nil {
			return app.WslInfo(), fmt.Errorf("写不了 %s：%v", path, err)
		}
		app.wslRestart.Store(true)
		slog.Info(message, "path", path)
	}
	return app.WslInfo(), nil
}

// RestartWsl 关掉 WSL（wsl --shutdown），下次打开时用上新的设置。
func (app *App) RestartWsl() (WslInfo, error) {
	command := exec.Command(filepath.Join(systemDir(), "wsl.exe"), "--shutdown")
	command.SysProcAttr = &syscall.SysProcAttr{HideWindow: true, CreationFlags: createNoWindow}
	if output, err := command.CombinedOutput(); err != nil {
		// wsl.exe 输出的是 UTF-16，去掉里面的 0 字节再显示。
		return app.WslInfo(), fmt.Errorf("重启 WSL 失败：%v %s", err, strings.TrimSpace(strings.ReplaceAll(string(output), "\x00", "")))
	}
	app.wslRestart.Store(false)
	slog.Info("已重启 WSL")
	return app.WslInfo(), nil
}
