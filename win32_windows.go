//go:build windows

package main

import (
	"errors"
	"fmt"
	"syscall"
	"unsafe"
)

// 本文件集中声明用到的 Win32 API、常量和结构体。
// 全部通过 syscall 直接调用，不依赖任何第三方库。

var (
	user32   = syscall.NewLazyDLL("user32.dll")
	kernel32 = syscall.NewLazyDLL("kernel32.dll")
	shell32  = syscall.NewLazyDLL("shell32.dll")
	advapi32 = syscall.NewLazyDLL("advapi32.dll")
	// wininet 不在 Go 内置的“系统 DLL 白名单”里，这里用绝对路径加载，避免同目录 DLL 劫持。
	wininet = syscall.NewLazyDLL(systemDir() + `\wininet.dll`)

	procRegisterClassExW            = user32.NewProc("RegisterClassExW")
	procCreateWindowExW             = user32.NewProc("CreateWindowExW")
	procDefWindowProcW              = user32.NewProc("DefWindowProcW")
	procDestroyWindow               = user32.NewProc("DestroyWindow")
	procGetMessageW                 = user32.NewProc("GetMessageW")
	procTranslateMessage            = user32.NewProc("TranslateMessage")
	procDispatchMessageW            = user32.NewProc("DispatchMessageW")
	procPostQuitMessage             = user32.NewProc("PostQuitMessage")
	procPostMessageW                = user32.NewProc("PostMessageW")
	procSendMessageTimeoutW         = user32.NewProc("SendMessageTimeoutW")
	procRegisterWindowMessageW      = user32.NewProc("RegisterWindowMessageW")
	procCreatePopupMenu             = user32.NewProc("CreatePopupMenu")
	procAppendMenuW                 = user32.NewProc("AppendMenuW")
	procCheckMenuRadioItem          = user32.NewProc("CheckMenuRadioItem")
	procSetMenuDefaultItem          = user32.NewProc("SetMenuDefaultItem")
	procTrackPopupMenu              = user32.NewProc("TrackPopupMenu")
	procDestroyMenu                 = user32.NewProc("DestroyMenu")
	procSetForegroundWindow         = user32.NewProc("SetForegroundWindow")
	procGetCursorPos                = user32.NewProc("GetCursorPos")
	procRegisterHotKey              = user32.NewProc("RegisterHotKey")
	procUnregisterHotKey            = user32.NewProc("UnregisterHotKey")
	procSetTimer                    = user32.NewProc("SetTimer")
	procKillTimer                   = user32.NewProc("KillTimer")
	procMessageBoxW                 = user32.NewProc("MessageBoxW")
	procLoadImageW                  = user32.NewProc("LoadImageW")
	procDestroyIcon                 = user32.NewProc("DestroyIcon")
	procGetSystemMetrics            = user32.NewProc("GetSystemMetrics")
	procLookupIconIdFromDirectoryEx = user32.NewProc("LookupIconIdFromDirectoryEx")
	procCreateIconFromResourceEx    = user32.NewProc("CreateIconFromResourceEx")

	procGetModuleHandleW    = kernel32.NewProc("GetModuleHandleW")
	procCreateMutexW        = kernel32.NewProc("CreateMutexW")
	procGetSystemDirectoryW = kernel32.NewProc("GetSystemDirectoryW")

	procShellNotifyIconW = shell32.NewProc("Shell_NotifyIconW")
	procShellExecuteW    = shell32.NewProc("ShellExecuteW")

	procInternetSetOptionW = wininet.NewProc("InternetSetOptionW")

	procRegOpenKeyExW    = advapi32.NewProc("RegOpenKeyExW")
	procRegCreateKeyExW  = advapi32.NewProc("RegCreateKeyExW")
	procRegQueryValueExW = advapi32.NewProc("RegQueryValueExW")
	procRegSetValueExW   = advapi32.NewProc("RegSetValueExW")
	procRegDeleteValueW  = advapi32.NewProc("RegDeleteValueW")
	procRegCloseKey      = advapi32.NewProc("RegCloseKey")
)

const (
	wmNull          = 0x0000
	wmDestroy       = 0x0002
	wmClose         = 0x0010
	wmSettingChange = 0x001A
	wmContextMenu   = 0x007B
	wmCommand       = 0x0111
	wmTimer         = 0x0113
	wmMouseMove     = 0x0200
	wmLButtonUp     = 0x0202
	wmLButtonDblClk = 0x0203
	wmRButtonUp     = 0x0205
	wmHotkey        = 0x0312
	wmUser          = 0x0400
	wmApp           = 0x8000

	wmTrayCallback = wmApp + 1 // 托盘图标回调消息

	ninSelect    = wmUser + 0
	ninKeySelect = wmUser + 1

	nimAdd        = 0x0
	nimModify     = 0x1
	nimDelete     = 0x2
	nimSetVersion = 0x4

	nifMessage = 0x01
	nifIcon    = 0x02
	nifTip     = 0x04
	nifInfo    = 0x10
	nifShowTip = 0x80

	niifNone             = 0x00
	niifInfo             = 0x01
	niifWarning          = 0x02
	niifError            = 0x03
	niifNoSound          = 0x10
	niifRespectQuietTime = 0x80

	mfString    = 0x0000
	mfGrayed    = 0x0001
	mfChecked   = 0x0008
	mfSeparator = 0x0800
	mfByCommand = 0x0000

	tpmLeftAlign   = 0x0000
	tpmRightButton = 0x0002
	tpmBottomAlign = 0x0020
	tpmNoNotify    = 0x0080
	tpmReturnCmd   = 0x0100

	modAlt      = 0x0001
	modControl  = 0x0002
	modShift    = 0x0004
	modWin      = 0x0008
	modNoRepeat = 0x4000

	imageIcon      = 1
	lrDefaultColor = 0x0000
	lrLoadFromFile = 0x0010
	lrDefaultSize  = 0x0040

	smCxSmIcon = 49
	smCySmIcon = 50

	mbOK              = 0x00000000
	mbIconError       = 0x00000010
	mbIconWarning     = 0x00000030
	mbIconInformation = 0x00000040
	mbSetForeground   = 0x00010000
	mbTopmost         = 0x00040000

	hwndBroadcast          = 0xFFFF
	smtoAbortIfHung        = 0x0002
	smtoNoTimeoutIfNotHung = 0x0008

	wsOverlapped = 0x00000000
	cwUseDefault = 0x80000000

	errorSuccess       = 0
	errorFileNotFound  = 2
	errorMoreData      = 234
	errorAlreadyExists = 183

	hkeyCurrentUser = 0x80000001
	keyQueryValue   = 0x0001
	keySetValue     = 0x0002
	keyRead         = 0x20019
	keyWrite        = 0x20006

	regNone     = 0
	regSz       = 1
	regExpandSz = 2
	regDword    = 4

	internetOptionRefresh              = 37
	internetOptionSettingsChanged      = 39
	internetOptionProxySettingsChanged = 95
)

type wndClassExW struct {
	cbSize        uint32
	style         uint32
	lpfnWndProc   uintptr
	cbClsExtra    int32
	cbWndExtra    int32
	hInstance     uintptr
	hIcon         uintptr
	hCursor       uintptr
	hbrBackground uintptr
	lpszMenuName  *uint16
	lpszClassName *uint16
	hIconSm       uintptr
}

type point struct {
	x, y int32
}

type msgW struct {
	hwnd    uintptr
	message uint32
	wParam  uintptr
	lParam  uintptr
	time    uint32
	pt      point
}

// notifyIconDataW 对应 NOTIFYICONDATAW（Vista 及以后版本，976 字节）。
type notifyIconDataW struct {
	cbSize           uint32
	hWnd             uintptr
	uID              uint32
	uFlags           uint32
	uCallbackMessage uint32
	hIcon            uintptr
	szTip            [128]uint16
	dwState          uint32
	dwStateMask      uint32
	szInfo           [256]uint16
	uVersion         uint32 // 与 uTimeout 共用
	szInfoTitle      [64]uint16
	dwInfoFlags      uint32
	guidItem         [16]byte
	hBalloonIcon     uintptr
}

// ---------- 小工具 ----------

func utf16Ptr(s string) *uint16 {
	p, err := syscall.UTF16PtrFromString(s)
	if err != nil {
		// 字符串里带 NUL，替换掉再试
		p, _ = syscall.UTF16PtrFromString(replaceNUL(s))
	}
	return p
}

func replaceNUL(s string) string {
	b := []rune(s)
	for i, r := range b {
		if r == 0 {
			b[i] = ' '
		}
	}
	return string(b)
}

// copyUTF16 把字符串写入定长 UTF-16 数组，超长截断并保证以 NUL 结尾。
func copyUTF16(dst []uint16, s string) {
	u := syscall.StringToUTF16(replaceNUL(s))
	n := len(u)
	if n > len(dst) {
		n = len(dst)
		u[n-1] = 0
	}
	copy(dst, u[:n])
	for i := n; i < len(dst); i++ {
		dst[i] = 0
	}
}

func systemDir() string {
	var buf [260]uint16
	n, _, _ := procGetSystemDirectoryW.Call(uintptr(unsafe.Pointer(&buf[0])), uintptr(len(buf)))
	if n == 0 || int(n) >= len(buf) {
		return `C:\Windows\System32`
	}
	return syscall.UTF16ToString(buf[:n])
}

func moduleHandle() uintptr {
	h, _, _ := procGetModuleHandleW.Call(0)
	return h
}

func messageBox(hwnd uintptr, text, title string, flags uint32) {
	procMessageBoxW.Call(hwnd,
		uintptr(unsafe.Pointer(utf16Ptr(text))),
		uintptr(unsafe.Pointer(utf16Ptr(title))),
		uintptr(flags))
}

// createSingleInstanceMutex 创建命名互斥量；已存在则返回 false。
func createSingleInstanceMutex(name string) (bool, error) {
	h, _, e := procCreateMutexW.Call(0, 0, uintptr(unsafe.Pointer(utf16Ptr(name))))
	if h == 0 {
		return false, fmt.Errorf("CreateMutex: %v", e)
	}
	if errno, ok := e.(syscall.Errno); ok && errno == errorAlreadyExists {
		return false, nil
	}
	return true, nil
}

// shellOpen 用系统默认方式打开文件/目录/URL。
func shellOpen(target string) error {
	r, _, _ := procShellExecuteW.Call(0,
		uintptr(unsafe.Pointer(utf16Ptr("open"))),
		uintptr(unsafe.Pointer(utf16Ptr(target))),
		0, 0, 1 /* SW_SHOWNORMAL */)
	if r <= 32 {
		return fmt.Errorf("ShellExecute 失败，返回码 %d", r)
	}
	return nil
}

// broadcastSettingChange 通知所有顶层窗口某类设置已变更（例如 "Environment"）。
func broadcastSettingChange(section string) {
	procSendMessageTimeoutW.Call(hwndBroadcast, wmSettingChange, 0,
		uintptr(unsafe.Pointer(utf16Ptr(section))),
		smtoAbortIfHung|smtoNoTimeoutIfNotHung, 2000, 0)
}

var errNotFound = errors.New("not found")
