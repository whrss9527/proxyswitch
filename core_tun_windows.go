//go:build windows

package main

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"
	"unsafe"
)

// TUN 模式的内核：创建虚拟网卡要管理员权限，所以由以管理员身份运行的 ProxySwitch.exe --core-host 启动内核
// （每次启动问一次）。宿主把内核放进“关闭即结束”的作业对象，ProxySwitch 退出或者发来停止信号时结束内核，
// 内核退出时自己也退出；ProxySwitch 通过宿主的进程句柄知道内核退出了，照常经 REST API 管理内核。
// 以管理员身份运行的只能是 ProxySwitch 下载的内核（按发布时写入的 SHA-256 校验）或者配置里指定的内核。

var (
	procCreateEventW           = kernel32.NewProc("CreateEventW")
	procOpenEventW             = kernel32.NewProc("OpenEventW")
	procSetEvent               = kernel32.NewProc("SetEvent")
	procWaitForMultipleObjects = kernel32.NewProc("WaitForMultipleObjects")
	procGetProcessId           = kernel32.NewProc("GetProcessId")
	procTerminateProcess       = kernel32.NewProc("TerminateProcess")
)

const (
	coreHostArgument  = "--core-host"
	coreHostStopWait  = 5 * time.Second
	waitObject0       = 0
	waitInfinite      = 0xFFFFFFFF
	eventSynchronize  = 0x00100000
	coreHostBadBinary = 3
)

// hostedCoreProcess 是以管理员身份运行的宿主进程。stop 是通知它结束内核的事件；两个句柄在它退出后由 Wait 关闭，
// mutex 保证 Kill 不会用到已经关闭的句柄。
type hostedCoreProcess struct {
	mutex  sync.Mutex
	handle uintptr
	stop   uintptr
	pid    int
	exited bool
}

func (process *hostedCoreProcess) Pid() int {
	return process.pid
}

func (process *hostedCoreProcess) Elevated() bool {
	return true
}

func (process *hostedCoreProcess) Wait() error {
	procWaitForSingleObject.Call(process.handle, waitInfinite)
	var code uint32
	procGetExitCodeProcess.Call(process.handle, uintptr(unsafe.Pointer(&code)))
	process.mutex.Lock()
	process.exited = true
	procCloseHandle.Call(process.handle)
	procCloseHandle.Call(process.stop)
	process.mutex.Unlock()
	switch code {
	case 0:
		return nil
	case coreHostBadBinary:
		return errCoreMismatch
	}
	return fmt.Errorf("退出码 %d", code)
}

// Kill 通知宿主结束内核，宿主没有及时退出时强行结束它。
func (process *hostedCoreProcess) Kill() error {
	process.mutex.Lock()
	defer process.mutex.Unlock()
	if process.exited {
		return nil
	}
	procSetEvent.Call(process.stop)
	if wait, _, _ := procWaitForSingleObject.Call(process.handle, uintptr(coreHostStopWait.Milliseconds())); wait == waitObject0 {
		return nil
	}
	if ok, _, err := procTerminateProcess.Call(process.handle, 1); ok == 0 {
		return fmt.Errorf("结束内核失败：%v", err)
	}
	return nil
}

// startElevatedCoreProcess 以管理员身份启动宿主进程，由它启动内核。用户在确认框里点了「否」时返回 errTunDeclined。
func startElevatedCoreProcess(binary, dir, configPath, logPath string) (coreProcess, error) {
	executable, err := os.Executable()
	if err != nil {
		return nil, err
	}
	for _, path := range []string{binary, dir, configPath, logPath} {
		if strings.ContainsAny(path, `"`) || strings.HasSuffix(path, `\`) {
			return nil, fmt.Errorf("路径里有特殊字符，不能以管理员身份运行内核：%s", path)
		}
	}
	name := fmt.Sprintf(`Local\ProxySwitchCore-%d-%d`, os.Getpid(), time.Now().UnixNano())
	stop, _, err := procCreateEventW.Call(0, 1, 0, uintptr(unsafe.Pointer(utf16Pointer(name))))
	if stop == 0 {
		return nil, fmt.Errorf("无法创建事件：%v", err)
	}
	parameters := fmt.Sprintf(`%s --parent=%d --event=%s --binary="%s" --dir="%s" --config="%s" --log="%s"`, coreHostArgument, os.Getpid(), name, binary, dir, configPath, logPath)
	handle, err := shellExecuteElevated(executable, parameters)
	if err != nil {
		procCloseHandle.Call(stop)
		if errors.Is(err, errElevationCancelled) {
			return nil, errTunDeclined
		}
		return nil, err
	}
	if handle == 0 {
		procCloseHandle.Call(stop)
		return nil, errors.New("没有拿到以管理员身份运行的内核")
	}
	pid, _, _ := procGetProcessId.Call(handle)
	return &hostedCoreProcess{handle: handle, stop: stop, pid: int(pid)}, nil
}

// ---------- 宿主进程 ----------

// runCoreHost 是以管理员身份运行的 ProxySwitch.exe --core-host：启动内核，等内核退出、ProxySwitch 退出或者收到
// 停止信号，后两种情况结束内核。退出码是内核的退出码（内核程序校验不过时是 coreHostBadBinary）。
func runCoreHost(arguments []string) int {
	options := map[string]string{}
	for _, argument := range arguments {
		if key, value, found := strings.Cut(strings.TrimPrefix(argument, "--"), "="); found {
			options[key] = value
		}
	}
	parent, _ := strconv.Atoi(options["parent"])
	binary, dir, config, logPath := options["binary"], options["dir"], options["config"], options["log"]
	if parent == 0 || options["event"] == "" || binary == "" || dir == "" || config == "" || logPath == "" {
		return exitUsage
	}
	logFile, err := os.OpenFile(logPath, os.O_WRONLY|os.O_APPEND|os.O_CREATE, 0o644)
	if err != nil {
		return exitFailure
	}
	defer logFile.Close()
	command := exec.Command(binary, "-d", dir, "-f", config)
	command.Dir = dir
	command.Stdout, command.Stderr = logFile, logFile
	prepareCoreCommand(command)
	if err := startVerifiedCore(command, dir); err != nil {
		fmt.Fprintf(logFile, "ProxySwitch：%v\n", err)
		if errors.Is(err, errCoreMismatch) {
			return coreHostBadBinary
		}
		return exitFailure
	}
	attachCoreProcess(command.Process)
	child, _, _ := procOpenProcess.Call(synchronize, 0, uintptr(command.Process.Pid))
	parentHandle, _, _ := procOpenProcess.Call(synchronize, 0, uintptr(parent))
	event, _, _ := procOpenEventW.Call(eventSynchronize, 0, uintptr(unsafe.Pointer(utf16Pointer(options["event"]))))
	if child == 0 || parentHandle == 0 || event == 0 {
		// 没法等 ProxySwitch 或停止信号时不留下没人管的内核。
		_ = command.Process.Kill()
		_ = command.Wait()
		return exitFailure
	}
	handles := [3]uintptr{child, parentHandle, event}
	index, _, _ := procWaitForMultipleObjects.Call(uintptr(len(handles)), uintptr(unsafe.Pointer(&handles[0])), 0, waitInfinite)
	for _, handle := range handles {
		procCloseHandle.Call(handle)
	}
	if index != waitObject0 {
		_ = command.Process.Kill()
		_ = command.Wait()
		return 0
	}
	if err := command.Wait(); err != nil {
		var exit *exec.ExitError
		if errors.As(err, &exit) && exit.ExitCode() > 0 {
			return exit.ExitCode()
		}
		return exitFailure
	}
	return 0
}

var errCoreMismatch = errors.New("内核程序和发布时的不一致，没有以管理员身份运行")

// startVerifiedCore 启动内核。ProxySwitch 下载的内核（在 dir 里）要和发布时写入的 SHA-256 一致；校验到启动期间
// 独占打开程序文件，别人改不了它。
func startVerifiedCore(command *exec.Cmd, dir string) error {
	expected := coreExeSha256()
	managed := strings.EqualFold(filepath.Clean(command.Path), filepath.Clean(filepath.Join(dir, coreBinaryName())))
	if !managed || expected == "" {
		return command.Start()
	}
	pathPointer, err := syscall.UTF16PtrFromString(command.Path)
	if err != nil {
		return err
	}
	handle, err := syscall.CreateFile(pathPointer, syscall.GENERIC_READ, syscall.FILE_SHARE_READ, nil, syscall.OPEN_EXISTING, syscall.FILE_ATTRIBUTE_NORMAL, 0)
	if err != nil {
		return fmt.Errorf("打不开内核程序：%v", err)
	}
	file := os.NewFile(uintptr(handle), command.Path)
	defer file.Close()
	hash := sha256.New()
	if _, err := io.Copy(hash, file); err != nil {
		return fmt.Errorf("读不了内核程序：%v", err)
	}
	if !strings.EqualFold(hex.EncodeToString(hash.Sum(nil)), expected) {
		slog.Warn("内核程序和发布时的不一致", "path", command.Path)
		return errCoreMismatch
	}
	return command.Start()
}
