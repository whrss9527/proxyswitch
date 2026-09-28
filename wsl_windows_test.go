//go:build windows

package main

import (
	"os"
	"path/filepath"
	"testing"
)

// TestWslSetup 在临时的用户目录里设置和撤销 .wslconfig，不重启 WSL。
func TestWslSetup(t *testing.T) {
	home := t.TempDir()
	t.Setenv("USERPROFILE", home)
	app := &App{}
	info := app.WslInfo()
	t.Logf("WSL：已安装 %v，发行版 %v，支持镜像网络 %v", info.Installed, info.Distros, info.Supported)
	if info.Config != filepath.Join(home, ".wslconfig") || info.Mirrored || info.Restart {
		t.Fatalf("还没有 .wslconfig 时：%+v", info)
	}
	if !info.Supported {
		t.Skip("这个 Windows 版本不支持镜像网络")
	}
	if err := os.WriteFile(info.Config, []byte("[wsl2]\r\nmemory=4GB\r\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	info, err := app.SetupWsl()
	data, _ := os.ReadFile(info.Config)
	if err != nil || !info.Ready() || !info.Restart || string(data) != "[wsl2]\r\nnetworkingMode=mirrored\r\nautoProxy=true\r\nmemory=4GB\r\n" {
		t.Fatalf("设置后：%+v %v %q", info, err, data)
	}
	info, err = app.ResetWsl()
	data, _ = os.ReadFile(info.Config)
	if err != nil || info.Mirrored || string(data) != "[wsl2]\r\nmemory=4GB\r\n" {
		t.Fatalf("撤销后：%+v %v %q", info, err, data)
	}
}
