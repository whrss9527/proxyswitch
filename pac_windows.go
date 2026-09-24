//go:build windows

package main

import (
	"errors"
	"fmt"
	"strings"
	"syscall"
	"time"
	"unsafe"
)

// 用 WinHTTP 执行 PAC 脚本，得到它为某个网址选择的代理，测速时按这个代理实际访问。

const (
	winHttpAccessTypeNoProxy    = 1
	winHttpAccessTypeNamedProxy = 3

	winHttpAutoProxyConfigUrl      = 0x00000002
	winHttpAutoProxyNoCacheClient  = 0x00080000
	winHttpAutoProxyNoCacheService = 0x00100000

	errorWinHttpTimeout              = 12002
	errorWinHttpInvalidUrl           = 12005
	errorWinHttpUnrecognizedScheme   = 12006
	errorWinHttpBadAutoProxyScript   = 12166
	errorWinHttpUnableToDownload     = 12167
	errorWinHttpAutoProxyServiceFail = 12178
)

// pacProxyForUrl 返回 PAC 脚本为 targetUrl 选择的第一个代理（host:port），选择直连时返回空字符串。
func pacProxyForUrl(pacUrl, targetUrl string, timeout time.Duration) (string, error) {
	if err := procWinHttpGetProxyForUrl.Find(); err != nil {
		return "", errPacUnsupported
	}
	session, _, err := procWinHttpOpen.Call(uintptr(unsafe.Pointer(utf16Pointer(appName+"/"+appVersion))), winHttpAccessTypeNoProxy, 0, 0, 0)
	if session == 0 {
		return "", fmt.Errorf("WinHttpOpen：%v", err)
	}
	defer procWinHttpCloseHandle.Call(session)
	milliseconds := uintptr(timeout.Milliseconds())
	procWinHttpSetTimeouts.Call(session, milliseconds, milliseconds, milliseconds, milliseconds)

	options := winHttpAutoProxyOptions{
		// 脚本交给系统的自动代理服务执行（不在本进程里运行下载来的脚本），不用缓存的结果，修改 PAC 后马上能测到新的结果。
		flags:                 winHttpAutoProxyConfigUrl | winHttpAutoProxyNoCacheClient | winHttpAutoProxyNoCacheService,
		autoConfigUrl:         utf16Pointer(pacUrl),
		autoLogonIfChallenged: 1,
	}
	var info winHttpProxyInfo
	result, _, err := procWinHttpGetProxyForUrl.Call(session, uintptr(unsafe.Pointer(utf16Pointer(targetUrl))), uintptr(unsafe.Pointer(&options)), uintptr(unsafe.Pointer(&info)))
	if result == 0 {
		return "", describeWinHttpError(err)
	}
	proxy := utf16PointerToString(info.proxy)
	for _, pointer := range []*uint16{info.proxy, info.bypass} {
		if pointer != nil {
			procGlobalFree.Call(uintptr(unsafe.Pointer(pointer)))
		}
	}
	if info.accessType != winHttpAccessTypeNamedProxy {
		return "", nil
	}
	// 结果可能是用分号或空格分隔的多个代理，测第一个。
	fields := strings.FieldsFunc(proxy, func(char rune) bool { return char == ';' || char == ' ' })
	if len(fields) == 0 {
		return "", nil
	}
	return fields[0], nil
}

func describeWinHttpError(err error) error {
	var errno syscall.Errno
	if !errors.As(err, &errno) {
		return err
	}
	switch errno {
	case errorWinHttpBadAutoProxyScript:
		return errors.New("脚本有错误，或者没有返回有效的结果")
	case errorWinHttpUnableToDownload:
		return errors.New("WinHTTP 下载不了这个脚本")
	case errorWinHttpTimeout:
		return errors.New("下载或执行脚本超时")
	case errorWinHttpInvalidUrl, errorWinHttpUnrecognizedScheme:
		return errors.New("地址格式不被支持")
	case errorWinHttpAutoProxyServiceFail:
		return errors.New("系统的自动代理服务出错")
	}
	return fmt.Errorf("WinHTTP 错误 %d", uint32(errno))
}
