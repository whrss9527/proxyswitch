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

// 以管理员身份运行程序：放行防火墙、设置商店应用的回环豁免等需要管理员权限的操作。

var (
	procGetExitCodeProcess = kernel32.NewProc("GetExitCodeProcess")
	procShellExecuteExW    = shell32.NewProc("ShellExecuteExW")
)

const (
	seeMaskNoCloseProcess = 0x00000040
	seeMaskNoAsync        = 0x00000100
	swHide                = 0
	errorCancelled        = 1223
)

var (
	errElevationCancelled = errors.New("没有获得管理员权限：需要在弹出的确认框里点「是」")
	errElevatedTimeout    = errors.New("等待超时")
)

// elevatedExitError 表示以管理员身份运行的程序退出码不是 0。
type elevatedExitError struct {
	code uint32
}

func (err elevatedExitError) Error() string {
	return fmt.Sprintf("退出码 %d", err.code)
}

// shellExecuteInfo 是 SHELLEXECUTEINFOW。
type shellExecuteInfo struct {
	size       uint32
	mask       uint32
	window     uintptr
	verb       *uint16
	file       *uint16
	parameters *uint16
	directory  *uint16
	show       int32
	instApp    uintptr
	idList     uintptr
	class      *uint16
	classKey   uintptr
	hotKey     uint32
	icon       uintptr
	process    uintptr
}

// shellExecuteElevated 以管理员身份启动程序（弹出用户账户控制的确认），返回它的进程句柄（可能为 0），由调用方关闭。
// ShellExecuteEx 要求调用的线程初始化了 COM，所以在一个专用线程上执行，结束后这个线程随之退出。
func shellExecuteElevated(file, parameters string) (uintptr, error) {
	type started struct {
		process uintptr
		err     error
	}
	done := make(chan started, 1)
	go func() {
		runtime.LockOSThread()
		coInitialize()
		info := shellExecuteInfo{
			mask:       seeMaskNoCloseProcess | seeMaskNoAsync,
			verb:       utf16Pointer("runas"),
			file:       utf16Pointer(file),
			parameters: utf16Pointer(parameters),
			show:       swHide,
		}
		info.size = uint32(unsafe.Sizeof(info))
		if result, _, err := procShellExecuteExW.Call(uintptr(unsafe.Pointer(&info))); result == 0 {
			if errno, ok := err.(syscall.Errno); ok && errno == errorCancelled {
				done <- started{err: errElevationCancelled}
				return
			}
			done <- started{err: fmt.Errorf("无法以管理员身份运行：%v", err)}
			return
		}
		done <- started{process: info.process}
	}()
	result := <-done
	return result.process, result.err
}

// runElevated 以管理员身份运行程序（弹出用户账户控制的确认），等它结束，退出码不是 0 时返回 elevatedExitError。
func runElevated(file, parameters string, timeout time.Duration) error {
	process, err := shellExecuteElevated(file, parameters)
	if err != nil || process == 0 {
		return err
	}
	defer procCloseHandle.Call(process)
	if wait, _, _ := procWaitForSingleObject.Call(process, uintptr(timeout.Milliseconds())); wait != 0 {
		return errElevatedTimeout
	}
	var code uint32
	procGetExitCodeProcess.Call(process, uintptr(unsafe.Pointer(&code)))
	if code != 0 {
		return elevatedExitError{code}
	}
	return nil
}
