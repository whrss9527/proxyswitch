//go:build windows

package main

import (
	"errors"
	"fmt"
	"syscall"
	"time"
	"unsafe"
)

// 本文件集中声明用到的 Win32 API、常量和结构体，全部通过 syscall 直接调用，不依赖第三方库。
// 结构体布局按 64 位（amd64 / arm64）编写。

var (
	kernel32 = syscall.NewLazyDLL("kernel32.dll")
	user32   = syscall.NewLazyDLL("user32.dll")
	gdi32    = syscall.NewLazyDLL("gdi32.dll")
	shell32  = syscall.NewLazyDLL("shell32.dll")
	advapi32 = syscall.NewLazyDLL("advapi32.dll")
	ole32    = syscall.NewLazyDLL("ole32.dll")
	ntdll    = syscall.NewLazyDLL("ntdll.dll")
	// 以下 DLL 不在 Go 内置的系统 DLL 名单里，用 System32 的绝对路径加载，避免同目录 DLL 劫持。
	wininet  = syscall.NewLazyDLL(systemDir() + `\wininet.dll`)
	rasapi32 = syscall.NewLazyDLL(systemDir() + `\rasapi32.dll`)
	iphlpapi = syscall.NewLazyDLL(systemDir() + `\iphlpapi.dll`)
	wlanapi  = syscall.NewLazyDLL(systemDir() + `\wlanapi.dll`)
	winhttp  = syscall.NewLazyDLL(systemDir() + `\winhttp.dll`)

	procGetModuleHandleW           = kernel32.NewProc("GetModuleHandleW")
	procGetCurrentThreadId         = kernel32.NewProc("GetCurrentThreadId")
	procCreateMutexW               = kernel32.NewProc("CreateMutexW")
	procGetSystemDirectoryW        = kernel32.NewProc("GetSystemDirectoryW")
	procOpenProcess                = kernel32.NewProc("OpenProcess")
	procWaitForSingleObject        = kernel32.NewProc("WaitForSingleObject")
	procCloseHandle                = kernel32.NewProc("CloseHandle")
	procQueryFullProcessImageNameW = kernel32.NewProc("QueryFullProcessImageNameW")
	procGlobalAlloc                = kernel32.NewProc("GlobalAlloc")
	procGlobalLock                 = kernel32.NewProc("GlobalLock")
	procGlobalUnlock               = kernel32.NewProc("GlobalUnlock")
	procGlobalFree                 = kernel32.NewProc("GlobalFree")
	procLoadLibraryExW             = kernel32.NewProc("LoadLibraryExW")
	procGetProcAddress             = kernel32.NewProc("GetProcAddress")

	procRegisterClassExW         = user32.NewProc("RegisterClassExW")
	procCreateWindowExW          = user32.NewProc("CreateWindowExW")
	procDefWindowProcW           = user32.NewProc("DefWindowProcW")
	procDestroyWindow            = user32.NewProc("DestroyWindow")
	procGetMessageW              = user32.NewProc("GetMessageW")
	procTranslateMessage         = user32.NewProc("TranslateMessage")
	procDispatchMessageW         = user32.NewProc("DispatchMessageW")
	procPostQuitMessage          = user32.NewProc("PostQuitMessage")
	procPostMessageW             = user32.NewProc("PostMessageW")
	procSendMessageTimeoutW      = user32.NewProc("SendMessageTimeoutW")
	procRegisterWindowMessageW   = user32.NewProc("RegisterWindowMessageW")
	procCreatePopupMenu          = user32.NewProc("CreatePopupMenu")
	procAppendMenuW              = user32.NewProc("AppendMenuW")
	procSetMenuItemInfoW         = user32.NewProc("SetMenuItemInfoW")
	procSetMenuDefaultItem       = user32.NewProc("SetMenuDefaultItem")
	procTrackPopupMenu           = user32.NewProc("TrackPopupMenu")
	procDestroyMenu              = user32.NewProc("DestroyMenu")
	procSetForegroundWindow      = user32.NewProc("SetForegroundWindow")
	procAllowSetForegroundWindow = user32.NewProc("AllowSetForegroundWindow")
	procGetCursorPos             = user32.NewProc("GetCursorPos")
	procRegisterHotKey           = user32.NewProc("RegisterHotKey")
	procUnregisterHotKey         = user32.NewProc("UnregisterHotKey")
	procSetTimer                 = user32.NewProc("SetTimer")
	procKillTimer                = user32.NewProc("KillTimer")
	procMessageBoxW              = user32.NewProc("MessageBoxW")
	procDestroyIcon              = user32.NewProc("DestroyIcon")
	procCreateIconIndirect       = user32.NewProc("CreateIconIndirect")
	procGetSystemMetrics         = user32.NewProc("GetSystemMetrics")
	procGetDpiForSystem          = user32.NewProc("GetDpiForSystem")
	procGetDoubleClickTime       = user32.NewProc("GetDoubleClickTime")
	procFindWindowW              = user32.NewProc("FindWindowW")
	procEnumWindows              = user32.NewProc("EnumWindows")
	procGetWindowTextW           = user32.NewProc("GetWindowTextW")
	procGetClassNameW            = user32.NewProc("GetClassNameW")
	procIsWindowVisible          = user32.NewProc("IsWindowVisible")
	procIsIconic                 = user32.NewProc("IsIconic")
	procShowWindow               = user32.NewProc("ShowWindow")
	procOpenClipboard            = user32.NewProc("OpenClipboard")
	procCloseClipboard           = user32.NewProc("CloseClipboard")
	procEmptyClipboard           = user32.NewProc("EmptyClipboard")
	procSetClipboardData         = user32.NewProc("SetClipboardData")

	procCreateDIBSection = gdi32.NewProc("CreateDIBSection")
	procCreateBitmap     = gdi32.NewProc("CreateBitmap")
	procDeleteObject     = gdi32.NewProc("DeleteObject")

	procShellNotifyIconW = shell32.NewProc("Shell_NotifyIconW")
	procShellExecuteW    = shell32.NewProc("ShellExecuteW")

	procCoInitializeEx = ole32.NewProc("CoInitializeEx")
	procRtlGetVersion  = ntdll.NewProc("RtlGetVersion")

	procInternetSetOptionW   = wininet.NewProc("InternetSetOptionW")
	procInternetQueryOptionW = wininet.NewProc("InternetQueryOptionW")

	procRasEnumEntriesW = rasapi32.NewProc("RasEnumEntriesW")

	procGetAdaptersAddresses = iphlpapi.NewProc("GetAdaptersAddresses")
	procGetIpNetTable        = iphlpapi.NewProc("GetIpNetTable")
	procGetExtendedTcpTable  = iphlpapi.NewProc("GetExtendedTcpTable")
	procSendARP              = iphlpapi.NewProc("SendARP")

	procWlanOpenHandle     = wlanapi.NewProc("WlanOpenHandle")
	procWlanCloseHandle    = wlanapi.NewProc("WlanCloseHandle")
	procWlanEnumInterfaces = wlanapi.NewProc("WlanEnumInterfaces")
	procWlanQueryInterface = wlanapi.NewProc("WlanQueryInterface")
	procWlanFreeMemory     = wlanapi.NewProc("WlanFreeMemory")

	procWinHttpOpen           = winhttp.NewProc("WinHttpOpen")
	procWinHttpCloseHandle    = winhttp.NewProc("WinHttpCloseHandle")
	procWinHttpSetTimeouts    = winhttp.NewProc("WinHttpSetTimeouts")
	procWinHttpGetProxyForUrl = winhttp.NewProc("WinHttpGetProxyForUrl")

	procRegOpenKeyExW    = advapi32.NewProc("RegOpenKeyExW")
	procRegCreateKeyExW  = advapi32.NewProc("RegCreateKeyExW")
	procRegQueryValueExW = advapi32.NewProc("RegQueryValueExW")
	procRegSetValueExW   = advapi32.NewProc("RegSetValueExW")
	procRegDeleteValueW  = advapi32.NewProc("RegDeleteValueW")
	procRegCloseKey      = advapi32.NewProc("RegCloseKey")
	procRegEnumKeyExW    = advapi32.NewProc("RegEnumKeyExW")
)

const (
	wmNull            = 0x0000
	wmDestroy         = 0x0002
	wmClose           = 0x0010
	wmQueryEndSession = 0x0011
	wmEndSession      = 0x0016
	wmSettingChange   = 0x001A
	wmDisplayChange   = 0x007E
	wmCopyData        = 0x004A
	wmContextMenu     = 0x007B
	wmTimer           = 0x0113
	wmLButtonDblClk   = 0x0203
	wmHotkey          = 0x0312
	wmDpiChanged      = 0x02E0
	wmUser            = 0x0400
	wmApp             = 0x8000

	wmTrayCallback = wmApp + 1
	wmRunOnUi      = wmApp + 2
	wmActivate     = wmApp + 3

	ninSelect           = wmUser + 0
	ninKeySelect        = wmUser + 1
	ninBalloonUserClick = wmUser + 5

	nimAdd        = 0x0
	nimModify     = 0x1
	nimDelete     = 0x2
	nimSetVersion = 0x4

	notifyIconVersion4 = 4

	nifMessage = 0x01
	nifIcon    = 0x02
	nifTip     = 0x04
	nifInfo    = 0x10
	nifShowTip = 0x80

	niifInfo             = 0x01
	niifWarning          = 0x02
	niifError            = 0x03
	niifUser             = 0x04
	niifLargeIcon        = 0x20
	niifRespectQuietTime = 0x80

	mfString    = 0x0000
	mfGrayed    = 0x0001
	mfChecked   = 0x0008
	mfPopup     = 0x0010
	mfSeparator = 0x0800
	mftRadio    = 0x0200

	miimBitmap = 0x0080
	miimFType  = 0x0100

	tpmRightButton = 0x0002
	tpmBottomAlign = 0x0020
	tpmNoNotify    = 0x0080
	tpmReturnCmd   = 0x0100

	modNoRepeat = 0x4000

	smCxIcon      = 11
	smCxSmIcon    = 49
	smCxMenuCheck = 71

	mbOk              = 0x00000000
	mbIconError       = 0x00000010
	mbIconWarning     = 0x00000030
	mbIconInformation = 0x00000040
	mbSetForeground   = 0x00010000
	mbTopmost         = 0x00040000

	swRestore = 9

	hwndBroadcast          = 0xFFFF
	smtoAbortIfHung        = 0x0002
	smtoNoTimeoutIfNotHung = 0x0008

	errorSuccess            = 0
	errorFileNotFound       = 2
	errorAccessDenied       = 5
	errorMoreData           = 234
	errorAlreadyExists      = 183
	errorBufferTooSmall     = 603
	errorInsufficientBuffer = 122

	hkeyCurrentUser  = 0x80000001
	hkeyLocalMachine = 0x80000002
	keyRead          = 0x20019
	keyWrite         = 0x20006

	regSz       = 1
	regExpandSz = 2
	regDword    = 4

	processQueryLimitedInformation = 0x1000
	synchronize                    = 0x00100000

	gmemMoveable  = 0x0002
	cfUnicodeText = 13

	loadLibrarySearchSystem32 = 0x00000800

	coinitApartmentThreaded = 0x2
	coinitDisableOle1Dde    = 0x4
)

type wndClassExW struct {
	size        uint32
	style       uint32
	windowProc  uintptr
	classExtra  int32
	windowExtra int32
	instance    uintptr
	icon        uintptr
	cursor      uintptr
	background  uintptr
	menuName    *uint16
	className   *uint16
	smallIcon   uintptr
}

type point struct {
	x, y int32
}

type message struct {
	window  uintptr
	message uint32
	wParam  uintptr
	lParam  uintptr
	time    uint32
	point   point
}

// notifyIconDataW 对应 NOTIFYICONDATAW（Vista 及以后，976 字节）。timeoutOrVersion 在 NIM_SETVERSION 时是版本号。
type notifyIconDataW struct {
	size             uint32
	window           uintptr
	id               uint32
	flags            uint32
	callbackMessage  uint32
	icon             uintptr
	tip              [128]uint16
	state            uint32
	stateMask        uint32
	info             [256]uint16
	timeoutOrVersion uint32
	infoTitle        [64]uint16
	infoFlags        uint32
	guidItem         [16]byte
	balloonIcon      uintptr
}

type iconInfo struct {
	isIcon   int32
	hotspotX uint32
	hotspotY uint32
	mask     uintptr
	color    uintptr
}

type bitmapInfoHeader struct {
	size            uint32
	width           int32
	height          int32
	planes          uint16
	bitCount        uint16
	compression     uint32
	sizeImage       uint32
	xPelsPerMeter   int32
	yPelsPerMeter   int32
	colorsUsed      uint32
	colorsImportant uint32
}

type menuItemInfoW struct {
	size      uint32
	mask      uint32
	kind      uint32
	state     uint32
	id        uint32
	subMenu   uintptr
	checked   uintptr
	unchecked uintptr
	itemData  uintptr
	typeData  *uint16
	length    uint32
	bitmap    uintptr
}

type copyDataStruct struct {
	data    uintptr
	size    uint32
	pointer uintptr
}

type winHttpAutoProxyOptions struct {
	flags                 uint32
	autoDetectFlags       uint32
	autoConfigUrl         *uint16
	reserved              uintptr
	reservedFlags         uint32
	autoLogonIfChallenged int32
}

type winHttpProxyInfo struct {
	accessType uint32
	proxy      *uint16
	bypass     *uint16
}

type osVersionInfo struct {
	size        uint32
	major       uint32
	minor       uint32
	build       uint32
	platform    uint32
	servicePack [128]uint16
}

// ---------- 小工具 ----------

func utf16Pointer(text string) *uint16 {
	pointer, err := syscall.UTF16PtrFromString(text)
	if err != nil {
		pointer, _ = syscall.UTF16PtrFromString(replaceNul(text))
	}
	return pointer
}

func replaceNul(text string) string {
	runes := []rune(text)
	for index, char := range runes {
		if char == 0 {
			runes[index] = ' '
		}
	}
	return string(runes)
}

// copyUtf16 把字符串写入定长 UTF-16 数组，超长截断并保证以 NUL 结尾。
func copyUtf16(destination []uint16, text string) {
	encoded := syscall.StringToUTF16(replaceNul(text))
	length := len(encoded)
	if length > len(destination) {
		length = len(destination)
		encoded[length-1] = 0
	}
	copy(destination, encoded[:length])
	for index := length; index < len(destination); index++ {
		destination[index] = 0
	}
}

// utf16PointerToString 读取以 NUL 结尾的 UTF-16 字符串。
func utf16PointerToString(pointer *uint16) string {
	if pointer == nil {
		return ""
	}
	var runes []uint16
	for address := unsafe.Pointer(pointer); ; address = unsafe.Add(address, 2) {
		char := *(*uint16)(address)
		if char == 0 {
			break
		}
		runes = append(runes, char)
	}
	return syscall.UTF16ToString(runes)
}

func systemDir() string {
	var buffer [260]uint16
	length, _, _ := procGetSystemDirectoryW.Call(uintptr(unsafe.Pointer(&buffer[0])), uintptr(len(buffer)))
	if length == 0 || int(length) >= len(buffer) {
		return `C:\Windows\System32`
	}
	return syscall.UTF16ToString(buffer[:length])
}

func moduleHandle() uintptr {
	handle, _, _ := procGetModuleHandleW.Call(0)
	return handle
}

func currentThreadId() uint32 {
	id, _, _ := procGetCurrentThreadId.Call()
	return uint32(id)
}

func messageBox(window uintptr, text, title string, flags uint32) {
	procMessageBoxW.Call(window, uintptr(unsafe.Pointer(utf16Pointer(text))), uintptr(unsafe.Pointer(utf16Pointer(title))), uintptr(flags))
}

// createSingleInstanceMutex 创建命名互斥量；已存在时返回 false。
func createSingleInstanceMutex(name string) (bool, error) {
	handle, _, err := procCreateMutexW.Call(0, 0, uintptr(unsafe.Pointer(utf16Pointer(name))))
	if handle == 0 {
		return false, fmt.Errorf("CreateMutex: %v", err)
	}
	if errno, ok := err.(syscall.Errno); ok && errno == errorAlreadyExists {
		return false, nil
	}
	return true, nil
}

// shellOpen 用系统默认方式打开文件、目录或网址。
func shellOpen(target string) error {
	result, _, _ := procShellExecuteW.Call(0,
		uintptr(unsafe.Pointer(utf16Pointer("open"))),
		uintptr(unsafe.Pointer(utf16Pointer(target))),
		0, 0, 1)
	if result <= 32 {
		return fmt.Errorf("无法打开 %s（错误码 %d）", target, result)
	}
	return nil
}

// setClipboardText 把文本放进剪贴板。剪贴板可能正被其他程序占用，打开失败时稍等重试。
func setClipboardText(window uintptr, text string) error {
	opened := false
	for attempt := 0; attempt < 10 && !opened; attempt++ {
		if result, _, _ := procOpenClipboard.Call(window); result != 0 {
			opened = true
		} else {
			time.Sleep(30 * time.Millisecond)
		}
	}
	if !opened {
		return errors.New("剪贴板正被其他程序占用，请稍后再试")
	}
	defer procCloseClipboard.Call()
	procEmptyClipboard.Call()
	encoded := syscall.StringToUTF16(replaceNul(text))
	memory, _, err := procGlobalAlloc.Call(gmemMoveable, uintptr(len(encoded)*2))
	if memory == 0 {
		return fmt.Errorf("GlobalAlloc：%v", err)
	}
	locked, _, err := procGlobalLock.Call(memory)
	if locked == 0 {
		procGlobalFree.Call(memory)
		return fmt.Errorf("GlobalLock：%v", err)
	}
	copy(unsafe.Slice((*uint16)(pointerFromParameter(&locked)), len(encoded)), encoded)
	procGlobalUnlock.Call(memory)
	// 成功后内存归剪贴板所有，失败时才需要自己释放。
	if result, _, err := procSetClipboardData.Call(cfUnicodeText, memory); result == 0 {
		procGlobalFree.Call(memory)
		return fmt.Errorf("SetClipboardData：%v", err)
	}
	return nil
}

// broadcastSettingChange 通知所有顶层窗口某类设置已变更（例如 "Environment"）。
func broadcastSettingChange(section string) {
	procSendMessageTimeoutW.Call(hwndBroadcast, wmSettingChange, 0,
		uintptr(unsafe.Pointer(utf16Pointer(section))),
		smtoAbortIfHung|smtoNoTimeoutIfNotHung, 2000, 0)
}

// windowsBuild 返回 Windows 的内部版本号，例如 Windows 11 23H2 为 22631。
func windowsBuild() uint32 {
	info := osVersionInfo{}
	info.size = uint32(unsafe.Sizeof(info))
	if status, _, _ := procRtlGetVersion.Call(uintptr(unsafe.Pointer(&info))); status != 0 {
		return 0
	}
	return info.build
}

func systemMetric(index int) int {
	value, _, _ := procGetSystemMetrics.Call(uintptr(index))
	return int(value)
}

// systemDpi 返回系统 DPI，拿不到时按 96。
func systemDpi() int {
	if procGetDpiForSystem.Find() == nil {
		if dpi, _, _ := procGetDpiForSystem.Call(); dpi != 0 {
			return int(dpi)
		}
	}
	return 96
}
