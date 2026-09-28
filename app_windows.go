//go:build windows

package main

import (
	"crypto/subtle"
	"encoding/json"
	"fmt"
	"log/slog"
	"strconv"
	"strings"
	"sync/atomic"
	"time"
)

// App 把核心逻辑（Engine）接到 Windows 托盘上：图标、菜单、通知、快捷键、定时检查，并为设置页提供接口。
// Engine 只在 UI 线程上调用；后台 goroutine 和设置页的请求都经 Tray.RunOnUi 排队执行。

const (
	menuHeader = iota + 1
	menuToggle
	menuSettings
	menuAutostart
	menuAutoSwitch
	menuTest
	menuEditConfig
	menuUpdate
	menuShare
	menuShareCopy
	menuTun
	menuDiagnose
	menuExit
	menuTerminalBase = 50
	menuProfileBase  = 100
	// 订阅子菜单里的各项（节点、测速、更新订阅）从这里开始编号，含义记在 App.menuChoices。
	menuChoiceBase = 1000
	// 托盘菜单在 UI 线程上读取节点，内核没有及时响应时不再等。
	menuNodesTimeout = 300 * time.Millisecond
)

// menuChoice 是订阅子菜单里的一项：action 为 select（node 为空表示自动选择）、use、test、update、mode 或 rules；
// 也可以是「策略组」子菜单里的一项：action 为 group，给策略组 group 选中 node（见 PolicyGroup.Node）。
type menuChoice struct {
	profileId string
	node      string
	action    string
	mode      string
	group     string
}

const (
	toggleHotkeyId    = 1
	profileHotkeyBase = 101

	statusTimerId   = 10
	statusInterval  = 2 * time.Second
	networkInterval = 3 * time.Second
	healthInterval  = 5 * time.Second
	healthTimeout   = 2 * time.Second
)

type App struct {
	// 订阅的下载和设置页上的订阅操作；托盘程序启动后才创建，命令行模式没有。
	*subscriptionService
	paths    Paths
	engine   *Engine
	tray     *Tray
	settings *SettingsServer

	icons        map[string]uintptr
	menuBitmaps  map[string]uintptr
	hotkeys      HotkeyStatus
	hotkeyConfig string
	// linksRegistered 是上次按配置登记链接时 url_links 的值，还没登记过时为 nil。
	linksRegistered *bool
	gitAvailable    atomic.Bool
	// wslRestart 表示改过 WSL 的设置，要重启 WSL 才生效。
	wslRestart  atomic.Bool
	exitHandled bool
	// 已显示的警告和错误通知数，用来判断一个错误是否已经提示过。
	problemNotices int
	// 设置页发起的操作由页面自己显示结果，这期间不弹托盘通知。
	quiet bool
	// 最近一条通知对应的设置页，点击通知时打开。
	noticePage string
	// toasts 显示系统通知（可以带按钮），托盘程序启动后才创建；noticeToken 是通知上「立即更新」的链接带的随机数。
	toasts      *toaster
	noticeToken string
	// tunActive 是上次看到的 TUN 模式有没有在接管流量，刚开始接管时清除 DNS 缓存。
	tunActive bool
	// 这次是程序内更新后重新启动，保持原来的代理状态。
	restartedForUpdate bool
	// 最近一次检查更新发现的新版本，设置页和托盘菜单据此提示。
	latestUpdate *UpdateInfo
	// 上次运行时程序崩溃过，启动后提示。
	crashedLastTime bool
	// 最近一次弹出的托盘菜单里订阅子菜单各项的含义，按编号减去 menuChoiceBase 查找。
	menuChoices []menuChoice
	// 局域网共享期间阻止睡眠；shareError 是已经提示过的共享入口的问题。
	sleep      sleepGuard
	shareError string
}

func newApp(paths Paths) *App {
	app := &App{paths: paths, icons: map[string]uintptr{}, menuBitmaps: map[string]uintptr{}, noticeToken: newNoticeToken()}
	app.engine = newEngine(windowsSystem{}, paths, app.notify)
	app.settings = newSettingsServer(app)
	_, err := gitPath()
	app.gitAvailable.Store(err == nil)
	return app
}

// run 创建托盘并进入消息循环，直到退出。settingsPage 不为空时启动后打开设置页的这一页。
func (app *App) run(autostarted bool, settingsPage string) error {
	coInitialize()
	enableDarkMenus()
	created, loadErr := app.engine.LoadConfig()
	tray, err := newTray(app)
	if err != nil {
		return err
	}
	app.tray = tray
	app.toasts = newToaster(func(notice Notice, timeout time.Duration) {
		_ = app.tray.RunOnUi(func() { app.showBalloon(notice, timeout) })
	})
	// 内核和订阅下载会从后台通知 UI 线程，所以在托盘创建之后再启动；配置已经加载，立即把状态交给内核。
	app.subscriptionService = newSubscriptionService(app.engine, newCore(app.onCoreError), app.tray.RunOnUi)
	app.engine.syncCore()
	go app.subscriptionService.Run()
	app.applyUiConfig()
	status := app.engine.Status()
	if err := tray.Show(app.iconFor(status), app.tooltipFor(status)); err != nil {
		return err
	}
	refreshAutostartPath()
	slog.Info("ProxySwitch 已启动", "version", appVersion, "portable", app.paths.Portable, "autostart", autostarted)

	if loadErr != nil && !created {
		app.notify(Notice{Level: noticeError, Title: "配置文件有错误", Text: loadErr.Error()})
	}
	if !app.restartedForUpdate {
		app.engine.RunStartupAction()
	}
	app.refresh()
	tray.StartTimer(statusTimerId, statusInterval)
	go app.watchNetwork()
	go app.watchHealth()
	go app.watchUpdates()

	if created {
		app.notify(Notice{Level: noticeInfo, Title: "ProxySwitch 已在托盘运行", Text: "单击托盘图标开关代理，右键打开菜单", Icon: iconStateOff})
	}
	if app.crashedLastTime {
		app.notify(Notice{Level: noticeWarning, Title: "ProxySwitch 上次意外退出了", Text: "详细信息已记录，点这里查看；反馈问题时附上会更快解决", Page: "diagnostics"})
	}
	if previous := app.engine.RecordVersion(appVersion); previous != "" {
		app.notify(Notice{Level: noticeInfo, Title: "ProxySwitch 已更新到 " + appVersion, Text: "原来的版本是 " + previous + "，设置和代理配置都已保留", Icon: iconStateOn, Color: profilePalette[1], Page: "about", Tag: noticeTagUpdate})
	}
	// 首次运行、或还没有任何代理配置时直接打开设置页引导添加；开机自启时不打扰。
	config := app.engine.Config()
	switch {
	case settingsPage != "":
		app.openSettingsAt(settingsPage, "")
	case created || (config != nil && len(config.Profiles) == 0 && !autostarted):
		app.openSettings()
	}
	tray.Run()
	return nil
}

// applyUiConfig 在配置加载或变更后同步托盘行为和快捷键。
func (app *App) applyUiConfig() {
	config := app.engine.Config()
	if config == nil {
		return
	}
	app.tray.DoubleClickEnabled = config.TrayDoubleClick != "none"
	app.registerHotkeys(config)
	app.registerLinks(config.UrlLinks)
}

// registerLinks 在启动时和 url_links 改变时登记或取消 proxyswitch:// 和 clash:// 链接。
func (app *App) registerLinks(enabled bool) {
	if app.linksRegistered != nil && *app.linksRegistered == enabled {
		return
	}
	app.linksRegistered = &enabled
	if err := applyLinks(enabled); err != nil {
		slog.Warn("登记链接失败", "err", err)
	}
	if !enabled {
		// 系统通知的点击和按钮靠链接，关闭链接后通知改用托盘气泡，通知用的应用 ID 也一起取消登记。
		if err := unregisterToastApp(); err != nil {
			slog.Warn("取消登记通知的应用 ID 失败", "err", err)
		}
		if app.toasts != nil {
			app.toasts.forget()
		}
	}
}

// toastsEnabled 表示通知用系统通知显示：登记了链接（点通知和按钮靠它），Windows 也支持没有打包的程序只在注册表里
// 登记应用 ID 就显示通知（Windows 10 1809 起）。
func (app *App) toastsEnabled() bool {
	return app.toasts != nil && app.linksRegistered != nil && *app.linksRegistered && windowsBuild() >= toastMinBuild
}

func (app *App) registerHotkeys(config *Config) {
	signature := config.Hotkey + "|" + config.ProfileHotkeys + "|" + strconv.Itoa(len(config.Profiles))
	if signature == app.hotkeyConfig {
		return
	}
	app.hotkeyConfig = signature
	app.tray.UnregisterHotkeys()
	app.hotkeys = HotkeyStatus{}
	var problems []string
	if config.Hotkey != "" {
		if hotkey, err := parseHotkey(config.Hotkey); err == nil {
			app.hotkeys.Toggle = hotkey.Text
			if err := app.tray.RegisterHotkey(toggleHotkeyId, hotkey); err != nil {
				app.hotkeys.ToggleError = "「" + hotkey.Text + "」已被其他程序占用，请换一个"
				problems = append(problems, hotkey.Text)
			}
		}
	}
	if config.ProfileHotkeys != "" {
		if modifiers, err := parseModifiers(config.ProfileHotkeys); err == nil {
			var failed []string
			for index := 0; index < len(config.Profiles) && index < maxProfileHotkeys; index++ {
				hotkey := Hotkey{Modifiers: modifiers.Modifiers, KeyCode: uint32('1' + index), Text: modifiers.Text + "+" + strconv.Itoa(index+1)}
				if err := app.tray.RegisterHotkey(profileHotkeyBase+index, hotkey); err != nil {
					failed = append(failed, hotkey.Text)
				}
			}
			if len(failed) > 0 {
				app.hotkeys.ProfilesError = strings.Join(failed, "、") + " 已被其他程序占用"
				problems = append(problems, failed...)
			}
		}
	}
	if len(problems) > 0 {
		app.notify(Notice{Level: noticeWarning, Title: "快捷键被占用", Text: strings.Join(problems, "、") + " 已被其他程序占用，点这里换一个", Page: "general"})
	}
}

// ---------- 状态显示 ----------

func (app *App) refresh() {
	if app.tray == nil {
		return
	}
	status := app.engine.Status()
	app.updateShare(status)
	if app.subscriptionService != nil {
		active := app.subscriptionService.core.Status().Tun.Active
		if active && !app.tunActive {
			// 之前缓存的真实地址会让连接绕过内核的域名规则，清掉后程序重新解析，拿到内核给的地址。
			go func() { _ = flushDnsCache() }()
		}
		app.tunActive = active
	}
	app.tray.Update(app.iconFor(status), app.tooltipFor(status))
}

func (app *App) iconStyleFor(status Status) (string, string) {
	if app.engine.Config() == nil {
		return iconStateError, ""
	}
	switch status.State {
	case statusOn:
		if health, _ := app.engine.HealthInfo(); health == healthDown {
			return iconStateWarn, status.Profile.Color
		}
		return iconStateOn, status.Profile.Color
	case statusExternal:
		return iconStateExternal, ""
	}
	return iconStateOff, ""
}

func (app *App) iconFor(status Status) uintptr {
	state, color := app.iconStyleFor(status)
	size := systemMetric(smCxSmIcon)
	if size <= 0 {
		size = 16 * systemDpi() / 96
	}
	key := fmt.Sprintf("%s|%s|%d", state, color, size)
	if icon, found := app.icons[key]; found {
		return icon
	}
	icon, err := createIcon(renderToggleIcon(size, trayIconStyle(state, color)))
	if err != nil {
		slog.Warn("创建托盘图标失败", "err", err)
		return 0
	}
	app.icons[key] = icon
	return icon
}

func (app *App) tooltipFor(status Status) string {
	lines := []string{appName}
	config := app.engine.Config()
	switch {
	case config == nil:
		lines = append(lines, "配置文件有错误")
	case status.State == statusOn:
		lines = append(lines, "已开启："+status.Profile.Name, status.Profile.Summary())
		if health, _ := app.engine.HealthInfo(); health == healthDown {
			lines = append(lines, "代理服务器连不上")
		}
	case status.State == statusExternal:
		lines = append(lines, "系统代理由其他程序设置", status.External)
	case app.engine.AutoOffPending():
		lines = append(lines, "已自动关闭：代理服务器连不上")
	case status.Profile == nil:
		lines = append(lines, "还没有代理配置，单击添加")
	default:
		lines = append(lines, "已关闭，单击开启："+status.Profile.Name)
	}
	if app.subscriptionService != nil && app.subscriptionService.core.Status().Tun.Active {
		lines = append(lines, "TUN 模式：接管全部流量")
	}
	if config != nil && config.Share.Enabled && app.subscriptionService != nil {
		if address := app.shareAddress(); address != "" {
			lines = append(lines, "局域网共享："+address)
		} else {
			lines = append(lines, "局域网共享：没有连上局域网")
		}
	}
	if app.subscriptionService != nil {
		if speed := app.Speed().Text(); speed != "" {
			lines = append(lines, speed)
		}
	}
	return truncateRunes(strings.Join(lines, "\n"), 127)
}

func truncateRunes(text string, limit int) string {
	runes := []rune(text)
	if len(runes) <= limit {
		return text
	}
	return string(runes[:limit-1]) + "…"
}

// notify 按通知级别显示托盘通知；命令行模式没有托盘，出错时弹对话框。
func (app *App) notify(notice Notice) {
	slog.Info("通知", "title", notice.Title, "text", strings.ReplaceAll(notice.Text, "\n", " | "))
	if notice.Level != noticeInfo {
		app.problemNotices++
	}
	if app.quiet {
		return
	}
	if app.tray == nil {
		if notice.Level != noticeInfo {
			icon := uint32(mbIconWarning)
			if notice.Level == noticeError {
				icon = mbIconError
			}
			messageBox(0, notice.Title+"\n\n"+notice.Text, appName, mbOk|icon|mbSetForeground|mbTopmost)
		}
		return
	}
	level, seconds := "all", defaultNotifySeconds
	if config := app.engine.Config(); config != nil {
		level, seconds = config.NotifyLevel, config.NotifySeconds
	}
	if level == "none" || (level == "errors" && notice.Level == noticeInfo) {
		return
	}
	timeout := time.Duration(seconds) * time.Second
	if notice.Level != noticeInfo && timeout > 0 && timeout < 6*time.Second {
		// 出错和警告多留几秒，免得没看清就消失了。
		timeout = 6 * time.Second
	}
	if app.toastsEnabled() && app.toasts.show(notice, timeout) {
		return
	}
	app.showBalloon(notice, timeout)
}

// showBalloon 用托盘气泡显示通知，点击时打开通知对应的设置页。
func (app *App) showBalloon(notice Notice, timeout time.Duration) {
	var flags uint32
	var largeIcon uintptr
	switch notice.Level {
	case noticeWarning:
		flags = niifWarning
	case noticeError:
		flags = niifError
	default:
		flags = niifInfo
		if notice.Icon != "" {
			size := systemMetric(smCxIcon)
			if size <= 0 {
				size = 32
			}
			if icon, err := createIcon(renderToggleIcon(size, trayIconStyle(notice.Icon, notice.Color))); err == nil {
				largeIcon = icon
			}
		}
	}
	app.noticePage = notice.Page
	app.tray.Notify(notice.Title, notice.Text, flags, largeIcon, timeout)
}

// ---------- 托盘事件 ----------

func (app *App) onTrayClick() {
	if config := app.engine.Config(); config != nil {
		app.runTrayAction(config.TrayClick)
		return
	}
	app.openSettings()
}

func (app *App) onTrayDoubleClick() {
	if config := app.engine.Config(); config != nil {
		app.runTrayAction(config.TrayDoubleClick)
	}
}

func (app *App) runTrayAction(action string) {
	switch action {
	case "toggle":
		app.toggleOrSetup()
	case "settings":
		app.openSettings()
	case "menu":
		app.onTrayMenu(cursorPosition())
	}
}

// setTunFromUi 在托盘菜单里开关 TUN 模式，保存到配置文件。
func (app *App) setTunFromUi(enabled bool) {
	updated := app.engine.Config().Clone()
	updated.Tun.Enabled = enabled
	if err := app.engine.SaveConfig(updated); err != nil {
		app.notify(Notice{Level: noticeError, Title: "保存配置失败", Text: err.Error()})
		return
	}
	switch {
	case !enabled:
		app.notify(Notice{Level: noticeInfo, Title: "TUN 模式已关闭", Text: "只有跟随系统代理的程序经过代理"})
	case app.engine.activeSubscriptionId() != "" && app.engine.Status().State == statusOn:
		app.notify(Notice{Level: noticeInfo, Title: "TUN 模式已开启", Text: "内核要以管理员权限运行，请在弹出的确认框里点「是」", Page: "general"})
	default:
		app.notify(Notice{Level: noticeInfo, Title: "TUN 模式已开启", Text: "开启订阅配置时生效，到时内核会请求管理员权限", Page: "general"})
	}
	app.refresh()
}

// toggleOrSetup 开关代理；还没有配置时打开设置页添加。
func (app *App) toggleOrSetup() {
	config := app.engine.Config()
	if config == nil || len(config.Profiles) == 0 {
		app.openSettings()
		return
	}
	_ = app.engine.Toggle()
	app.refresh()
}

func (app *App) onTrayMenu(anchor point) {
	items := app.menuItems()
	command := app.tray.ShowMenu(items, anchor)
	if command != 0 {
		app.handleMenu(command)
	}
}

func escapeMenuText(text string) string {
	return strings.ReplaceAll(strings.ReplaceAll(text, "&", "&&"), "\t", " ")
}

func (app *App) menuItems() []MenuItem {
	app.menuChoices = nil
	separator := MenuItem{Separator: true}
	config := app.engine.Config()
	if config == nil {
		return []MenuItem{
			{Id: menuHeader, Text: "配置文件有错误", Disabled: true},
			separator,
			{Id: menuEditConfig, Text: "编辑配置文件..."},
			{Id: menuSettings, Text: "设置...", Default: true},
			separator,
			{Id: menuExit, Text: "退出"},
		}
	}
	status := app.engine.Status()
	toggleKey := ""
	if app.hotkeys.Toggle != "" && app.hotkeys.ToggleError == "" {
		toggleKey = "\t" + app.hotkeys.Toggle
	}
	clickToggles := config.TrayClick == "toggle"
	var items []MenuItem
	switch status.State {
	case statusOn:
		header := "代理已开启：" + escapeMenuText(status.Profile.Name)
		if health, _ := app.engine.HealthInfo(); health == healthDown {
			header += "（连不上代理服务器）"
		}
		items = append(items, MenuItem{Id: menuHeader, Text: header, Disabled: true}, MenuItem{Id: menuToggle, Text: "关闭代理" + toggleKey, Default: clickToggles})
	case statusExternal:
		items = append(items,
			MenuItem{Id: menuHeader, Text: "系统代理由其他程序设置：" + escapeMenuText(truncateRunes(status.External, 40)), Disabled: true},
			MenuItem{Id: menuToggle, Text: "关闭系统代理" + toggleKey, Default: clickToggles})
	default:
		header := "代理已关闭"
		if app.engine.AutoOffPending() {
			header = "代理已自动关闭（代理服务器连不上）"
		}
		items = append(items, MenuItem{Id: menuHeader, Text: header, Disabled: true})
		if status.Profile != nil {
			items = append(items, MenuItem{Id: menuToggle, Text: "开启代理" + toggleKey, Default: clickToggles})
		} else {
			items = append(items, MenuItem{Id: menuSettings, Text: "添加代理配置...", Default: true})
		}
	}
	if len(config.Profiles) > 0 {
		items = append(items, separator)
		modifiers, modifiersErr := parseModifiers(config.ProfileHotkeys)
		for index := range config.Profiles {
			profile := &config.Profiles[index]
			text := escapeMenuText(profile.Name)
			if config.ProfileHotkeys != "" && modifiersErr == nil && index < maxProfileHotkeys {
				text += "\t" + modifiers.Text + "+" + strconv.Itoa(index+1)
			}
			active := status.State == statusOn && status.Profile.Id == profile.Id
			item := MenuItem{Id: uint32(menuProfileBase + index), Text: text, Checked: active, Radio: true, Bitmap: app.menuDot(profile.Color)}
			if profile.IsSubscription() && app.subscriptionService != nil {
				item.Children = app.subscriptionMenu(config, profile, active)
			}
			items = append(items, item)
		}
		if groups := app.groupsMenu(config); len(groups) > 0 {
			items = append(items, MenuItem{Text: "策略组", Children: groups})
		}
	}
	items = append(items, separator)
	if status.State == statusOn {
		items = append(items, MenuItem{Id: menuTest, Text: "测试代理连接"})
	}
	items = append(items, MenuItem{Id: menuDiagnose, Text: "网址诊断..."})
	if status.State == statusOn && status.Profile.Server != "" {
		var commands []MenuItem
		for index, command := range terminalCommands(serverToUrl(status.Profile.Server), status.Profile.NoProxy) {
			commands = append(commands, MenuItem{Id: uint32(menuTerminalBase + index), Text: command.Label})
		}
		items = append(items, MenuItem{Text: "复制终端代理命令", Children: commands})
	}
	if len(config.AutoSwitch.Rules) > 0 {
		items = append(items, MenuItem{Id: menuAutoSwitch, Text: "按网络自动切换", Checked: config.AutoSwitch.Enabled})
	}
	if app.subscriptionService != nil && config.HasSubscriptions() {
		items = append(items, MenuItem{Id: menuTun, Text: "TUN 模式（接管全部流量）", Checked: config.Tun.Enabled})
	}
	if app.subscriptionService != nil {
		items = append(items, MenuItem{Id: menuShare, Text: "局域网共享（PS5 等设备）", Checked: config.Share.Enabled})
		if address := app.shareAddress(); config.Share.Enabled && address != "" {
			items = append(items, MenuItem{Id: menuShareCopy, Text: "复制设备上要填的地址 " + address})
		}
	}
	items = append(items,
		MenuItem{Id: menuSettings, Text: "设置...", Default: config.TrayClick == "settings"},
		MenuItem{Id: menuAutostart, Text: "开机自动启动", Checked: isAutostartEnabled()},
		MenuItem{Id: menuUpdate, Text: app.updateMenuText()},
		separator,
		MenuItem{Id: menuExit, Text: "退出"},
	)
	return items
}

// subscriptionMenu 是订阅配置的子菜单：没开启时可以直接开启；选择节点（自动选择或某个节点，显示最近测得的延迟）；
// 切换按规则分流和全局代理；测速、更新订阅和分流规则。
func (app *App) subscriptionMenu(config *Config, profile *Profile, active bool) []MenuItem {
	var items []MenuItem
	if !active {
		items = append(items, app.menuChoice(MenuItem{Text: "使用这个配置"}, menuChoice{profileId: profile.Id, action: "use"}), MenuItem{Separator: true})
	}
	if err := app.engine.subscriptionProblem(profile); err != nil {
		items = append(items, MenuItem{Text: escapeMenuText(truncateRunes(err.Error(), 40)), Disabled: true})
	} else if nodes, err := app.core.NodesWithin(profile.Id, menuNodesTimeout); err != nil {
		items = append(items, MenuItem{Text: "读不到节点：" + escapeMenuText(truncateRunes(err.Error(), 30)), Disabled: true})
	} else {
		auto := "自动选择（延迟最低）"
		if nodes.Selected == "" && nodes.Current != "" {
			auto = "自动选择：" + escapeMenuText(nodes.Current)
		}
		items = append(items, app.menuChoice(MenuItem{Text: auto, Radio: true, Checked: nodes.Selected == ""}, menuChoice{profileId: profile.Id, action: "select"}))
		for _, node := range nodes.Nodes {
			text := escapeMenuText(node.Name)
			switch {
			case node.Tested && !node.Alive:
				text += "\t超时"
			case node.Tested:
				text += fmt.Sprintf("\t%d ms", max(1, node.Delay))
			}
			items = append(items, app.menuChoice(MenuItem{Text: text, Radio: true, Checked: nodes.Selected == node.Name}, menuChoice{profileId: profile.Id, node: node.Name, action: "select"}))
		}
	}
	items = append(items, MenuItem{Separator: true},
		app.menuChoice(MenuItem{Text: "按规则分流（" + escapeMenuText(truncateRunes(rulesSummary(config), 20)) + "）", Radio: true, Checked: profile.Mode == "rule"}, menuChoice{profileId: profile.Id, action: "mode", mode: "rule"}),
		app.menuChoice(MenuItem{Text: "全局代理", Radio: true, Checked: profile.Mode == "global"}, menuChoice{profileId: profile.Id, action: "mode", mode: "global"}),
		MenuItem{Separator: true},
		app.menuChoice(MenuItem{Text: "全部测速"}, menuChoice{profileId: profile.Id, action: "test"}),
		app.menuChoice(MenuItem{Text: "更新订阅"}, menuChoice{profileId: profile.Id, action: "update"}))
	if config.hasDownloadedRuleSets() {
		items = append(items, app.menuChoice(MenuItem{Text: "更新分流规则"}, menuChoice{profileId: profile.Id, action: "rules"}))
	}
	return items
}

// groupsMenu 是「策略组」子菜单：每个组一个子菜单，列出候选和最近测得的延迟，手动选择的组可以点选，自动挑选的组
// 勾出它挑中的节点。没有策略组或内核没在运行时为空。
func (app *App) groupsMenu(config *Config) []MenuItem {
	if app.subscriptionService == nil || len(config.PolicyGroups) == 0 {
		return nil
	}
	source := app.engine.groupSource()
	if source == "" || !app.core.Status().Running {
		return nil
	}
	states, err := app.core.GroupStates(config.PolicyGroups, source, menuNodesTimeout)
	if err != nil {
		return []MenuItem{{Text: "读不到策略组：" + escapeMenuText(truncateRunes(err.Error(), 30)), Disabled: true}}
	}
	var items []MenuItem
	for _, state := range states {
		text := escapeMenuText(state.Name)
		if current := groupCurrentText(state); current != "" {
			text += "\t" + escapeMenuText(truncateRunes(current, 24))
		}
		children := []MenuItem{{Text: groupTypeTitles[state.Type] + "：" + groupTypeDetails[state.Type], Disabled: true}, {Separator: true}}
		for _, member := range state.Members {
			label := escapeMenuText(member.Label)
			switch {
			case member.Node && member.Tested && !member.Alive:
				label += "\t超时"
			case member.Node && member.Tested:
				label += fmt.Sprintf("\t%d ms", max(1, member.Delay))
			}
			item := MenuItem{Text: label, Radio: true, Checked: state.Now == member.Value, Disabled: state.Type != groupSelect}
			if state.Type == groupSelect {
				item = app.menuChoice(item, menuChoice{action: "group", group: state.Name, node: member.Value})
			}
			children = append(children, item)
		}
		items = append(items, MenuItem{Text: text, Children: children})
	}
	return items
}

// groupCurrentText 是策略组现在用的：跟随节点、自动选择时是它们选中的节点，直连写成直连。
func groupCurrentText(state CoreGroupState) string {
	switch {
	case state.Current != "":
		return state.Current
	case state.Now == groupMemberDirect:
		return groupDirectLabel
	}
	return ""
}

// groupNodeLabel 是策略组选中的成员（配置里的写法）的显示名。
func groupNodeLabel(node string) string {
	switch node {
	case "":
		return groupFollowLabel
	case groupMemberDirect:
		return groupDirectLabel
	}
	return node
}

// menuChoice 给订阅子菜单的一项分配编号并记下它的含义。
func (app *App) menuChoice(item MenuItem, choice menuChoice) MenuItem {
	item.Id = uint32(menuChoiceBase + len(app.menuChoices))
	app.menuChoices = append(app.menuChoices, choice)
	return item
}

// runMenuChoice 执行订阅子菜单里的一项。选择节点时配置还没开启就顺便开启；测速、更新订阅和分流规则在后台进行，完成后通知。
func (app *App) runMenuChoice(choice menuChoice) {
	config := app.engine.Config()
	if config == nil {
		return
	}
	if choice.action == "group" {
		if err := app.engine.SelectGroupNode(choice.group, choice.node); err != nil {
			app.notify(Notice{Level: noticeError, Title: "没有切换成功", Text: err.Error()})
			return
		}
		notice := Notice{Level: noticeInfo, Title: "已切换策略组", Text: choice.group + " · " + groupNodeLabel(choice.node), Icon: iconStateOn}
		if status := app.engine.Status(); status.State == statusOn && status.Profile != nil {
			notice.Color = status.Profile.Color
		}
		app.notify(notice)
		return
	}
	profile := config.FindProfileById(choice.profileId)
	if profile == nil {
		return
	}
	profileCopy := *profile
	switch choice.action {
	case "use":
		_ = app.engine.UseProfile(profile.Name)
	case "select":
		if err := app.engine.SelectNode(profile.Id, choice.node); err != nil {
			app.notify(Notice{Level: noticeError, Title: "没有切换成功", Text: err.Error()})
			return
		}
		if status := app.engine.Status(); status.State == statusOn && status.Profile != nil && status.Profile.Id == profileCopy.Id {
			label := choice.node
			if label == "" {
				label = "自动选择（延迟最低）"
			}
			app.notify(Notice{Level: noticeInfo, Title: "已切换节点", Text: profileCopy.Name + " · " + label, Icon: iconStateOn, Color: profileCopy.Color})
		} else {
			_ = app.engine.UseProfile(profileCopy.Name)
		}
	case "test":
		testUrl := config.TestUrl
		go func() {
			delays, err := app.core.TestDelays(profileCopy.Id, testUrl)
			notice := delaysNotice(profileCopy, delays, err)
			_ = app.tray.RunOnUi(func() { app.notify(notice) })
		}()
	case "mode":
		if err := app.engine.SetMode(profile.Id, choice.mode); err != nil {
			app.notify(Notice{Level: noticeError, Title: "没有切换成功", Text: err.Error()})
			return
		}
		text := "所有网站都经过节点"
		if choice.mode == "rule" {
			text = "分流规则：" + rulesSummary(config)
		}
		title := map[string]string{"rule": "已切换到按规则分流", "global": "已切换到全局代理"}[choice.mode]
		app.notify(Notice{Level: noticeInfo, Title: title, Text: profileCopy.Name + " · " + text, Icon: iconStateOn, Color: profileCopy.Color})
	case "rules":
		go func() {
			result, err := app.UpdateAllRuleSets()
			_ = app.tray.RunOnUi(func() {
				switch {
				case err != nil:
					app.notify(Notice{Level: noticeWarning, Title: "分流规则没有更新成功", Text: err.Error(), Page: "rules"})
				case len(result.Failed) > 0:
					app.notify(Notice{Level: noticeWarning, Title: fmt.Sprintf("有 %d 个规则集没有更新成功", len(result.Failed)), Text: strings.Join(result.Failed, "\n"), Page: "rules"})
				default:
					app.notify(Notice{Level: noticeInfo, Title: "分流规则已更新", Text: fmt.Sprintf("更新了 %d 个规则集", result.Updated), Icon: iconStateOn, Color: profileCopy.Color})
				}
			})
		}()
	case "update":
		go func() {
			err := app.UpdateSubscription(profileCopy.Id)
			_ = app.tray.RunOnUi(func() {
				if err != nil {
					app.notify(Notice{Level: noticeWarning, Title: "订阅没有更新成功", Text: profileCopy.Name + "\n" + err.Error(), Page: "proxies"})
					return
				}
				nodes := app.engine.subscriptionInfos()[profileCopy.Id].Nodes
				app.notify(Notice{Level: noticeInfo, Title: "订阅已更新", Text: fmt.Sprintf("%s · 共 %d 个节点", profileCopy.Name, nodes), Icon: iconStateOn, Color: profileCopy.Color})
			})
		}()
	}
}

// updateMenuText 是托盘菜单里检查更新一项的文字，已经知道有新版本时直接显示版本号。
func (app *App) updateMenuText() string {
	if app.latestUpdate != nil {
		return "更新到 " + escapeMenuText(app.latestUpdate.Latest) + "..."
	}
	return "检查更新..."
}

// menuDot 返回配置颜色的圆点位图，按颜色缓存。
func (app *App) menuDot(profileColor string) uintptr {
	if bitmap, found := app.menuBitmaps[profileColor]; found {
		return bitmap
	}
	fill, ok := parseHexColor(profileColor)
	if !ok {
		return 0
	}
	size := systemMetric(smCxMenuCheck)
	if size <= 0 {
		size = 16
	}
	bitmap, err := createMenuBitmap(renderDot(size, fill))
	if err != nil {
		return 0
	}
	app.menuBitmaps[profileColor] = bitmap
	return bitmap
}

func (app *App) handleMenu(command uint32) {
	config := app.engine.Config()
	switch {
	case command == menuToggle:
		app.toggleOrSetup()
	case command == menuSettings:
		app.openSettings()
	case command == menuAutostart:
		if err := setAutostart(!isAutostartEnabled()); err != nil {
			app.notify(Notice{Level: noticeError, Title: "设置开机自启失败", Text: err.Error()})
		}
	case command == menuAutoSwitch && config != nil:
		updated := *config
		updated.AutoSwitch.Enabled = !config.AutoSwitch.Enabled
		if err := app.engine.SaveConfig(&updated); err != nil {
			app.notify(Notice{Level: noticeError, Title: "保存配置失败", Text: err.Error()})
		}
	case command == menuTest:
		app.testActiveProxy()
	case command == menuShare && config != nil:
		_ = app.setShareFromUi(!config.Share.Enabled)
	case command == menuTun && config != nil:
		app.setTunFromUi(!config.Tun.Enabled)
	case command == menuShareCopy:
		app.copyShareAddress()
	case command == menuDiagnose:
		app.openSettingsAt("diagnose", "")
	case command == menuUpdate:
		// 打开「关于」页并立即检查，有新版本时在那里一键更新。
		app.openSettingsAt("about", "check-update")
	case command >= menuTerminalBase && command < menuProfileBase:
		app.copyTerminalCommand(int(command - menuTerminalBase))
	case command >= menuChoiceBase:
		if index := int(command - menuChoiceBase); index < len(app.menuChoices) {
			app.runMenuChoice(app.menuChoices[index])
		}
	case command == menuEditConfig:
		if err := openWithEditor("", app.paths.Config); err != nil {
			app.notify(Notice{Level: noticeError, Title: "无法打开配置文件", Text: err.Error()})
		}
	case command == menuExit:
		app.warnWinHttpOnExit()
		app.tray.Quit()
		return
	case command >= menuProfileBase && config != nil:
		index := int(command - menuProfileBase)
		if index < len(config.Profiles) {
			_ = app.engine.UseProfile(config.Profiles[index].Name)
		}
	}
	app.refresh()
}

// copyTerminalCommand 把当前代理的终端命令复制到剪贴板，index 对应 terminalCommands 的顺序。
func (app *App) copyTerminalCommand(index int) {
	status := app.engine.Status()
	if status.State != statusOn || status.Profile.Server == "" {
		return
	}
	commands := terminalCommands(serverToUrl(status.Profile.Server), status.Profile.NoProxy)
	if index >= len(commands) {
		return
	}
	if err := setClipboardText(app.tray.window, commands[index].Command); err != nil {
		app.notify(Notice{Level: noticeError, Title: "复制失败", Text: err.Error()})
		return
	}
	app.notify(Notice{Level: noticeInfo, Title: "已复制 " + commands[index].Label + " 命令", Text: "粘贴到终端里回车，这个终端窗口就会使用代理", Icon: iconStateOn, Color: status.Profile.Color})
}

// testActiveProxy 在后台测试当前代理，结果用通知显示。
func (app *App) testActiveProxy() {
	status := app.engine.Status()
	config := app.engine.Config()
	if status.State != statusOn || config == nil {
		return
	}
	profile := *status.Profile
	testUrl := config.TestUrl
	go func() {
		result := testProfileConnection(profile.Server, profile.Pac, testUrl, proxyTestTimeout)
		// 没有测出延迟时（例如只能检查 PAC 脚本能否读取）不说“连接正常”。
		title := profile.Name + "：可以使用"
		if result.Millis > 0 {
			title = fmt.Sprintf("%s：连接正常，%d ms", profile.Name, result.Millis)
		}
		notice := Notice{Level: noticeInfo, Title: title, Text: result.Message, Icon: iconStateOn, Color: profile.Color}
		if !result.Ok {
			notice = Notice{Level: noticeWarning, Title: profile.Name + "：连接失败", Text: result.Message}
		}
		_ = app.tray.RunOnUi(func() { app.notify(notice) })
	}()
}

func (app *App) onHotkey(id int) {
	switch {
	case id == toggleHotkeyId:
		app.toggleOrSetup()
	case id >= profileHotkeyBase && id < profileHotkeyBase+maxProfileHotkeys:
		config := app.engine.Config()
		index := id - profileHotkeyBase
		if config == nil || index >= len(config.Profiles) {
			return
		}
		// 再按一次正在使用的配置的快捷键就关闭代理。
		status := app.engine.Status()
		if status.State == statusOn && status.Profile.Id == config.Profiles[index].Id {
			_ = app.engine.TurnOff()
		} else {
			_ = app.engine.UseProfile(config.Profiles[index].Name)
		}
		app.refresh()
	}
}

func (app *App) onTimer(id uintptr) {
	if id != statusTimerId {
		return
	}
	if app.engine.ReloadIfChanged() {
		app.applyUiConfig()
	}
	app.engine.GuardSystemProxy()
	app.refresh()
}

// onCopyData 执行另一个 ProxySwitch 进程转发来的命令行，返回退出码。
func (app *App) onCopyData(data []byte) uintptr {
	command, argument, _ := strings.Cut(string(data), "\x00")
	shown := app.problemNotices
	var err error
	switch command {
	case "on":
		err = app.engine.TurnOn()
	case "off":
		err = app.engine.TurnOff()
	case "toggle":
		err = app.engine.Toggle()
	case "use":
		err = app.engine.UseProfile(argument)
	case "share":
		err = app.shareCommand(argument)
	case "diagnose":
		app.openDiagnose(argument)
	case "update":
		// 通知上的「立即更新」带着这次运行的随机数，直接安装；其他地方打开的只检查更新。
		action := "check-update"
		if argument != "" && subtle.ConstantTimeCompare([]byte(argument), []byte(app.noticeToken)) == 1 {
			action = "install-update"
		}
		app.openSettingsAt("about", action)
	case "import":
		// 机场网站的「一键导入」：打开添加订阅的对话框，填好地址。
		app.openSettingsWith("proxies", "import-subscription", argument)
	case "settings":
		app.openSettingsAt(argument, "")
	default:
		return exitUsage
	}
	app.refresh()
	if err != nil {
		if app.problemNotices == shown {
			app.notify(Notice{Level: noticeError, Title: "命令执行失败", Text: err.Error()})
		}
		return exitFailure
	}
	return exitSuccess
}

func (app *App) onActivateRequest() {
	app.openSettings()
}

func (app *App) onNotificationClick() {
	page := app.noticePage
	if page == "" {
		page = "proxies"
	}
	app.openSettingsAt(page, "")
}

func (app *App) onSettingChange(section string) {
	switch section {
	case "ImmersiveColorSet":
		refreshMenuTheme()
	case "":
		// 分辨率或缩放变了，图标尺寸可能变化。
		app.clearIcons()
		app.refresh()
	}
}

func (app *App) onEndSession() {
	app.handleExit()
}

func (app *App) onDestroy() {
	app.handleExit()
	app.sleep.update(ShareConfig{})
	if app.subscriptionService != nil {
		app.core.Kill()
	}
	app.settings.Stop()
	app.clearIcons()
	for key, bitmap := range app.menuBitmaps {
		procDeleteObject.Call(bitmap)
		delete(app.menuBitmaps, key)
	}
	slog.Info("ProxySwitch 已退出")
}

// handleExit 在退出或注销时按 disable_on_exit 关闭代理，只执行一次。正在使用订阅时，内核随程序退出，
// 不管 disable_on_exit 都先关闭代理，下次启动后重新开启。
func (app *App) handleExit() {
	if app.exitHandled {
		return
	}
	app.exitHandled = true
	if config := app.engine.Config(); config != nil && config.DisableOnExit && app.engine.Status().State == statusOn {
		_ = app.engine.TurnOff()
	}
	app.engine.PrepareExit()
}

// onCoreError 在内核启动失败或意外退出时提示（在内核的后台 goroutine 里调用）。还没下载内核是正常情况，设置页会提示下载。
func (app *App) onCoreError(message string) {
	if message == errCoreMissing.Error() {
		return
	}
	_ = app.tray.RunOnUi(func() {
		app.notify(Notice{Level: noticeError, Title: "代理内核出错", Text: message, Page: "proxies"})
	})
}

func (app *App) clearIcons() {
	for key, icon := range app.icons {
		if icon != app.tray.icon {
			procDestroyIcon.Call(icon)
		}
		delete(app.icons, key)
	}
}

// openSettings 打开设置页（已打开时切到前台）。
func (app *App) openSettings() {
	app.openSettingsAt("", "")
}

// openSettingsAt 打开设置页并切到 page；page 为空时保持设置页当前的页面。action 不为空时切换后执行页面上的这个操作。
func (app *App) openSettingsAt(page, action string) {
	app.openSettingsWith(page, action, "")
}

// openDiagnose 打开网址诊断页；request 是命令行的「网址\x00视角」，有网址时立即诊断。
func (app *App) openDiagnose(request string) {
	address, perspective, _ := strings.Cut(request, "\x00")
	if strings.TrimSpace(address) == "" {
		app.openSettingsAt("diagnose", "")
		return
	}
	argument, _ := json.Marshal(map[string]string{"url": address, "perspective": perspective})
	app.openSettingsWith("diagnose", "diagnose", string(argument))
}

// openSettingsWith 和 openSettingsAt 一样，argument 是交给页面操作的参数。
func (app *App) openSettingsWith(page, action, argument string) {
	address, err := app.settings.Start()
	if err != nil {
		app.notify(Notice{Level: noticeError, Title: "无法打开设置", Text: err.Error()})
		return
	}
	if page != "" {
		app.settings.ShowPageWith(page, action, argument)
		address += "#" + page
	}
	mode := "app"
	if config := app.engine.Config(); config != nil {
		mode = config.SettingsWindow
	}
	if err := openSettingsWindow(address, mode); err != nil {
		app.notify(Notice{Level: noticeError, Title: "无法打开设置", Text: err.Error()})
	}
}

// ---------- 后台检查 ----------

func (app *App) watchNetwork() {
	for {
		info := readNetworkInfo()
		_ = app.tray.RunOnUi(func() {
			app.engine.UpdateNetwork(info)
			app.refresh()
		})
		time.Sleep(networkInterval)
	}
}

func (app *App) watchHealth() {
	for {
		time.Sleep(healthInterval)
		var target string
		if err := app.tray.RunOnUi(func() { target = app.engine.HealthTarget() }); err != nil || target == "" {
			continue
		}
		err := checkProxyReachable(target, healthTimeout)
		_ = app.tray.RunOnUi(func() {
			app.engine.HealthResult(target, err)
			app.refresh()
		})
	}
}

// ---------- 设置页接口（SettingsBackend） ----------

func (app *App) onUi(action func() error) error {
	var result error
	if err := app.tray.RunOnUi(func() {
		app.quiet = true
		result = action()
		app.quiet = false
		app.refresh()
	}); err != nil {
		return err
	}
	return result
}

func (app *App) State() SettingsState {
	var state SettingsState
	if err := app.tray.RunOnUi(func() { state = app.settingsState() }); err != nil {
		return SettingsState{Version: appVersion, Platform: "windows", ConfigError: err.Error(), Palette: profilePalette}
	}
	// 内核的情况要调用内核的接口，放在 UI 线程之外：内核没有响应时不会卡住托盘。
	if app.subscriptionService != nil {
		app.fillState(&state)
	}
	return state
}

func (app *App) settingsState() SettingsState {
	state := app.engine.settingsState()
	state.Platform = "windows"
	state.Autostart = isAutostartEnabled()
	state.Hotkeys = app.hotkeys
	state.Accent = systemAccentColor()
	state.Targets = targetInfos(app.gitAvailable.Load())
	state.Update = app.latestUpdate
	state.Links = currentLinks()
	if app.sleep.status != "" {
		state.Share.Awake = app.sleep.status
	}
	return state
}

func (app *App) SaveConfig(config *Config) error {
	return app.onUi(func() error {
		err := app.engine.SaveConfig(config)
		app.applyUiConfig()
		return err
	})
}

func (app *App) TurnOn() error {
	return app.onUi(app.engine.TurnOn)
}

func (app *App) TurnOff() error {
	return app.onUi(app.engine.TurnOff)
}

func (app *App) UseProfile(name string) error {
	return app.onUi(func() error { return app.engine.UseProfile(name) })
}

func (app *App) ApplyAutoSwitch() error {
	return app.onUi(app.engine.ApplyAutoSwitch)
}

func (app *App) ClearAllProxies() error {
	return app.onUi(app.engine.ClearAll)
}

func (app *App) SetAutostart(enabled bool) error {
	return setAutostart(enabled)
}

// Listeners 返回本机监听的端口；再补上常见代理端口，系统列表不完整时也能找到。
func (app *App) Listeners() []Listener {
	listeners := listTcpListeners()
	known := map[int]bool{}
	for _, listener := range listeners {
		known[listener.Port] = true
	}
	for _, listener := range commonPortListeners() {
		if !known[listener.Port] {
			listeners = append(listeners, listener)
		}
	}
	return listeners
}

func (app *App) Diagnostics() Diagnostics {
	system, source, err := readSystemProxy()
	diagnostics := Diagnostics{
		System:        system,
		SystemSource:  source,
		Connections:   append([]string{"局域网"}, rasEntryNames()...),
		MachinePolicy: machineWideProxy(),
		Env:           readEnvironmentProxy(),
		Git:           readGitStatus(),
		Npm:           readNpmProxy(),
	}
	if err != nil {
		diagnostics.SystemError = err.Error()
	}
	if crash, when := readPreviousCrash(app.paths.PreviousCrash, time.Now()); crash != "" {
		diagnostics.LastCrash, diagnostics.LastCrashTime = crash, when.Format("2006-01-02 15:04")
	}
	diagnostics.NpmrcPath, _ = npmrcPath()
	if proxy, _, err := readWinHttpProxy(); err == nil {
		diagnostics.WinHttp = proxy
	}
	app.gitAvailable.Store(diagnostics.Git.Available)
	return diagnostics
}

func (app *App) LogTail(maxLines int) string {
	return readLogTail(app.paths.Log, maxLines)
}

func (app *App) OpenConfigDir() error {
	return app.onUi(func() error { return shellOpen(app.paths.Dir) })
}

func (app *App) OpenConfigFile() error {
	return app.onUi(func() error {
		editor := ""
		if config := app.engine.Config(); config != nil {
			editor = config.Editor
		}
		return openWithEditor(editor, app.paths.Config)
	})
}

func (app *App) OpenLogFile() error {
	return app.onUi(func() error { return openWithEditor("", app.paths.Log) })
}

func (app *App) OpenUrl(address string) error {
	return app.onUi(func() error { return shellOpen(address) })
}

// RememberUpdate 记下检查更新的结果：有新版本时托盘菜单显示「更新到 x.y.z」，没有时清除。
func (app *App) RememberUpdate(info UpdateInfo) {
	_ = app.tray.RunOnUi(func() {
		app.latestUpdate = nil
		if info.Newer {
			app.latestUpdate = &info
		}
	})
}

// UpdatePaths 是检查和下载更新时依次尝试的网络路径：先经正在使用的代理、其他软件设置的系统代理和内核，
// 最后直连（GitHub 直连常常很慢）。
func (app *App) UpdatePaths() []string {
	paths := []string{""}
	_ = app.tray.RunOnUi(func() { paths = proxiesFirst(app.engine.DownloadPaths()) })
	return paths
}

// systemAccentColor 返回 Windows 的强调色（#rrggbb），读不到时用默认蓝色。
func systemAccentColor() string {
	value, found := readRegistryDword(hkeyCurrentUser, `Software\Microsoft\Windows\DWM`, "AccentColor")
	if !found {
		return "#0067c0"
	}
	// 注册表里是 0xAABBGGRR。
	return fmt.Sprintf("#%02x%02x%02x", value&0xFF, (value>>8)&0xFF, (value>>16)&0xFF)
}

func coInitialize() {
	procCoInitializeEx.Call(0, coinitApartmentThreaded|coinitDisableOle1Dde)
}
