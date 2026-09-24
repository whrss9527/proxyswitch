package main

import (
	"errors"
	"os"
	"strings"
	"testing"
	"time"
)

type engineFixture struct {
	engine  *Engine
	system  *memorySystem
	notices []Notice
}

const engineTestConfig = `{
  "profiles": [
    {"name": "本机", "server": "127.0.0.1:7890", "apply_to": ["system", "env", "git"]},
    {"name": "公司", "server": "http=10.0.0.1:8080;https=10.0.0.1:8080", "apply_to": ["system", "npm"]},
    {"name": "PAC", "pac": "http://10.0.0.1/proxy.pac", "apply_to": ["system"]},
    {"name": "只改终端", "server": "socks5://127.0.0.1:1080", "apply_to": ["env"]}
  ]
}`

func newEngineFixture(t *testing.T, configText string) *engineFixture {
	t.Helper()
	fixture := &engineFixture{system: newMemorySystem()}
	paths := pathsIn(t.TempDir(), false)
	config, err := parseConfig(configText)
	if err != nil {
		t.Fatal(err)
	}
	if err := writeConfigFile(paths.Config, config); err != nil {
		t.Fatal(err)
	}
	fixture.engine = newEngine(fixture.system, paths, func(notice Notice) { fixture.notices = append(fixture.notices, notice) })
	if _, err := fixture.engine.LoadConfig(); err != nil {
		t.Fatal(err)
	}
	return fixture
}

func (fixture *engineFixture) lastNotice(t *testing.T) Notice {
	t.Helper()
	if len(fixture.notices) == 0 {
		t.Fatal("没有发出通知")
	}
	return fixture.notices[len(fixture.notices)-1]
}

func (fixture *engineFixture) expectStatus(t *testing.T, state, profile string) {
	t.Helper()
	status := fixture.engine.Status()
	name := ""
	if status.Profile != nil {
		name = status.Profile.Name
	}
	if status.State != state || name != profile {
		t.Fatalf("状态应为 %s / %s，实际 %s / %s", state, profile, status.State, name)
	}
}

func TestEngineTurnOnOff(t *testing.T) {
	fixture := newEngineFixture(t, engineTestConfig)
	system := fixture.system
	fixture.expectStatus(t, statusOff, "本机")

	if err := fixture.engine.TurnOn(); err != nil {
		t.Fatal(err)
	}
	fixture.expectStatus(t, statusOn, "本机")
	if !system.System.ProxyEnabled || system.System.Server != "127.0.0.1:7890" || system.System.AutoDetect {
		t.Errorf("系统代理设置不对：%+v", system.System)
	}
	if system.Env["HTTPS_PROXY"] != "http://127.0.0.1:7890" || system.Git != "http://127.0.0.1:7890" {
		t.Errorf("环境变量或 git 没有设置：%v %q", system.Env, system.Git)
	}
	notice := fixture.lastNotice(t)
	if notice.Level != noticeInfo || notice.Title != "代理已开启" || !strings.Contains(notice.Text, "生效范围：系统代理、环境变量、git") {
		t.Errorf("开启通知不对：%+v", notice)
	}

	if err := fixture.engine.TurnOff(); err != nil {
		t.Fatal(err)
	}
	fixture.expectStatus(t, statusOff, "本机")
	if system.System.ProxyEnabled || !system.System.AutoDetect || system.System.Server != "127.0.0.1:7890" {
		t.Errorf("关闭后应直连、恢复自动检测、保留地址：%+v", system.System)
	}
	if len(system.Env) != 0 || system.Git != "" {
		t.Errorf("关闭后环境变量和 git 应清除：%v %q", system.Env, system.Git)
	}
	if fixture.lastNotice(t).Title != "代理已关闭" {
		t.Errorf("关闭通知不对：%+v", fixture.lastNotice(t))
	}
}

func TestEngineSwitchCleansPreviousTargets(t *testing.T) {
	fixture := newEngineFixture(t, engineTestConfig)
	system := fixture.system
	if err := fixture.engine.UseProfile("本机"); err != nil {
		t.Fatal(err)
	}
	if err := fixture.engine.UseProfile("公司"); err != nil {
		t.Fatal(err)
	}
	fixture.expectStatus(t, statusOn, "公司")
	if system.System.Server != "http=10.0.0.1:8080;https=10.0.0.1:8080" {
		t.Errorf("系统代理没有切换：%+v", system.System)
	}
	if len(system.Env) != 0 || system.Git != "" {
		t.Errorf("切换后上一个配置的环境变量和 git 应清除：%v %q", system.Env, system.Git)
	}
	if system.Npm["proxy"] != "http://10.0.0.1:8080" {
		t.Errorf("npm 没有设置：%v", system.Npm)
	}
	if err := fixture.engine.UseProfile("PAC"); err != nil {
		t.Fatal(err)
	}
	fixture.expectStatus(t, statusOn, "PAC")
	if !system.System.PacEnabled || system.System.ProxyEnabled || len(system.Npm) != 0 {
		t.Errorf("切到 PAC 后系统代理或 npm 不对：%+v %v", system.System, system.Npm)
	}
	if err := fixture.engine.UseProfile("不存在"); err == nil {
		t.Error("切到不存在的配置应报错")
	}
}

func TestEngineProfileWithoutSystemTarget(t *testing.T) {
	fixture := newEngineFixture(t, engineTestConfig)
	if err := fixture.engine.UseProfile("只改终端"); err != nil {
		t.Fatal(err)
	}
	fixture.expectStatus(t, statusOn, "只改终端")
	if fixture.system.System.Active() {
		t.Error("不含系统代理的配置不应改动系统代理")
	}
	if fixture.system.Env["HTTP_PROXY"] != "socks5://127.0.0.1:1080" {
		t.Errorf("环境变量不对：%v", fixture.system.Env)
	}
	if err := fixture.engine.Toggle(); err != nil {
		t.Fatal(err)
	}
	fixture.expectStatus(t, statusOff, "只改终端")
	if len(fixture.system.Env) != 0 {
		t.Error("关闭后环境变量应清除")
	}
}

func TestEngineExternalProxy(t *testing.T) {
	fixture := newEngineFixture(t, engineTestConfig)
	fixture.system.System = SystemProxyState{ProxyEnabled: true, Server: "192.168.1.9:3128", Bypass: "<local>", AutoDetect: true}
	status := fixture.engine.Status()
	if status.State != statusExternal || status.External != "192.168.1.9:3128" {
		t.Fatalf("应识别为其他程序设置的代理：%+v", status)
	}
	// 系统代理的值与某个配置一致时识别为该配置。
	fixture.system.System = SystemProxyState{ProxyEnabled: true, Server: "http://127.0.0.1:7890"}
	fixture.expectStatus(t, statusOn, "本机")

	fixture.system.System = SystemProxyState{ProxyEnabled: true, Server: "192.168.1.9:3128", AutoDetect: true}
	if err := fixture.engine.Toggle(); err != nil {
		t.Fatal(err)
	}
	if fixture.system.System.ProxyEnabled || !fixture.system.System.AutoDetect {
		t.Errorf("关闭外部代理应改为直连并保留自动检测：%+v", fixture.system.System)
	}
	if !strings.Contains(fixture.lastNotice(t).Text, "192.168.1.9:3128") {
		t.Errorf("通知应说明关闭的是外部代理：%+v", fixture.lastNotice(t))
	}
}

func TestEngineRestoreMode(t *testing.T) {
	fixture := newEngineFixture(t, strings.Replace(engineTestConfig, "{\n", "{\n  \"off_mode\": \"restore\",\n", 1))
	original := SystemProxyState{ProxyEnabled: true, Server: "192.168.1.9:3128", Bypass: "*.corp", AutoDetect: true}
	fixture.system.System = original
	if err := fixture.engine.UseProfile("本机"); err != nil {
		t.Fatal(err)
	}
	if err := fixture.engine.UseProfile("公司"); err != nil {
		t.Fatal(err)
	}
	if err := fixture.engine.TurnOff(); err != nil {
		t.Fatal(err)
	}
	if fixture.system.System != original {
		t.Errorf("restore 模式应恢复第一次开启前的设置：%+v", fixture.system.System)
	}
	if !strings.Contains(fixture.lastNotice(t).Text, "已恢复开启前的系统代理设置") {
		t.Errorf("通知应说明已恢复：%+v", fixture.lastNotice(t))
	}
}

func TestEnginePartialFailure(t *testing.T) {
	fixture := newEngineFixture(t, engineTestConfig)
	fixture.system.Failures[targetGit] = errors.New("没有找到 git")
	err := fixture.engine.UseProfile("本机")
	if err == nil || !strings.Contains(err.Error(), "git：没有找到 git") {
		t.Fatalf("部分失败应返回错误：%v", err)
	}
	fixture.expectStatus(t, statusOn, "本机")
	notice := fixture.lastNotice(t)
	if notice.Level != noticeWarning || !strings.Contains(notice.Title, "部分设置失败") {
		t.Errorf("部分失败应发警告：%+v", notice)
	}

	fixture.system.Failures[targetSystem] = errors.New("拒绝访问")
	fixture.system.Failures[targetEnv] = errors.New("拒绝访问")
	if err := fixture.engine.UseProfile("本机"); err == nil {
		t.Fatal("全部失败应返回错误")
	}
	if notice := fixture.lastNotice(t); notice.Level != noticeError || notice.Title != "开启代理失败" {
		t.Errorf("全部失败应发错误通知：%+v", notice)
	}
}

func TestEngineNoProfiles(t *testing.T) {
	fixture := newEngineFixture(t, `{}`)
	if err := fixture.engine.TurnOn(); !errors.Is(err, errNoProfile) {
		t.Errorf("没有配置时开启应提示先添加：%v", err)
	}
	if fixture.system.Writes != 0 {
		t.Error("没有配置时不应改动系统代理")
	}
}

func TestEngineReplaceConfig(t *testing.T) {
	fixture := newEngineFixture(t, engineTestConfig)
	if err := fixture.engine.UseProfile("本机"); err != nil {
		t.Fatal(err)
	}
	updated, _ := parseConfig(fixture.configText(t))
	local := updated.FindProfile("本机")
	local.Server = "127.0.0.1:7897"
	local.ApplyTo = []string{"system"}
	if err := fixture.engine.SaveConfig(updated); err != nil {
		t.Fatal(err)
	}
	if fixture.system.System.Server != "127.0.0.1:7897" || len(fixture.system.Env) != 0 || fixture.system.Git != "" {
		t.Errorf("修改正在使用的配置后应重新应用：%+v %v %q", fixture.system.System, fixture.system.Env, fixture.system.Git)
	}
	if fixture.lastNotice(t).Title != "已应用修改" {
		t.Errorf("通知不对：%+v", fixture.lastNotice(t))
	}

	renamed, _ := parseConfig(fixture.configText(t))
	renamed.FindProfile("本机").Name = "家里"
	if err := fixture.engine.SaveConfig(renamed); err != nil {
		t.Fatal(err)
	}
	fixture.expectStatus(t, statusOn, "家里")

	withoutActive, _ := parseConfig(fixture.configText(t))
	withoutActive.Profiles = withoutActive.Profiles[1:]
	if err := fixture.engine.SaveConfig(withoutActive); err != nil {
		t.Fatal(err)
	}
	if fixture.system.System.ProxyEnabled {
		t.Error("正在使用的配置被删除后应关闭代理")
	}
	if !strings.Contains(fixture.lastNotice(t).Text, "已被删除") {
		t.Errorf("通知应说明原因：%+v", fixture.lastNotice(t))
	}
}

func (fixture *engineFixture) configText(t *testing.T) string {
	return string(mustReadFile(t, fixture.engine.paths.Config))
}

func TestEngineReloadIfChanged(t *testing.T) {
	fixture := newEngineFixture(t, engineTestConfig)
	if fixture.engine.ReloadIfChanged() {
		t.Error("文件没变时不应重新加载")
	}
	path := fixture.engine.paths.Config
	time.Sleep(10 * time.Millisecond)
	writeTestFile(t, path, `{"profiles": [{"name": "新", "server": "a:1"}], }`)
	if !fixture.engine.ReloadIfChanged() || fixture.engine.Config().Profiles[0].Name != "新" {
		t.Fatal("手动修改后应重新加载")
	}
	writeTestFile(t, path, `{"profiles": [ }`)
	if !fixture.engine.ReloadIfChanged() {
		t.Fatal("修改成错误内容后也应检测到")
	}
	if fixture.engine.ConfigError() == "" || fixture.engine.Config().Profiles[0].Name != "新" {
		t.Error("出错时应保留上次的配置并记录错误")
	}
	if notice := fixture.lastNotice(t); notice.Level != noticeError || !strings.Contains(notice.Text, "仍在使用修改前的配置") {
		t.Errorf("出错时应通知：%+v", notice)
	}
	writeTestFile(t, path, `{"profiles": [{"name": "修好了", "server": "a:1"}]}`)
	fixture.engine.ReloadIfChanged()
	if fixture.engine.ConfigError() != "" || fixture.lastNotice(t).Title != "配置文件已生效" {
		t.Errorf("修正后应恢复：%q %+v", fixture.engine.ConfigError(), fixture.lastNotice(t))
	}
}

func writeTestFile(t *testing.T, path, content string) {
	t.Helper()
	// 让修改时间一定变化：部分文件系统的时间精度较低。
	time.Sleep(20 * time.Millisecond)
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestEngineHealthNotify(t *testing.T) {
	fixture := newEngineFixture(t, engineTestConfig)
	if err := fixture.engine.UseProfile("本机"); err != nil {
		t.Fatal(err)
	}
	target := fixture.engine.HealthTarget()
	if target != "127.0.0.1:7890" {
		t.Fatalf("健康检查地址不对：%q", target)
	}
	refused := errors.New("connection refused")
	noticeCount := len(fixture.notices)
	fixture.engine.HealthResult(target, refused)
	if state, _ := fixture.engine.HealthInfo(); state == healthDown || len(fixture.notices) != noticeCount {
		t.Fatal("一次失败不应判定为连不上")
	}
	fixture.engine.HealthResult(target, refused)
	if state, _ := fixture.engine.HealthInfo(); state != healthDown {
		t.Fatal("连续两次失败应判定为连不上")
	}
	if notice := fixture.lastNotice(t); notice.Level != noticeWarning || notice.Title != "代理服务器连不上" {
		t.Errorf("应发出警告：%+v", notice)
	}
	fixture.engine.HealthResult(target, refused)
	if len(fixture.notices) != noticeCount+1 {
		t.Error("持续连不上时不应重复通知")
	}
	fixture.engine.HealthResult(target, nil)
	fixture.engine.HealthResult(target, nil)
	if state, _ := fixture.engine.HealthInfo(); state != healthOk || fixture.lastNotice(t).Title != "代理服务器已恢复" {
		t.Errorf("恢复后应通知：%s %+v", state, fixture.lastNotice(t))
	}
	fixture.engine.HealthResult("1.2.3.4:5", refused)
	fixture.engine.HealthResult("1.2.3.4:5", refused)
	if state, _ := fixture.engine.HealthInfo(); state != healthOk {
		t.Error("过期的检查结果应忽略")
	}
	if err := fixture.engine.UseProfile("PAC"); err != nil {
		t.Fatal(err)
	}
	if fixture.engine.HealthTarget() != "" {
		t.Error("只有 PAC 的配置不做健康检查")
	}
}

func TestEngineHealthAutoOff(t *testing.T) {
	fixture := newEngineFixture(t, strings.Replace(engineTestConfig, "{\n", "{\n  \"health_check\": \"auto_off\",\n", 1))
	if err := fixture.engine.UseProfile("本机"); err != nil {
		t.Fatal(err)
	}
	target := fixture.engine.HealthTarget()
	refused := errors.New("connection refused")
	fixture.engine.HealthResult(target, refused)
	fixture.engine.HealthResult(target, refused)
	fixture.expectStatus(t, statusOff, "本机")
	if !fixture.engine.AutoOffPending() || !strings.Contains(fixture.lastNotice(t).Title, "已自动关闭代理") {
		t.Fatalf("应自动关闭并等待恢复：%+v", fixture.lastNotice(t))
	}
	if fixture.engine.HealthTarget() != target {
		t.Fatal("自动关闭后应继续检查原来的地址")
	}
	fixture.engine.HealthResult(target, nil)
	fixture.expectStatus(t, statusOff, "本机")
	fixture.engine.HealthResult(target, nil)
	fixture.expectStatus(t, statusOn, "本机")
	if fixture.engine.AutoOffPending() || fixture.lastNotice(t).Title != "代理服务器已恢复，已重新开启" {
		t.Errorf("恢复后应自动重新开启：%+v", fixture.lastNotice(t))
	}

	fixture.engine.HealthResult(target, refused)
	fixture.engine.HealthResult(target, refused)
	if err := fixture.engine.TurnOff(); err != nil {
		t.Fatal(err)
	}
	if fixture.engine.AutoOffPending() || fixture.engine.HealthTarget() != "" {
		t.Error("手动操作后不应再自动重新开启")
	}
}

func TestEngineAutoSwitch(t *testing.T) {
	fixture := newEngineFixture(t, `{
  "auto_switch": {
    "enabled": true,
    "rules": [
      {"match": "ssid", "value": "Office-5G", "action": "use", "profile": "公司"},
      {"match": "ssid", "value": "Home", "action": "off"}
    ],
    "default_action": "keep"
  },
  "profiles": [
    {"name": "本机", "server": "127.0.0.1:7890"},
    {"name": "公司", "server": "10.0.0.1:8080"}
  ]
}`)
	engine := fixture.engine
	engine.UpdateNetwork(officeNetwork)
	if fixture.system.System.Active() {
		t.Fatal("网络刚变化时应等下一次检查确认")
	}
	engine.UpdateNetwork(officeNetwork)
	fixture.expectStatus(t, statusOn, "公司")
	if notice := fixture.lastNotice(t); notice.Title != "已切换到「公司」" || !strings.Contains(notice.Text, "Wi-Fi「Office-5G」") {
		t.Errorf("自动切换通知不对：%+v", notice)
	}

	// 手动切换后，同一网络下不再自动改回。
	if err := engine.UseProfile("本机"); err != nil {
		t.Fatal(err)
	}
	engine.UpdateNetwork(officeNetwork)
	engine.UpdateNetwork(NetworkInfo{})
	engine.UpdateNetwork(NetworkInfo{})
	engine.UpdateNetwork(officeNetwork)
	engine.UpdateNetwork(officeNetwork)
	fixture.expectStatus(t, statusOn, "本机")

	home := NetworkInfo{Ssids: []string{"Home"}}
	engine.UpdateNetwork(home)
	engine.UpdateNetwork(home)
	fixture.expectStatus(t, statusOff, "本机")
	state := engine.settingsState()
	if state.AutoSwitch.MatchIndex != 1 || !strings.Contains(state.AutoSwitch.Result, "关闭代理") || state.AutoSwitch.Time == "" {
		t.Errorf("设置页的自动切换状态不对：%+v", state.AutoSwitch)
	}

	cafe := NetworkInfo{Ssids: []string{"Cafe"}}
	engine.UpdateNetwork(cafe)
	engine.UpdateNetwork(cafe)
	fixture.expectStatus(t, statusOff, "本机")

	engine.UpdateNetwork(officeNetwork)
	engine.UpdateNetwork(officeNetwork)
	fixture.expectStatus(t, statusOn, "公司")
	if err := engine.UseProfile("本机"); err != nil {
		t.Fatal(err)
	}
	if err := engine.ApplyAutoSwitch(); err != nil {
		t.Fatal(err)
	}
	fixture.expectStatus(t, statusOn, "公司")
}

func TestEngineAutoSwitchTurnedOn(t *testing.T) {
	fixture := newEngineFixture(t, `{
  "auto_switch": {"enabled": false, "rules": [{"match": "ssid", "value": "Office-5G", "action": "use", "profile": "公司"}]},
  "profiles": [{"name": "公司", "server": "10.0.0.1:8080"}]
}`)
	fixture.engine.UpdateNetwork(officeNetwork)
	fixture.engine.UpdateNetwork(officeNetwork)
	fixture.expectStatus(t, statusOff, "公司")
	config, _ := parseConfig(fixture.configText(t))
	config.AutoSwitch.Enabled = true
	if err := fixture.engine.SaveConfig(config); err != nil {
		t.Fatal(err)
	}
	fixture.expectStatus(t, statusOn, "公司")
}

func TestEngineStartupAction(t *testing.T) {
	fixture := newEngineFixture(t, strings.Replace(engineTestConfig, "{\n", "{\n  \"startup_action\": \"on\",\n", 1))
	fixture.engine.RunStartupAction()
	fixture.expectStatus(t, statusOn, "本机")

	off := newEngineFixture(t, strings.Replace(engineTestConfig, "{\n", "{\n  \"startup_action\": \"off\",\n", 1))
	off.system.System = SystemProxyState{ProxyEnabled: true, Server: "127.0.0.1:7890"}
	off.engine.RunStartupAction()
	off.expectStatus(t, statusOff, "本机")

	external := newEngineFixture(t, strings.Replace(engineTestConfig, "{\n", "{\n  \"startup_action\": \"off\",\n", 1))
	external.system.System = SystemProxyState{ProxyEnabled: true, Server: "9.9.9.9:1"}
	external.engine.RunStartupAction()
	if !external.system.System.ProxyEnabled {
		t.Error("启动时关闭只处理本程序开启的代理，不动其他程序的设置")
	}
}

func TestEngineClearAll(t *testing.T) {
	fixture := newEngineFixture(t, engineTestConfig)
	if err := fixture.engine.UseProfile("本机"); err != nil {
		t.Fatal(err)
	}
	fixture.system.Npm = map[string]string{"proxy": "http://left:1"}
	if err := fixture.engine.ClearAll(); err != nil {
		t.Fatal(err)
	}
	system := fixture.system
	if system.System.Active() || system.System.Server != "" || len(system.Env) != 0 || system.Git != "" || len(system.Npm) != 0 {
		t.Errorf("应清除所有设置：%+v %v %q %v", system.System, system.Env, system.Git, system.Npm)
	}
	fixture.expectStatus(t, statusOff, "本机")
}

func TestEngineSettingsState(t *testing.T) {
	fixture := newEngineFixture(t, engineTestConfig)
	if err := fixture.engine.UseProfile("公司"); err != nil {
		t.Fatal(err)
	}
	state := fixture.engine.settingsState()
	if state.Status.State != statusOn || state.Status.Profile != "公司" || strings.Join(state.Status.Applied, ",") != "系统代理,npm" {
		t.Errorf("状态不对：%+v", state.Status)
	}
	if state.Network.Ssids == nil || state.Network.Adapters == nil || state.Config == nil || len(state.Palette) == 0 {
		t.Error("设置页状态缺少字段")
	}
	if len(state.Status.Terminal) != 3 || !strings.Contains(state.Status.Terminal[0].Command, "http://10.0.0.1:8080") || state.Status.ExternalProfile != nil {
		t.Errorf("开启时应提供终端命令：%+v", state.Status)
	}

	fixture.system.System = SystemProxyState{ProxyEnabled: true, Server: "socks=192.168.1.9:1080", Bypass: "<local>"}
	state = fixture.engine.settingsState()
	external := state.Status.ExternalProfile
	if state.Status.State != statusExternal || external == nil || external.Server != "socks5://192.168.1.9:1080" || external.Bypass != "<local>" || external.Pac != "" {
		t.Errorf("其他程序设置的代理应转成配置：%+v %+v", state.Status, external)
	}
	if state.Status.Terminal != nil {
		t.Error("其他程序设置的代理不提供终端命令")
	}
	fixture.system.System = SystemProxyState{PacEnabled: true, Pac: "http://wpad/proxy.pac", Server: "1.2.3.4:80"}
	if external := fixture.engine.settingsState().Status.ExternalProfile; external == nil || external.Pac != "http://wpad/proxy.pac" || external.Server != "" {
		t.Errorf("只开了 PAC 时不应带上未启用的代理服务器：%+v", external)
	}
}
