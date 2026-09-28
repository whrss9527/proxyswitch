package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

// syncMachine 是测试里的一台电脑：自己的配置目录、引擎和同步服务，时钟由测试控制。
type syncMachine struct {
	t       *testing.T
	name    string
	engine  *Engine
	service *syncService
	mutex   sync.Mutex
	clock   time.Time
}

const syncTestConfigA = `{
  "core": { "port": 7893 },
  "editor": "notepad",
  "custom_rules": [ {"value": "example.com", "policy": "proxy"} ],
  "profiles": [
    {"name": "家", "server": "127.0.0.1:7890", "apply_to": ["system"]},
    {"name": "公司", "server": "10.0.0.1:8080", "apply_to": ["system"]},
    {"name": "机场", "subscription": "https://example.com/sub"}
  ]
}`

const syncTestConfigB = `{
  "core": { "port": 7990 },
  "tun": { "enabled": true },
  "editor": "code",
  "custom_rules": [ {"value": "internal.test", "policy": "direct"} ],
  "auto_switch": { "enabled": true, "rules": [ {"match": "ssid", "value": "Lab", "action": "use", "profile": "公司"} ] },
  "profiles": [
    {"name": "家", "server": "127.0.0.1:7890", "apply_to": ["system"]},
    {"name": "公司", "server": "10.9.9.9:3128", "apply_to": ["system"]},
    {"name": "实验室", "server": "10.1.1.1:3128", "apply_to": ["env"]}
  ]
}`

var syncTestStart = time.Date(2026, 9, 28, 10, 0, 0, 0, time.UTC)

func newSyncMachine(t *testing.T, name, configText string) *syncMachine {
	t.Helper()
	paths := pathsIn(t.TempDir(), false)
	config, err := parseConfig(configText)
	if err != nil {
		t.Fatal(err)
	}
	if err := writeConfigFile(paths.Config, config); err != nil {
		t.Fatal(err)
	}
	machine := &syncMachine{t: t, name: name, clock: syncTestStart}
	machine.engine = newEngine(newMemorySystem(), paths, func(Notice) {})
	if _, err := machine.engine.LoadConfig(); err != nil {
		t.Fatal(err)
	}
	machine.engine.configStamp.modified = machine.clock
	machine.restart()
	return machine
}

// restart 模拟重新打开 ProxySwitch：新的同步服务，上次同步的情况只能从状态文件读回来。
func (machine *syncMachine) restart() {
	machine.service = newSyncService(machine.engine, func(action func()) error {
		machine.mutex.Lock()
		defer machine.mutex.Unlock()
		action()
		return nil
	})
	machine.service.now = func() time.Time { return machine.clock }
	machine.service.this = machine.name
}

// at 把时钟拨到开始后 seconds 秒。
func (machine *syncMachine) at(seconds int) *syncMachine {
	machine.clock = syncTestStart.Add(time.Duration(seconds) * time.Second)
	return machine
}

// change 改本机的配置，改动时间是这台电脑的时钟。
func (machine *syncMachine) change(edit func(config *Config)) {
	machine.t.Helper()
	config := machine.engine.Config().Clone()
	edit(config)
	if err := machine.engine.SaveConfig(config); err != nil {
		machine.t.Fatal(err)
	}
	machine.engine.configStamp.modified = machine.clock
}

func (machine *syncMachine) config() *Config {
	return machine.engine.Config()
}

func (machine *syncMachine) profileNames() string {
	var names []string
	for _, profile := range machine.config().Profiles {
		names = append(names, profile.Name)
	}
	return strings.Join(names, ",")
}

func (machine *syncMachine) info() SyncInfo {
	return machine.service.Info(machine.config())
}

func readTestSyncFile(t *testing.T, folder string) *syncFile {
	t.Helper()
	file, err := readSyncFile(filepath.Join(folder, syncFileName))
	if err != nil || file == nil {
		t.Fatalf("同步文件读不出来：%v", err)
	}
	return file
}

func TestSyncedConfigJsonLeavesOutLocalSettings(t *testing.T) {
	config, err := parseConfig(syncTestConfigB)
	if err != nil {
		t.Fatal(err)
	}
	config.Sync = SyncConfig{Enabled: true, Folder: filepath.Join(t.TempDir(), "sync")}
	config.Share.Enabled = true
	data, err := syncedConfigJson(config)
	if err != nil {
		t.Fatal(err)
	}
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(data, &fields); err != nil {
		t.Fatal(err)
	}
	for key := range syncLocalKeys {
		if _, found := fields[key]; found {
			t.Errorf("本机的设置 %s 不应该同步", key)
		}
	}
	for _, key := range []string{"profiles", "custom_rules", "auto_switch", "hotkey", "rule_sets", "policy_groups"} {
		if _, found := fields[key]; !found {
			t.Errorf("%s 应该同步", key)
		}
	}

	// 订阅配置的代理地址由本机内核的端口决定，不同步；内核端口不一样的两台电脑同步的内容一样。
	subscription, err := parseConfig(syncTestConfigA)
	if err != nil {
		t.Fatal(err)
	}
	first, _ := syncedConfigJson(subscription)
	subscription.Core.Port = 8123
	subscription, err = parseConfig(string(mustMarshal(t, subscription)))
	if err != nil {
		t.Fatal(err)
	}
	second, _ := syncedConfigJson(subscription)
	if string(first) != string(second) {
		t.Errorf("内核端口不同步，同步的内容不应该变：\n%s\n%s", first, second)
	}
	if !strings.Contains(string(first), `"server":"127.0.0.1:7890"`) || strings.Contains(string(first), "7893") {
		t.Errorf("手动配置的代理地址要同步，订阅配置的不同步：%s", first)
	}
}

func TestSyncBetweenTwoMachines(t *testing.T) {
	folder := filepath.Join(t.TempDir(), "ProxySwitch")
	a := newSyncMachine(t, "甲", syncTestConfigA)
	b := newSyncMachine(t, "乙", syncTestConfigB)

	// 甲先开启：同步文件夹是空的，把甲的配置写上去。
	if err := a.service.Enable(folder); err != nil {
		t.Fatal(err)
	}
	if !a.config().Sync.Enabled || a.config().Sync.Folder != folder {
		t.Fatalf("甲的同步设置没有保存：%+v", a.config().Sync)
	}
	if file := readTestSyncFile(t, folder); file.Device != "甲" {
		t.Fatalf("同步文件应该是甲写的：%+v", file)
	}
	if info := a.info(); info.State != syncSynced || info.Device != "甲" || info.This != "甲" {
		t.Fatalf("甲的同步状态不对：%+v", info)
	}

	// 乙开启时同步文件夹里已经有甲的配置，先问怎么办。
	if err := b.service.Enable(folder); err != nil {
		t.Fatal(err)
	}
	info := b.info()
	if info.Pending == nil || info.Pending.Device != "甲" || info.Pending.Profiles != 3 || info.Pending.LocalProfiles != 3 || info.Folder != folder {
		t.Fatalf("乙应该等着选择：%+v %+v", info, info.Pending)
	}
	if b.config().Sync.Enabled {
		t.Fatal("选择之前不应该开启")
	}
	if err := b.service.Resolve("folder"); err != nil {
		t.Fatal(err)
	}
	if b.profileNames() != "家,公司,机场" || b.config().Profiles[1].Server != "10.0.0.1:8080" {
		t.Fatalf("乙应该换成甲的配置：%s", b.profileNames())
	}
	if b.config().Core.Port != 7990 || !b.config().Tun.Enabled || b.config().Editor != "code" || !b.config().Sync.Enabled {
		t.Fatalf("乙本机的设置要保留：%+v %+v %s %+v", b.config().Core, b.config().Tun, b.config().Editor, b.config().Sync)
	}
	if server := b.config().Profiles[2].Server; server != coreServer(7990) {
		t.Fatalf("订阅配置的代理地址应该是乙的内核端口：%s", server)
	}
	if b.info().Pending != nil {
		t.Fatal("选择之后不再等待")
	}

	// 两边的内核端口不一样，但不会来回改。
	before := readTestSyncFile(t, folder)
	b.at(5).service.tick()
	a.at(5).service.tick()
	b.at(10).service.tick()
	if after := readTestSyncFile(t, folder); after.Device != "甲" || after.Updated != before.Updated {
		t.Fatalf("没有改动时不应该写同步文件：%+v", after)
	}

	// 甲改了配置：等一秒写上去，乙读回来。
	a.at(20).change(func(config *Config) { config.Profiles[0].Name = "家里" })
	a.at(20).service.tick()
	if readTestSyncFile(t, folder).Updated != before.Updated {
		t.Fatal("改动后要等一会儿再写")
	}
	a.at(22).service.tick()
	if file := readTestSyncFile(t, folder); file.Device != "甲" || !strings.Contains(string(file.Config), "家里") {
		t.Fatalf("甲的改动没有写上去：%s", file.Config)
	}
	b.at(23).service.tick()
	if b.profileNames() != "家里,公司,机场" || b.config().Core.Port != 7990 || !b.config().Sync.Enabled {
		t.Fatalf("乙没有收到甲的改动：%s", b.profileNames())
	}

	// 乙改了本机的设置：不同步。
	written := readTestSyncFile(t, folder)
	b.at(30).change(func(config *Config) { config.Core.Port = 8000; config.Editor = "notepad" })
	b.at(35).service.tick()
	if file := readTestSyncFile(t, folder); file.Updated != written.Updated {
		t.Fatal("本机的设置改了不应该写同步文件")
	}
	a.at(36).service.tick()
	if a.config().Core.Port != 7893 {
		t.Fatal("甲的内核端口不应该变")
	}

	// 乙改了规则：写上去，甲读回来。
	b.at(40).change(func(config *Config) {
		config.CustomRules = append(config.CustomRules, CustomRule{Value: "b.example", Policy: rulePolicyDirect})
	})
	b.at(42).service.tick()
	if file := readTestSyncFile(t, folder); file.Device != "乙" {
		t.Fatalf("乙的改动没有写上去：%+v", file)
	}
	a.at(43).service.tick()
	if rules := a.config().CustomRules; len(rules) != 2 || rules[1].Value != "b.example" {
		t.Fatalf("甲没有收到乙的规则：%+v", rules)
	}
	if info := a.info(); info.State != syncSynced || info.Device != "乙" {
		t.Fatalf("甲的同步状态不对：%+v", info)
	}
}

// syncedPair 是已经开启同步、配置一致的两台电脑。
func syncedPair(t *testing.T) (folder string, a, b *syncMachine) {
	t.Helper()
	folder = filepath.Join(t.TempDir(), "ProxySwitch")
	a = newSyncMachine(t, "甲", syncTestConfigA)
	b = newSyncMachine(t, "乙", syncTestConfigB)
	if err := a.service.Enable(folder); err != nil {
		t.Fatal(err)
	}
	if err := b.service.Enable(folder); err != nil {
		t.Fatal(err)
	}
	if err := b.service.Resolve("folder"); err != nil {
		t.Fatal(err)
	}
	return folder, a, b
}

func TestSyncNewestChangeWins(t *testing.T) {
	folder, a, b := syncedPair(t)

	// 两边都改了：乙改得晚，乙的胜出。
	a.at(10).change(func(config *Config) { config.Profiles[0].Name = "甲改的" })
	b.at(20).change(func(config *Config) { config.Profiles[0].Name = "乙改的" })
	b.at(22).service.tick()
	a.at(23).service.tick()
	if a.config().Profiles[0].Name != "乙改的" {
		t.Fatalf("改得晚的应该胜出：%s", a.profileNames())
	}
	a.at(30).service.tick()
	if file := readTestSyncFile(t, folder); file.Device != "乙" {
		t.Fatalf("甲不应该再写回去：%+v", file)
	}

	// 甲改得晚，但乙先写了上去：甲读到时比较改动时间，甲的胜出并写上去，乙跟着改。
	b.at(40).change(func(config *Config) { config.Profiles[1].Name = "乙的公司" })
	a.at(45).change(func(config *Config) { config.Profiles[1].Name = "甲的公司" })
	b.at(46).service.tick()
	a.at(47).service.tick()
	if file := readTestSyncFile(t, folder); file.Device != "甲" || !strings.Contains(string(file.Config), "甲的公司") {
		t.Fatalf("改得晚的甲应该写上去：%s", file.Config)
	}
	b.at(48).service.tick()
	if b.config().Profiles[1].Name != "甲的公司" || b.config().Core.Port != 7990 {
		t.Fatalf("乙应该换成甲的：%s", b.profileNames())
	}
}

func TestSyncRememberedAcrossRestarts(t *testing.T) {
	folder, a, b := syncedPair(t)
	if a.engine.state.Synced == "" || a.engine.state.SyncedFolder != folder {
		t.Fatalf("上次同步的情况要记进状态文件：%+v", a.engine.state)
	}

	// 乙改了配置写上去；甲这时没有运行，之后改了本机的设置（配置文件的修改时间比乙的改动晚），再打开时仍然用乙的。
	b.at(10).change(func(config *Config) { config.Profiles[0].Name = "乙改的" })
	b.at(12).service.tick()
	a.at(20).change(func(config *Config) { config.Editor = "code"; config.Tun.Enabled = true })
	a.restart()
	a.at(21).service.tick()
	if a.config().Profiles[0].Name != "乙改的" {
		t.Fatalf("重启后应该收到乙的改动：%s", a.profileNames())
	}
	if a.config().Editor != "code" || !a.config().Tun.Enabled {
		t.Fatal("甲本机的设置要保留")
	}
	if file := readTestSyncFile(t, folder); file.Device != "乙" {
		t.Fatalf("甲不应该写回去：%+v", file)
	}

	// 甲不运行的时候改了要同步的配置，重启后写上去。
	a.at(30).change(func(config *Config) { config.Profiles[0].Name = "甲改的" })
	a.restart()
	a.at(31).service.tick()
	if file := readTestSyncFile(t, folder); file.Device != "甲" || !strings.Contains(string(file.Config), "甲改的") {
		t.Fatalf("重启后应该写上甲的改动：%s", file.Config)
	}
}

func TestSyncMergeKeepsBothSides(t *testing.T) {
	folder := filepath.Join(t.TempDir(), "ProxySwitch")
	a := newSyncMachine(t, "甲", syncTestConfigA)
	b := newSyncMachine(t, "乙", syncTestConfigB)
	if err := a.service.Enable(folder); err != nil {
		t.Fatal(err)
	}
	if err := b.service.Enable(folder); err != nil {
		t.Fatal(err)
	}
	if err := b.service.Resolve("merge"); err != nil {
		t.Fatal(err)
	}
	// 同名同地址的「家」算一个；地址不一样的「公司」改名；乙独有的追加在后面。
	if names := b.profileNames(); names != "家,公司,机场,公司（本机）,实验室" {
		t.Fatalf("合并后的配置不对：%s", names)
	}
	if server := b.config().FindProfile("公司（本机）").Server; server != "10.9.9.9:3128" {
		t.Fatalf("改名的是乙的「公司」：%s", server)
	}
	rules := b.config().AutoSwitch.Rules
	if len(rules) != 1 || rules[0].Profile != "公司（本机）" {
		t.Fatalf("自动切换规则要跟着改名：%+v", rules)
	}
	if custom := b.config().CustomRules; len(custom) != 2 || custom[0].Value != "example.com" || custom[1].Value != "internal.test" {
		t.Fatalf("自定义规则要合并：%+v", custom)
	}
	if b.config().Core.Port != 7990 || !b.config().Tun.Enabled {
		t.Fatal("乙本机的设置要保留")
	}
	// 合并的结果写上去，甲也变成合并后的。
	if file := readTestSyncFile(t, folder); file.Device != "乙" {
		t.Fatalf("合并后要写上去：%+v", file)
	}
	a.at(5).service.tick()
	if names := a.profileNames(); names != "家,公司,机场,公司（本机）,实验室" {
		t.Fatalf("甲应该收到合并后的配置：%s", names)
	}
	if a.config().Core.Port != 7893 || a.config().Profiles[2].Server != coreServer(7893) {
		t.Fatal("甲的内核端口和订阅的代理地址不应该变")
	}
}

func TestSyncResolveLocalAndCancel(t *testing.T) {
	folder := filepath.Join(t.TempDir(), "ProxySwitch")
	a := newSyncMachine(t, "甲", syncTestConfigA)
	b := newSyncMachine(t, "乙", syncTestConfigB)
	if err := a.service.Enable(folder); err != nil {
		t.Fatal(err)
	}

	// 取消：不开启，同步文件不变。
	if err := b.service.Enable(folder); err != nil {
		t.Fatal(err)
	}
	if err := b.service.Resolve("cancel"); err != nil {
		t.Fatal(err)
	}
	if info := b.info(); b.config().Sync.Enabled || info.Pending != nil || info.State != syncOff {
		t.Fatalf("取消后不应该开启：%+v", info)
	}
	if err := b.service.Resolve("folder"); err == nil {
		t.Fatal("没有等待的选择时应该报错")
	}

	// 用本机的：乙的配置覆盖同步文件夹里的，甲跟着变。
	if err := b.service.Enable(folder); err != nil {
		t.Fatal(err)
	}
	if err := b.service.Resolve("local"); err != nil {
		t.Fatal(err)
	}
	if file := readTestSyncFile(t, folder); file.Device != "乙" {
		t.Fatalf("乙的配置应该写上去：%+v", file)
	}
	a.at(5).service.tick()
	if names := a.profileNames(); names != "家,公司,实验室" || a.config().Profiles[1].Server != "10.9.9.9:3128" {
		t.Fatalf("甲应该换成乙的配置：%s", names)
	}
	if a.config().Tun.Enabled || a.config().Editor != "notepad" {
		t.Fatal("甲本机的设置不应该变")
	}

	// 关闭：同步文件留着，之后不再同步。
	if err := a.service.Disable(); err != nil {
		t.Fatal(err)
	}
	if a.config().Sync.Enabled || a.info().State != syncOff {
		t.Fatalf("关闭后的状态不对：%+v", a.info())
	}
	b.at(10).change(func(config *Config) { config.Profiles[0].Name = "乙改的" })
	b.at(12).service.tick()
	a.at(13).service.tick()
	if a.config().Profiles[0].Name == "乙改的" {
		t.Fatal("关闭同步后不应该再收到改动")
	}
}

func TestSyncEnableWithSameConfigNeedsNoChoice(t *testing.T) {
	folder := filepath.Join(t.TempDir(), "ProxySwitch")
	a := newSyncMachine(t, "甲", syncTestConfigA)
	// 乙的配置和甲一样，只有本机的设置不同。
	b := newSyncMachine(t, "乙", strings.Replace(syncTestConfigA, `"port": 7893`, `"port": 7990`, 1))
	if err := a.service.Enable(folder); err != nil {
		t.Fatal(err)
	}
	if err := b.service.Enable(folder); err != nil {
		t.Fatal(err)
	}
	if info := b.info(); info.Pending != nil || !info.Enabled || info.State != syncSynced || info.Device != "甲" {
		t.Fatalf("一样的配置直接开启：%+v", info)
	}
}

func TestSyncEnableChecksFolder(t *testing.T) {
	machine := newSyncMachine(t, "甲", syncTestConfigA)
	if err := machine.service.Enable("相对路径"); err == nil || !strings.Contains(err.Error(), "完整的路径") {
		t.Fatalf("相对路径应该报错：%v", err)
	}
	for _, name := range []string{syncFolderVariable, "OneDrive", "OneDriveConsumer", "OneDriveCommercial"} {
		t.Setenv(name, "")
	}
	if err := machine.service.Enable(""); err == nil || !strings.Contains(err.Error(), "OneDrive") {
		t.Fatalf("没有 OneDrive 时应该要求填文件夹：%v", err)
	}
	oneDrive := t.TempDir()
	t.Setenv("OneDrive", oneDrive)
	if folder := defaultSyncFolder(); folder != filepath.Join(oneDrive, syncFolderName) {
		t.Fatalf("默认用 OneDrive 里的文件夹：%s", folder)
	}
	if err := machine.service.Enable(""); err != nil {
		t.Fatal(err)
	}
	if info := machine.info(); info.Folder != filepath.Join(oneDrive, syncFolderName) || info.DefaultFolder != info.Folder || machine.config().Sync.Folder != "" {
		t.Fatalf("留空时用 OneDrive：%+v", info)
	}
	if _, err := os.Stat(filepath.Join(oneDrive, syncFolderName, syncFileName)); err != nil {
		t.Fatal("同步文件应该写在 OneDrive 里")
	}
}

func TestSyncConflictCopies(t *testing.T) {
	folder := t.TempDir()
	write := func(name, device string, minutes int) {
		t.Helper()
		file := syncFile{Format: syncFormat, Updated: syncTestStart.Add(time.Duration(minutes) * time.Minute).Format(time.RFC3339Nano), Device: device, Config: json.RawMessage(`{"profiles":[]}`)}
		if err := writeSyncFile(folder, file); err != nil {
			t.Fatal(err)
		}
		if name != syncFileName {
			if err := os.Rename(filepath.Join(folder, syncFileName), filepath.Join(folder, name)); err != nil {
				t.Fatal(err)
			}
		}
	}
	write("config-LAPTOP.json", "LAPTOP", 60)
	write("config (DESKTOP's conflicted copy 2026-09-28).json", "DESKTOP", 30)
	write(syncFileName, "甲", 0)
	if err := os.WriteFile(filepath.Join(folder, "config-notes.json"), []byte(`{"note": 1}`), 0o644); err != nil {
		t.Fatal(err)
	}
	if winner := resolveSyncConflicts(folder); winner != "LAPTOP" {
		t.Fatalf("写入最晚的副本应该胜出：%q", winner)
	}
	if file := readTestSyncFile(t, folder); file.Device != "LAPTOP" {
		t.Fatalf("胜出的副本要写回 config.json：%+v", file)
	}
	entries, _ := os.ReadDir(folder)
	var names []string
	for _, entry := range entries {
		names = append(names, entry.Name())
	}
	if strings.Join(names, ",") != "config-notes.json,config.json" {
		t.Fatalf("冲突副本要删掉，不认识的文件留着：%v", names)
	}
	if winner := resolveSyncConflicts(folder); winner != "" {
		t.Fatalf("没有冲突时什么都不做：%q", winner)
	}
}

func TestReadSyncFile(t *testing.T) {
	folder := t.TempDir()
	path := filepath.Join(folder, syncFileName)
	if file, err := readSyncFile(path); file != nil || err != nil {
		t.Fatalf("文件不存在时返回空：%v %v", file, err)
	}
	for text, want := range map[string]string{
		`{"format": 2, "updated": "2026-09-28T10:00:00Z", "config": {}}`: "更新版本",
		`{"format": 1, "updated": "2026-09-28T10:00:00Z"}`:               "格式不对",
		`not json`: "格式不对",
		"\xef\xbb\xbf" + `{"format": 1, "updated": "2026-09-28T10:00:00Z", "config": {}}`: "",
	} {
		if err := os.WriteFile(path, []byte(text), 0o644); err != nil {
			t.Fatal(err)
		}
		_, err := readSyncFile(path)
		if (want == "") != (err == nil) || (err != nil && !strings.Contains(err.Error(), want)) {
			t.Errorf("%s：想要 %q，得到 %v", text, want, err)
		}
	}
}

func TestSyncSkipsApplyWhenLocalChangedMeanwhile(t *testing.T) {
	folder, a, b := syncedPair(t)
	b.at(10).change(func(config *Config) { config.Profiles[0].Name = "乙改的" })
	b.at(12).service.tick()
	remote := readTestSyncFile(t, folder)
	// 甲读同步文件之后、应用之前本机的配置又改了：这次不应用。
	stale, _ := syncedConfigJson(a.config())
	a.at(13).change(func(config *Config) { config.Profiles[0].Name = "甲刚改的" })
	a.service.apply(folder, stale, remote, "hash", "stamp")
	if a.config().Profiles[0].Name != "甲刚改的" {
		t.Fatalf("本机刚改过不应该被覆盖：%s", a.profileNames())
	}
	// 下一次同步时比较改动时间：甲改得晚，写上去。
	a.at(15).service.tick()
	if file := readTestSyncFile(t, folder); file.Device != "甲" || !strings.Contains(string(file.Config), "甲刚改的") {
		t.Fatalf("甲的改动应该写上去：%s", file.Config)
	}
}

func TestSyncReportsBrokenConfigFile(t *testing.T) {
	folder, a, _ := syncedPair(t)
	if err := os.WriteFile(a.engine.paths.Config, []byte("{ 坏掉的配置"), 0o644); err != nil {
		t.Fatal(err)
	}
	a.engine.ReloadIfChanged()
	a.at(5).service.tick()
	if info := a.info(); info.State != syncError || !strings.Contains(info.Message, "配置文件有错误") {
		t.Fatalf("配置文件有错误时不同步：%+v", info)
	}
	if file := readTestSyncFile(t, folder); file.Device != "甲" {
		t.Fatalf("同步文件不应该变：%+v", file)
	}
}
