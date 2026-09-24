package main

import (
	"crypto/rand"
	"crypto/sha1"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"unicode/utf8"
)

// 生效目标
const (
	targetSystem = "system"
	targetEnv    = "env"
	targetGit    = "git"
	targetNpm    = "npm"
)

var validTargets = []string{targetSystem, targetEnv, targetGit, targetNpm}

// 系统代理默认例外：本机、局域网网段、不含点的本地名称。
const defaultBypass = "localhost;127.*;10.*;172.16.*;172.17.*;172.18.*;172.19.*;172.20.*;172.21.*;172.22.*;172.23.*;172.24.*;172.25.*;172.26.*;172.27.*;172.28.*;172.29.*;172.30.*;172.31.*;192.168.*;<local>"

const defaultNoProxy = "localhost,127.0.0.1,::1"

const defaultTestUrl = "https://cp.cloudflare.com/generate_204"

const (
	defaultNotifySeconds = 3
	maxNotifySeconds     = 60
	maxProfileNameLength = 40
	maxProfileHotkeys    = 9
)

// 配置颜色：新建配置按顺序取色，托盘图标和设置页用它区分配置。
var profilePalette = []string{"#16a34a", "#2563eb", "#7c3aed", "#db2777", "#ea580c", "#0891b2"}

type Profile struct {
	Id      string   `json:"id"`
	Name    string   `json:"name"`
	Color   string   `json:"color"`
	Server  string   `json:"server"`
	Pac     string   `json:"pac"`
	Bypass  string   `json:"bypass"`
	NoProxy string   `json:"no_proxy"`
	ApplyTo []string `json:"apply_to"`
}

type NetRule struct {
	Match   string `json:"match"`
	Value   string `json:"value"`
	Action  string `json:"action"`
	Profile string `json:"profile"`
}

type AutoSwitch struct {
	Enabled        bool      `json:"enabled"`
	Rules          []NetRule `json:"rules"`
	DefaultAction  string    `json:"default_action"`
	DefaultProfile string    `json:"default_profile"`
}

type Config struct {
	Hotkey          string     `json:"hotkey"`
	ProfileHotkeys  string     `json:"profile_hotkeys"`
	NotifyLevel     string     `json:"notify_level"`
	NotifySeconds   int        `json:"notify_seconds"`
	StartupAction   string     `json:"startup_action"`
	OffMode         string     `json:"off_mode"`
	DisableOnExit   bool       `json:"disable_on_exit"`
	HealthCheck     string     `json:"health_check"`
	TrayClick       string     `json:"tray_click"`
	TrayDoubleClick string     `json:"tray_double_click"`
	Theme           string     `json:"theme"`
	SettingsWindow  string     `json:"settings_window"`
	TestUrl         string     `json:"test_url"`
	Editor          string     `json:"editor"`
	CheckUpdates    bool       `json:"check_updates"`
	AutoSwitch      AutoSwitch `json:"auto_switch"`
	Profiles        []Profile  `json:"profiles"`
	// 旧版配置的通知开关：读入时换算成 notify_level，保存时不再写出。
	Notify *bool `json:"notify,omitempty"`
}

func defaultConfig() *Config {
	return &Config{
		Hotkey:          "Ctrl+Alt+P",
		NotifySeconds:   defaultNotifySeconds,
		StartupAction:   "keep",
		OffMode:         "direct",
		HealthCheck:     "notify",
		TrayClick:       "toggle",
		TrayDoubleClick: "none",
		Theme:           "system",
		SettingsWindow:  "app",
		TestUrl:         defaultTestUrl,
		CheckUpdates:    true,
		AutoSwitch:      AutoSwitch{Rules: []NetRule{}, DefaultAction: "keep"},
		Profiles:        []Profile{},
	}
}

// defaultConfigText 是首次运行时写入的配置文件，字段说明都在注释里。
const defaultConfigText = `// ProxySwitch 配置文件。推荐在托盘菜单「设置...」里图形化修改；手改保存后几秒内自动生效。
// 允许 // 注释和末尾逗号。
{
  // 全局快捷键：开 / 关代理。Ctrl / Alt / Shift / Win 组合 + 字母、数字、F1~F24 等；留空不注册
  "hotkey": "Ctrl+Alt+P",

  // 按数字快速切换配置的修饰键，例如 "Ctrl+Alt" 表示 Ctrl+Alt+1 切到第 1 个配置；留空不启用
  "profile_hotkeys": "",

  // 通知：all 全部显示 / errors 只显示出错和警告 / none 不显示
  "notify_level": "all",
  // 通知几秒后自动关闭，0 表示由系统决定
  "notify_seconds": 3,

  // 启动时：keep 保持现状 / on 自动开启上次使用的配置 / off 自动关闭代理
  "startup_action": "keep",
  // 关闭代理时：direct 直接连接 / restore 恢复开启前的系统代理设置
  "off_mode": "direct",
  // 退出程序时是否顺便关闭代理
  "disable_on_exit": false,
  // 代理服务器连不上时：notify 提醒 / auto_off 自动关闭代理 / off 不检查
  "health_check": "notify",

  // 单击托盘图标：toggle 开关代理 / settings 打开设置 / menu 弹出菜单
  "tray_click": "toggle",
  // 双击托盘图标：none 不响应 / settings 打开设置 / toggle 开关代理（设置后单击会等双击判定，稍有延迟）
  "tray_double_click": "none",

  // 设置界面主题：system 跟随系统 / light 浅色 / dark 深色
  "theme": "system",
  // 设置界面打开方式：app 独立窗口（Edge 或 Chrome 应用模式）/ browser 默认浏览器
  "settings_window": "app",

  // 测速时经代理访问的地址
  "test_url": "https://cp.cloudflare.com/generate_204",
  // 「编辑配置文件」使用的编辑器，留空用记事本，也可以填 code 等命令
  "editor": "",
  // 自动检查更新：每天最多访问一次 GitHub，发现新版本时在托盘提示
  "check_updates": true,

  // 按所在网络自动切换
  "auto_switch": {
    "enabled": false,
    // match：ssid Wi-Fi 名称 / dns_suffix 网络的 DNS 后缀 / gateway 网关的 MAC 或 IP
    // action：use 使用 profile 指定的配置 / off 关闭代理；从上到下第一条匹配的规则生效
    "rules": [
      // { "match": "ssid", "value": "Office-WiFi", "action": "use", "profile": "公司代理" }
    ],
    // 没有规则匹配时：keep 保持不变 / off 关闭代理 / use 使用 default_profile
    "default_action": "keep",
    "default_profile": ""
  },

  // 代理配置，托盘菜单按这个顺序显示
  "profiles": [
    // {
    //   "name": "本机代理",
    //   "color": "#16a34a",
    //   // host:port；SOCKS5 写 socks5://host:port；按协议分别指定写 http=host:port;https=host:port;socks=host:port
    //   "server": "127.0.0.1:7890",
    //   // PAC 自动配置脚本地址，填了就以 PAC 模式开启系统代理
    //   "pac": "",
    //   // 系统代理例外，分号分隔，* 为通配符，<local> 表示不含点的本地名称
    //   "bypass": "localhost;127.*;192.168.*;<local>",
    //   // 环境变量和 npm 使用的 NO_PROXY，逗号分隔
    //   "no_proxy": "localhost,127.0.0.1,::1",
    //   // 生效范围：system 系统代理 / env 环境变量 / git / npm
    //   "apply_to": ["system"]
    // }
  ]
}
`

func (profile *Profile) Has(target string) bool {
	for _, item := range profile.ApplyTo {
		if item == target {
			return true
		}
	}
	return false
}

// Summary 是菜单、通知里展示的简短地址。
func (profile *Profile) Summary() string {
	switch {
	case profile.Pac != "" && profile.Server != "":
		return "PAC + " + profile.Server
	case profile.Pac != "":
		return "PAC"
	default:
		return profile.Server
	}
}

// Kind 是配置的类型：pac / socks / custom（按协议分别指定）/ http。
func (profile *Profile) Kind() string {
	switch {
	case profile.Pac != "":
		return "pac"
	case strings.Contains(profile.Server, "="):
		return "custom"
	case isSocksServer(profile.Server):
		return "socks"
	default:
		return "http"
	}
}

var targetLabels = map[string]string{
	targetSystem: "系统代理",
	targetEnv:    "环境变量",
	targetGit:    "git",
	targetNpm:    "npm",
}

func (profile *Profile) TargetsText() string {
	labels := make([]string, 0, len(profile.ApplyTo))
	for _, target := range profile.ApplyTo {
		labels = append(labels, targetLabels[target])
	}
	return strings.Join(labels, "、")
}

// FindProfile 按名字查找（不区分大小写），找不到返回 nil。
func (config *Config) FindProfile(name string) *Profile {
	name = strings.TrimSpace(name)
	for index := range config.Profiles {
		if strings.EqualFold(config.Profiles[index].Name, name) {
			return &config.Profiles[index]
		}
	}
	return nil
}

func (config *Config) FindProfileById(id string) *Profile {
	for index := range config.Profiles {
		if config.Profiles[index].Id == id {
			return &config.Profiles[index]
		}
	}
	return nil
}

// parseConfig 解析配置文本：补默认值、换算旧字段、规范化，再做完整校验。
func parseConfig(text string) (*Config, error) {
	config := defaultConfig()
	stripped := stripJsonc(strings.TrimPrefix(text, "\xef\xbb\xbf"))
	decoder := json.NewDecoder(strings.NewReader(stripped))
	if err := decoder.Decode(config); err != nil {
		return nil, describeJsonError(stripped, err)
	}
	if _, err := decoder.Token(); !errors.Is(err, io.EOF) {
		return nil, fmt.Errorf("第 %d 行附近格式错误：配置结束后还有多余的内容", lineOfOffset(stripped, decoder.InputOffset()))
	}
	normalizeConfig(config)
	if err := validateConfig(config); err != nil {
		return nil, err
	}
	return config, nil
}

func normalizeConfig(config *Config) {
	config.Hotkey = strings.TrimSpace(config.Hotkey)
	config.ProfileHotkeys = strings.TrimSpace(config.ProfileHotkeys)
	config.NotifyLevel = strings.ToLower(strings.TrimSpace(config.NotifyLevel))
	if config.NotifyLevel == "" {
		config.NotifyLevel = "all"
		if config.Notify != nil && !*config.Notify {
			config.NotifyLevel = "errors"
		}
	}
	config.Notify = nil
	config.StartupAction = lowerTrim(config.StartupAction, "keep")
	config.OffMode = lowerTrim(config.OffMode, "direct")
	config.HealthCheck = lowerTrim(config.HealthCheck, "notify")
	config.TrayClick = lowerTrim(config.TrayClick, "toggle")
	config.TrayDoubleClick = lowerTrim(config.TrayDoubleClick, "none")
	config.Theme = lowerTrim(config.Theme, "system")
	config.SettingsWindow = lowerTrim(config.SettingsWindow, "app")
	config.TestUrl = strings.TrimSpace(config.TestUrl)
	if config.TestUrl == "" {
		config.TestUrl = defaultTestUrl
	}
	config.Editor = strings.TrimSpace(config.Editor)
	if config.Profiles == nil {
		config.Profiles = []Profile{}
	}

	usedIds := map[string]bool{}
	for index := range config.Profiles {
		profile := &config.Profiles[index]
		profile.Name = strings.TrimSpace(profile.Name)
		profile.Server = strings.TrimSpace(profile.Server)
		profile.Pac = strings.TrimSpace(profile.Pac)
		profile.Bypass = strings.TrimSpace(profile.Bypass)
		profile.NoProxy = strings.TrimSpace(profile.NoProxy)
		profile.Color = strings.ToLower(strings.TrimSpace(profile.Color))
		if profile.Color == "" {
			profile.Color = profilePalette[index%len(profilePalette)]
		}
		profile.Id = strings.TrimSpace(profile.Id)
		if profile.Id == "" || usedIds[profile.Id] {
			profile.Id = stableProfileId(profile.Name, usedIds)
		}
		usedIds[profile.Id] = true
		targets := make([]string, 0, len(profile.ApplyTo))
		seenTargets := map[string]bool{}
		for _, target := range profile.ApplyTo {
			target = strings.ToLower(strings.TrimSpace(target))
			if target == "" || seenTargets[target] {
				continue
			}
			seenTargets[target] = true
			targets = append(targets, target)
		}
		if len(targets) == 0 {
			targets = []string{targetSystem}
		}
		profile.ApplyTo = targets
		if profile.Bypass == "" {
			profile.Bypass = defaultBypass
		}
		if profile.NoProxy == "" {
			profile.NoProxy = defaultNoProxy
		}
	}

	autoSwitch := &config.AutoSwitch
	if autoSwitch.Rules == nil {
		autoSwitch.Rules = []NetRule{}
	}
	for index := range autoSwitch.Rules {
		rule := &autoSwitch.Rules[index]
		rule.Match = strings.ToLower(strings.TrimSpace(rule.Match))
		rule.Value = strings.TrimSpace(rule.Value)
		rule.Action = lowerTrim(rule.Action, "use")
		rule.Profile = strings.TrimSpace(rule.Profile)
	}
	autoSwitch.DefaultAction = lowerTrim(autoSwitch.DefaultAction, "keep")
	autoSwitch.DefaultProfile = strings.TrimSpace(autoSwitch.DefaultProfile)
}

func lowerTrim(value, fallback string) string {
	value = strings.ToLower(strings.TrimSpace(value))
	if value == "" {
		return fallback
	}
	return value
}

// stableProfileId 为没有 id 的配置生成 id：优先由名字推导，手写的配置文件每次重新加载得到的 id 不变。
func stableProfileId(name string, used map[string]bool) string {
	digest := sha1.Sum([]byte(strings.ToLower(name)))
	id := "p" + hex.EncodeToString(digest[:4])
	if name != "" && !used[id] {
		return id
	}
	return newProfileId(used)
}

func newProfileId(used map[string]bool) string {
	for {
		buffer := make([]byte, 4)
		if _, err := rand.Read(buffer); err != nil {
			panic(err)
		}
		id := "p" + hex.EncodeToString(buffer)
		if !used[id] {
			return id
		}
	}
}

var hexColorPattern = regexp.MustCompile(`^#[0-9a-f]{6}$`)

func validateConfig(config *Config) error {
	enums := []struct {
		field   string
		value   string
		allowed []string
	}{
		{"notify_level", config.NotifyLevel, []string{"all", "errors", "none"}},
		{"startup_action", config.StartupAction, []string{"keep", "on", "off"}},
		{"off_mode", config.OffMode, []string{"direct", "restore"}},
		{"health_check", config.HealthCheck, []string{"notify", "auto_off", "off"}},
		{"tray_click", config.TrayClick, []string{"toggle", "settings", "menu"}},
		{"tray_double_click", config.TrayDoubleClick, []string{"none", "settings", "toggle"}},
		{"theme", config.Theme, []string{"system", "light", "dark"}},
		{"settings_window", config.SettingsWindow, []string{"app", "browser"}},
		{"auto_switch.default_action", config.AutoSwitch.DefaultAction, []string{"keep", "off", "use"}},
	}
	for _, enum := range enums {
		if !containsString(enum.allowed, enum.value) {
			return fmt.Errorf("%s 的取值 %q 不认识，可用：%s", enum.field, enum.value, strings.Join(enum.allowed, " / "))
		}
	}
	if config.NotifySeconds < 0 || config.NotifySeconds > maxNotifySeconds {
		return fmt.Errorf("notify_seconds 需要在 0~%d 之间", maxNotifySeconds)
	}
	if err := validateTestUrl(config.TestUrl); err != nil {
		return err
	}
	if config.Hotkey != "" {
		if _, err := parseHotkey(config.Hotkey); err != nil {
			return fmt.Errorf("快捷键 %q 无法识别：%v", config.Hotkey, err)
		}
	}
	if config.ProfileHotkeys != "" {
		if _, err := parseModifiers(config.ProfileHotkeys); err != nil {
			return fmt.Errorf("切换配置的快捷键 %q 无法识别：%v", config.ProfileHotkeys, err)
		}
	}

	seenNames := map[string]bool{}
	for index := range config.Profiles {
		profile := &config.Profiles[index]
		if err := validateProfile(profile); err != nil {
			return err
		}
		key := strings.ToLower(profile.Name)
		if seenNames[key] {
			return fmt.Errorf("有两个配置都叫「%s」，名字不能重复", profile.Name)
		}
		seenNames[key] = true
	}
	return validateAutoSwitch(config)
}

func validateProfile(profile *Profile) error {
	if profile.Name == "" {
		return errors.New("有一个配置没有填名字")
	}
	if utf8.RuneCountInString(profile.Name) > maxProfileNameLength {
		return fmt.Errorf("配置「%s」的名字太长，最多 %d 个字", profile.Name, maxProfileNameLength)
	}
	if !hexColorPattern.MatchString(profile.Color) {
		return fmt.Errorf("配置「%s」的颜色 %q 格式不对，应为 #rrggbb", profile.Name, profile.Color)
	}
	if profile.Server == "" && profile.Pac == "" {
		return fmt.Errorf("配置「%s」需要填写代理服务器地址或 PAC 脚本地址", profile.Name)
	}
	if profile.Server != "" {
		if err := validateServer(profile.Server); err != nil {
			return fmt.Errorf("配置「%s」的代理地址不对：%v", profile.Name, err)
		}
	}
	if profile.Pac != "" {
		if err := validatePacUrl(profile.Pac); err != nil {
			return fmt.Errorf("配置「%s」的 PAC 地址不对：%v", profile.Name, err)
		}
	}
	for _, target := range profile.ApplyTo {
		if !containsString(validTargets, target) {
			return fmt.Errorf("配置「%s」的生效范围 %q 不认识，可用：%s", profile.Name, target, strings.Join(validTargets, " / "))
		}
		if target != targetSystem && profile.Server == "" {
			return fmt.Errorf("配置「%s」勾选了%s，但只填了 PAC：%s 不支持 PAC，需要再填代理服务器地址", profile.Name, targetLabels[target], targetLabels[target])
		}
		if target == targetNpm && isSocksServer(profile.Server) {
			return fmt.Errorf("配置「%s」：npm 不支持 SOCKS5 代理，请取消勾选 npm 或改用 HTTP 代理", profile.Name)
		}
	}
	return nil
}

func validateAutoSwitch(config *Config) error {
	autoSwitch := &config.AutoSwitch
	for index, rule := range autoSwitch.Rules {
		position := index + 1
		if !containsString([]string{"ssid", "dns_suffix", "gateway"}, rule.Match) {
			return fmt.Errorf("第 %d 条自动切换规则的条件 %q 不认识，可用：ssid / dns_suffix / gateway", position, rule.Match)
		}
		if rule.Value == "" {
			return fmt.Errorf("第 %d 条自动切换规则没有填要匹配的值", position)
		}
		switch rule.Action {
		case "off":
		case "use":
			if config.FindProfile(rule.Profile) == nil {
				return fmt.Errorf("第 %d 条自动切换规则要使用的配置「%s」不存在", position, rule.Profile)
			}
		default:
			return fmt.Errorf("第 %d 条自动切换规则的动作 %q 不认识，可用：use / off", position, rule.Action)
		}
	}
	if autoSwitch.DefaultAction == "use" && config.FindProfile(autoSwitch.DefaultProfile) == nil {
		return fmt.Errorf("自动切换的默认配置「%s」不存在", autoSwitch.DefaultProfile)
	}
	return nil
}

func validateTestUrl(text string) error {
	parsed, err := url.Parse(text)
	if err != nil || (parsed.Scheme != "http" && parsed.Scheme != "https") || parsed.Host == "" {
		return fmt.Errorf("测速地址 %q 不对，应以 http:// 或 https:// 开头", text)
	}
	return nil
}

func validatePacUrl(text string) error {
	parsed, err := url.Parse(text)
	if err != nil {
		return err
	}
	switch parsed.Scheme {
	case "http", "https":
		if parsed.Host == "" {
			return errors.New("缺少主机名")
		}
		return nil
	case "file":
		return nil
	}
	return errors.New("应以 http:// 或 https:// 开头")
}

func containsString(list []string, value string) bool {
	for _, item := range list {
		if item == value {
			return true
		}
	}
	return false
}

// loadConfig 读取配置文件；文件不存在时写入默认配置并返回 created=true。
func loadConfig(path string) (config *Config, created bool, err error) {
	data, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			return nil, false, err
		}
		// 带 UTF-8 BOM 写入：旧版记事本对无 BOM 的中文会按 ANSI 打开显示乱码；parseConfig 会去掉 BOM。
		if err := os.WriteFile(path, []byte("\xEF\xBB\xBF"+defaultConfigText), 0o644); err != nil {
			return nil, false, err
		}
		data = []byte(defaultConfigText)
		created = true
	} else if err != nil {
		return nil, false, err
	}
	config, err = parseConfig(string(data))
	return config, created, err
}

func marshalConfigFile(config *Config) ([]byte, error) {
	data, err := json.MarshalIndent(config, "", "  ")
	if err != nil {
		return nil, err
	}
	header := "// ProxySwitch 配置文件。推荐在托盘菜单「设置...」里图形化修改；手改保存后几秒内自动生效。\n" +
		"// 各字段说明见 " + repositoryUrl + "#配置文件\n"
	return []byte("\xEF\xBB\xBF" + header + string(data) + "\n"), nil
}

// writeConfigFile 先写临时文件再改名，避免写到一半被读到。
func writeConfigFile(path string, config *Config) error {
	data, err := marshalConfigFile(config)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	temporary := path + ".tmp"
	if err := os.WriteFile(temporary, data, 0o644); err != nil {
		return err
	}
	if err := os.Rename(temporary, path); err != nil {
		_ = os.Remove(temporary)
		// 目标文件被编辑器以独占方式打开时 Windows 上改名会失败，退回直接覆盖。
		return os.WriteFile(path, data, 0o644)
	}
	return nil
}

// State 是运行状态，与用户手写的配置文件分开存放。
// Original 是开启代理前的系统代理设置，关闭时据此恢复；NextUpdateCheck 是下次自动检查更新的时间，
// UpdateNotified 是已经提示过的新版本号，同一个版本只提示一次；Version 是上次运行的版本，用来发现程序已经更新。
type State struct {
	Profile         string            `json:"profile"`
	ProfileId       string            `json:"profile_id,omitempty"`
	Enabled         bool              `json:"enabled"`
	Original        *SystemProxyState `json:"original,omitempty"`
	NextUpdateCheck string            `json:"next_update_check,omitempty"`
	UpdateNotified  string            `json:"update_notified,omitempty"`
	Version         string            `json:"version,omitempty"`
}

func loadState(path string) *State {
	state := &State{}
	data, err := os.ReadFile(path)
	if err != nil {
		return state
	}
	_ = json.Unmarshal(data, state)
	return state
}

func saveState(path string, state *State) error {
	data, err := json.MarshalIndent(state, "", "  ")
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	return os.WriteFile(path, data, 0o644)
}
