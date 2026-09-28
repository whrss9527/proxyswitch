//go:build windows

package main

import (
	"sort"
	"strings"
	"syscall"
	"unsafe"
)

// 正在运行的程序：给「按程序分流」的自定义规则选程序名用。

var (
	procCreateToolhelp32Snapshot = kernel32.NewProc("CreateToolhelp32Snapshot")
	procProcess32FirstW          = kernel32.NewProc("Process32FirstW")
	procProcess32NextW           = kernel32.NewProc("Process32NextW")
)

const th32csSnapProcess = 0x00000002

// processEntry32 是 PROCESSENTRY32W。
type processEntry32 struct {
	size            uint32
	usage           uint32
	processId       uint32
	defaultHeapId   uintptr
	moduleId        uint32
	threads         uint32
	parentProcessId uint32
	priClassBase    int32
	flags           uint32
	exeFile         [260]uint16
}

// systemPrograms 是系统自己的进程，不会经代理上网，不列出来。
var systemPrograms = map[string]bool{
	"[system process]": true, "system": true, "registry": true, "secure system": true, "memory compression": true,
	"smss.exe": true, "csrss.exe": true, "wininit.exe": true, "winlogon.exe": true, "services.exe": true, "lsass.exe": true,
	"svchost.exe": true, "fontdrvhost.exe": true, "dwm.exe": true, "conhost.exe": true, "sihost.exe": true, "taskhostw.exe": true,
	"ctfmon.exe": true, "dllhost.exe": true, "runtimebroker.exe": true, "wudfhost.exe": true, "spoolsv.exe": true,
}

// runningPrograms 列出正在运行的程序的名字（例如 chrome.exe），去掉重复和系统进程，按名字排序。
func runningPrograms() []string {
	snapshot, _, _ := procCreateToolhelp32Snapshot.Call(th32csSnapProcess, 0)
	if snapshot == 0 || snapshot == uintptr(syscall.InvalidHandle) {
		return []string{}
	}
	defer procCloseHandle.Call(snapshot)
	seen := map[string]bool{}
	names := []string{}
	entry := processEntry32{}
	entry.size = uint32(unsafe.Sizeof(entry))
	for result, _, _ := procProcess32FirstW.Call(snapshot, uintptr(unsafe.Pointer(&entry))); result != 0; result, _, _ = procProcess32NextW.Call(snapshot, uintptr(unsafe.Pointer(&entry))) {
		name := syscall.UTF16ToString(entry.exeFile[:])
		key := strings.ToLower(name)
		if name == "" || seen[key] || systemPrograms[key] {
			continue
		}
		seen[key] = true
		names = append(names, name)
	}
	sort.Slice(names, func(i, j int) bool { return strings.ToLower(names[i]) < strings.ToLower(names[j]) })
	return names
}

// RunningPrograms 是正在运行的程序，给按程序分流的规则选程序名。
func (app *App) RunningPrograms() []string {
	return runningPrograms()
}
