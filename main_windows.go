//go:build windows

package main

import (
	"fmt"
	"io"
	"log"
	"os"
	"path/filepath"
	"runtime"
	"strings"
)

func init() {
	// 窗口和消息循环必须固定在同一个 OS 线程上
	runtime.LockOSThread()
}

func main() {
	paths := resolvePaths()
	logger := openLogger(paths.Log)

	if len(os.Args) > 1 {
		os.Exit(runCLI(paths, logger, os.Args[1:]))
	}

	ok, err := createSingleInstanceMutex("Local\\ProxySwitch-3f1c2a9e")
	if err != nil {
		logger.Printf("创建互斥量失败: %v", err)
	} else if !ok {
		messageBox(0, appName+" 已经在运行了，请在系统托盘（任务栏右下角）找到它的图标。",
			appName, mbOK|mbIconInformation|mbSetForeground|mbTopmost)
		return
	}

	app := newApp(paths, logger)
	if err := app.run(iconOnICO, iconOffICO); err != nil {
		logger.Printf("启动失败: %v", err)
		messageBox(0, "启动失败："+err.Error()+"\n\n日志："+paths.Log, appName, mbOK|mbIconError|mbSetForeground|mbTopmost)
		os.Exit(1)
	}
}

// runCLI 处理命令行用法（不启动托盘）。托盘实例如果在运行，会在几秒内自动同步图标状态。
func runCLI(paths Paths, logger *log.Logger, args []string) int {
	cmd := strings.ToLower(strings.TrimSpace(args[0]))
	cmd = strings.TrimLeft(cmd, "-/")

	app := newApp(paths, logger)
	if _, err := app.loadConfigFile(); err != nil && cmd != "help" {
		messageBox(0, "配置文件有错误：\n"+err.Error()+"\n\n"+paths.Config, appName, mbOK|mbIconError|mbSetForeground|mbTopmost)
		return 2
	}

	switch cmd {
	case "on", "enable", "start":
		st := app.status()
		if st.On && st.Profile != nil {
			logger.Printf("cli on: 已经是开启状态 (%s)", st.Profile.Name)
			return 0
		}
		app.turnOn(st.Profile)
	case "off", "disable", "stop":
		st := app.status()
		if !st.On {
			logger.Printf("cli off: 已经是关闭状态")
			return 0
		}
		app.turnOff(st)
	case "toggle":
		app.toggle()
	case "use", "switch", "select":
		if len(args) < 2 {
			messageBox(0, "用法：ProxySwitch.exe use <配置名>", appName, mbOK|mbIconWarning|mbSetForeground|mbTopmost)
			return 2
		}
		name := strings.Join(args[1:], " ")
		p := app.cfg.FindProfile(name)
		if p == nil {
			var names []string
			for _, pp := range app.cfg.Profiles {
				names = append(names, pp.Name)
			}
			messageBox(0, fmt.Sprintf("没有名为 %q 的配置。\n可用配置：\n%s", name, strings.Join(names, "\n")),
				appName, mbOK|mbIconWarning|mbSetForeground|mbTopmost)
			return 2
		}
		app.selectProfile(p)
	case "settings", "ui":
		data, err := os.ReadFile(filepath.Join(paths.Dir, settingsURLFile))
		if err != nil || len(data) == 0 {
			messageBox(0, "托盘程序没有在运行，请先启动 ProxySwitch.exe，再从托盘菜单打开设置。", appName, mbOK|mbIconWarning|mbSetForeground|mbTopmost)
			return 2
		}
		if err := shellOpen(strings.TrimSpace(string(data))); err != nil {
			messageBox(0, "无法打开浏览器："+err.Error(), appName, mbOK|mbIconError|mbSetForeground|mbTopmost)
			return 1
		}
	case "status":
		st := app.status()
		var text string
		switch {
		case st.On && st.Profile != nil:
			text = fmt.Sprintf("代理已开启：%s (%s)\n生效范围：%s", st.Profile.Name, st.Profile.Summary(), st.Profile.TargetsText())
		case st.On:
			text = "代理已开启（由其他程序设置）：" + st.External
		case st.Profile != nil:
			text = "代理已关闭。当前配置：" + st.Profile.Name
		default:
			text = "代理已关闭"
		}
		messageBox(0, text, appName+" 状态", mbOK|mbIconInformation|mbSetForeground|mbTopmost)
		if st.On {
			return 0
		}
		return 1
	default:
		messageBox(0, "用法：\n  ProxySwitch.exe            启动托盘程序\n"+
			"  ProxySwitch.exe on         开启当前配置的代理\n"+
			"  ProxySwitch.exe off        关闭代理\n"+
			"  ProxySwitch.exe toggle     开/关切换\n"+
			"  ProxySwitch.exe use <配置名>  切换到某套配置并开启\n"+
			"  ProxySwitch.exe status     查看状态（退出码 0=开启 1=关闭）\n"+
			"  ProxySwitch.exe settings   打开设置页面（托盘程序需在运行）",
			appName, mbOK|mbIconInformation|mbSetForeground|mbTopmost)
		if cmd != "help" && cmd != "h" && cmd != "?" {
			return 2
		}
	}
	return 0
}

// openLogger 打开日志文件（超过 1MB 时轮转一次）。
func openLogger(path string) *log.Logger {
	_ = os.MkdirAll(filepath.Dir(path), 0o755)
	if st, err := os.Stat(path); err == nil && st.Size() > 1<<20 {
		_ = os.Remove(path + ".1")
		_ = os.Rename(path, path+".1")
	}
	f, err := os.OpenFile(path, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o644)
	var w io.Writer = io.Discard
	if err == nil {
		w = f
	}
	return log.New(w, "", log.LstdFlags)
}
