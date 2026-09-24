//go:build windows

package main

import (
	"log/slog"
	"os"
	"os/exec"
	"sync"
	"syscall"
	"unsafe"
)

// 在 Windows 上启动内核：不显示控制台窗口；把内核放进一个“关闭即结束”的作业对象，
// ProxySwitch 退出或崩溃时作业对象的句柄随进程关闭，Windows 会一起结束内核，不会留下没人管的进程。

func prepareCoreCommand(command *exec.Cmd) {
	command.SysProcAttr = &syscall.SysProcAttr{HideWindow: true, CreationFlags: createNoWindow}
}

var coreJob struct {
	once   sync.Once
	handle uintptr
}

func attachCoreProcess(process *os.Process) {
	coreJob.once.Do(func() { coreJob.handle = createKillOnCloseJob() })
	if coreJob.handle == 0 {
		return
	}
	handle, _, err := procOpenProcess.Call(processSetQuota|processTerminate, 0, uintptr(process.Pid))
	if handle == 0 {
		slog.Warn("无法打开内核进程", "err", err)
		return
	}
	defer procCloseHandle.Call(handle)
	if ok, _, err := procAssignProcessToJobObject.Call(coreJob.handle, handle); ok == 0 {
		slog.Warn("无法把内核放进作业对象，ProxySwitch 意外退出时内核可能留在后台", "err", err)
	}
}

// createKillOnCloseJob 创建作业对象，最后一个句柄关闭时结束其中所有进程。句柄一直不关，随 ProxySwitch 退出释放。
func createKillOnCloseJob() uintptr {
	job, _, err := procCreateJobObjectW.Call(0, 0)
	if job == 0 {
		slog.Warn("无法创建作业对象", "err", err)
		return 0
	}
	info := jobObjectExtendedLimitInformation{limitFlags: jobObjectLimitKillOnJobClose}
	if ok, _, err := procSetInformationJobObject.Call(job, jobObjectExtendedLimitInformationClass, uintptr(unsafe.Pointer(&info)), unsafe.Sizeof(info)); ok == 0 {
		slog.Warn("无法设置作业对象", "err", err)
		procCloseHandle.Call(job)
		return 0
	}
	return job
}
