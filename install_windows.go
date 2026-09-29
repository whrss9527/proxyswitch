//go:build windows

package main

import (
	"errors"
	"fmt"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"
	"unsafe"
)

// 安装版：安装程序（installer/ProxySwitch.nsi）把 ProxySwitch 装到 %LOCALAPPDATA%\Programs\ProxySwitch，在开始菜单
// 里加快捷方式，在「设置 → 应用」里登记卸载项（都在当前用户下，不需要管理员权限）。卸载项执行的是
// ProxySwitch.exe --uninstall：程序自己退出正在运行的实例、关闭它开启的代理、取消开机自启和链接等登记，
// 再在退出后删掉安装目录。直接下载的 exe 也可以用 --uninstall 清理这些设置。

const (
	uninstallKey = `Software\Microsoft\Windows\CurrentVersion\Uninstall\` + appName
	// uninstallShortcutValue 是安装程序记在卸载项里的开始菜单快捷方式的位置，卸载时删掉它。
	uninstallShortcutValue = "StartMenuShortcut"
	// closeTimeout 是等正在运行的 ProxySwitch 退出的时间：它要先关闭代理和内核。
	closeTimeout = 20 * time.Second
)

var procOpenMutexW = kernel32.NewProc("OpenMutexW")

// installedDirectory 是安装程序记下的安装目录；没有用安装程序安装时为空。
func installedDirectory() string {
	location := strings.TrimSpace(readRegistryString(hkeyCurrentUser, uninstallKey, "InstallLocation"))
	if location == "" {
		return ""
	}
	return filepath.Clean(location)
}

// isInstalledCopy 表示 executable 是安装程序装的那一份。
func isInstalledCopy(executable string) bool {
	location := installedDirectory()
	return location != "" && strings.EqualFold(location, filepath.Clean(filepath.Dir(executable)))
}

// refreshInstallInfo 在安装版启动时让「设置 → 应用」里显示的版本跟上程序内更新后的版本，并删掉安装和更新时
// 留下的旧程序文件。
func refreshInstallInfo() {
	executable, err := os.Executable()
	if err != nil {
		return
	}
	_ = os.Remove(executable + ".old")
	if !isInstalledCopy(executable) || readRegistryString(hkeyCurrentUser, uninstallKey, "DisplayVersion") == appVersion {
		return
	}
	key, err := openRegistryKey(hkeyCurrentUser, uninstallKey, keyWrite)
	if err != nil {
		return
	}
	defer key.Close()
	if err := key.SetString("DisplayVersion", appVersion); err != nil {
		slog.Warn("更新卸载项的版本失败", "err", err)
	}
}

// instanceRunning 表示有托盘程序在运行：它的进程结束前一直持有单实例互斥量。
func instanceRunning() bool {
	handle, _, _ := procOpenMutexW.Call(synchronize, 0, uintptr(unsafe.Pointer(utf16Pointer(singleInstanceMutex))))
	if handle == 0 {
		return false
	}
	procCloseHandle.Call(handle)
	return true
}

// closeRunningInstance 让正在运行的托盘程序退出（和从托盘菜单退出一样关闭代理和内核），等它的进程结束。
func closeRunningInstance(timeout time.Duration) error {
	if window := findRunningInstance(); window != 0 {
		procPostMessageW.Call(window, wmClose, 0, 0)
	}
	deadline := time.Now().Add(timeout)
	for instanceRunning() {
		if time.Now().After(deadline) {
			return errors.New("ProxySwitch 没有退出")
		}
		time.Sleep(200 * time.Millisecond)
	}
	return nil
}

const (
	mbYesNo        = 0x00000004
	mbIconQuestion = 0x00000020
	mbDefButton2   = 0x00000100
	idYes          = 6
)

// askYesNo 弹出询问框，选「是」时返回 true。defaultNo 表示默认按钮是「否」。
func askYesNo(text string, defaultNo bool) bool {
	flags := uint32(mbYesNo | mbIconQuestion | mbSetForeground | mbTopmost)
	if defaultNo {
		flags |= mbDefButton2
	}
	result, _, _ := procMessageBoxW.Call(0, uintptr(unsafe.Pointer(utf16Pointer(text))), uintptr(unsafe.Pointer(utf16Pointer(appName))), uintptr(flags))
	return result == idYes
}

// runUninstall 卸载：quiet（--quiet）时不询问，保留配置。
func runUninstall(paths Paths, arguments []string) int {
	quiet := false
	for _, argument := range arguments {
		switch strings.ToLower(strings.TrimLeft(argument, "-/")) {
		case "quiet", "silent", "q", "s":
			quiet = true
		}
	}
	executable, err := os.Executable()
	if err != nil {
		return exitFailure
	}
	installed := isInstalledCopy(executable)
	if !quiet && !askYesNo("要卸载 ProxySwitch 吗？\n\n会先退出 ProxySwitch、关闭它开启的代理，再取消开机自启、网页链接和通知的登记。", false) {
		return exitFailure
	}
	if err := closeRunningInstance(closeTimeout); err != nil {
		if !quiet {
			messageBox(0, "ProxySwitch 还在运行，请从托盘图标的菜单里选「退出」，然后再卸载。", appName, mbOk|mbIconWarning|mbSetForeground|mbTopmost)
		}
		return exitFailure
	}
	slog.Info("开始卸载", "path", executable, "installed", installed, "quiet", quiet)

	problems := cleanUpSystem(paths, quiet)
	removeData := false
	if !quiet && !paths.Portable {
		removeData = askYesNo("要同时删除 ProxySwitch 的配置、订阅和日志吗？\n\n"+paths.Dir+"\n\n选「否」会保留，以后重新安装接着用。", true)
	}
	if installed {
		if shortcut := readRegistryString(hkeyCurrentUser, uninstallKey, uninstallShortcutValue); strings.EqualFold(filepath.Ext(shortcut), ".lnk") {
			if err := os.Remove(shortcut); err != nil && !os.IsNotExist(err) {
				problems = append(problems, "删除开始菜单里的快捷方式："+err.Error())
			}
		}
		if err := deleteRegistryTree(hkeyCurrentUser, uninstallKey); err != nil {
			problems = append(problems, "删除卸载项："+err.Error())
		}
	}
	// 程序文件（安装版）和数据要等这个进程退出后才删得掉：日志文件和程序本身都还开着。
	cleanup := removalPlan{}
	if installed {
		cleanup.executable = executable
	}
	if removeData {
		cleanup.data = []string{paths.Dir, filepath.Join(os.Getenv("LOCALAPPDATA"), appName)}
	}
	if !cleanup.empty() {
		if err := cleanup.start(); err != nil {
			problems = append(problems, "删除程序文件："+err.Error())
		}
	}
	for _, problem := range problems {
		slog.Warn("卸载时有一步没有完成", "problem", problem)
	}
	if quiet {
		if len(problems) > 0 {
			return exitFailure
		}
		return exitSuccess
	}
	text := "ProxySwitch 已卸载。"
	if !installed {
		text = "已经清理了 ProxySwitch 在这台电脑上的设置，现在可以删除这个程序文件了：\n" + executable
	}
	if !removeData && !paths.Portable {
		text += "\n\n配置保留在 " + paths.Dir + "，以后重新安装接着用。"
	}
	if paths.Portable {
		text += "\n\n配置和订阅在程序旁边的文件里，不需要时连同文件夹一起删除。"
	}
	if len(problems) > 0 {
		text += "\n\n下面几项没有完成：\n" + strings.Join(problems, "\n")
		messageBox(0, text, appName, mbOk|mbIconWarning|mbSetForeground|mbTopmost)
		return exitFailure
	}
	messageBox(0, text, appName, mbOk|mbIconInformation|mbSetForeground|mbTopmost)
	return exitSuccess
}

// cleanUpSystem 撤销 ProxySwitch 对这台电脑的设置：关闭它开启的代理，取消开机自启、网页链接和通知的登记；
// WinHTTP 还指向内置内核时问一下改回直连（要管理员确认）。返回没有完成的步骤。
func cleanUpSystem(paths Paths, quiet bool) []string {
	var problems []string
	// 没有配置文件时不加载：加载会创建一份默认配置。
	if fileExists(paths.Config) {
		app := newApp(paths)
		if _, err := app.engine.LoadConfig(); err == nil || app.engine.Config() != nil {
			// 托盘程序按「退出程序时关闭代理」的设置退出，代理可能还开着：关闭它，恢复开启前的设置。
			if app.engine.Status().State == statusOn {
				if err := app.engine.TurnOff(); err != nil {
					problems = append(problems, "关闭代理："+err.Error())
				}
			}
			if proxy, _, err := readWinHttpProxy(); err == nil && app.engine.WinHttpUsesCore(proxy) && !quiet &&
				askYesNo("Windows 更新等系统服务（WinHTTP）的代理还是 ProxySwitch 内核的地址 "+proxy+"，卸载后它们会连不上网。\n\n要改回直连吗？需要管理员确认。", false) {
				if err := writeWinHttpProxy("", ""); err != nil {
					problems = append(problems, "WinHTTP 改回直连："+err.Error())
				}
			}
		}
	}
	if readRegistryString(hkeyCurrentUser, runKey, runValueName) != "" {
		if err := setAutostart(false); err != nil {
			problems = append(problems, "取消开机自启："+err.Error())
		}
	}
	for _, scheme := range []string{linkScheme, clashLinkScheme} {
		if err := unregisterProxySwitchLink(scheme); err != nil {
			problems = append(problems, "取消 "+scheme+":// 链接的登记："+err.Error())
		}
	}
	if err := unregisterToastApp(); err != nil {
		problems = append(problems, "取消通知的登记："+err.Error())
	}
	return problems
}

// unregisterProxySwitchLink 取消 scheme 链接对 ProxySwitch 的登记，不管登记的是哪个位置的 ProxySwitch.exe
// 或 ProxySwitch-arm64.exe（程序挪过位置时登记的可能是原来的路径）。
func unregisterProxySwitchLink(scheme string) error {
	command := readRegistryString(hkeyCurrentUser, classesKey+scheme+`\shell\open\command`, "")
	if !strings.HasPrefix(strings.ToLower(handlerName(command)), strings.ToLower(appName)) {
		return nil
	}
	return unregisterLink(scheme, commandProgram(command))
}

// removalPlan 是卸载进程退出后要删掉的东西：安装版的程序文件（连同安装目录）和数据文件夹。waitFor 是要等它退出的
// 进程，默认是这个进程。
type removalPlan struct {
	executable string
	data       []string
	waitFor    int
}

func (plan removalPlan) empty() bool {
	return plan.executable == "" && len(plan.data) == 0
}

// removalScript 等卸载进程退出，删掉程序文件和数据，最后删掉自己。批处理按系统的代码页解析，所以脚本里只有 ASCII，
// 路径经环境变量传入（不经过代码页），路径里有中文也不会乱。系统工具写完整路径：装了 Git 的电脑上 PATH 里的 find
// 可能是另一个同名程序。
const removalScript = `@echo off
set tries=0
:wait
"%SystemRoot%\System32\tasklist.exe" /fi "pid eq %PROXYSWITCH_PID%" /nh 2>nul | "%SystemRoot%\System32\find.exe" "%PROXYSWITCH_PID%" >nul || goto gone
set /a tries+=1
if %tries% geq 900 goto gone
"%SystemRoot%\System32\ping.exe" -n 2 127.0.0.1 >nul
goto wait
:gone
if "%PROXYSWITCH_EXE%"=="" goto data
del /f /q "%PROXYSWITCH_EXE%" "%PROXYSWITCH_EXE%.old" "%PROXYSWITCH_EXE%.download" 2>nul
for %%f in ("%PROXYSWITCH_EXE%.*.old") do del /f /q "%%~f" 2>nul
rd "%PROXYSWITCH_DIR%" 2>nul
:data
if not "%PROXYSWITCH_DATA1%"=="" rd /s /q "%PROXYSWITCH_DATA1%" 2>nul
if not "%PROXYSWITCH_DATA2%"=="" rd /s /q "%PROXYSWITCH_DATA2%" 2>nul
(goto) 2>nul & del "%~f0"
`

// start 在后台启动删除用的批处理：它等这个进程退出后再删。安装目录只删 ProxySwitch 自己的文件，目录里还有别的
// 文件时目录留着。
func (plan removalPlan) start() error {
	if len(plan.data) > 2 {
		return errors.New("要删除的数据文件夹太多")
	}
	script, err := os.CreateTemp("", "proxyswitch-uninstall-*.cmd")
	if err != nil {
		return err
	}
	_, writeErr := script.WriteString(strings.ReplaceAll(removalScript, "\n", "\r\n"))
	if closeErr := script.Close(); writeErr == nil {
		writeErr = closeErr
	}
	if writeErr != nil {
		_ = os.Remove(script.Name())
		return writeErr
	}
	waitFor := plan.waitFor
	if waitFor == 0 {
		waitFor = os.Getpid()
	}
	environment := append(os.Environ(), "PROXYSWITCH_PID="+strconv.Itoa(waitFor), "PROXYSWITCH_EXE="+plan.executable)
	if plan.executable != "" {
		environment = append(environment, "PROXYSWITCH_DIR="+filepath.Dir(plan.executable))
	}
	for index, folder := range plan.data {
		environment = append(environment, fmt.Sprintf("PROXYSWITCH_DATA%d=%s", index+1, folder))
	}
	command := exec.Command(filepath.Join(systemDir(), "cmd.exe"), "/d", "/c", script.Name())
	command.Env = environment
	command.SysProcAttr = &syscall.SysProcAttr{HideWindow: true, CreationFlags: createNoWindow}
	if err := command.Start(); err != nil {
		_ = os.Remove(script.Name())
		return err
	}
	return command.Process.Release()
}
