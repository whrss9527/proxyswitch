package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestDefaultConfigText(t *testing.T) {
	config, err := parseConfig(defaultConfigText)
	if err != nil {
		t.Fatalf("默认配置应能解析：%v", err)
	}
	if len(config.Profiles) != 0 {
		t.Errorf("默认配置不应带代理配置，得到 %d 个", len(config.Profiles))
	}
	if config.Hotkey != "Ctrl+Alt+P" || config.NotifyLevel != "all" || config.NotifySeconds != 3 {
		t.Errorf("默认值不对：%+v", config)
	}
	if config.StartupAction != "keep" || config.OffMode != "direct" || config.HealthCheck != "notify" {
		t.Errorf("默认值不对：%+v", config)
	}
	if config.AutoSwitch.Enabled || config.AutoSwitch.DefaultAction != "keep" || config.AutoSwitch.Rules == nil {
		t.Errorf("自动切换默认值不对：%+v", config.AutoSwitch)
	}
}

func TestParseConfigJsonc(t *testing.T) {
	text := "\xef\xbb\xbf" + `// 注释
{
  /* 块注释
     跨行 */
  "hotkey": "ctrl + alt + x", // 行尾注释
  "notify_level": "Errors",
  "profiles": [
    {
      "name": " 本机 ",
      "server": "127.0.0.1:7890",
      "pac": "",
      "apply_to": ["system", "ENV", "system"],
    },
    { "name": "网址 // 不是注释", "server": "socks5://127.0.0.1:1080", "color": "#ABCDEF" },
  ],
}`
	config, err := parseConfig(text)
	if err != nil {
		t.Fatalf("解析失败：%v", err)
	}
	if config.NotifyLevel != "errors" {
		t.Errorf("notify_level 应转小写，得到 %q", config.NotifyLevel)
	}
	first := config.Profiles[0]
	if first.Name != "本机" || strings.Join(first.ApplyTo, ",") != "system,env" {
		t.Errorf("第一个配置规范化不对：%+v", first)
	}
	if first.Bypass != defaultBypass || first.NoProxy != defaultNoProxy || first.Color != profilePalette[0] {
		t.Errorf("第一个配置默认值不对：%+v", first)
	}
	second := config.Profiles[1]
	if second.Name != "网址 // 不是注释" || second.Color != "#abcdef" || !strings.HasPrefix(second.Id, "p") {
		t.Errorf("第二个配置不对：%+v", second)
	}
	if first.Id == second.Id {
		t.Error("两个配置的 id 不应相同")
	}
}

func TestStableProfileIds(t *testing.T) {
	text := `{"profiles": [{"name": "公司", "server": "10.0.0.1:8080"}, {"name": "家里", "server": "127.0.0.1:7890"}]}`
	first, err := parseConfig(text)
	if err != nil {
		t.Fatal(err)
	}
	second, _ := parseConfig(text)
	for index := range first.Profiles {
		if first.Profiles[index].Id != second.Profiles[index].Id {
			t.Errorf("没有 id 的配置每次解析应得到相同的 id：%s / %s", first.Profiles[index].Id, second.Profiles[index].Id)
		}
	}
	withIds, _ := parseConfig(`{"profiles": [{"id": "same", "name": "甲", "server": "a:1"}, {"id": "same", "name": "乙", "server": "b:2"}]}`)
	if withIds.Profiles[0].Id != "same" || withIds.Profiles[1].Id == "same" {
		t.Errorf("重复的 id 应重新生成：%v / %v", withIds.Profiles[0].Id, withIds.Profiles[1].Id)
	}
}

func TestLegacyNotifyMigration(t *testing.T) {
	config, err := parseConfig(`{"notify": false}`)
	if err != nil {
		t.Fatal(err)
	}
	if config.NotifyLevel != "errors" || config.Notify != nil {
		t.Errorf("notify=false 应换算成 errors，得到 %q %v", config.NotifyLevel, config.Notify)
	}
	config, _ = parseConfig(`{"notify": true}`)
	if config.NotifyLevel != "all" {
		t.Errorf("notify=true 应换算成 all，得到 %q", config.NotifyLevel)
	}
	config, _ = parseConfig(`{"notify": false, "notify_level": "none"}`)
	if config.NotifyLevel != "none" {
		t.Errorf("已有 notify_level 时不应被旧字段覆盖，得到 %q", config.NotifyLevel)
	}
}

func TestParseConfigErrors(t *testing.T) {
	cases := []struct {
		text    string
		wanted  string
		explain string
	}{
		{"{\n\"hotkey\": \"a\"\n\"x\": 1}", "第 3 行", "缺逗号时报出行号"},
		{`{"hotkey": 5}`, "类型不对", "类型错误"},
		{`{} {}`, "多余的内容", "多余内容"},
		{`{"notify_level": "loud"}`, "notify_level", "枚举值"},
		{`{"notify_seconds": 99}`, "notify_seconds", "通知秒数范围"},
		{`{"hotkey": "P"}`, "修饰键", "快捷键缺修饰键"},
		{`{"profile_hotkeys": "Ctrl+1"}`, "不是修饰键", "按数字切换只能填修饰键"},
		{`{"test_url": "ftp://x"}`, "测速地址", "测速地址协议"},
		{`{"profiles": [{"name": "", "server": "a:1"}]}`, "没有填名字", "空名字"},
		{`{"profiles": [{"name": "甲", "server": "a:1"}, {"name": "甲", "server": "b:2"}]}`, "名字不能重复", "重名"},
		{`{"profiles": [{"name": "甲"}]}`, "需要填写代理服务器地址或 PAC", "地址为空"},
		{`{"profiles": [{"name": "甲", "server": "a:99999"}]}`, "端口", "端口范围"},
		{`{"profiles": [{"name": "甲", "server": "ftp://a:1"}]}`, "不支持 ftp://", "协议"},
		{`{"profiles": [{"name": "甲", "server": "a:1", "color": "red"}]}`, "颜色", "颜色格式"},
		{`{"profiles": [{"name": "甲", "pac": "http://x/p.pac", "apply_to": ["git"]}]}`, "只填了 PAC", "git 不支持 PAC"},
		{`{"profiles": [{"name": "甲", "server": "socks5://a:1", "apply_to": ["npm"]}]}`, "npm 不支持 SOCKS5", "npm 不支持 SOCKS"},
		{`{"profiles": [{"name": "甲", "server": "a:1", "apply_to": ["docker"]}]}`, "docker", "未知生效范围"},
		{`{"auto_switch": {"rules": [{"match": "ssid", "value": "x", "action": "use", "profile": "不存在"}]}}`, "不存在", "规则引用不存在的配置"},
		{`{"auto_switch": {"rules": [{"match": "mac", "value": "x", "action": "off"}]}}`, "条件", "未知规则条件"},
		{`{"auto_switch": {"default_action": "use", "default_profile": "无"}}`, "默认配置", "默认配置不存在"},
	}
	for _, item := range cases {
		_, err := parseConfig(item.text)
		if err == nil {
			t.Errorf("%s：应当报错", item.explain)
			continue
		}
		if !strings.Contains(err.Error(), item.wanted) {
			t.Errorf("%s：错误信息应包含 %q，得到 %q", item.explain, item.wanted, err)
		}
	}
}

func TestConfigFileRoundTrip(t *testing.T) {
	directory := t.TempDir()
	path := filepath.Join(directory, configFileName)
	config, created, err := loadConfig(path)
	if err != nil || !created {
		t.Fatalf("首次加载应创建默认配置：%v %v", created, err)
	}
	data, _ := os.ReadFile(path)
	if !strings.HasPrefix(string(data), "\xef\xbb\xbf") {
		t.Error("默认配置文件应带 UTF-8 BOM")
	}
	config.Profiles = append(config.Profiles, Profile{Name: "公司", Server: "http=10.0.0.1:8080;https=10.0.0.1:8443", ApplyTo: []string{"system", "git"}})
	config.AutoSwitch = AutoSwitch{Enabled: true, Rules: []NetRule{{Match: "ssid", Value: "Office", Action: "use", Profile: "公司"}}, DefaultAction: "off"}
	normalizeConfig(config)
	if err := validateConfig(config); err != nil {
		t.Fatal(err)
	}
	if err := writeConfigFile(path, config); err != nil {
		t.Fatal(err)
	}
	loaded, created, err := loadConfig(path)
	if err != nil || created {
		t.Fatalf("重新加载失败：%v", err)
	}
	if len(loaded.Profiles) != 1 || loaded.Profiles[0].Id != config.Profiles[0].Id || loaded.Profiles[0].Server != config.Profiles[0].Server {
		t.Errorf("保存后再读取不一致：%+v", loaded.Profiles)
	}
	if !loaded.AutoSwitch.Enabled || loaded.AutoSwitch.Rules[0].Value != "Office" {
		t.Errorf("自动切换没有保存：%+v", loaded.AutoSwitch)
	}
	if content := string(mustReadFile(t, path)); strings.Contains(content, `"notify":`) {
		t.Errorf("不应再写出旧的 notify 字段：\n%s", content)
	}
}

func TestStateFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), stateFileName)
	if state := loadState(path); state.Enabled || state.Profile != "" {
		t.Errorf("状态文件不存在时应返回空状态：%+v", state)
	}
	original := &SystemProxyState{ProxyEnabled: true, Server: "1.2.3.4:80", AutoDetect: true}
	if err := saveState(path, &State{Profile: "甲", ProfileId: "p1", Enabled: true, Original: original}); err != nil {
		t.Fatal(err)
	}
	state := loadState(path)
	if !state.Enabled || state.ProfileId != "p1" || state.Original == nil || *state.Original != *original {
		t.Errorf("状态读写不一致：%+v", state)
	}
}

func TestProfileHelpers(t *testing.T) {
	profile := Profile{Name: "甲", Server: "socks5://127.0.0.1:1080", ApplyTo: []string{"system", "env"}}
	if profile.Kind() != "socks" || profile.Summary() != "socks5://127.0.0.1:1080" || profile.TargetsText() != "系统代理、环境变量" {
		t.Errorf("辅助函数结果不对：%s %s %s", profile.Kind(), profile.Summary(), profile.TargetsText())
	}
	pac := Profile{Pac: "http://x/p.pac", Server: "a:1"}
	if pac.Kind() != "pac" || pac.Summary() != "PAC + a:1" {
		t.Errorf("PAC 配置辅助函数结果不对：%s %s", pac.Kind(), pac.Summary())
	}
	if (&Profile{Server: "http=a:1;https=a:2"}).Kind() != "custom" {
		t.Error("按协议分别指定应识别为 custom")
	}
}

func mustReadFile(t *testing.T, path string) []byte {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return data
}
