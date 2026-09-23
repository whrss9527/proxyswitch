//go:build windows

package main

import (
	"fmt"
	"log/slog"
	"os"
	"runtime"
	"strings"
	"time"
	"unsafe"
)

func init() {
	// 窗口和消息循环必须固定在同一个系统线程上。
	runtime.LockOSThread()
}

const singleInstanceMutex = `Local\ProxySwitch-3f1c2a9e`

const (
	exitSuccess = 0
	exitFailure = 1
	exitUsage   = 2
)

const usageText = `用法：
  ProxySwitch.exe                 启动托盘程序
  ProxySwitch.exe on              开启代理（上次使用的配置）
  ProxySwitch.exe off             关闭代理
  ProxySwitch.exe toggle          开 / 关切换
  ProxySwitch.exe use <配置名>     切换到指定配置并开启
  ProxySwitch.exe status          查看当前状态（退出码 0 表示已开启，1 表示已关闭）
  ProxySwitch.exe settings        打开设置

托盘程序在运行时，命令交给它执行，托盘图标会立即更新。`

func main() {
	paths := resolvePaths()
	setupLogger(paths.Log)
	autostarted := false
	command := ""
	var arguments []string
	for index, argument := range os.Args[1:] {
		normalized := strings.ToLower(strings.TrimLeft(argument, "-/"))
		if normalized == "autostart" {
			autostarted = true
			continue
		}
		command, arguments = normalized, os.Args[index+2:]
		break
	}
	if command != "" && command != "settings" {
		os.Exit(runCommand(paths, command, arguments))
	}

	first, err := createSingleInstanceMutex(singleInstanceMutex)
	if err != nil {
		slog.Warn("创建互斥量失败", "err", err)
	} else if !first {
		// 已经在运行：再次打开程序时让它打开设置页，开机自启重复启动时静默退出。
		if window := waitForRunningInstance(3 * time.Second); window != 0 {
			if !autostarted {
				procAllowSetForegroundWindow.Call(asfwAny)
				procPostMessageW.Call(window, wmActivate, 0, 0)
			}
			return
		}
		if !autostarted {
			messageBox(0, appName+" 已经在运行了，请在任务栏右下角的托盘区域找到它的图标。", appName, mbOk|mbIconInformation|mbSetForeground|mbTopmost)
		}
		return
	}

	app := newApp(paths)
	if err := app.run(autostarted, command == "settings"); err != nil {
		slog.Error("启动失败", "err", err)
		messageBox(0, "启动失败："+err.Error()+"\n\n日志："+paths.Log, appName, mbOk|mbIconError|mbSetForeground|mbTopmost)
		os.Exit(exitFailure)
	}
}

func findRunningInstance() uintptr {
	window, _, _ := procFindWindowW.Call(uintptr(unsafe.Pointer(utf16Pointer(trayWindowClass))), 0)
	return window
}

// waitForRunningInstance 等待正在启动的另一个实例创建好窗口。
func waitForRunningInstance(timeout time.Duration) uintptr {
	deadline := time.Now().Add(timeout)
	for {
		if window := findRunningInstance(); window != 0 {
			return window
		}
		if time.Now().After(deadline) {
			return 0
		}
		time.Sleep(100 * time.Millisecond)
	}
}

// sendCommand 把命令交给正在运行的实例执行，返回它的退出码。
func sendCommand(window uintptr, payload string) int {
	data := []byte(payload)
	message := copyDataStruct{size: uint32(len(data))}
	if len(data) > 0 {
		message.pointer = uintptr(unsafe.Pointer(&data[0]))
	}
	procAllowSetForegroundWindow.Call(asfwAny)
	var result uintptr
	sent, _, _ := procSendMessageTimeoutW.Call(window, wmCopyData, 0, uintptr(unsafe.Pointer(&message)), smtoAbortIfHung, 30000, uintptr(unsafe.Pointer(&result)))
	runtime.KeepAlive(data)
	if sent == 0 {
		return exitFailure
	}
	return int(result)
}

// runCommand 处理命令行用法。托盘程序在运行时转交给它，否则直接执行。
func runCommand(paths Paths, command string, arguments []string) int {
	switch command {
	case "on", "enable", "start":
		return forwardOrRun(paths, "on", func(engine *Engine) error { return engine.TurnOn() })
	case "off", "disable", "stop":
		return forwardOrRun(paths, "off", func(engine *Engine) error { return engine.TurnOff() })
	case "toggle":
		return forwardOrRun(paths, "toggle", func(engine *Engine) error { return engine.Toggle() })
	case "use", "switch":
		name := strings.TrimSpace(strings.Join(arguments, " "))
		if name == "" {
			messageBox(0, "请写上配置名，例如：ProxySwitch.exe use 公司代理", appName, mbOk|mbIconWarning|mbSetForeground|mbTopmost)
			return exitUsage
		}
		return forwardOrRun(paths, "use\x00"+name, func(engine *Engine) error { return engine.UseProfile(name) })
	case "status":
		return showStatus(paths)
	case "help", "h", "?":
		messageBox(0, usageText, appName, mbOk|mbIconInformation|mbSetForeground|mbTopmost)
		return exitSuccess
	}
	messageBox(0, "不认识的命令："+command+"\n\n"+usageText, appName, mbOk|mbIconWarning|mbSetForeground|mbTopmost)
	return exitUsage
}

func forwardOrRun(paths Paths, payload string, action func(engine *Engine) error) int {
	if window := findRunningInstance(); window != 0 {
		return sendCommand(window, payload)
	}
	app := newApp(paths)
	if _, err := app.engine.LoadConfig(); err != nil && app.engine.Config() == nil {
		messageBox(0, "配置文件有错误：\n"+err.Error()+"\n\n"+paths.Config, appName, mbOk|mbIconError|mbSetForeground|mbTopmost)
		return exitFailure
	}
	shown := app.problemNotices
	if err := action(app.engine); err != nil {
		// 已经弹过提示的错误不再重复提示。
		if app.problemNotices == shown {
			messageBox(0, err.Error(), appName, mbOk|mbIconWarning|mbSetForeground|mbTopmost)
		}
		return exitFailure
	}
	return exitSuccess
}

func showStatus(paths Paths) int {
	app := newApp(paths)
	if _, err := app.engine.LoadConfig(); err != nil && app.engine.Config() == nil {
		messageBox(0, "配置文件有错误：\n"+err.Error(), appName, mbOk|mbIconError|mbSetForeground|mbTopmost)
		return exitFailure
	}
	status := app.engine.Status()
	var text string
	switch {
	case status.State == statusOn:
		text = fmt.Sprintf("代理已开启：%s\n地址：%s\n生效范围：%s", status.Profile.Name, status.Profile.Summary(), status.Profile.TargetsText())
	case status.State == statusExternal:
		text = "系统代理由其他程序设置：" + status.External
	case status.Profile != nil:
		text = "代理已关闭。下次开启：" + status.Profile.Name
	default:
		text = "代理已关闭"
	}
	messageBox(0, text, appName+" 状态", mbOk|mbIconInformation|mbSetForeground|mbTopmost)
	if status.State == statusOff {
		return exitFailure
	}
	return exitSuccess
}
