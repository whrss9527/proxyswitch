//go:build windows

package main

import (
	"fmt"
	"log"
	"os"
	"path/filepath"
	"strings"
)

// 菜单项 ID
const (
	idToggle      = 1
	idEdit        = 2
	idReload      = 3
	idOpenDir     = 4
	idAutostart   = 5
	idAbout       = 6
	idSettings    = 7
	idExit        = 9
	idProfileBase = 100
)

// Status 是“当前代理是否开启、对应哪套配置”的判断结果。
type Status struct {
	On       bool
	Profile  *Profile // On 时：匹配到的配置；Off 时：当前选中的（下次开启用的）配置
	External string   // On 且 Profile==nil：系统代理由别的程序设置，这里是它的描述
}

type App struct {
	paths     Paths
	cfg       *Config
	cfgErr    string // 配置文件有错时的错误信息（此时 cfg 可能为 nil）
	state     *State
	tray      *Tray
	logger    *log.Logger
	hotkey    *Hotkey
	hotkeyErr string // 快捷键注册失败的原因
	lastOn    bool
	started   bool
	settings  *SettingsServer
}

func newApp(paths Paths, logger *log.Logger) *App {
	a := &App{paths: paths, logger: logger}
	a.state = loadState(paths.State)
	a.settings = newSettingsServer(a, logger.Printf)
	return a
}

// loadConfigFile 读取配置文件；出错时保留旧配置并记录错误。
func (a *App) loadConfigFile() (created bool, err error) {
	cfg, created, err := loadConfig(a.paths.Config)
	if err != nil {
		a.cfgErr = err.Error()
		a.logger.Printf("配置加载失败: %v", err)
		return created, err
	}
	a.cfg = cfg
	a.cfgErr = ""
	return created, nil
}

func (a *App) profiles() []Profile {
	if a.cfg == nil {
		return nil
	}
	return a.cfg.Profiles
}

// selectedProfile 返回最近选择的配置；没有则返回第一套。
func (a *App) selectedProfile() *Profile {
	if a.cfg == nil || len(a.cfg.Profiles) == 0 {
		return nil
	}
	if p := a.cfg.FindProfile(a.state.Profile); p != nil {
		return p
	}
	return &a.cfg.Profiles[0]
}

// matchProfile 根据系统代理的实际值找出对应的配置。
func (a *App) matchProfile(sys SystemProxyState) *Profile {
	if a.cfg == nil {
		return nil
	}
	matches := func(p *Profile) bool {
		if !p.Has(targetSystem) {
			return false
		}
		if sys.PAC != "" {
			return p.PAC != "" && strings.EqualFold(p.PAC, sys.PAC)
		}
		return p.Server != "" && sameServer(p.Server, sys.Server)
	}
	if p := a.cfg.FindProfile(a.state.Profile); p != nil && matches(p) {
		return p
	}
	for i := range a.cfg.Profiles {
		if matches(&a.cfg.Profiles[i]) {
			return &a.cfg.Profiles[i]
		}
	}
	return nil
}

// status 读取系统当前状态并给出判断。
func (a *App) status() Status {
	sys, err := readSystemProxy()
	if err != nil {
		a.logger.Printf("读取系统代理失败: %v", err)
	}
	if sys.Active() {
		if p := a.matchProfile(sys); p != nil {
			return Status{On: true, Profile: p}
		}
		return Status{On: true, External: sys.Describe()}
	}
	p := a.selectedProfile()
	if p != nil && !p.Has(targetSystem) && a.state.Enabled {
		return Status{On: true, Profile: p}
	}
	return Status{On: false, Profile: p}
}

// ---------- 开 / 关 / 切换 ----------

func (a *App) applyProfile(p *Profile) []error {
	var errs []error
	url := serverToURL(p.Server)
	for _, t := range p.ApplyTo {
		var err error
		switch t {
		case targetSystem:
			err = setSystemProxy(serverToWinINET(p.Server), p.Bypass, p.PAC)
		case targetEnv:
			err = setUserEnvProxy(url, p.NoProxy)
		case targetGit:
			err = setGitProxy(url)
		case targetNpm:
			err = setNpmProxy(url, p.NoProxy)
		}
		if err != nil {
			a.logger.Printf("开启 [%s] 目标 %s 失败: %v", p.Name, t, err)
			errs = append(errs, fmt.Errorf("%s: %v", t, err))
		} else {
			a.logger.Printf("开启 [%s] 目标 %s 成功", p.Name, t)
		}
	}
	return errs
}

func (a *App) revertProfile(p *Profile) []error {
	var errs []error
	for _, t := range p.ApplyTo {
		var err error
		switch t {
		case targetSystem:
			err = disableSystemProxy()
		case targetEnv:
			err = clearUserEnvProxy()
		case targetGit:
			err = clearGitProxy()
		case targetNpm:
			err = setNpmProxy("", "")
		}
		if err != nil {
			a.logger.Printf("关闭 [%s] 目标 %s 失败: %v", p.Name, t, err)
			errs = append(errs, fmt.Errorf("%s: %v", t, err))
		} else {
			a.logger.Printf("关闭 [%s] 目标 %s 成功", p.Name, t)
		}
	}
	return errs
}

func joinErrors(errs []error) string {
	var parts []string
	for _, e := range errs {
		parts = append(parts, e.Error())
	}
	return strings.Join(parts, "\n")
}

// turnOn 开启指定配置。
func (a *App) turnOn(p *Profile) {
	if p == nil {
		a.notify("没有可用的配置", "请先在配置文件里添加 profiles", niifWarning)
		return
	}
	errs := a.applyProfile(p)
	a.state.Profile = p.Name
	a.state.Enabled = len(errs) < len(p.ApplyTo) // 至少一个目标成功才算开启
	a.saveState()
	switch {
	case len(errs) >= len(p.ApplyTo):
		a.notify("开启代理失败", p.Name+"\n"+joinErrors(errs), niifError)
	case len(errs) > 0:
		a.notify("代理已开启，但部分目标失败", p.Name+" — "+p.Summary()+"\n"+joinErrors(errs), niifWarning)
	default:
		text := p.Name + " — " + p.Summary()
		if len(p.ApplyTo) > 1 || !p.Has(targetSystem) {
			text += "\n生效范围：" + p.TargetsText()
		}
		a.notify("代理已开启", text, niifInfo)
	}
	a.refreshTray()
}

// turnOff 关闭当前代理。
func (a *App) turnOff(st Status) {
	var errs []error
	name := ""
	if st.Profile != nil {
		errs = a.revertProfile(st.Profile)
		name = st.Profile.Name
	} else {
		if err := disableSystemProxy(); err != nil {
			errs = append(errs, err)
		}
		name = "外部设置的系统代理"
		a.logger.Printf("关闭外部设置的系统代理 (%s)", st.External)
	}
	a.state.Enabled = false
	a.saveState()
	if len(errs) > 0 {
		a.notify("代理已关闭，但部分目标失败", name+"\n"+joinErrors(errs), niifWarning)
	} else {
		a.notify("代理已关闭", name, niifInfo)
	}
	a.refreshTray()
}

func (a *App) toggle() {
	st := a.status()
	if st.On {
		a.turnOff(st)
	} else {
		a.turnOn(st.Profile)
	}
}

// selectProfile 切换到指定配置并开启；如果之前开着别的配置，先把它清理掉。
func (a *App) selectProfile(p *Profile) {
	if p == nil {
		return
	}
	st := a.status()
	if st.On && st.Profile != nil && st.Profile != p {
		if errs := a.revertProfile(st.Profile); len(errs) > 0 {
			a.logger.Printf("切换前清理 [%s] 出错: %s", st.Profile.Name, joinErrors(errs))
		}
	}
	a.turnOn(p)
}

func (a *App) saveState() {
	if err := saveState(a.paths.State, a.state); err != nil {
		a.logger.Printf("保存状态失败: %v", err)
	}
}

// ---------- 托盘 UI ----------

func (a *App) notify(title, text string, kind uint32) {
	a.logger.Printf("通知: %s | %s", title, strings.ReplaceAll(text, "\n", " "))
	if a.tray == nil {
		// 命令行模式没有托盘：出错/警告时弹个框，正常提示只记日志
		if kind == niifError || kind == niifWarning {
			icon := uint32(mbIconWarning)
			if kind == niifError {
				icon = mbIconError
			}
			messageBox(0, title+"\n\n"+text, appName, mbOK|icon|mbSetForeground|mbTopmost)
		}
		return
	}
	if kind == niifInfo && a.cfg != nil && !a.cfg.Notify {
		return
	}
	a.tray.Notify(title, text, kind)
}

func (a *App) refreshTray() {
	if a.tray == nil {
		return
	}
	st := a.status()
	var tip string
	switch {
	case a.cfgErr != "":
		tip = appName + " — 配置文件有错误，请右键 → 编辑配置"
	case st.On && st.Profile != nil:
		tip = fmt.Sprintf("%s — 已开启：%s (%s)", appName, st.Profile.Name, st.Profile.Summary())
	case st.On:
		tip = fmt.Sprintf("%s — 已开启（外部设置：%s）", appName, st.External)
	case st.Profile != nil:
		tip = fmt.Sprintf("%s — 已关闭（当前配置：%s）", appName, st.Profile.Name)
	default:
		tip = appName + " — 已关闭"
	}
	a.lastOn = st.On
	a.tray.SetState(st.On, tip)
}

func (a *App) buildMenu() []MenuItem {
	st := a.status()
	var items []MenuItem
	hk := ""
	if a.hotkey != nil {
		hk = "\t" + a.hotkey.Text
	}

	if a.cfgErr != "" {
		items = append(items,
			MenuItem{ID: idToggle, Text: "配置文件有错误：" + firstLine(a.cfgErr), Disabled: true},
		)
	} else {
		switch {
		case st.On && st.Profile != nil:
			items = append(items, MenuItem{ID: idToggle, Text: "关闭代理（" + st.Profile.Name + "）" + hk, Default: true})
		case st.On:
			items = append(items, MenuItem{ID: idToggle, Text: "关闭代理（外部设置：" + st.External + "）" + hk, Default: true})
		case st.Profile != nil:
			items = append(items, MenuItem{ID: idToggle, Text: "开启代理（" + st.Profile.Name + "）" + hk, Default: true})
		default:
			items = append(items, MenuItem{ID: idToggle, Text: "开启代理", Disabled: true})
		}
	}
	items = append(items, MenuItem{Separator: true})

	for i := range a.profiles() {
		p := &a.cfg.Profiles[i]
		text := p.Name + " — " + p.Summary()
		items = append(items, MenuItem{
			ID:      uint32(idProfileBase + i),
			Text:    text,
			Radio:   true,
			Checked: st.Profile == p,
		})
	}
	if len(a.profiles()) > 0 {
		items = append(items, MenuItem{Separator: true})
	}

	items = append(items,
		MenuItem{ID: idSettings, Text: "设置..."},
		MenuItem{ID: idEdit, Text: "编辑配置文件（高级）..."},
		MenuItem{ID: idReload, Text: "重新加载配置"},
		MenuItem{ID: idOpenDir, Text: "打开配置目录"},
		MenuItem{Separator: true},
		MenuItem{ID: idAutostart, Text: "开机自启", Checked: isAutostartEnabled()},
		MenuItem{ID: idAbout, Text: "关于 " + appName + "..."},
		MenuItem{Separator: true},
		MenuItem{ID: idExit, Text: "退出"},
	)
	return items
}

func firstLine(s string) string {
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		return s[:i]
	}
	return s
}

func (a *App) handleCommand(id uint32) {
	switch {
	case id == 0:
		return
	case id == idToggle:
		a.toggle()
	case id >= idProfileBase && int(id-idProfileBase) < len(a.profiles()):
		a.selectProfile(&a.cfg.Profiles[id-idProfileBase])
	case id == idSettings:
		a.openSettings()
	case id == idEdit:
		editor := ""
		if a.cfg != nil {
			editor = a.cfg.Editor
		}
		if err := openWithEditor(editor, a.paths.Config); err != nil {
			a.notify("无法打开配置文件", err.Error(), niifError)
		}
	case id == idReload:
		a.reload(true)
	case id == idOpenDir:
		if err := shellOpen(a.paths.Dir); err != nil {
			a.notify("无法打开目录", err.Error(), niifError)
		}
	case id == idAutostart:
		enable := !isAutostartEnabled()
		if err := setAutostart(enable); err != nil {
			a.notify("设置开机自启失败", err.Error(), niifError)
		} else if enable {
			a.notify("已开启开机自启", "登录 Windows 后会自动在托盘运行", niifInfo)
		} else {
			a.notify("已关闭开机自启", "", niifInfo)
		}
	case id == idAbout:
		a.showAbout()
	case id == idExit:
		a.exit()
	}
}

func (a *App) reload(notifyResult bool) {
	oldHotkey := ""
	if a.cfg != nil {
		oldHotkey = a.cfg.Hotkey
	}
	if _, err := a.loadConfigFile(); err != nil {
		a.notify("配置文件有错误", err.Error(), niifError)
		a.refreshTray()
		return
	}
	if a.cfg.Hotkey != oldHotkey || a.hotkey == nil {
		a.setupHotkey()
	}
	if notifyResult {
		a.notify("配置已重新加载", fmt.Sprintf("共 %d 套配置", len(a.cfg.Profiles)), niifInfo)
	}
	a.refreshTray()
}

func (a *App) setupHotkey() {
	if a.tray == nil {
		return
	}
	a.tray.UnregisterHotkey()
	a.hotkey = nil
	a.hotkeyErr = ""
	if a.cfg == nil || a.cfg.Hotkey == "" {
		return
	}
	hk, err := parseHotkey(a.cfg.Hotkey)
	if err != nil {
		a.hotkeyErr = err.Error()
		a.notify("快捷键无效", err.Error(), niifWarning)
		return
	}
	if err := a.tray.RegisterHotkey(hk); err != nil {
		a.logger.Printf("注册快捷键失败: %v", err)
		a.hotkeyErr = hk.Text + " 注册失败，可能已被其他程序占用，请换一个组合"
		a.notify("快捷键注册失败", a.hotkeyErr, niifWarning)
		return
	}
	a.hotkey = &hk
	a.logger.Printf("快捷键已注册: %s", hk.Text)
}

func (a *App) showAbout() {
	hk := "未设置"
	if a.hotkey != nil {
		hk = a.hotkey.Text
	}
	mode := "标准模式（%APPDATA%）"
	if a.paths.Portable {
		mode = "便携模式（exe 同目录）"
	}
	text := fmt.Sprintf("%s v%s\n快捷切换 Windows 系统代理 / 环境变量 / git / npm 代理\n\n"+
		"左键托盘图标：开 / 关当前代理\n右键托盘图标：切换配置、打开设置页面\n全局快捷键：%s\n\n"+
		"%s\n配置文件：%s\n日志文件：%s\n\n"+
		"命令行用法：\n  ProxySwitch.exe on | off | toggle | status\n  ProxySwitch.exe use <配置名>",
		appName, appVersion, hk, mode, a.paths.Config, a.paths.Log)
	messageBox(0, text, "关于 "+appName, mbOK|mbIconInformation|mbSetForeground|mbTopmost)
}

func (a *App) exit() {
	a.settings.Stop()
	_ = os.Remove(filepath.Join(a.paths.Dir, settingsURLFile))
	if a.cfg != nil && a.cfg.DisableOnExit {
		if st := a.status(); st.On {
			a.logger.Printf("退出时关闭代理")
			a.turnOff(st)
		}
	}
	a.tray.Quit()
}

// onTimer 定时对比系统实际状态，别的程序改了系统代理时也能让图标跟上。
func (a *App) onTimer() {
	st := a.status()
	if st.On != a.lastOn {
		a.logger.Printf("检测到系统代理状态变化: on=%v", st.On)
		a.refreshTray()
	}
}

// run 启动托盘并进入消息循环。
func (a *App) run(iconOn, iconOff []byte) error {
	created, cfgErr := a.loadConfigFile()

	tray, err := newTray(iconOn, iconOff, a.paths.Dir)
	if err != nil {
		return err
	}
	a.tray = tray
	tray.OnLeftClick = a.toggle
	tray.OnRightClick = func() { a.handleCommand(tray.ShowMenu(a.buildMenu())) }
	tray.OnHotkey = a.toggle
	tray.OnTimer = a.onTimer
	tray.OnQuit = func() { a.logger.Printf("退出") }

	refreshAutostartPath()
	a.setupHotkey()
	a.refreshTray()
	tray.StartTimer(3000)
	a.started = true

	switch {
	case cfgErr != nil:
		a.notify("配置文件有错误", cfgErr.Error()+"\n已打开设置页面，可以在页面里重新添加配置并保存", niifError)
		a.openSettings()
	case created:
		a.notify("欢迎使用 "+appName, "已在浏览器打开设置页面，填入你的代理地址后点「保存」即可。\n之后左键托盘图标就能一键开关代理。", niifInfo)
		a.openSettings()
	}
	a.logger.Printf("%s v%s 启动，配置：%s", appName, appVersion, a.paths.Config)

	tray.Run()
	return nil
}

// ---------- 图形化设置页面 ----------

const settingsURLFile = "settings.url"

// openSettings 启动（或复用）本地设置服务并用默认浏览器打开。
func (a *App) openSettings() {
	url, err := a.settings.Start()
	if err != nil {
		a.notify("无法打开设置页面", err.Error(), niifError)
		return
	}
	// 记下地址，命令行 `ProxySwitch.exe settings` 可以直接打开
	_ = os.WriteFile(filepath.Join(a.paths.Dir, settingsURLFile), []byte(url), 0o600)
	if err := shellOpen(url); err != nil {
		a.logger.Printf("打开浏览器失败: %v", err)
		a.notify("无法自动打开浏览器", "请手动在浏览器里打开：\n"+url, niifWarning)
		return
	}
	a.logger.Printf("已打开设置页面")
}

// ui 把 fn 调度到托盘线程执行；没有托盘（命令行模式）时直接执行。
func (a *App) ui(fn func()) error {
	if a.tray == nil {
		fn()
		return nil
	}
	return a.tray.RunOnUI(fn)
}

// SettingsState 实现 SettingsBackend。
func (a *App) SettingsState() SettingsState {
	var st SettingsState
	_ = a.ui(func() { st = a.buildSettingsState() })
	return st
}

func (a *App) buildSettingsState() SettingsState {
	st := SettingsState{
		Version:     appVersion,
		Paths:       PathsInfo{Dir: a.paths.Dir, Config: a.paths.Config, Log: a.paths.Log, Portable: a.paths.Portable},
		Config:      a.cfg,
		ConfigError: a.cfgErr,
		Autostart:   isAutostartEnabled(),
		HotkeyError: a.hotkeyErr,
		Defaults:    DefaultsInfo{Bypass: defaultBypass, NoProxy: defaultNoProxy, Hotkey: "Ctrl+Alt+P"},
		Targets:     targetInfos,
		Platform:    "windows",
	}
	if a.hotkey != nil {
		st.HotkeyText = a.hotkey.Text
	}
	s := a.status()
	st.Status.On = s.On
	if s.Profile != nil {
		st.Status.Profile = s.Profile.Name
	}
	st.Status.External = s.External
	return st
}

// SaveConfig 实现 SettingsBackend：写文件 → 重新加载 → 重新注册快捷键。
func (a *App) SaveConfig(cfg *Config) error {
	var err error
	uiErr := a.ui(func() {
		if err = writeConfigFile(a.paths.Config, cfg); err != nil {
			err = fmt.Errorf("写入配置文件失败：%v", err)
			return
		}
		a.logger.Printf("设置页面保存了配置（%d 套）", len(cfg.Profiles))
		oldHotkey := ""
		if a.cfg != nil {
			oldHotkey = a.cfg.Hotkey
		}
		if _, e := a.loadConfigFile(); e != nil {
			err = e
			a.refreshTray()
			return
		}
		if a.cfg.Hotkey != oldHotkey || (a.hotkey == nil && a.cfg.Hotkey != "") {
			a.setupHotkey()
		}
		a.refreshTray()
	})
	if uiErr != nil {
		return uiErr
	}
	return err
}

// DoAction 实现 SettingsBackend。
func (a *App) DoAction(action, name string) error {
	var err error
	uiErr := a.ui(func() {
		if a.cfg == nil {
			err = fmt.Errorf("配置文件有错误，请先保存一份正确的配置")
			return
		}
		switch action {
		case "toggle":
			a.toggle()
		case "on":
			if st := a.status(); !st.On {
				a.turnOn(st.Profile)
			}
		case "off":
			if st := a.status(); st.On {
				a.turnOff(st)
			}
		case "use":
			p := a.cfg.FindProfile(name)
			if p == nil {
				err = fmt.Errorf("没有名为 %q 的配置", name)
				return
			}
			a.selectProfile(p)
		}
	})
	if uiErr != nil {
		return uiErr
	}
	return err
}

// SetAutostart 实现 SettingsBackend。
func (a *App) SetAutostart(enabled bool) error {
	var err error
	uiErr := a.ui(func() {
		err = setAutostart(enabled)
		if err == nil {
			a.logger.Printf("设置页面把开机自启改为 %v", enabled)
		}
	})
	if uiErr != nil {
		return uiErr
	}
	return err
}
