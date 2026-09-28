//go:build windows

package main

import (
	"path/filepath"
	"runtime"
	"testing"
	"time"
	"unsafe"
)

// TestToastNotifier 登记通知用的应用 ID，经 WinRT 显示一条带按钮的通知再收起，结束后恢复原来的登记。
func TestToastNotifier(t *testing.T) {
	requireRegistryTests(t)
	previousIcon := readRegistryString(hkeyCurrentUser, toastAppKey, "IconUri")
	t.Cleanup(func() {
		if previousIcon != "" {
			_ = registerToastApp(previousIcon)
		} else {
			_ = unregisterToastApp()
		}
	})
	icon := filepath.Join(t.TempDir(), "on.png")
	if err := writeIconPng(icon, trayIconStyle(iconStateOn, "")); err != nil {
		t.Fatal(err)
	}
	if err := registerToastApp(icon); err != nil {
		t.Fatal(err)
	}

	runtime.LockOSThread()
	defer runtime.UnlockOSThread()
	if result, _, _ := procRoInitialize.Call(roInitMultithreaded); hresultError(result) != nil {
		t.Fatal(hresultError(result))
	}
	if _, err := loadToastXml("<toast><visual>"); err == nil {
		t.Error("XML 有错误时应读不进来")
	}
	notifier, err := createToastNotifier()
	if err != nil {
		t.Fatal(err)
	}
	defer comRelease(notifier)
	// IToastNotifier.get_Setting：0 表示可以显示，其余是被用户、组策略等关掉了。
	var setting int32
	if err := comCall(notifier, 8, uintptr(unsafe.Pointer(&setting))); err != nil {
		t.Fatal(err)
	}
	t.Logf("通知设置：%d", setting)
	notice := Notice{Level: noticeWarning, Title: "ProxySwitch 测试", Text: "测试通知 <马上收起> & 恢复", Actions: []NoticeAction{{Label: "关闭代理", Link: turnOffLink()}}}
	toast, err := createToast(toastXml(notice, icon), "test")
	if err != nil {
		t.Fatal(err)
	}
	defer comRelease(toast)
	if err := comCall(notifier, methodShow, uintptr(toast)); err != nil {
		t.Fatal(err)
	}
	time.Sleep(500 * time.Millisecond)
	if err := comCall(notifier, methodHide, uintptr(toast)); err != nil {
		t.Logf("收起通知：%v", err)
	}
}

// TestToaster 经通知线程显示通知，到点收起；显示不了时应交给托盘气泡。
func TestToaster(t *testing.T) {
	requireRegistryTests(t)
	previousIcon := readRegistryString(hkeyCurrentUser, toastAppKey, "IconUri")
	t.Cleanup(func() {
		if previousIcon != "" {
			_ = registerToastApp(previousIcon)
		} else {
			_ = unregisterToastApp()
		}
	})
	fellBack := make(chan Notice, 4)
	toaster := newToaster(func(notice Notice, timeout time.Duration) { fellBack <- notice })
	toaster.iconDir = t.TempDir()
	done := make(chan bool)
	for _, notice := range []Notice{
		{Level: noticeInfo, Title: "ProxySwitch 测试", Text: "一秒后收起", Icon: iconStateOn, Color: "#2563eb"},
		{Level: noticeError, Title: "ProxySwitch 测试", Text: "出错的通知"},
	} {
		if !toaster.show(notice, time.Second) {
			t.Fatal("通知线程没有接收通知")
		}
	}
	toaster.requests <- func() { done <- len(toaster.shown) == 1 }
	if !<-done {
		t.Error("同一种的通知应该替换旧的")
	}
	time.Sleep(1500 * time.Millisecond)
	toaster.requests <- func() { done <- len(toaster.shown) == 0 }
	if !<-done {
		t.Error("到点后应收起通知")
	}
	select {
	case notice := <-fellBack:
		t.Fatalf("通知改用了托盘气泡：%s", notice.Text)
	default:
	}
	if toaster.failed.Load() {
		t.Fatal("系统通知不应被标为不可用")
	}
	for _, name := range []string{"on-2563eb.png", "error.png"} {
		if !fileExists(filepath.Join(toaster.iconDir, name)) {
			t.Errorf("没有生成通知图标 %s", name)
		}
	}
}
