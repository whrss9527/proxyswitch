//go:build windows

package main

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"unsafe"
)

// 设置页的窗口：优先用 Edge（Windows 10 / 11 自带）或 Chrome 的应用模式打开，没有地址栏和标签页，像一个独立程序；
// 使用单独的浏览器数据目录，不影响用户自己的浏览器。都没有或设置为 browser 时用默认浏览器打开。

// settingsWindowTitle 必须与 web/settings.html 的 <title> 一致，用来找到已经打开的设置窗口。
const settingsWindowTitle = "ProxySwitch 设置"

const asfwAny = 0xFFFFFFFF

func findAppBrowser() string {
	var candidates []string
	for _, name := range []string{"msedge.exe", "chrome.exe"} {
		for _, root := range []uintptr{hkeyCurrentUser, hkeyLocalMachine} {
			if path := readRegistryString(root, `SOFTWARE\Microsoft\Windows\CurrentVersion\App Paths\`+name, ""); path != "" {
				candidates = append(candidates, strings.Trim(path, `"`))
			}
		}
	}
	for _, base := range []string{os.Getenv("ProgramFiles(x86)"), os.Getenv("ProgramFiles"), os.Getenv("LOCALAPPDATA")} {
		if base != "" {
			candidates = append(candidates,
				filepath.Join(base, `Microsoft\Edge\Application\msedge.exe`),
				filepath.Join(base, `Google\Chrome\Application\chrome.exe`))
		}
	}
	for _, candidate := range candidates {
		if fileExists(candidate) {
			return candidate
		}
	}
	return ""
}

// openSettingsWindow 打开设置页；已经打开时把窗口切到前台。
func openSettingsWindow(address, mode string) error {
	if focusSettingsWindow() {
		return nil
	}
	procAllowSetForegroundWindow.Call(asfwAny)
	if mode == "app" {
		if browser := findAppBrowser(); browser != "" {
			dataDir := filepath.Join(os.Getenv("LOCALAPPDATA"), appName, "WebView")
			width, height, left, top := settingsWindowBounds()
			command := exec.Command(browser,
				"--app="+address,
				"--user-data-dir="+dataDir,
				fmt.Sprintf("--window-size=%d,%d", width, height),
				fmt.Sprintf("--window-position=%d,%d", left, top),
				"--no-first-run",
				"--no-default-browser-check",
				"--disable-features=Translate,msEdgeTranslate",
			)
			if err := command.Start(); err == nil {
				go func() { _ = command.Wait() }()
				return nil
			}
		}
	}
	return shellOpen(address)
}

const (
	smCxScreen = 0
	smCyScreen = 1
)

// settingsWindowBounds 按主屏幕大小算出设置窗口的尺寸和居中位置（与缩放无关的逻辑像素）。
func settingsWindowBounds() (width, height, left, top int) {
	dpi := systemDpi()
	screenWidth := systemMetric(smCxScreen) * 96 / dpi
	screenHeight := systemMetric(smCyScreen) * 96 / dpi
	if screenWidth <= 0 || screenHeight <= 0 {
		return 1120, 800, 100, 60
	}
	width = min(1120, screenWidth*9/10)
	height = min(800, screenHeight*85/100)
	return width, height, (screenWidth - width) / 2, max(0, (screenHeight-height)/2-20)
}

var (
	foundSettingsWindow uintptr
	enumSettingsWindows = syscall.NewCallback(func(window, parameter uintptr) uintptr {
		if visible, _, _ := procIsWindowVisible.Call(window); visible == 0 {
			return 1
		}
		title := make([]uint16, 256)
		length, _, _ := procGetWindowTextW.Call(window, uintptr(unsafe.Pointer(&title[0])), uintptr(len(title)))
		if !strings.HasPrefix(syscall.UTF16ToString(title[:length]), settingsWindowTitle) {
			return 1
		}
		className := make([]uint16, 64)
		classLength, _, _ := procGetClassNameW.Call(window, uintptr(unsafe.Pointer(&className[0])), uintptr(len(className)))
		if !strings.HasPrefix(syscall.UTF16ToString(className[:classLength]), "Chrome_WidgetWin") {
			return 1
		}
		foundSettingsWindow = window
		return 0
	})
)

// focusSettingsWindow 找到已打开的设置窗口并切到前台，找不到返回 false。
func focusSettingsWindow() bool {
	foundSettingsWindow = 0
	procEnumWindows.Call(enumSettingsWindows, 0)
	window := foundSettingsWindow
	if window == 0 {
		return false
	}
	if minimized, _, _ := procIsIconic.Call(window); minimized != 0 {
		procShowWindow.Call(window, swRestore)
	}
	procSetForegroundWindow.Call(window)
	return true
}
