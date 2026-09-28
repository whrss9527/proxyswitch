//go:build windows

package main

import (
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"syscall"
	"time"
	"unsafe"
)

// 商店应用的回环豁免：列出应用和读取豁免不需要管理员权限；修改要管理员权限，由以管理员身份运行的
// ProxySwitch.exe --loopback-exempt=文件 完成（文件里每行一个 SID，是修改后全部有豁免的应用）。

var (
	firewallApi                               = syscall.NewLazyDLL(systemDir() + `\FirewallAPI.dll`)
	procNetworkIsolationEnumAppContainers     = firewallApi.NewProc("NetworkIsolationEnumAppContainers")
	procNetworkIsolationFreeAppContainers     = firewallApi.NewProc("NetworkIsolationFreeAppContainers")
	procNetworkIsolationGetAppContainerConfig = firewallApi.NewProc("NetworkIsolationGetAppContainerConfig")
	procNetworkIsolationSetAppContainerConfig = firewallApi.NewProc("NetworkIsolationSetAppContainerConfig")
	procConvertSidToStringSidW                = advapi32.NewProc("ConvertSidToStringSidW")
	procConvertStringSidToSidW                = advapi32.NewProc("ConvertStringSidToSidW")
	procLocalFree                             = kernel32.NewProc("LocalFree")
	shlwapi                                   = syscall.NewLazyDLL(systemDir() + `\shlwapi.dll`)
	procSHLoadIndirectString                  = shlwapi.NewProc("SHLoadIndirectString")
)

const (
	loopbackArgument = "--loopback-exempt="
	loopbackTimeout  = time.Minute
)

// inetFirewallAppContainer 是 INET_FIREWALL_APP_CONTAINER。
type inetFirewallAppContainer struct {
	appContainerSid  unsafe.Pointer
	userSid          unsafe.Pointer
	appContainerName *uint16
	displayName      *uint16
	description      *uint16
	capabilityCount  uint32
	capabilities     unsafe.Pointer
	binaryCount      uint32
	binaries         unsafe.Pointer
	workingDirectory *uint16
	packageFullName  *uint16
}

// sidAndAttributes 是 SID_AND_ATTRIBUTES。
type sidAndAttributes struct {
	sid        unsafe.Pointer
	attributes uint32
}

func sidToString(sid unsafe.Pointer) string {
	if sid == nil {
		return ""
	}
	var text *uint16
	if result, _, _ := procConvertSidToStringSidW.Call(uintptr(sid), uintptr(unsafe.Pointer(&text))); result == 0 {
		return ""
	}
	defer procLocalFree.Call(uintptr(unsafe.Pointer(text)))
	return utf16PointerToString(text)
}

// resolveIndirectString 把 @{包名?ms-resource://…} 这样的间接字符串换成当前语言的文字，换不了时返回空。
func resolveIndirectString(text string) string {
	if !strings.HasPrefix(text, "@") {
		return text
	}
	buffer := make([]uint16, 512)
	if result, _, _ := procSHLoadIndirectString.Call(uintptr(unsafe.Pointer(utf16Pointer(text))), uintptr(unsafe.Pointer(&buffer[0])), uintptr(len(buffer)), 0); result != 0 {
		return ""
	}
	return syscall.UTF16ToString(buffer)
}

// loopbackExemptSids 是现在有回环豁免的应用的 SID。返回的数组按文档要用 HeapFree 逐个释放，这里不释放：
// 只在打开设置页的列表和修改时调用，每次几百字节。
func loopbackExemptSids() ([]string, error) {
	var count uint32
	var array unsafe.Pointer
	if result, _, _ := procNetworkIsolationGetAppContainerConfig.Call(uintptr(unsafe.Pointer(&count)), uintptr(unsafe.Pointer(&array))); result != 0 {
		return nil, fmt.Errorf("读取商店应用的回环豁免失败：%v", syscall.Errno(result))
	}
	if array == nil || count == 0 {
		return nil, nil
	}
	sids := make([]string, 0, count)
	for _, item := range unsafe.Slice((*sidAndAttributes)(array), count) {
		if sid := sidToString(item.sid); sid != "" {
			sids = append(sids, sid)
		}
	}
	return sids, nil
}

// listLoopbackApps 列出这台电脑上的商店应用和它们有没有回环豁免。
func listLoopbackApps() (LoopbackInfo, error) {
	exempt, err := loopbackExemptSids()
	if err != nil {
		return LoopbackInfo{}, err
	}
	isExempt := map[string]bool{}
	for _, sid := range exempt {
		isExempt[strings.ToUpper(sid)] = true
	}
	var count uint32
	var array unsafe.Pointer
	if result, _, _ := procNetworkIsolationEnumAppContainers.Call(0, uintptr(unsafe.Pointer(&count)), uintptr(unsafe.Pointer(&array))); result != 0 {
		return LoopbackInfo{}, fmt.Errorf("列出商店应用失败：%v", syscall.Errno(result))
	}
	if array == nil {
		return LoopbackInfo{Apps: []LoopbackApp{}}, nil
	}
	defer procNetworkIsolationFreeAppContainers.Call(uintptr(array))
	apps := make([]LoopbackApp, 0, count)
	seen := map[string]bool{}
	for _, container := range unsafe.Slice((*inetFirewallAppContainer)(array), count) {
		sid := sidToString(container.appContainerSid)
		if sid == "" || seen[strings.ToUpper(sid)] {
			continue
		}
		seen[strings.ToUpper(sid)] = true
		packageName := utf16PointerToString(container.appContainerName)
		name := resolveIndirectString(utf16PointerToString(container.displayName))
		if name == "" {
			name = packageName
		}
		apps = append(apps, LoopbackApp{Sid: sid, Name: name, Package: packageName, Exempt: isExempt[strings.ToUpper(sid)]})
	}
	sortLoopbackApps(apps)
	return LoopbackInfo{Apps: apps}, nil
}

// setLoopbackExempt 让 sids 这些应用有回环豁免，其余的没有。要管理员权限。
func setLoopbackExempt(sids []string) error {
	items := make([]sidAndAttributes, 0, len(sids))
	defer func() {
		for _, item := range items {
			procLocalFree.Call(uintptr(item.sid))
		}
	}()
	for _, text := range sids {
		if !strings.HasPrefix(strings.ToUpper(text), appContainerSidPrefix) {
			return fmt.Errorf("%s 不是商店应用的 SID", text)
		}
		var sid unsafe.Pointer
		if result, _, err := procConvertStringSidToSidW.Call(uintptr(unsafe.Pointer(utf16Pointer(text))), uintptr(unsafe.Pointer(&sid))); result == 0 {
			return fmt.Errorf("SID %s 不对：%v", text, err)
		}
		items = append(items, sidAndAttributes{sid: sid})
	}
	result, _, _ := procNetworkIsolationSetAppContainerConfig.Call(uintptr(len(items)), uintptr(unsafe.Pointer(unsafe.SliceData(items))))
	runtime.KeepAlive(items)
	if result != 0 {
		return fmt.Errorf("设置回环豁免失败：%w", syscall.Errno(result))
	}
	return nil
}

// runLoopbackHelper 是以管理员身份运行的 ProxySwitch.exe --loopback-exempt=文件：按文件设置回环豁免后退出，
// 出错时把原因写在「文件.error」里。
func runLoopbackHelper(path string) int {
	data, err := os.ReadFile(path)
	if err == nil {
		err = setLoopbackExempt(strings.Fields(string(data)))
	}
	if err != nil {
		slog.Warn("设置商店应用的回环豁免失败", "err", err)
		_ = os.WriteFile(path+".error", []byte(err.Error()), 0o644)
		return exitFailure
	}
	return exitSuccess
}

// ---------- App ----------

// LoopbackApps 列出商店应用和它们能不能连接本机的代理。
func (app *App) LoopbackApps() (LoopbackInfo, error) {
	return listLoopbackApps()
}

// SetLoopback 让 exempt 这些已安装的应用可以连接本机的代理，其余已安装的应用不可以；要管理员确认一次。
func (app *App) SetLoopback(exempt []string) (LoopbackInfo, error) {
	info, err := listLoopbackApps()
	if err != nil {
		return info, err
	}
	current, err := loopbackExemptSids()
	if err != nil {
		return info, err
	}
	desired := mergeLoopback(current, info.Apps, exempt)
	if sameSids(desired, current) {
		return info, nil
	}
	executable, err := os.Executable()
	if err != nil {
		return info, err
	}
	request := filepath.Join(app.paths.Dir, "loopback-exempt.txt")
	defer os.Remove(request)
	defer os.Remove(request + ".error")
	_ = os.Remove(request + ".error")
	if err := os.WriteFile(request, []byte(strings.Join(desired, "\n")), 0o644); err != nil {
		return info, err
	}
	if err := runElevated(executable, loopbackArgument+`"`+request+`"`, loopbackTimeout); err != nil {
		var exit elevatedExitError
		if errors.As(err, &exit) {
			if reason, readErr := os.ReadFile(request + ".error"); readErr == nil && len(reason) > 0 {
				return info, errors.New(string(reason))
			}
		}
		return info, err
	}
	slog.Info("已修改商店应用的回环豁免", "exempt", len(desired))
	return listLoopbackApps()
}
