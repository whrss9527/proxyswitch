package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// fakeCore 记录引擎交给内核的设定。
type fakeCore struct {
	history []CoreSettings
}

func (core *fakeCore) Sync(settings CoreSettings) int {
	core.history = append(core.history, settings)
	return len(core.history)
}

func (core *fakeCore) last() CoreSettings {
	if len(core.history) == 0 {
		return CoreSettings{}
	}
	return core.history[len(core.history)-1]
}

const subscriptionTestConfig = `{
  "profiles": [
    {"name": "本机", "server": "127.0.0.1:7890", "apply_to": ["system"]},
    {"name": "机场", "subscription": "https://sub.example.com/api?token=abc", "apply_to": ["system", "env"]}
  ]
}`

const testSubscriptionContent = "proxies:\n  - {name: 香港 01, type: ss, server: hk.example.com, port: 443, cipher: aes-128-gcm, password: x}\n  - {name: 日本 02, type: ss, server: jp.example.com, port: 443, cipher: aes-128-gcm, password: x}\n"

// newSubscriptionFixture 创建带假内核的引擎；downloads 记录引擎请求后台下载的次数。
func newSubscriptionFixture(t *testing.T, configText string) (*engineFixture, *fakeCore, *int) {
	t.Helper()
	fixture := newEngineFixture(t, configText)
	core := &fakeCore{}
	downloads := 0
	fixture.engine.core = core
	fixture.engine.downloadsNeeded = func() { downloads++ }
	return fixture, core, &downloads
}

func installFakeCoreBinary(t *testing.T, engine *Engine) {
	t.Helper()
	if err := os.MkdirAll(engine.paths.Core, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(engine.paths.Core, coreBinaryName()), []byte("fake"), 0o755); err != nil {
		t.Fatal(err)
	}
}

func recordTestSubscription(t *testing.T, engine *Engine, profile *Profile) {
	t.Helper()
	result := subscriptionDownload{Content: []byte(testSubscriptionContent), Nodes: 2, Format: "clash", Info: SubscriptionInfo{Upload: 1, Download: 2, Total: 100, Expire: 1798761600}}
	if err := engine.RecordSubscription(profile.Id, profile.Subscription, result, nil); err != nil {
		t.Fatal(err)
	}
}

func TestSubscriptionConfig(t *testing.T) {
	config, err := parseConfig(`{"core": {"port": 18000}, "profiles": [
		{"name": "机场", "subscription": " https://sub.example.com/a ", "server": "1.2.3.4:5", "pac": "http://x/p.pac", "node": "香港 01"},
		{"name": "本机", "server": "127.0.0.1:7890", "node": "不该保留", "mode": "global"}
	]}`)
	if err != nil {
		t.Fatal(err)
	}
	subscription, local := config.Profiles[0], config.Profiles[1]
	if subscription.Server != "127.0.0.1:18000" || subscription.Pac != "" || subscription.Mode != "rule" || subscription.Subscription != "https://sub.example.com/a" || subscription.Kind() != "subscription" {
		t.Errorf("订阅配置应使用内核的端口：%+v", subscription)
	}
	if subscription.Summary() != "订阅 · 香港 01" {
		t.Errorf("订阅配置的说明不对：%q", subscription.Summary())
	}
	if local.Node != "" || local.Mode != "" {
		t.Errorf("普通配置不应带节点和模式：%+v", local)
	}
	if config, _ := parseConfig(`{"profiles": []}`); config.Core.Port != defaultCorePort || config.Core.Path != "" {
		t.Errorf("内核默认设置不对：%+v", config.Core)
	}
	for text, problem := range map[string]string{
		`{"profiles": [{"name": "a", "subscription": "ftp://x/y"}]}`:                   "http",
		`{"profiles": [{"name": "a", "subscription": "https://x/y", "mode": "fast"}]}`: "mode",
		`{"core": {"port": 70000}, "profiles": []}`:                                    "core.port",
	} {
		if _, err := parseConfig(text); err == nil || !strings.Contains(err.Error(), problem) {
			t.Errorf("%s 应报错（%s）：%v", text, problem, err)
		}
	}

	copied := config.Clone()
	copied.Profiles[0].Node = "改了"
	copied.Profiles[0].ApplyTo[0] = "env"
	if config.Profiles[0].Node != "香港 01" || config.Profiles[0].ApplyTo[0] != "system" {
		t.Error("修改副本不应影响原来的配置")
	}
}

func TestEngineSubscriptionFlow(t *testing.T) {
	fixture, core, downloads := newSubscriptionFixture(t, subscriptionTestConfig)
	engine := fixture.engine
	now := time.Date(2026, 9, 24, 9, 0, 0, 0, time.Local)
	engine.now = func() time.Time { return now }
	profile := *engine.Config().FindProfile("机场")
	if profile.Server != "127.0.0.1:17890" {
		t.Fatalf("订阅配置应指向内核的端口：%q", profile.Server)
	}
	if due := engine.SubscriptionsDue(); len(due) != 1 || due[0].Id != profile.Id {
		t.Fatalf("新添加的订阅应立即下载：%+v", due)
	}

	// 内核或订阅还没下载时不能开启，也不改动系统代理。
	if err := engine.UseProfile("机场"); err == nil || !strings.Contains(err.Error(), "内核") {
		t.Errorf("没有内核时应拒绝开启：%v", err)
	}
	fixture.expectStatus(t, statusOff, "本机")
	if notice := fixture.lastNotice(t); notice.Level != noticeError || notice.Title != "开启代理失败" {
		t.Errorf("应提示开启失败：%+v", notice)
	}
	installFakeCoreBinary(t, engine)
	before := *downloads
	if err := engine.UseProfile("机场"); err == nil || !strings.Contains(err.Error(), "还在下载") {
		t.Errorf("订阅还没下载时应拒绝开启：%v", err)
	}
	if *downloads == before {
		t.Error("订阅还没下载时应请求后台下载")
	}
	if fixture.system.System.ProxyEnabled {
		t.Error("不能开启时不应改动系统代理")
	}

	recordTestSubscription(t, engine, &profile)
	if notice := fixture.lastNotice(t); notice.Title != "订阅已就绪：机场" || !strings.Contains(notice.Text, "2 个节点") {
		t.Errorf("第一次下载成功应提示：%+v", notice)
	}
	if data, _ := os.ReadFile(engine.subscriptionPath(profile.Id)); string(data) != testSubscriptionContent {
		t.Error("应保存订阅文件")
	}
	settings := core.last()
	if len(settings.Subscriptions) != 1 || settings.Subscriptions[0].Id != profile.Id || settings.Active != "" || settings.Port != 17890 || settings.BinaryStamp == "" {
		t.Errorf("下载后应把订阅交给内核：%+v", settings)
	}
	if info := engine.subscriptionInfos()[profile.Id]; info.Nodes != 2 || info.Total != 100 || info.Updated == "" || info.Source != "" {
		t.Errorf("设置页的订阅信息不对：%+v", info)
	}
	if len(engine.SubscriptionsDue()) != 0 {
		t.Error("刚下载过不应再下载")
	}

	// 开启：系统代理和环境变量指向内核，内核把流量交给这个订阅。
	if err := engine.UseProfile("机场"); err != nil {
		t.Fatal(err)
	}
	fixture.expectStatus(t, statusOn, "机场")
	if fixture.system.System.Server != "127.0.0.1:17890" || fixture.system.Env["HTTP_PROXY"] != "http://127.0.0.1:17890" {
		t.Errorf("应指向内核：%+v %v", fixture.system.System, fixture.system.Env)
	}
	if settings := core.last(); settings.Active != profile.Id || settings.Mode != "rule" {
		t.Errorf("内核应使用这个订阅：%+v", settings)
	}
	if paths := engine.DownloadPaths(); len(paths) != 2 || paths[0] != "" || paths[1] != "http://127.0.0.1:17890" {
		t.Errorf("下载路径应先直连再经内核：%v", paths)
	}

	// 选择节点：记在配置文件里，内核随之切换。
	if err := engine.SelectNode(profile.Id, "日本 02"); err != nil {
		t.Fatal(err)
	}
	reloaded, _, _ := loadConfig(engine.paths.Config)
	if reloaded.FindProfile("机场").Node != "日本 02" || core.last().Subscriptions[0].Node != "日本 02" {
		t.Errorf("选中的节点应保存并交给内核：%+v", core.last())
	}
	fixture.expectStatus(t, statusOn, "机场")
	if err := engine.SelectNode("不存在", "x"); err == nil {
		t.Error("不存在的订阅应报错")
	}

	// 切到普通配置：内核不再负责流量。
	if err := engine.UseProfile("本机"); err != nil {
		t.Fatal(err)
	}
	if fixture.system.System.Server != "127.0.0.1:7890" || core.last().Active != "" || fixture.system.Env["HTTP_PROXY"] != "" {
		t.Errorf("切到普通配置后不应再使用内核：%+v %v", core.last(), fixture.system.Env)
	}

	// 每天更新一次；失败后隔一段时间重试，期间继续使用上次下载的节点。
	now = now.Add(25 * time.Hour)
	if due := engine.SubscriptionsDue(); len(due) != 1 {
		t.Fatalf("超过一天应更新订阅：%+v", due)
	}
	_ = engine.RecordSubscription(profile.Id, profile.Subscription, subscriptionDownload{}, httpStatusError{502})
	if len(engine.SubscriptionsDue()) != 0 || len(core.last().Subscriptions) != 1 {
		t.Error("更新失败后应继续使用上次的节点，并等一段时间再试")
	}
	if info := engine.subscriptionInfos()[profile.Id]; !strings.Contains(info.Error, "502") || info.Nodes != 2 {
		t.Errorf("应记下失败原因并保留上次的信息：%+v", info)
	}
	now = now.Add(2 * time.Hour)
	if len(engine.SubscriptionsDue()) != 1 {
		t.Error("失败一段时间后应重试")
	}

	// 改了订阅地址：旧地址的节点不再使用，立即下载新地址；旧地址的下载结果被忽略。
	config := engine.Config().Clone()
	config.FindProfile("机场").Subscription = "https://sub.example.com/api?token=new"
	if err := engine.SaveConfig(config); err != nil {
		t.Fatal(err)
	}
	if len(core.last().Subscriptions) != 0 || len(engine.SubscriptionsDue()) != 1 {
		t.Errorf("地址改了应立即重新下载，旧的节点不再交给内核：%+v", core.last())
	}
	if err := engine.RecordSubscription(profile.Id, profile.Subscription, subscriptionDownload{Content: []byte(testSubscriptionContent)}, nil); err == nil {
		t.Error("旧地址的下载结果应被忽略")
	}
	if info := engine.subscriptionInfos()[profile.Id]; info.Updated != "" {
		t.Errorf("新地址还没下载时设置页应显示为未下载：%+v", info)
	}

	// 删除订阅配置：清理记录和文件。
	config = engine.Config().Clone()
	config.Profiles = config.Profiles[:1]
	if err := engine.SaveConfig(config); err != nil {
		t.Fatal(err)
	}
	if _, found := engine.state.Subscriptions[profile.Id]; found || fileExists(engine.subscriptionPath(profile.Id)) {
		t.Error("删除订阅配置后应清理记录和文件")
	}
	if len(core.last().Subscriptions) != 0 {
		t.Error("删除后不应再交给内核")
	}
}

func TestEngineSubscriptionExitAndResume(t *testing.T) {
	fixture, _, _ := newSubscriptionFixture(t, subscriptionTestConfig)
	engine := fixture.engine
	installFakeCoreBinary(t, engine)
	profile := *engine.Config().FindProfile("机场")
	recordTestSubscription(t, engine, &profile)
	if err := engine.UseProfile("机场"); err != nil {
		t.Fatal(err)
	}

	// 退出前关闭：内核随程序退出，系统代理不能指向没有程序监听的端口。
	engine.PrepareExit()
	fixture.expectStatus(t, statusOff, "机场")
	if fixture.system.System.ProxyEnabled || len(fixture.system.Env) != 0 {
		t.Errorf("退出前应关闭使用订阅的代理：%+v %v", fixture.system.System, fixture.system.Env)
	}

	// 下次启动（startup_action 为 keep）时重新开启，只开启一次。
	restarted := newEngine(fixture.system, engine.paths, func(Notice) {})
	restarted.core = &fakeCore{}
	if _, err := restarted.LoadConfig(); err != nil {
		t.Fatal(err)
	}
	restarted.RunStartupAction()
	if status := restarted.Status(); status.State != statusOn || status.Profile.Name != "机场" {
		t.Errorf("启动后应重新开启退出时关闭的订阅：%+v", status)
	}
	if restarted.state.Resume != "" {
		t.Error("重新开启后应清除记录")
	}

	// 普通配置退出时不受影响。
	if err := restarted.UseProfile("本机"); err != nil {
		t.Fatal(err)
	}
	restarted.PrepareExit()
	if !fixture.system.System.ProxyEnabled || restarted.state.Resume != "" {
		t.Error("普通配置退出时不应关闭代理")
	}
}

func TestEngineSubscriptionWithoutCore(t *testing.T) {
	fixture := newEngineFixture(t, subscriptionTestConfig)
	if err := fixture.engine.UseProfile("机场"); err == nil || !strings.Contains(err.Error(), "保持运行") {
		t.Errorf("命令行模式（没有内核）不能开启订阅配置：%v", err)
	}
	fixture.expectStatus(t, statusOff, "本机")
}

func TestEngineGeoData(t *testing.T) {
	fixture, core, _ := newSubscriptionFixture(t, subscriptionTestConfig)
	engine := fixture.engine
	now := time.Date(2026, 9, 24, 9, 0, 0, 0, time.Local)
	engine.now = func() time.Time { return now }
	if !engine.GeoDue() {
		t.Fatal("使用大陆直连的订阅需要地理数据")
	}
	engine.RecordGeoDownload(os.ErrNotExist)
	if engine.GeoDue() {
		t.Error("下载失败后应等一段时间再试")
	}
	now = now.Add(2 * time.Hour)
	if !engine.GeoDue() {
		t.Error("失败一段时间后应重试")
	}
	if err := os.MkdirAll(engine.paths.Core, 0o755); err != nil {
		t.Fatal(err)
	}
	for _, geo := range coreGeoFiles {
		if err := os.WriteFile(filepath.Join(engine.paths.Core, geo.Name), []byte("data"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	engine.RecordGeoDownload(nil)
	if engine.GeoDue() || !core.last().GeoReady {
		t.Errorf("下载成功后应让内核用上大陆直连规则：%+v", core.last())
	}

	global := newEngineFixture(t, `{"profiles": [{"name": "机场", "subscription": "https://sub.example.com/a", "mode": "global"}]}`)
	global.engine.core = &fakeCore{}
	if global.engine.GeoDue() {
		t.Error("只用全局模式时不需要地理数据")
	}
}
