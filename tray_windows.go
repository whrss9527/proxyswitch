//go:build windows

package main

import (
	"errors"
	"fmt"
	"runtime"
	"syscall"
	"time"
	"unsafe"
)

// 托盘图标、隐藏的消息窗口和消息循环。所有界面操作都在创建窗口的线程（主线程）上执行，
// 其他 goroutine 通过 RunOnUi 把函数排队到这个线程。

const (
	trayWindowClass = "ProxySwitchTrayWindow"
	trayIconId      = 1

	balloonTimerId = 1
	clickTimerId   = 2
)

// trayHandler 接收托盘事件，由 App 实现。
type trayHandler interface {
	onTrayClick()
	onTrayDoubleClick()
	onTrayMenu(anchor point)
	onHotkey(id int)
	onTimer(id uintptr)
	onCopyData(data []byte) uintptr
	onActivateRequest()
	onSettingChange(section string)
	onEndSession()
	onDestroy()
}

type MenuItem struct {
	Id        uint32
	Text      string
	Checked   bool
	Radio     bool
	Disabled  bool
	Default   bool
	Separator bool
	Bitmap    uintptr
}

type Tray struct {
	handler        trayHandler
	window         uintptr
	iconData       notifyIconDataW
	taskbarCreated uint32
	icon           uintptr
	tooltip        string
	balloonIcon    uintptr
	hotkeys        map[int]bool
	uiThread       uint32
	uiQueue        chan func()
	windowProc     uintptr

	DoubleClickEnabled bool
	clickPending       bool
	ignoreSelectUntil  time.Time
}

func newTray(handler trayHandler) (*Tray, error) {
	tray := &Tray{handler: handler, hotkeys: map[int]bool{}, uiThread: currentThreadId(), uiQueue: make(chan func(), 64)}
	tray.windowProc = syscall.NewCallback(tray.windowProcedure)
	instance := moduleHandle()
	className := utf16Pointer(trayWindowClass)
	class := wndClassExW{windowProc: tray.windowProc, instance: instance, className: className}
	class.size = uint32(unsafe.Sizeof(class))
	if atom, _, err := procRegisterClassExW.Call(uintptr(unsafe.Pointer(&class))); atom == 0 {
		return nil, fmt.Errorf("RegisterClassEx：%v", err)
	}
	window, _, err := procCreateWindowExW.Call(0, uintptr(unsafe.Pointer(className)), uintptr(unsafe.Pointer(utf16Pointer(appName))),
		0, 0, 0, 0, 0, 0, 0, instance, 0)
	if window == 0 {
		return nil, fmt.Errorf("CreateWindowEx：%v", err)
	}
	tray.window = window
	message, _, _ := procRegisterWindowMessageW.Call(uintptr(unsafe.Pointer(utf16Pointer("TaskbarCreated"))))
	tray.taskbarCreated = uint32(message)
	tray.iconData = notifyIconDataW{window: window, id: trayIconId, callbackMessage: wmTrayCallback}
	tray.iconData.size = uint32(unsafe.Sizeof(tray.iconData))
	return tray, nil
}

// Show 把图标加到通知区域；explorer 重启后也调用它重新添加。
func (tray *Tray) Show(icon uintptr, tooltip string) error {
	tray.icon, tray.tooltip = icon, tooltip
	data := tray.iconData
	data.flags = nifMessage | nifIcon | nifTip | nifShowTip
	data.icon = icon
	copyUtf16(data.tip[:], tooltip)
	if result, _, err := procShellNotifyIconW.Call(nimAdd, uintptr(unsafe.Pointer(&data))); result == 0 {
		return fmt.Errorf("Shell_NotifyIcon：%v", err)
	}
	data.timeoutOrVersion = notifyIconVersion4
	procShellNotifyIconW.Call(nimSetVersion, uintptr(unsafe.Pointer(&data)))
	return nil
}

func (tray *Tray) remove() {
	data := tray.iconData
	procShellNotifyIconW.Call(nimDelete, uintptr(unsafe.Pointer(&data)))
}

// Update 更新图标和悬停提示，没有变化时不调用系统接口。
func (tray *Tray) Update(icon uintptr, tooltip string) {
	if icon == tray.icon && tooltip == tray.tooltip {
		return
	}
	tray.icon, tray.tooltip = icon, tooltip
	data := tray.iconData
	data.flags = nifIcon | nifTip | nifShowTip
	data.icon = icon
	copyUtf16(data.tip[:], tooltip)
	procShellNotifyIconW.Call(nimModify, uintptr(unsafe.Pointer(&data)))
}

// Notify 弹出通知。largeIcon 非 0 时用它作为通知图标（信息类通知），否则按 flags 显示系统图标。
// Windows 10 起系统忽略 uTimeout，所以自己用定时器到点后收起。
func (tray *Tray) Notify(title, text string, flags uint32, largeIcon uintptr, timeout time.Duration) {
	procKillTimer.Call(tray.window, balloonTimerId)
	if tray.balloonIcon != 0 {
		procDestroyIcon.Call(tray.balloonIcon)
		tray.balloonIcon = 0
	}
	data := tray.iconData
	data.flags = nifInfo
	data.infoFlags = flags | niifRespectQuietTime
	if largeIcon != 0 {
		data.infoFlags = niifUser | niifLargeIcon | niifRespectQuietTime
		data.balloonIcon = largeIcon
		tray.balloonIcon = largeIcon
	}
	data.timeoutOrVersion = uint32(timeout.Milliseconds())
	copyUtf16(data.infoTitle[:], title)
	copyUtf16(data.info[:], text)
	procShellNotifyIconW.Call(nimModify, uintptr(unsafe.Pointer(&data)))
	if timeout > 0 {
		procSetTimer.Call(tray.window, balloonTimerId, uintptr(timeout.Milliseconds()), 0)
	}
}

func (tray *Tray) hideBalloon() {
	data := tray.iconData
	data.flags = nifInfo
	procShellNotifyIconW.Call(nimModify, uintptr(unsafe.Pointer(&data)))
}

func (tray *Tray) RegisterHotkey(id int, hotkey Hotkey) error {
	result, _, err := procRegisterHotKey.Call(tray.window, uintptr(id), uintptr(hotkey.Modifiers|modNoRepeat), uintptr(hotkey.KeyCode))
	if result == 0 {
		return fmt.Errorf("快捷键 %s 注册失败：%v", hotkey.Text, err)
	}
	tray.hotkeys[id] = true
	return nil
}

func (tray *Tray) UnregisterHotkeys() {
	for id := range tray.hotkeys {
		procUnregisterHotKey.Call(tray.window, uintptr(id))
	}
	tray.hotkeys = map[int]bool{}
}

func (tray *Tray) StartTimer(id uintptr, interval time.Duration) {
	procSetTimer.Call(tray.window, id, uintptr(interval.Milliseconds()), 0)
}

// ShowMenu 在 anchor 处弹出菜单，返回选中项的 Id，没有选择时返回 0。
func (tray *Tray) ShowMenu(items []MenuItem, anchor point) uint32 {
	menu, _, _ := procCreatePopupMenu.Call()
	if menu == 0 {
		return 0
	}
	defer procDestroyMenu.Call(menu)
	var texts []*uint16
	for _, item := range items {
		if item.Separator {
			procAppendMenuW.Call(menu, mfSeparator, 0, 0)
			continue
		}
		flags := uintptr(mfString)
		if item.Disabled {
			flags |= mfGrayed
		}
		if item.Checked {
			flags |= mfChecked
		}
		text := utf16Pointer(item.Text)
		texts = append(texts, text)
		procAppendMenuW.Call(menu, flags, uintptr(item.Id), uintptr(unsafe.Pointer(text)))
		if item.Radio || item.Bitmap != 0 {
			info := menuItemInfoW{}
			info.size = uint32(unsafe.Sizeof(info))
			if item.Radio {
				info.mask |= miimFType
				info.kind = mftRadio
			}
			if item.Bitmap != 0 {
				info.mask |= miimBitmap
				info.bitmap = item.Bitmap
			}
			procSetMenuItemInfoW.Call(menu, uintptr(item.Id), 0, uintptr(unsafe.Pointer(&info)))
		}
		if item.Default {
			procSetMenuDefaultItem.Call(menu, uintptr(item.Id), 0)
		}
	}
	// 先把窗口设为前台，否则点击菜单外部时菜单不会消失。
	procSetForegroundWindow.Call(tray.window)
	command, _, _ := procTrackPopupMenu.Call(menu, tpmRightButton|tpmBottomAlign|tpmReturnCmd|tpmNoNotify,
		uintptr(anchor.x), uintptr(anchor.y), 0, tray.window, 0)
	procPostMessageW.Call(tray.window, wmNull, 0, 0)
	runtime.KeepAlive(texts)
	return uint32(command)
}

func cursorPosition() point {
	var position point
	procGetCursorPos.Call(uintptr(unsafe.Pointer(&position)))
	return position
}

var errUiBusy = errors.New("程序正忙，请稍后再试")

// RunOnUi 让 action 在 UI 线程上执行并等待完成。
func (tray *Tray) RunOnUi(action func()) error {
	if currentThreadId() == tray.uiThread {
		action()
		return nil
	}
	done := make(chan struct{})
	select {
	case tray.uiQueue <- func() { defer close(done); action() }:
	case <-time.After(5 * time.Second):
		return errUiBusy
	}
	procPostMessageW.Call(tray.window, wmRunOnUi, 0, 0)
	select {
	case <-done:
		return nil
	case <-time.After(30 * time.Second):
		return errors.New("程序没有响应，请稍后再试")
	}
}

func (tray *Tray) drainUiQueue() {
	for {
		select {
		case action := <-tray.uiQueue:
			action()
		default:
			return
		}
	}
}

// Run 进入消息循环，直到窗口销毁。
func (tray *Tray) Run() {
	var current message
	for {
		result, _, _ := procGetMessageW.Call(uintptr(unsafe.Pointer(&current)), 0, 0, 0)
		if int32(result) <= 0 {
			return
		}
		procTranslateMessage.Call(uintptr(unsafe.Pointer(&current)))
		procDispatchMessageW.Call(uintptr(unsafe.Pointer(&current)))
	}
}

func (tray *Tray) Quit() {
	procDestroyWindow.Call(tray.window)
}

func doubleClickTime() time.Duration {
	milliseconds, _, _ := procGetDoubleClickTime.Call()
	if milliseconds == 0 {
		milliseconds = 500
	}
	return time.Duration(milliseconds) * time.Millisecond
}

// pointerFromParameter 把消息参数里的指针取出来（写成读变量内存的形式，避免 uintptr 直接转指针）。
func pointerFromParameter(parameter *uintptr) unsafe.Pointer {
	return *(*unsafe.Pointer)(unsafe.Pointer(parameter))
}

func (tray *Tray) windowProcedure(window, messageId, wParam, lParam uintptr) uintptr {
	switch uint32(messageId) {
	case wmTrayCallback:
		tray.handleTrayEvent(uint32(lParam&0xFFFF), wParam)
		return 0
	case wmRunOnUi:
		tray.drainUiQueue()
		return 0
	case wmActivate:
		tray.handler.onActivateRequest()
		return 0
	case wmHotkey:
		tray.handler.onHotkey(int(wParam))
		return 0
	case wmTimer:
		switch wParam {
		case balloonTimerId:
			procKillTimer.Call(window, balloonTimerId)
			tray.hideBalloon()
		case clickTimerId:
			procKillTimer.Call(window, clickTimerId)
			if tray.clickPending {
				tray.clickPending = false
				tray.handler.onTrayClick()
			}
		default:
			tray.handler.onTimer(wParam)
		}
		return 0
	case wmCopyData:
		pointer := pointerFromParameter(&lParam)
		if pointer == nil {
			return 0
		}
		data := (*copyDataStruct)(pointer)
		var payload []byte
		if data.size > 0 && data.size < 1<<16 {
			payload = append(payload, unsafe.Slice((*byte)(pointerFromParameter(&data.pointer)), data.size)...)
		}
		return tray.handler.onCopyData(payload)
	case wmSettingChange:
		section := ""
		if pointer := pointerFromParameter(&lParam); pointer != nil {
			section = utf16PointerToString((*uint16)(pointer))
		}
		tray.handler.onSettingChange(section)
	case wmDisplayChange, wmDpiChanged:
		tray.handler.onSettingChange("")
	case wmQueryEndSession:
		return 1
	case wmEndSession:
		if wParam != 0 {
			tray.handler.onEndSession()
		}
		return 0
	case wmClose:
		procDestroyWindow.Call(window)
		return 0
	case wmDestroy:
		procKillTimer.Call(window, balloonTimerId)
		tray.UnregisterHotkeys()
		tray.handler.onDestroy()
		tray.remove()
		procPostQuitMessage.Call(0)
		return 0
	}
	if tray.taskbarCreated != 0 && uint32(messageId) == tray.taskbarCreated {
		// explorer.exe 重启后托盘图标会丢失，重新添加。
		_ = tray.Show(tray.icon, tray.tooltip)
		return 0
	}
	result, _, _ := procDefWindowProcW.Call(window, messageId, wParam, lParam)
	return result
}

// handleTrayEvent 处理 NOTIFYICON_VERSION_4 的回调：单击、双击、右键菜单。
// 设置了双击动作时，单击要等双击判定时间过去才执行，避免双击同时触发单击。
func (tray *Tray) handleTrayEvent(event uint32, anchor uintptr) {
	switch event {
	case ninSelect:
		if time.Now().Before(tray.ignoreSelectUntil) {
			return
		}
		if !tray.DoubleClickEnabled {
			tray.handler.onTrayClick()
			return
		}
		if !tray.clickPending {
			tray.clickPending = true
			procSetTimer.Call(tray.window, clickTimerId, uintptr(doubleClickTime().Milliseconds()), 0)
		}
	case ninKeySelect:
		tray.handler.onTrayClick()
	case wmLButtonDblClk:
		tray.ignoreSelectUntil = time.Now().Add(doubleClickTime())
		if tray.DoubleClickEnabled {
			procKillTimer.Call(tray.window, clickTimerId)
			tray.clickPending = false
			tray.handler.onTrayDoubleClick()
		}
	case wmContextMenu:
		tray.handler.onTrayMenu(point{x: int32(int16(anchor & 0xFFFF)), y: int32(int16((anchor >> 16) & 0xFFFF))})
	}
}
