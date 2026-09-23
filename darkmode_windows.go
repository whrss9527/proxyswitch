//go:build windows

package main

import (
	"syscall"
	"unsafe"
)

// 让托盘右键菜单跟随系统的深色 / 浅色模式。
// 用的是 uxtheme.dll 未公开的序号导出（135 SetPreferredAppMode、136 FlushMenuThemes），Windows 10 1903 起可用；
// 记事本、资源管理器等系统程序也是这样做的。更早的系统上什么都不做。

const (
	uxthemeSetPreferredAppMode = 135
	uxthemeFlushMenuThemes     = 136
	appModeAllowDark           = 1
	minimumDarkMenuBuild       = 18362
)

var flushMenuThemes uintptr

func enableDarkMenus() {
	if windowsBuild() < minimumDarkMenuBuild {
		return
	}
	module, _, _ := procLoadLibraryExW.Call(uintptr(unsafe.Pointer(utf16Pointer("uxtheme.dll"))), 0, loadLibrarySearchSystem32)
	if module == 0 {
		return
	}
	setPreferredAppMode, _, _ := procGetProcAddress.Call(module, uxthemeSetPreferredAppMode)
	flushMenuThemes, _, _ = procGetProcAddress.Call(module, uxthemeFlushMenuThemes)
	if setPreferredAppMode == 0 {
		return
	}
	callPointer(setPreferredAppMode, appModeAllowDark)
	refreshMenuTheme()
}

// refreshMenuTheme 在系统切换深浅色后调用，让菜单立即换色。
func refreshMenuTheme() {
	if flushMenuThemes != 0 {
		callPointer(flushMenuThemes)
	}
}

func callPointer(address uintptr, args ...uintptr) uintptr {
	result, _, _ := syscall.SyscallN(address, args...)
	return result
}
