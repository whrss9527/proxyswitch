//go:build windows

package main

import (
	"errors"
	"fmt"
	"log/slog"
	"time"
	"unsafe"
)

// WinHTTP 的代理在 Windows 上的部分：用 WinHttpGetDefaultProxyConfiguration 读取（不需要管理员权限），
// 以管理员身份运行 netsh winhttp 修改（弹出一次用户账户控制的确认）。

const winHttpTimeout = 2 * time.Minute

var procWinHttpGetDefaultProxyConfiguration = winhttp.NewProc("WinHttpGetDefaultProxyConfiguration")

// readWinHttpProxy 读取 WinHTTP 的代理，直接连接时 proxy 为空。
func readWinHttpProxy() (proxy, bypass string, err error) {
	var info winHttpProxyInfo
	if result, _, callErr := procWinHttpGetDefaultProxyConfiguration.Call(uintptr(unsafe.Pointer(&info))); result == 0 {
		return "", "", fmt.Errorf("读取 WinHTTP 的代理失败：%v", callErr)
	}
	proxy, bypass = utf16PointerToString(info.proxy), utf16PointerToString(info.bypass)
	for _, pointer := range []*uint16{info.proxy, info.bypass} {
		if pointer != nil {
			procGlobalFree.Call(uintptr(unsafe.Pointer(pointer)))
		}
	}
	if info.accessType != winHttpAccessTypeNamedProxy {
		return "", "", nil
	}
	return proxy, bypass, nil
}

// writeWinHttpProxy 以管理员身份运行 netsh 设置 WinHTTP 的代理，proxy 为空时改为直接连接。
func writeWinHttpProxy(proxy, bypass string) error {
	parameters := "winhttp reset proxy"
	if proxy != "" {
		parameters = fmt.Sprintf(`winhttp set proxy proxy-server="%s"`, proxy)
		if bypass != "" {
			parameters += fmt.Sprintf(` bypass-list="%s"`, bypass)
		}
	}
	if err := runElevated(systemDir()+`\netsh.exe`, parameters, winHttpTimeout); err != nil {
		var exit elevatedExitError
		switch {
		case errors.As(err, &exit):
			return fmt.Errorf("netsh 没有执行成功（退出码 %d）", exit.code)
		case errors.Is(err, errElevatedTimeout):
			return errors.New("等待超时，请稍后在「诊断」页查看 WinHTTP 的代理")
		}
		return err
	}
	return nil
}

// ---------- App ----------

// WinHttpInfo 是 WinHTTP 的代理和现在可以设给它的代理。
func (app *App) WinHttpInfo() WinHttpInfo {
	var info WinHttpInfo
	var err error
	info.Proxy, info.Bypass, err = readWinHttpProxy()
	if err != nil {
		info.Error = err.Error()
	}
	_ = app.tray.RunOnUi(func() { info.Suggested, info.SuggestedBypass, info.Unsupported = app.engine.WinHttpSuggestion() })
	return info
}

// SetWinHttp 把 WinHTTP 的代理设为当前代理（useProxy）或改回直连，要管理员确认一次。
func (app *App) SetWinHttp(useProxy bool) (WinHttpInfo, error) {
	info := app.WinHttpInfo()
	proxy, bypass := "", ""
	if useProxy {
		if info.Suggested == "" {
			return info, errors.New(info.Unsupported)
		}
		proxy, bypass = info.Suggested, info.SuggestedBypass
	}
	if err := writeWinHttpProxy(proxy, bypass); err != nil {
		return info, err
	}
	slog.Info("已修改 WinHTTP 的代理", "proxy", proxy)
	return app.WinHttpInfo(), nil
}

// warnWinHttpOnExit 在从托盘菜单退出前提醒：WinHTTP 还指向内置内核，内核随 ProxySwitch 退出后系统服务会连不上。
// 系统通知留在通知中心，点它会重新打开 ProxySwitch 的系统集成页。
func (app *App) warnWinHttpOnExit() {
	proxy, _, err := readWinHttpProxy()
	if err != nil || !app.engine.WinHttpUsesCore(proxy) {
		return
	}
	app.notify(Notice{Level: noticeWarning, Title: "WinHTTP 还在使用内置内核的代理", Text: "ProxySwitch 退出后 " + proxy + " 就用不了了，Windows 更新等系统服务可能连不上。点这里打开 ProxySwitch 改回直连", Page: "system"})
	if app.toasts != nil {
		app.toasts.flush(2 * time.Second)
	}
}
