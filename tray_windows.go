//go:build windows

package main

import (
	"encoding/binary"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"syscall"
	"time"
	"unsafe"
)

// 托盘图标 + 隐藏窗口 + 消息循环。所有 UI 相关操作都在主线程（已 LockOSThread）执行。

const (
	trayIconID  = 1
	hotkeyID    = 1
	statusTimer = 1
	className   = "ProxySwitchTrayWindow"
)

// MenuItem 是右键菜单的一项。
type MenuItem struct {
	ID        uint32
	Text      string
	Checked   bool
	Radio     bool // 用圆点而不是对勾表示选中
	Disabled  bool
	Default   bool // 加粗显示（默认项）
	Separator bool
}

type Tray struct {
	hwnd           uintptr
	hInst          uintptr
	iconOn         uintptr
	iconOff        uintptr
	nid            notifyIconDataW
	taskbarCreated uint32
	skipUpUntil    time.Time
	on             bool
	tip            string
	hotkeyOn       bool
	wndProcCB      uintptr
	classNamePtr   *uint16
	tempDir        string
	uiThread       uint32
	uiQueue        chan func()

	OnLeftClick  func()
	OnRightClick func()
	OnHotkey     func()
	OnTimer      func()
	OnQuit       func()
}

func newTray(iconOnData, iconOffData []byte, tempDir string) (*Tray, error) {
	t := &Tray{hInst: moduleHandle(), tempDir: tempDir, uiQueue: make(chan func(), 64)}
	tid, _, _ := procGetCurrentThreadId.Call()
	t.uiThread = uint32(tid)
	t.wndProcCB = syscall.NewCallback(t.wndProc)
	t.classNamePtr = utf16Ptr(className)

	wc := wndClassExW{
		lpfnWndProc:   t.wndProcCB,
		hInstance:     t.hInst,
		lpszClassName: t.classNamePtr,
	}
	wc.cbSize = uint32(unsafe.Sizeof(wc))
	if atom, _, e := procRegisterClassExW.Call(uintptr(unsafe.Pointer(&wc))); atom == 0 {
		return nil, fmt.Errorf("RegisterClassEx: %v", e)
	}

	hwnd, _, e := procCreateWindowExW.Call(0,
		uintptr(unsafe.Pointer(t.classNamePtr)),
		uintptr(unsafe.Pointer(utf16Ptr(appName))),
		wsOverlapped,
		cwUseDefault, cwUseDefault, cwUseDefault, cwUseDefault,
		0, 0, t.hInst, 0)
	if hwnd == 0 {
		return nil, fmt.Errorf("CreateWindowEx: %v", e)
	}
	t.hwnd = hwnd

	msgID, _, _ := procRegisterWindowMessageW.Call(uintptr(unsafe.Pointer(utf16Ptr("TaskbarCreated"))))
	t.taskbarCreated = uint32(msgID)

	cx, _, _ := procGetSystemMetrics.Call(smCxSmIcon)
	cy, _, _ := procGetSystemMetrics.Call(smCySmIcon)
	if cx == 0 || cy == 0 {
		cx, cy = 16, 16
	}
	var err error
	if t.iconOn, err = t.loadIcon(iconOnData, "on", int32(cx), int32(cy)); err != nil {
		return nil, fmt.Errorf("加载图标失败: %v", err)
	}
	if t.iconOff, err = t.loadIcon(iconOffData, "off", int32(cx), int32(cy)); err != nil {
		return nil, fmt.Errorf("加载图标失败: %v", err)
	}

	t.nid = notifyIconDataW{
		hWnd:             hwnd,
		uID:              trayIconID,
		uCallbackMessage: wmTrayCallback,
	}
	t.nid.cbSize = uint32(unsafe.Sizeof(t.nid))
	t.tip = appName
	if err := t.addIcon(); err != nil {
		return nil, err
	}
	return t, nil
}

func (t *Tray) addIcon() error {
	nid := t.nid
	nid.uFlags = nifMessage | nifIcon | nifTip | nifShowTip
	nid.hIcon = t.currentIcon()
	copyUTF16(nid.szTip[:], t.tip)
	r, _, e := procShellNotifyIconW.Call(nimAdd, uintptr(unsafe.Pointer(&nid)))
	if r == 0 {
		return fmt.Errorf("Shell_NotifyIcon(NIM_ADD): %v", e)
	}
	return nil
}

func (t *Tray) removeIcon() {
	nid := t.nid
	procShellNotifyIconW.Call(nimDelete, uintptr(unsafe.Pointer(&nid)))
}

func (t *Tray) currentIcon() uintptr {
	if t.on {
		return t.iconOn
	}
	return t.iconOff
}

// SetState 更新图标（开/关）与悬停提示。
func (t *Tray) SetState(on bool, tip string) {
	t.on = on
	t.tip = tip
	nid := t.nid
	nid.uFlags = nifIcon | nifTip | nifShowTip
	nid.hIcon = t.currentIcon()
	copyUTF16(nid.szTip[:], tip)
	procShellNotifyIconW.Call(nimModify, uintptr(unsafe.Pointer(&nid)))
}

// Notify 弹出气泡/Toast 通知。kind 为 niifInfo / niifWarning / niifError。
func (t *Tray) Notify(title, text string, kind uint32) {
	nid := t.nid
	nid.uFlags = nifInfo
	nid.dwInfoFlags = kind | niifRespectQuietTime
	copyUTF16(nid.szInfoTitle[:], title)
	copyUTF16(nid.szInfo[:], text)
	procShellNotifyIconW.Call(nimModify, uintptr(unsafe.Pointer(&nid)))
}

func (t *Tray) RegisterHotkey(hk Hotkey) error {
	t.UnregisterHotkey()
	r, _, e := procRegisterHotKey.Call(t.hwnd, hotkeyID, uintptr(hk.Mods|modNoRepeat), uintptr(hk.VK))
	if r == 0 {
		// 旧系统不认识 MOD_NOREPEAT，再试一次
		r, _, e = procRegisterHotKey.Call(t.hwnd, hotkeyID, uintptr(hk.Mods), uintptr(hk.VK))
	}
	if r == 0 {
		return fmt.Errorf("RegisterHotKey(%s): %v", hk.Text, e)
	}
	t.hotkeyOn = true
	return nil
}

func (t *Tray) UnregisterHotkey() {
	if t.hotkeyOn {
		procUnregisterHotKey.Call(t.hwnd, hotkeyID)
		t.hotkeyOn = false
	}
}

func (t *Tray) StartTimer(intervalMs uint32) {
	procSetTimer.Call(t.hwnd, statusTimer, uintptr(intervalMs), 0)
}

// ShowMenu 在鼠标位置弹出菜单，返回用户点击的项 ID（0 表示没有选择）。
func (t *Tray) ShowMenu(items []MenuItem) uint32 {
	hMenu, _, _ := procCreatePopupMenu.Call()
	if hMenu == 0 {
		return 0
	}
	defer procDestroyMenu.Call(hMenu)

	var keep []*uint16 // 防止字符串在 TrackPopupMenu 前被回收
	for _, it := range items {
		if it.Separator {
			procAppendMenuW.Call(hMenu, mfSeparator, 0, 0)
			continue
		}
		flags := uintptr(mfString)
		if it.Disabled {
			flags |= mfGrayed
		}
		if it.Checked && !it.Radio {
			flags |= mfChecked
		}
		p := utf16Ptr(it.Text)
		keep = append(keep, p)
		procAppendMenuW.Call(hMenu, flags, uintptr(it.ID), uintptr(unsafe.Pointer(p)))
	}
	for _, it := range items {
		if it.Radio && it.Checked {
			procCheckMenuRadioItem.Call(hMenu, uintptr(it.ID), uintptr(it.ID), uintptr(it.ID), mfByCommand)
		}
		if it.Default {
			procSetMenuDefaultItem.Call(hMenu, uintptr(it.ID), 0)
		}
	}

	var pt point
	procGetCursorPos.Call(uintptr(unsafe.Pointer(&pt)))
	// 必须先把窗口设为前台，否则点击菜单外部时菜单不会自动消失
	procSetForegroundWindow.Call(t.hwnd)
	cmd, _, _ := procTrackPopupMenu.Call(hMenu,
		tpmLeftAlign|tpmBottomAlign|tpmRightButton|tpmReturnCmd|tpmNoNotify,
		uintptr(pt.x), uintptr(pt.y), 0, t.hwnd, 0)
	// 微软文档推荐：弹出菜单结束后发一个空消息，避免菜单“粘住”
	procPostMessageW.Call(t.hwnd, wmNull, 0, 0)
	runtime.KeepAlive(keep)
	return uint32(cmd)
}

// RunOnUI 让 fn 在创建窗口的线程（消息循环所在线程）上执行，并等待其完成。
// 设置页面的 HTTP 处理函数跑在别的 goroutine 上，涉及托盘/快捷键/状态的操作都要经由这里。
func (t *Tray) RunOnUI(fn func()) error {
	tid, _, _ := procGetCurrentThreadId.Call()
	if uint32(tid) == t.uiThread {
		fn()
		return nil
	}
	done := make(chan struct{})
	select {
	case t.uiQueue <- func() { defer close(done); fn() }:
	case <-time.After(5 * time.Second):
		return fmt.Errorf("程序正忙，请稍后再试")
	}
	procPostMessageW.Call(t.hwnd, wmRunOnUI, 0, 0)
	select {
	case <-done:
		return nil
	case <-time.After(15 * time.Second):
		return fmt.Errorf("程序没有响应，请稍后再试")
	}
}

func (t *Tray) drainUIQueue() {
	for {
		select {
		case fn := <-t.uiQueue:
			fn()
		default:
			return
		}
	}
}

// Run 进入消息循环，直到窗口被销毁。
func (t *Tray) Run() {
	var m msgW
	for {
		r, _, _ := procGetMessageW.Call(uintptr(unsafe.Pointer(&m)), 0, 0, 0)
		if int32(r) <= 0 { // 0 = WM_QUIT，-1 = 出错
			break
		}
		procTranslateMessage.Call(uintptr(unsafe.Pointer(&m)))
		procDispatchMessageW.Call(uintptr(unsafe.Pointer(&m)))
	}
}

// Quit 销毁窗口，消息循环随之退出。
func (t *Tray) Quit() {
	procDestroyWindow.Call(t.hwnd)
}

func (t *Tray) wndProc(hwnd, msg, wParam, lParam uintptr) uintptr {
	switch msg {
	case wmTrayCallback:
		switch lParam {
		case wmLButtonUp:
			if time.Now().Before(t.skipUpUntil) {
				t.skipUpUntil = time.Time{}
			} else if t.OnLeftClick != nil {
				t.OnLeftClick()
			}
		case wmLButtonDblClk:
			// 双击序列为 DOWN, UP, DBLCLK, UP：第一个 UP 已经切换过了，跳过紧随其后的第二个 UP
			t.skipUpUntil = time.Now().Add(700 * time.Millisecond)
		case ninSelect, ninKeySelect:
			// 键盘（回车/空格）选中托盘图标
			if t.OnLeftClick != nil {
				t.OnLeftClick()
			}
		case wmRButtonUp, wmContextMenu:
			if t.OnRightClick != nil {
				t.OnRightClick()
			}
		}
		return 0
	case wmRunOnUI:
		t.drainUIQueue()
		return 0
	case wmHotkey:
		if wParam == hotkeyID && t.OnHotkey != nil {
			t.OnHotkey()
		}
		return 0
	case wmTimer:
		if wParam == statusTimer && t.OnTimer != nil {
			t.OnTimer()
		}
		return 0
	case wmClose:
		procDestroyWindow.Call(hwnd)
		return 0
	case wmDestroy:
		procKillTimer.Call(hwnd, statusTimer)
		t.UnregisterHotkey()
		t.removeIcon()
		if t.OnQuit != nil {
			t.OnQuit()
		}
		procPostQuitMessage.Call(0)
		return 0
	}
	if t.taskbarCreated != 0 && uint32(msg) == t.taskbarCreated {
		// explorer.exe 重启后托盘图标会丢失，重新添加
		_ = t.addIcon()
		return 0
	}
	r, _, _ := procDefWindowProcW.Call(hwnd, msg, wParam, lParam)
	return r
}

// ---------- 图标加载 ----------

// loadIcon 从内存中的 .ico 数据创建 HICON；失败时退回到写临时文件 + LoadImage。
func (t *Tray) loadIcon(ico []byte, name string, cx, cy int32) (uintptr, error) {
	if h, err := iconFromICOBytes(ico, cx, cy); err == nil {
		return h, nil
	}
	if t.tempDir == "" {
		t.tempDir = os.TempDir()
	}
	_ = os.MkdirAll(t.tempDir, 0o755)
	path := filepath.Join(t.tempDir, "tray-"+name+".ico")
	if err := os.WriteFile(path, ico, 0o644); err != nil {
		return 0, err
	}
	h, _, e := procLoadImageW.Call(0,
		uintptr(unsafe.Pointer(utf16Ptr(path))),
		imageIcon, uintptr(cx), uintptr(cy), lrLoadFromFile)
	if h == 0 {
		return 0, fmt.Errorf("LoadImage(%s): %v", path, e)
	}
	return h, nil
}

// iconFromICOBytes 把 .ico 文件的目录转换成资源目录格式，让系统挑选最合适的尺寸，再由该帧创建图标。
func iconFromICOBytes(ico []byte, cx, cy int32) (uintptr, error) {
	if len(ico) < 6 {
		return 0, fmt.Errorf("ico 数据过短")
	}
	count := int(binary.LittleEndian.Uint16(ico[4:6]))
	if count == 0 || len(ico) < 6+16*count {
		return 0, fmt.Errorf("ico 目录损坏")
	}
	// GRPICONDIR：头 6 字节 + 每项 14 字节（前 12 字节与 ICONDIRENTRY 相同，最后 2 字节是资源 ID）
	grp := make([]byte, 6+14*count)
	copy(grp[:6], ico[:6])
	type frame struct{ off, size uint32 }
	frames := make([]frame, count)
	for i := 0; i < count; i++ {
		src := ico[6+16*i : 6+16*i+16]
		dst := grp[6+14*i : 6+14*i+14]
		copy(dst[:12], src[:12])
		binary.LittleEndian.PutUint16(dst[12:14], uint16(i+1))
		frames[i] = frame{
			size: binary.LittleEndian.Uint32(src[8:12]),
			off:  binary.LittleEndian.Uint32(src[12:16]),
		}
	}
	id, _, _ := procLookupIconIdFromDirectoryEx.Call(uintptr(unsafe.Pointer(&grp[0])), 1,
		uintptr(cx), uintptr(cy), lrDefaultColor)
	idx := int(id) - 1
	if idx < 0 || idx >= count {
		idx = 0
	}
	f := frames[idx]
	if uint64(f.off)+uint64(f.size) > uint64(len(ico)) || f.size == 0 {
		return 0, fmt.Errorf("ico 帧越界")
	}
	img := ico[f.off : f.off+f.size]
	h, _, e := procCreateIconFromResourceEx.Call(uintptr(unsafe.Pointer(&img[0])), uintptr(len(img)),
		1, 0x00030000, uintptr(cx), uintptr(cy), lrDefaultColor)
	if h == 0 {
		return 0, fmt.Errorf("CreateIconFromResourceEx: %v", e)
	}
	return h, nil
}
