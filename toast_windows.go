//go:build windows

package main

import (
	"fmt"
	"image/png"
	"log/slog"
	"os"
	"path/filepath"
	"runtime"
	"sync/atomic"
	"syscall"
	"time"
	"unsafe"
)

// 系统通知在 Windows 上的部分：用 WinRT 的 ToastNotificationManager 显示 toast.go 写好的 XML。
// 没有打包的桌面程序要先在 HKCU\Software\Classes\AppUserModelId 下登记一个应用 ID（名字和图标），通知以这个
// 名字显示，在 Windows 设置的「通知」里也能单独开关。WinRT 的调用都在单独的线程里（多线程套间），不占用界面线程，
// 也不会在界面线程里重入消息循环。

const (
	toastAppId          = "whrss9527.ProxySwitch"
	toastAppKey         = `Software\Classes\AppUserModelId\` + toastAppId
	toastGroup          = "ProxySwitch"
	toastIconSize       = 96
	toastMinBuild       = 17763
	roInitMultithreaded = 1
)

var (
	// combase.dll 不在 Go 内置的系统 DLL 名单里，用 System32 的绝对路径加载。
	combase                    = syscall.NewLazyDLL(systemDir() + `\combase.dll`)
	procRoInitialize           = combase.NewProc("RoInitialize")
	procRoActivateInstance     = combase.NewProc("RoActivateInstance")
	procRoGetActivationFactory = combase.NewProc("RoGetActivationFactory")
	procWindowsCreateString    = combase.NewProc("WindowsCreateString")
	procWindowsDeleteString    = combase.NewProc("WindowsDeleteString")
)

// 用到的 WinRT 接口
var (
	iidXmlDocument                     = guid{0xF7F3A506, 0x1E87, 0x42D6, [8]byte{0xBC, 0xFB, 0xB8, 0xC8, 0x09, 0xFA, 0x54, 0x94}}
	iidXmlDocumentIO                   = guid{0x6CD0E74E, 0xEE65, 0x4489, [8]byte{0x9E, 0xBF, 0xCA, 0x43, 0xE8, 0x7B, 0xA6, 0x37}}
	iidToastNotificationManagerStatics = guid{0x50AC103F, 0xD235, 0x4598, [8]byte{0xBB, 0xEF, 0x98, 0xFE, 0x4D, 0x1A, 0x3A, 0xD4}}
	iidToastNotificationFactory        = guid{0x04124B20, 0x82C6, 0x4229, [8]byte{0xB1, 0x09, 0xFD, 0x9E, 0xD4, 0x66, 0x2B, 0x53}}
	iidToastNotification2              = guid{0x9DFB9FD1, 0x143A, 0x490E, [8]byte{0x90, 0xBF, 0xB9, 0xFB, 0xA7, 0x13, 0x2D, 0xE7}}
)

// 虚函数表里的位置：0~2 是 IUnknown 的方法，3~5 是 IInspectable 的，接口自己的从 6 开始。
const (
	methodLoadXml                   = 6 // IXmlDocumentIO
	methodCreateToastNotifierWithId = 7 // IToastNotificationManagerStatics
	methodCreateToastNotification   = 6 // IToastNotificationFactory
	methodPutTag                    = 6 // IToastNotification2
	methodPutGroup                  = 8 // IToastNotification2
	methodShow                      = 6 // IToastNotifier
	methodHide                      = 7 // IToastNotifier
)

// newHstring 创建 WinRT 字符串，用完用 deleteHstring 释放。
func newHstring(text string) (uintptr, error) {
	chars, err := syscall.UTF16FromString(text)
	if err != nil {
		return 0, err
	}
	var handle uintptr
	result, _, _ := procWindowsCreateString.Call(uintptr(unsafe.Pointer(&chars[0])), uintptr(len(chars)-1), uintptr(unsafe.Pointer(&handle)))
	return handle, hresultError(result)
}

func deleteHstring(handle uintptr) {
	if handle != 0 {
		procWindowsDeleteString.Call(handle)
	}
}

// activationFactory 取得 WinRT 类的静态方法或构造方法接口。
func activationFactory(class string, iid *guid) (unsafe.Pointer, error) {
	name, err := newHstring(class)
	if err != nil {
		return nil, err
	}
	defer deleteHstring(name)
	var factory unsafe.Pointer
	result, _, _ := procRoGetActivationFactory.Call(name, uintptr(unsafe.Pointer(iid)), uintptr(unsafe.Pointer(&factory)))
	if err := hresultError(result); err != nil {
		return nil, fmt.Errorf("%s：%w", class, err)
	}
	return factory, nil
}

// loadToastXml 把 toast 的 XML 读成 XmlDocument，返回它的 IXmlDocument 接口。
func loadToastXml(text string) (unsafe.Pointer, error) {
	class, err := newHstring("Windows.Data.Xml.Dom.XmlDocument")
	if err != nil {
		return nil, err
	}
	defer deleteHstring(class)
	var instance unsafe.Pointer
	result, _, _ := procRoActivateInstance.Call(class, uintptr(unsafe.Pointer(&instance)))
	if err := hresultError(result); err != nil {
		return nil, fmt.Errorf("XmlDocument：%w", err)
	}
	defer comRelease(instance)
	loader, err := comQuery(instance, &iidXmlDocumentIO)
	if err != nil {
		return nil, err
	}
	defer comRelease(loader)
	content, err := newHstring(text)
	if err != nil {
		return nil, err
	}
	defer deleteHstring(content)
	if err := comCall(loader, methodLoadXml, content); err != nil {
		return nil, fmt.Errorf("通知的 XML 有错误：%w", err)
	}
	return comQuery(instance, &iidXmlDocument)
}

// createToastNotifier 取得以 toastAppId 显示通知的 IToastNotifier。
func createToastNotifier() (unsafe.Pointer, error) {
	statics, err := activationFactory("Windows.UI.Notifications.ToastNotificationManager", &iidToastNotificationManagerStatics)
	if err != nil {
		return nil, err
	}
	defer comRelease(statics)
	appId, err := newHstring(toastAppId)
	if err != nil {
		return nil, err
	}
	defer deleteHstring(appId)
	var notifier unsafe.Pointer
	if err := comCall(statics, methodCreateToastNotifierWithId, appId, uintptr(unsafe.Pointer(&notifier))); err != nil {
		return nil, fmt.Errorf("CreateToastNotifier：%w", err)
	}
	return notifier, nil
}

// createToast 用 XML 创建通知，tag 相同的新通知会替换旧的。
func createToast(text, tag string) (unsafe.Pointer, error) {
	content, err := loadToastXml(text)
	if err != nil {
		return nil, err
	}
	defer comRelease(content)
	factory, err := activationFactory("Windows.UI.Notifications.ToastNotification", &iidToastNotificationFactory)
	if err != nil {
		return nil, err
	}
	defer comRelease(factory)
	var toast unsafe.Pointer
	if err := comCall(factory, methodCreateToastNotification, uintptr(content), uintptr(unsafe.Pointer(&toast))); err != nil {
		return nil, fmt.Errorf("CreateToastNotification：%w", err)
	}
	labels, err := comQuery(toast, &iidToastNotification2)
	if err == nil {
		defer comRelease(labels)
		for _, item := range []struct {
			method int
			value  string
		}{{methodPutTag, tag}, {methodPutGroup, toastGroup}} {
			value, _ := newHstring(item.value)
			err = comCall(labels, item.method, value)
			deleteHstring(value)
			if err != nil {
				break
			}
		}
	}
	if err != nil {
		comRelease(toast)
		return nil, err
	}
	return toast, nil
}

// registerToastApp 登记通知用的应用 ID：通知上显示的名字和图标。
func registerToastApp(icon string) error {
	key, err := createRegistryKey(hkeyCurrentUser, toastAppKey)
	if err != nil {
		return err
	}
	defer key.Close()
	if err := key.SetString("DisplayName", appName); err != nil {
		return err
	}
	return key.SetString("IconUri", icon)
}

// unregisterToastApp 取消登记（关闭网页链接时，这时通知改用托盘气泡）。
func unregisterToastApp() error {
	return deleteRegistryTree(hkeyCurrentUser, toastAppKey)
}

// toaster 在自己的线程里显示系统通知；显示不了时交给 fallback 改用托盘气泡，之后都用托盘气泡。
type toaster struct {
	requests   chan func()
	failed     atomic.Bool
	registered atomic.Bool
	fallback   func(Notice, time.Duration)
	iconDir    string

	// 以下只在通知线程里使用。
	notifier   unsafe.Pointer
	shown      map[string]shownToast
	generation int
	icons      map[string]string
}

// shownToast 是显示着的通知，generation 用来判断到点收起时它有没有被同一种的新通知替换。
type shownToast struct {
	toast      unsafe.Pointer
	generation int
}

func newToaster(fallback func(Notice, time.Duration)) *toaster {
	iconDir := ""
	if base, err := os.UserCacheDir(); err == nil {
		iconDir = filepath.Join(base, appName, "NotifyIcons")
	}
	toaster := &toaster{
		requests: make(chan func(), 16),
		fallback: fallback,
		iconDir:  iconDir,
		shown:    map[string]shownToast{},
		icons:    map[string]string{},
	}
	go toaster.loop()
	return toaster
}

func (toaster *toaster) loop() {
	runtime.LockOSThread()
	if result, _, _ := procRoInitialize.Call(roInitMultithreaded); hresultError(result) != nil {
		slog.Warn("系统通知不可用，改用托盘气泡", "err", hresultError(result))
		toaster.failed.Store(true)
	}
	for request := range toaster.requests {
		request()
	}
}

// show 显示通知，timeout 大于 0 时到点收起（带按钮的通知留在通知中心）。系统通知不可用时返回 false。
func (toaster *toaster) show(notice Notice, timeout time.Duration) bool {
	if toaster.failed.Load() {
		return false
	}
	select {
	case toaster.requests <- func() { toaster.display(notice, timeout) }:
		return true
	default:
		return false
	}
}

// flush 等通知线程处理完已经排队的通知，最多等 timeout。退出前调用，免得通知还没显示程序就退出了。
func (toaster *toaster) flush(timeout time.Duration) {
	done := make(chan struct{})
	select {
	case toaster.requests <- func() { close(done) }:
	default:
		return
	}
	select {
	case <-done:
	case <-time.After(timeout):
	}
}

// forget 在取消登记应用 ID 之后调用，下次显示前重新登记。
func (toaster *toaster) forget() {
	toaster.registered.Store(false)
}

func (toaster *toaster) display(notice Notice, timeout time.Duration) {
	err := toaster.prepare()
	if err == nil {
		err = toaster.showToast(notice, timeout)
	}
	if err != nil {
		slog.Warn("系统通知显示失败，改用托盘气泡", "err", err)
		toaster.failed.Store(true)
		toaster.fallback(notice, timeout)
	}
}

// prepare 登记应用 ID，第一次显示时创建 IToastNotifier。
func (toaster *toaster) prepare() error {
	if !toaster.registered.Load() {
		if err := registerToastApp(toaster.icon(iconStateOn, "")); err != nil {
			return fmt.Errorf("登记通知的应用 ID 失败：%w", err)
		}
		toaster.registered.Store(true)
	}
	if toaster.notifier == nil {
		notifier, err := createToastNotifier()
		if err != nil {
			return err
		}
		toaster.notifier = notifier
	}
	return nil
}

func (toaster *toaster) showToast(notice Notice, timeout time.Duration) error {
	tag := notice.Tag
	if tag == "" {
		tag = "notice"
	}
	toast, err := createToast(toastXml(notice, toaster.logo(notice)), tag)
	if err != nil {
		return err
	}
	if err := comCall(toaster.notifier, methodShow, uintptr(toast)); err != nil {
		comRelease(toast)
		return fmt.Errorf("显示通知失败：%w", err)
	}
	toaster.generation++
	if previous, ok := toaster.shown[tag]; ok {
		comRelease(previous.toast)
	}
	toaster.shown[tag] = shownToast{toast: toast, generation: toaster.generation}
	if timeout > 0 && len(notice.Actions) == 0 {
		generation := toaster.generation
		time.AfterFunc(timeout, func() {
			toaster.requests <- func() { toaster.hide(tag, generation) }
		})
	}
	return nil
}

// hide 收起到点的通知；已经被同一种的新通知替换时不动。
func (toaster *toaster) hide(tag string, generation int) {
	shown, ok := toaster.shown[tag]
	if !ok || shown.generation != generation {
		return
	}
	delete(toaster.shown, tag)
	_ = comCall(toaster.notifier, methodHide, uintptr(shown.toast))
	comRelease(shown.toast)
}

// logo 是通知的图标：信息类通知用开关状态和配置的颜色，警告和错误用带红色的开关；其余用登记的程序图标。
func (toaster *toaster) logo(notice Notice) string {
	switch {
	case notice.Level == noticeError:
		return toaster.icon(iconStateError, "")
	case notice.Level == noticeWarning:
		return toaster.icon(iconStateWarn, notice.Color)
	case notice.Icon != "":
		return toaster.icon(notice.Icon, notice.Color)
	}
	return ""
}

// icon 把开关图标画成 PNG 文件，返回路径；每次运行第一次用到时重画，程序更新后图标也跟着更新。
func (toaster *toaster) icon(state, color string) string {
	if toaster.iconDir == "" {
		return ""
	}
	name := state
	if accent, ok := parseHexColor(color); ok {
		name += fmt.Sprintf("-%02x%02x%02x", accent.R, accent.G, accent.B)
	}
	if path, ok := toaster.icons[name]; ok {
		return path
	}
	path := filepath.Join(toaster.iconDir, name+".png")
	if err := writeIconPng(path, trayIconStyle(state, color)); err != nil && !fileExists(path) {
		// 文件还在（例如正被系统读着换不掉）时继续用上次画的。
		slog.Warn("生成通知图标失败", "err", err)
		path = ""
	}
	toaster.icons[name] = path
	return path
}

func writeIconPng(path string, style iconStyle) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	temporary := path + ".tmp"
	file, err := os.Create(temporary)
	if err != nil {
		return err
	}
	err = png.Encode(file, renderToggleIcon(toastIconSize, style))
	if closeErr := file.Close(); err == nil {
		err = closeErr
	}
	if err == nil {
		err = os.Rename(temporary, path)
	}
	if err != nil {
		os.Remove(temporary)
	}
	return err
}
