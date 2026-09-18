//go:build windows

// sendmsg 是测试辅助工具：向 ProxySwitch 的隐藏窗口投递托盘回调消息，模拟鼠标点击托盘图标。
// 用法：sendmsg left | right | dbl | find
package main

import (
	"fmt"
	"os"
	"syscall"
	"unsafe"
)

var (
	user32           = syscall.NewLazyDLL("user32.dll")
	procFindWindowW  = user32.NewProc("FindWindowW")
	procPostMessageW = user32.NewProc("PostMessageW")
)

func main() {
	if len(os.Args) < 2 {
		fmt.Println("usage: sendmsg left|right|dbl|find")
		os.Exit(2)
	}
	cls, _ := syscall.UTF16PtrFromString("ProxySwitchTrayWindow")
	hwnd, _, _ := procFindWindowW.Call(uintptr(unsafe.Pointer(cls)), 0)
	if hwnd == 0 {
		fmt.Println("window not found")
		os.Exit(1)
	}
	fmt.Printf("hwnd=%#x\n", hwnd)
	const wmTray = 0x8000 + 1
	var lp uintptr
	switch os.Args[1] {
	case "left":
		lp = 0x0202
	case "right":
		lp = 0x0205
	case "dbl":
		// 模拟完整双击序列：UP, DBLCLK, UP
		procPostMessageW.Call(hwnd, wmTray, 1, 0x0202)
		procPostMessageW.Call(hwnd, wmTray, 1, 0x0203)
		lp = 0x0202
	case "find":
		return
	default:
		os.Exit(2)
	}
	r, _, e := procPostMessageW.Call(hwnd, wmTray, 1, lp)
	fmt.Println("posted:", r, e)
}
