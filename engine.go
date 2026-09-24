package main

import (
	"errors"
	"fmt"
	"log/slog"
	"os"
	"strings"
	"time"
)

// Engine 是开关代理的核心逻辑，与界面无关：判断当前状态，开启、关闭、切换配置，配置变更后重新应用，
// 按网络自动切换，代理服务器健康检查。不是并发安全的，调用方负责串行调用（Windows 上都在 UI 线程执行）。

const (
	statusOff      = "off"
	statusOn       = "on"
	statusExternal = "external"
)

// ProxySystem 是开关代理时要改动的系统设置。Windows 上改真实设置，开发模式和测试里用内存实现。
type ProxySystem interface {
	ReadSystemProxy() (SystemProxyState, error)
	WriteSystemProxy(state SystemProxyState) error
	SetEnvProxy(proxyUrl, noProxy string) error
	ClearEnvProxy() error
	SetGitProxy(proxyUrl string) error
	ClearGitProxy() error
	SetNpmProxy(proxyUrl, noProxy string) error
	ClearNpmProxy() error
}

type NoticeLevel int

const (
	noticeInfo NoticeLevel = iota
	noticeWarning
	noticeError
)

// Notice 是一条托盘通知。Icon 和 Color 决定信息类通知的图标：开关状态和配置的颜色；
// Page 是点击通知时打开的设置页，为空时打开「代理」页。
type Notice struct {
	Level NoticeLevel
	Title string
	Text  string
	Icon  string
	Color string
	Page  string
}

// Status 是当前代理状态的判断结果。
// State 为 on 时 Profile 是正在使用的配置；为 off 时 Profile 是下次开启会用的配置；为 external 时系统代理由其他程序设置。
type Status struct {
	State       string
	Profile     *Profile
	External    string
	System      SystemProxyState
	SystemError error
}

const (
	healthOk   = "ok"
	healthDown = "down"
)

// 连续两次检查结果一致才改变健康状态，避免偶发抖动。
const healthThreshold = 2

type healthTracker struct {
	endpoint  string
	failures  int
	successes int
	state     string
	message   string
}

var (
	errNoConfig  = errors.New("配置文件有错误，请先修正配置")
	errNoProfile = errors.New("还没有代理配置，请先在设置里添加一个")
)

type Engine struct {
	system ProxySystem
	paths  Paths
	notify func(Notice)
	now    func() time.Time
	// 订阅使用的代理内核；为空时不管理内核（部分测试）。downloadsNeeded 通知后台检查需要下载的订阅。
	core            ProxyCore
	coreGeneration  int
	downloadsNeeded func()

	config      *Config
	configError string
	configStamp configStamp
	state       *State

	health healthTracker
	// 健康检查自动关闭的配置，代理服务器恢复后自动重新开启。
	autoOffProfileId string

	network           NetworkInfo
	pendingSignature  string
	appliedSignature  string
	autoSwitchResult  string
	autoSwitchTime    time.Time
	autoSwitchEnabled bool
}

func newEngine(system ProxySystem, paths Paths, notify func(Notice)) *Engine {
	return &Engine{system: system, paths: paths, notify: notify, now: time.Now, state: loadState(paths.State)}
}

// LoadConfig 从文件加载配置；出错时继续使用上次成功加载的配置，错误记在 configError。
func (engine *Engine) LoadConfig() (created bool, err error) {
	config, created, err := loadConfig(engine.paths.Config)
	engine.configStamp = readConfigStamp(engine.paths.Config)
	if err != nil {
		engine.configError = err.Error()
		slog.Warn("配置加载失败", "err", err)
		return created, err
	}
	engine.ReplaceConfig(config)
	return created, nil
}

// SaveConfig 写入配置文件并立即生效。
func (engine *Engine) SaveConfig(config *Config) error {
	if err := writeConfigFile(engine.paths.Config, config); err != nil {
		return fmt.Errorf("保存配置文件失败：%v", err)
	}
	engine.configStamp = readConfigStamp(engine.paths.Config)
	engine.ReplaceConfig(config)
	return nil
}

// ReloadIfChanged 发现配置文件被手动修改后重新加载，返回是否重新加载过。
func (engine *Engine) ReloadIfChanged() bool {
	if readConfigStamp(engine.paths.Config) == engine.configStamp {
		return false
	}
	hadError := engine.configError != ""
	if _, err := engine.LoadConfig(); err != nil {
		text := err.Error()
		if engine.config != nil {
			text += "\n仍在使用修改前的配置"
		}
		engine.notify(Notice{Level: noticeError, Title: "配置文件有错误", Text: text})
		return true
	}
	slog.Info("配置文件已重新加载")
	if hadError {
		engine.notify(Notice{Level: noticeInfo, Title: "配置文件已生效", Text: "错误已修正"})
	}
	return true
}

// ReplaceConfig 换用新配置。正在使用的配置被修改时重新应用，被删除时关闭代理。
func (engine *Engine) ReplaceConfig(config *Config) {
	defer engine.afterConfigChange()
	if engine.config == nil {
		engine.config = config
		engine.configError = ""
		engine.autoSwitchEnabled = config.AutoSwitch.Enabled
		return
	}
	status := engine.Status()
	engine.config = config
	engine.configError = ""
	autoSwitchTurnedOn := config.AutoSwitch.Enabled && !engine.autoSwitchEnabled
	engine.autoSwitchEnabled = config.AutoSwitch.Enabled
	defer func() {
		if autoSwitchTurnedOn && engine.network.Online() {
			_ = engine.applyNetworkRules(engine.network)
		}
	}()

	if status.State != statusOn || status.Profile == nil {
		return
	}
	previous := *status.Profile
	updated := engine.findUpdatedProfile(&previous)
	switch {
	case updated == nil:
		result, _ := engine.revert(&previous, status.System)
		engine.finishOff()
		engine.notify(turnedOffNotice(&previous, result, "正在使用的配置已被删除"))
	case profileSettingsChanged(&previous, updated):
		if err := engine.subscriptionProblem(updated); err != nil {
			// 改成了订阅但还不能用（订阅还在下载等），先关闭代理，免得系统代理指向没有节点的内核。
			result, _ := engine.revert(&previous, status.System)
			engine.finishOff()
			engine.notify(turnedOffNotice(&previous, result, err.Error()))
			return
		}
		result := engine.apply(updated, &previous, status.System)
		engine.remember(updated, result.applied > 0)
		engine.resetHealth()
		engine.notify(switchedNotice(updated, result, "已应用修改", ""))
	case previous.Name != updated.Name:
		engine.remember(updated, true)
	}
}

// afterConfigChange 在换用配置后清理已删除的订阅，把新的状态交给内核，并检查是否有需要下载的订阅。
func (engine *Engine) afterConfigChange() {
	engine.forgetSubscriptions()
	engine.syncCore()
	if len(engine.SubscriptionsDue()) > 0 || engine.GeoDue() {
		engine.requestDownloads()
	}
}

// findUpdatedProfile 在新配置里找到与 previous 对应的配置：先按 id，再按名字，最后按相同的地址。
func (engine *Engine) findUpdatedProfile(previous *Profile) *Profile {
	if profile := engine.config.FindProfileById(previous.Id); profile != nil {
		return profile
	}
	if profile := engine.config.FindProfile(previous.Name); profile != nil {
		return profile
	}
	for index := range engine.config.Profiles {
		profile := &engine.config.Profiles[index]
		if profile.Pac == previous.Pac && normalizeServer(profile.Server) == normalizeServer(previous.Server) {
			return profile
		}
	}
	return nil
}

func profileSettingsChanged(previous, updated *Profile) bool {
	return normalizeServer(previous.Server) != normalizeServer(updated.Server) ||
		previous.Pac != updated.Pac ||
		previous.Bypass != updated.Bypass ||
		previous.NoProxy != updated.NoProxy ||
		strings.Join(previous.ApplyTo, ",") != strings.Join(updated.ApplyTo, ",")
}

func (engine *Engine) Config() *Config {
	return engine.config
}

func (engine *Engine) ConfigError() string {
	return engine.configError
}

// selectedProfile 返回最近使用的配置，没有记录时返回第一个。
func (engine *Engine) selectedProfile() *Profile {
	if engine.config == nil || len(engine.config.Profiles) == 0 {
		return nil
	}
	if profile := engine.config.FindProfileById(engine.state.ProfileId); profile != nil {
		return profile
	}
	if profile := engine.config.FindProfile(engine.state.Profile); profile != nil {
		return profile
	}
	return &engine.config.Profiles[0]
}

// matchProfile 根据系统代理的实际值找出对应的配置，优先匹配最近使用的配置。
func (engine *Engine) matchProfile(system SystemProxyState) *Profile {
	if engine.config == nil {
		return nil
	}
	matches := func(profile *Profile) bool {
		if !profile.Has(targetSystem) {
			return false
		}
		if system.PacEnabled {
			return profile.Pac != "" && strings.EqualFold(profile.Pac, system.Pac)
		}
		return profile.Pac == "" && system.ProxyEnabled && sameServer(profile.Server, system.Server)
	}
	if selected := engine.selectedProfile(); selected != nil && matches(selected) {
		return selected
	}
	for index := range engine.config.Profiles {
		if matches(&engine.config.Profiles[index]) {
			return &engine.config.Profiles[index]
		}
	}
	return nil
}

// Status 读取系统当前的代理设置并判断状态。
func (engine *Engine) Status() Status {
	system, err := engine.system.ReadSystemProxy()
	status := Status{System: system, SystemError: err}
	if system.Active() {
		if profile := engine.matchProfile(system); profile != nil {
			status.State, status.Profile = statusOn, profile
		} else {
			status.State, status.External = statusExternal, system.Describe()
		}
		return status
	}
	status.State = statusOff
	status.Profile = engine.selectedProfile()
	// 不含系统代理的配置（只设环境变量、git、npm）无法从系统代理判断，按记录的状态算。
	if status.Profile != nil && engine.state.Enabled && !status.Profile.Has(targetSystem) {
		status.State = statusOn
	}
	return status
}

// applyResult 记录一次开启或关闭中各项设置的结果。
type applyResult struct {
	applied  int
	failures []string
}

func (result *applyResult) record(target string, err error) {
	if err != nil {
		slog.Warn("修改代理设置失败", "target", target, "err", err)
		result.failures = append(result.failures, targetLabels[target]+"："+err.Error())
		return
	}
	result.applied++
}

func (result applyResult) err() error {
	if len(result.failures) == 0 {
		return nil
	}
	return errors.New(strings.Join(result.failures, "；"))
}

// systemStateFor 是开启 profile 时要写入的系统代理设置。
// 开启期间关闭“自动检测设置”：网络里有 WPAD 时它的优先级比手动代理高，会让配置不生效；关闭代理时再恢复。
func systemStateFor(profile *Profile, current SystemProxyState) SystemProxyState {
	state := SystemProxyState{
		ProxyEnabled: profile.Server != "",
		PacEnabled:   profile.Pac != "",
		Server:       current.Server,
		Bypass:       profile.Bypass,
		Pac:          profile.Pac,
	}
	if profile.Server != "" {
		state.Server = serverToWinInet(profile.Server)
	}
	return state
}

// systemOffState 是关闭代理时要写入的系统代理设置：restore 模式恢复开启前的设置，否则改为直连。
// 直连时保留代理地址（系统设置里还能看到），并恢复开启前的“自动检测设置”。
func (engine *Engine) systemOffState(current SystemProxyState) SystemProxyState {
	original := engine.state.Original
	if original != nil && engine.config != nil && engine.config.OffMode == "restore" {
		return *original
	}
	off := SystemProxyState{AutoDetect: current.AutoDetect, Server: current.Server, Bypass: current.Bypass}
	if original != nil {
		off.AutoDetect = original.AutoDetect
	}
	return off
}

func (engine *Engine) setTarget(target string, profile *Profile, current SystemProxyState) error {
	proxyUrl := serverToUrl(profile.Server)
	switch target {
	case targetSystem:
		return engine.system.WriteSystemProxy(systemStateFor(profile, current))
	case targetEnv:
		return engine.system.SetEnvProxy(proxyUrl, profile.NoProxy)
	case targetGit:
		return engine.system.SetGitProxy(proxyUrl)
	case targetNpm:
		return engine.system.SetNpmProxy(proxyUrl, profile.NoProxy)
	}
	return fmt.Errorf("不认识的生效范围 %q", target)
}

func (engine *Engine) clearTarget(target string, current SystemProxyState) error {
	switch target {
	case targetSystem:
		return engine.system.WriteSystemProxy(engine.systemOffState(current))
	case targetEnv:
		return engine.system.ClearEnvProxy()
	case targetGit:
		return engine.system.ClearGitProxy()
	case targetNpm:
		return engine.system.ClearNpmProxy()
	}
	return nil
}

// apply 开启 profile 的各项设置；previous 是之前生效的配置，它有而新配置没有的设置会被清除。
func (engine *Engine) apply(profile, previous *Profile, current SystemProxyState) applyResult {
	var result applyResult
	if previous != nil {
		for _, target := range previous.ApplyTo {
			if profile.Has(target) {
				continue
			}
			if err := engine.clearTarget(target, current); err != nil {
				slog.Warn("清除上一个配置的设置失败", "target", target, "err", err)
				result.failures = append(result.failures, targetLabels[target]+"（清除）："+err.Error())
			}
		}
	}
	for _, target := range profile.ApplyTo {
		result.record(target, engine.setTarget(target, profile, current))
	}
	slog.Info("开启代理", "profile", profile.Name, "applied", result.applied, "failed", len(result.failures))
	return result
}

// activate 开启 profile，不发通知。订阅配置还不能用时（内核或订阅还没下载）不改动任何设置。
func (engine *Engine) activate(profile *Profile) applyResult {
	if err := engine.subscriptionProblem(profile); err != nil {
		return applyResult{failures: []string{err.Error()}}
	}
	status := engine.Status()
	var previous *Profile
	switch {
	case status.State == statusOn:
		previous = status.Profile
	case engine.state.Enabled:
		// 系统代理被别处关掉了，但上次开启的配置可能还留着环境变量等设置。
		previous = engine.selectedProfile()
	}
	if status.State != statusOn || engine.state.Original == nil {
		snapshot := status.System
		engine.state.Original = &snapshot
	}
	result := engine.apply(profile, previous, status.System)
	engine.remember(profile, result.applied > 0)
	engine.resetHealth()
	return result
}

// deactivate 关闭当前代理，不发通知。restored 表示恢复了开启前的系统代理设置。
func (engine *Engine) deactivate() (status Status, result applyResult, restored bool) {
	status = engine.Status()
	switch status.State {
	case statusExternal:
		off := SystemProxyState{AutoDetect: status.System.AutoDetect, Server: status.System.Server, Bypass: status.System.Bypass}
		result.record(targetSystem, engine.system.WriteSystemProxy(off))
	case statusOn:
		result, restored = engine.revert(status.Profile, status.System)
	}
	engine.finishOff()
	slog.Info("关闭代理", "state", status.State, "failed", len(result.failures))
	return status, result, restored
}

// revert 清除 profile 开启时改动的各项设置。
func (engine *Engine) revert(profile *Profile, current SystemProxyState) (result applyResult, restored bool) {
	restored = profile.Has(targetSystem) && engine.state.Original != nil && engine.config.OffMode == "restore"
	for _, target := range profile.ApplyTo {
		result.record(target, engine.clearTarget(target, current))
	}
	return result, restored
}

func (engine *Engine) finishOff() {
	engine.state.Enabled = false
	engine.state.Original = nil
	engine.saveState()
	engine.resetHealth()
}

func (engine *Engine) remember(profile *Profile, enabled bool) {
	engine.state.Profile = profile.Name
	engine.state.ProfileId = profile.Id
	engine.state.Enabled = enabled
	engine.saveState()
	// 最近使用的订阅决定内核把流量交给哪个订阅。
	engine.syncCore()
}

func (engine *Engine) saveState() {
	if err := saveState(engine.paths.State, engine.state); err != nil {
		slog.Warn("保存状态失败", "err", err)
	}
}

func (engine *Engine) requireProfiles() error {
	if engine.config == nil {
		return errNoConfig
	}
	if len(engine.config.Profiles) == 0 {
		return errNoProfile
	}
	return nil
}

// TurnOn 开启最近使用的配置。
func (engine *Engine) TurnOn() error {
	if err := engine.requireProfiles(); err != nil {
		return err
	}
	return engine.use(engine.selectedProfile())
}

// UseProfile 切换到指定配置并开启。
func (engine *Engine) UseProfile(name string) error {
	if err := engine.requireProfiles(); err != nil {
		return err
	}
	profile := engine.config.FindProfile(name)
	if profile == nil {
		return fmt.Errorf("没有名为「%s」的配置", name)
	}
	return engine.use(profile)
}

func (engine *Engine) use(profile *Profile) error {
	engine.autoOffProfileId = ""
	result := engine.activate(profile)
	engine.notify(switchedNotice(profile, result, "代理已开启", ""))
	return result.err()
}

// TurnOff 关闭代理：本程序开启的按配置逐项清除，其他程序设置的系统代理改为直连。
func (engine *Engine) TurnOff() error {
	engine.autoOffProfileId = ""
	status, result, restored := engine.deactivate()
	switch status.State {
	case statusOn:
		detail := ""
		if restored {
			detail = "已恢复开启前的系统代理设置"
		}
		engine.notify(turnedOffNotice(status.Profile, result, detail))
	case statusExternal:
		notice := Notice{Level: noticeInfo, Title: "代理已关闭", Text: "已关闭其他程序设置的系统代理：" + status.External, Icon: iconStateOff}
		if len(result.failures) > 0 {
			notice = Notice{Level: noticeError, Title: "关闭代理失败", Text: strings.Join(result.failures, "\n")}
		}
		engine.notify(notice)
	}
	return result.err()
}

func (engine *Engine) Toggle() error {
	if engine.Status().State == statusOff {
		return engine.TurnOn()
	}
	return engine.TurnOff()
}

// ClearAll 把系统代理、环境变量、git、npm 的代理设置全部清除，用于排查问题或卸载前清理。
func (engine *Engine) ClearAll() error {
	engine.autoOffProfileId = ""
	status := engine.Status()
	var result applyResult
	result.record(targetSystem, engine.system.WriteSystemProxy(SystemProxyState{AutoDetect: status.System.AutoDetect}))
	result.record(targetEnv, engine.system.ClearEnvProxy())
	result.record(targetGit, engine.system.ClearGitProxy())
	result.record(targetNpm, engine.system.ClearNpmProxy())
	engine.finishOff()
	if err := result.err(); err != nil {
		engine.notify(Notice{Level: noticeWarning, Title: "部分代理设置没能清除", Text: strings.Join(result.failures, "\n")})
		return err
	}
	engine.notify(Notice{Level: noticeInfo, Title: "已清除所有代理设置", Text: "系统代理、环境变量、git、npm 都已恢复为直连", Icon: iconStateOff})
	return nil
}

// switchedNotice 是开启、切换、重新应用配置后的通知；有失败项时提升为警告或错误。
func switchedNotice(profile *Profile, result applyResult, title, detail string) Notice {
	notice := Notice{Level: noticeInfo, Title: title, Color: profile.Color, Icon: iconStateOn}
	lines := []string{profile.Name + " · " + profile.Summary()}
	if len(profile.ApplyTo) > 1 || !profile.Has(targetSystem) {
		lines = append(lines, "生效范围："+profile.TargetsText())
	}
	if detail != "" {
		lines = append(lines, detail)
	}
	if len(result.failures) > 0 {
		lines = append(lines, result.failures...)
		notice.Level, notice.Title = noticeWarning, title+"，部分设置失败"
		if result.applied == 0 {
			notice.Level, notice.Title = noticeError, "开启代理失败"
		}
	}
	notice.Text = strings.Join(lines, "\n")
	return notice
}

// turnedOffNotice 是关闭代理后的通知。
func turnedOffNotice(profile *Profile, result applyResult, detail string) Notice {
	notice := Notice{Level: noticeInfo, Title: "代理已关闭", Color: profile.Color, Icon: iconStateOff}
	lines := []string{profile.Name + " · " + profile.Summary()}
	if detail != "" {
		lines = append(lines, detail)
	}
	if len(result.failures) > 0 {
		lines = append(lines, result.failures...)
		notice.Level, notice.Title = noticeWarning, "代理已关闭，部分设置没能清除"
	}
	notice.Text = strings.Join(lines, "\n")
	return notice
}

// RunStartupAction 按 startup_action 处理启动时的代理状态。上次退出时因内核停止而关闭的订阅配置，
// 在 keep（保持现状）时重新开启。
func (engine *Engine) RunStartupAction() {
	if engine.config == nil {
		return
	}
	resume := engine.state.Resume
	if resume != "" {
		engine.state.Resume = ""
		engine.saveState()
	}
	status := engine.Status()
	switch engine.config.StartupAction {
	case "on":
		if status.State != statusOn && len(engine.config.Profiles) > 0 {
			_ = engine.TurnOn()
		}
	case "off":
		if status.State == statusOn {
			_ = engine.TurnOff()
		}
	default:
		if profile := engine.config.FindProfileById(resume); profile != nil && status.State == statusOff {
			_ = engine.use(profile)
		}
	}
}

// ---------- 健康检查 ----------

// HealthTarget 返回需要检查的代理服务器地址（host:port），不需要检查时返回空串。
func (engine *Engine) HealthTarget() string {
	if engine.config == nil || engine.config.HealthCheck == "off" {
		return ""
	}
	if engine.autoOffProfileId != "" {
		if profile := engine.config.FindProfileById(engine.autoOffProfileId); profile != nil {
			return serverEndpoint(profile.Server)
		}
		return ""
	}
	status := engine.Status()
	if status.State != statusOn || status.Profile == nil || status.Profile.Server == "" {
		return ""
	}
	return serverEndpoint(status.Profile.Server)
}

func (engine *Engine) resetHealth() {
	engine.health = healthTracker{}
}

// HealthResult 接收一次检查结果（err 为 nil 表示能连上）。
func (engine *Engine) HealthResult(endpoint string, err error) {
	if endpoint == "" || endpoint != engine.HealthTarget() {
		return
	}
	tracker := &engine.health
	if tracker.endpoint != endpoint {
		*tracker = healthTracker{endpoint: endpoint}
	}
	if err != nil {
		tracker.successes = 0
		tracker.failures++
		tracker.message = friendlyNetError(err)
		if tracker.state != healthDown && tracker.failures >= healthThreshold {
			tracker.state = healthDown
			engine.proxyDown(endpoint)
		}
		return
	}
	tracker.failures = 0
	tracker.successes++
	switch {
	case engine.autoOffProfileId != "" && tracker.successes >= healthThreshold:
		engine.proxyRecovered()
	case tracker.state == healthDown && tracker.successes >= healthThreshold:
		tracker.state = healthOk
		tracker.message = ""
		if profile := engine.Status().Profile; profile != nil {
			engine.notify(Notice{Level: noticeInfo, Title: "代理服务器已恢复", Text: profile.Name + " · " + profile.Summary(), Color: profile.Color, Icon: iconStateOn})
		}
	case tracker.state == "":
		tracker.state = healthOk
	}
}

func (engine *Engine) proxyDown(endpoint string) {
	status := engine.Status()
	if status.Profile == nil {
		return
	}
	profile := *status.Profile
	message := engine.health.message
	slog.Warn("代理服务器连不上", "profile", profile.Name, "endpoint", endpoint, "reason", message)
	if engine.config.HealthCheck != "auto_off" {
		engine.notify(Notice{
			Level: noticeWarning,
			Title: "代理服务器连不上",
			Text:  profile.Name + " · " + endpoint + "\n" + message,
			Color: profile.Color,
		})
		return
	}
	_, result, _ := engine.deactivate()
	engine.autoOffProfileId = profile.Id
	engine.health = healthTracker{endpoint: endpoint, failures: healthThreshold, state: healthDown, message: message}
	notice := Notice{
		Level: noticeWarning,
		Title: "代理服务器连不上，已自动关闭代理",
		Text:  profile.Name + " · " + endpoint + "\n" + message + "\n恢复后会自动重新开启",
		Color: profile.Color,
	}
	if len(result.failures) > 0 {
		notice.Text += "\n" + strings.Join(result.failures, "\n")
	}
	engine.notify(notice)
}

func (engine *Engine) proxyRecovered() {
	profile := engine.config.FindProfileById(engine.autoOffProfileId)
	engine.autoOffProfileId = ""
	if profile == nil {
		return
	}
	result := engine.activate(profile)
	engine.notify(switchedNotice(profile, result, "代理服务器已恢复，已重新开启", ""))
}

// HealthInfo 返回给设置页和托盘显示的健康状态。
func (engine *Engine) HealthInfo() (state, message string) {
	if engine.autoOffProfileId != "" {
		return healthDown, "代理服务器连不上，已自动关闭代理，恢复后自动重新开启"
	}
	if engine.health.state == healthDown {
		return healthDown, engine.health.message
	}
	return engine.health.state, ""
}

func (engine *Engine) AutoOffPending() bool {
	return engine.autoOffProfileId != ""
}

// ---------- 按网络自动切换 ----------

// UpdateNetwork 由网络监视定期调用。网络特征变化并在下一次检查时保持不变后，按规则自动切换；
// 断网期间和回到原来的网络时不做切换，避免与手动选择冲突。
func (engine *Engine) UpdateNetwork(info NetworkInfo) {
	engine.network = info
	signature := info.Signature()
	if signature != engine.pendingSignature {
		engine.pendingSignature = signature
		return
	}
	if signature == "" || signature == engine.appliedSignature {
		return
	}
	engine.appliedSignature = signature
	if engine.config != nil && engine.config.AutoSwitch.Enabled {
		_ = engine.applyNetworkRules(info)
	}
}

// ApplyAutoSwitch 立即按当前网络应用自动切换规则（设置页的“立即应用”）。
func (engine *Engine) ApplyAutoSwitch() error {
	if engine.config == nil {
		return errNoConfig
	}
	if !engine.network.Online() {
		return errors.New("当前没有连接网络")
	}
	return engine.applyNetworkRules(engine.network)
}

func (engine *Engine) applyNetworkRules(info NetworkInfo) error {
	decision := decideNetworkAction(engine.config.AutoSwitch, info)
	where := info.Describe()
	status := engine.Status()
	slog.Info("按网络自动切换", "network", where, "action", decision.Action, "profile", decision.Profile, "rule", decision.RuleIndex)
	switch decision.Action {
	case "use":
		profile := engine.config.FindProfile(decision.Profile)
		if profile == nil {
			engine.recordAutoSwitch(where, "要使用的配置「"+decision.Profile+"」不存在")
			return fmt.Errorf("要使用的配置「%s」不存在", decision.Profile)
		}
		if status.State == statusOn && status.Profile != nil && status.Profile.Id == profile.Id {
			engine.recordAutoSwitch(where, "保持「"+profile.Name+"」")
			return nil
		}
		engine.autoOffProfileId = ""
		result := engine.activate(profile)
		notice := switchedNotice(profile, result, "已切换到「"+profile.Name+"」", "按网络自动切换 · "+where)
		notice.Page = "network"
		engine.notify(notice)
		engine.recordAutoSwitch(where, "切换到「"+profile.Name+"」")
		return result.err()
	case "off":
		if status.State != statusOn {
			engine.recordAutoSwitch(where, "保持关闭")
			return nil
		}
		engine.autoOffProfileId = ""
		_, result, _ := engine.deactivate()
		notice := turnedOffNotice(status.Profile, result, "按网络自动切换 · "+where)
		notice.Page = "network"
		engine.notify(notice)
		engine.recordAutoSwitch(where, "关闭代理")
		return result.err()
	}
	engine.recordAutoSwitch(where, "保持不变")
	return nil
}

func (engine *Engine) recordAutoSwitch(where, result string) {
	engine.autoSwitchResult = where + "：" + result
	engine.autoSwitchTime = engine.now()
}

// ---------- 自动检查更新 ----------

// UpdateCheckDue 判断现在是否该自动检查更新：开启了自动检查，并且到了记录的下次检查时间。
func (engine *Engine) UpdateCheckDue() bool {
	if engine.config == nil || !engine.config.CheckUpdates {
		return false
	}
	next, err := time.Parse(time.RFC3339, engine.state.NextUpdateCheck)
	now := engine.now()
	// 下次检查时间离现在超过一天，说明系统时间被往回调过，也立即检查。
	return err != nil || !now.Before(next) || next.Sub(now) > updateCheckInterval
}

// RecordUpdateCheck 记录一次自动检查并安排下次检查：成功后一天，失败（例如开机时网络还没连上）几小时后重试。
// 发现的新版本还没提示过时返回 true，同一个版本只提示一次。
func (engine *Engine) RecordUpdateCheck(info UpdateInfo, err error) bool {
	wait := updateCheckInterval
	if err != nil {
		wait = updateRetryInterval
	}
	engine.state.NextUpdateCheck = engine.now().Add(wait).Format(time.RFC3339)
	notify := err == nil && info.Newer && info.Latest != engine.state.UpdateNotified
	if notify {
		engine.state.UpdateNotified = info.Latest
	}
	engine.saveState()
	return notify
}

// RecordVersion 记录这次运行的版本号；比上次运行的版本新时返回上次的版本，否则返回空字符串。
func (engine *Engine) RecordVersion(version string) string {
	previous := engine.state.Version
	if previous == version {
		return ""
	}
	engine.state.Version = version
	engine.saveState()
	if previous == "" || compareVersions(version, previous) <= 0 {
		return ""
	}
	return previous
}

// ---------- 设置页 ----------

// settingsState 填好设置页状态里与平台无关的部分。
func (engine *Engine) settingsState() SettingsState {
	status := engine.Status()
	info := StatusInfo{State: status.State, External: status.External, Applied: []string{}}
	if status.Profile != nil {
		info.Profile = status.Profile.Name
		if status.State == statusOn {
			for _, target := range status.Profile.ApplyTo {
				info.Applied = append(info.Applied, targetLabels[target])
			}
			info.Terminal = terminalCommands(serverToUrl(status.Profile.Server), status.Profile.NoProxy)
		}
	}
	if status.State == statusExternal {
		external := profileFromSystem(status.System)
		info.ExternalProfile = &external
	}
	info.Health, info.HealthMessage = engine.HealthInfo()
	if status.State != statusOn && !engine.AutoOffPending() {
		info.Health, info.HealthMessage = "", ""
	}

	autoSwitch := AutoSwitchStatus{Network: engine.network.Describe(), MatchIndex: -1, Result: engine.autoSwitchResult}
	if !engine.autoSwitchTime.IsZero() {
		autoSwitch.Time = engine.autoSwitchTime.Format("15:04")
	}
	if engine.config != nil && engine.network.Online() {
		autoSwitch.MatchIndex = decideNetworkAction(engine.config.AutoSwitch, engine.network).RuleIndex
	}
	network := engine.network
	if network.Ssids == nil {
		network.Ssids = []string{}
	}
	if network.Adapters == nil {
		network.Adapters = []NetworkAdapter{}
	}
	return SettingsState{
		Version:       appVersion,
		Paths:         PathsInfo{Dir: engine.paths.Dir, Config: engine.paths.Config, Log: engine.paths.Log, Portable: engine.paths.Portable},
		Config:        engine.config,
		ConfigError:   engine.configError,
		Status:        info,
		Network:       network,
		AutoSwitch:    autoSwitch,
		Defaults:      defaultsInfo(),
		Palette:       profilePalette,
		Subscriptions: engine.subscriptionInfos(),
		Core:          engine.coreInfo(),
	}
}

// profileFromSystem 把其他程序设置的系统代理转成一套配置的内容（不含名称和颜色）。
func profileFromSystem(state SystemProxyState) Profile {
	profile := Profile{Bypass: state.Bypass, ApplyTo: []string{targetSystem}}
	if state.PacEnabled {
		profile.Pac = state.Pac
	}
	if state.ProxyEnabled {
		profile.Server = serverFromWinInet(state.Server)
	}
	return profile
}

// configStamp 记录配置文件的修改时间和大小，用于发现手动修改。
type configStamp struct {
	modified time.Time
	size     int64
}

func readConfigStamp(path string) configStamp {
	info, err := os.Stat(path)
	if err != nil {
		return configStamp{}
	}
	return configStamp{modified: info.ModTime(), size: info.Size()}
}
