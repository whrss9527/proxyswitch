package main

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// 生效目标
const (
	targetSystem = "system" // Windows 系统代理
	targetEnv    = "env"    // 用户环境变量 HTTP_PROXY / HTTPS_PROXY / NO_PROXY
	targetGit    = "git"    // git config --global http.proxy / https.proxy
	targetNpm    = "npm"    // ~/.npmrc proxy / https-proxy（pnpm 同样读取）
)

var validTargets = []string{targetSystem, targetEnv, targetGit, targetNpm}

// 默认的系统代理例外列表（与 Clash for Windows 默认值一致）
const defaultBypass = "localhost;127.*;10.*;172.16.*;172.17.*;172.18.*;172.19.*;172.20.*;172.21.*;172.22.*;172.23.*;172.24.*;172.25.*;172.26.*;172.27.*;172.28.*;172.29.*;172.30.*;172.31.*;192.168.*;<local>"

const defaultNoProxy = "localhost,127.0.0.1,::1"

// Profile 是一套代理配置。
type Profile struct {
	Name    string   `json:"name"`
	Server  string   `json:"server"`   // host:port 或 http=..;https=..;socks=..
	Bypass  string   `json:"bypass"`   // 系统代理例外列表，分号分隔
	PAC     string   `json:"pac"`      // 自动配置脚本地址（只对 system 生效）
	NoProxy string   `json:"no_proxy"` // env / npm 的 NO_PROXY，逗号分隔
	ApplyTo []string `json:"apply_to"` // system / env / git / npm
}

// Has 判断该配置是否包含某个生效目标。
func (p *Profile) Has(target string) bool {
	for _, t := range p.ApplyTo {
		if t == target {
			return true
		}
	}
	return false
}

// Summary 返回菜单里展示用的简短描述。
func (p *Profile) Summary() string {
	switch {
	case p.PAC != "" && p.Server != "":
		return "PAC + " + p.Server
	case p.PAC != "":
		return "PAC"
	default:
		return p.Server
	}
}

// TargetsText 返回生效目标的中文描述，如“系统代理 + 环境变量”。
func (p *Profile) TargetsText() string {
	names := map[string]string{
		targetSystem: "系统代理",
		targetEnv:    "环境变量",
		targetGit:    "git",
		targetNpm:    "npm",
	}
	var parts []string
	for _, t := range p.ApplyTo {
		parts = append(parts, names[t])
	}
	return strings.Join(parts, " + ")
}

// Config 是配置文件的结构。
type Config struct {
	Hotkey        string    `json:"hotkey"`
	Notify        bool      `json:"notify"`
	NotifySeconds int       `json:"notify_seconds"` // 通知几秒后自动关闭；0 = 跟随系统
	DisableOnExit bool      `json:"disable_on_exit"`
	Editor        string    `json:"editor"`
	Profiles      []Profile `json:"profiles"`
}

const (
	defaultNotifySeconds = 3
	maxNotifySeconds     = 60
)

func defaultConfig() *Config {
	return &Config{
		Hotkey:        "Ctrl+Alt+P",
		Notify:        true,
		NotifySeconds: defaultNotifySeconds,
	}
}

// defaultConfigText 是首次运行时生成的配置文件内容（JSONC，允许注释）。
const defaultConfigText = `// ProxySwitch 配置文件（JSON，允许 // 注释和末尾逗号）
// 修改保存后，在托盘菜单里点「重新加载配置」即可生效。
{
  // 全局快捷键：一键开/关当前代理。支持 Ctrl / Alt / Shift / Win 组合，
  // 主键可以是字母、数字、F1~F12、Space、Enter 等。留空表示不注册快捷键。
  "hotkey": "Ctrl+Alt+P",

  // 切换后是否弹出系统通知
  "notify": true,

  // 通知几秒后自动关闭（0 表示跟随系统默认，不主动关闭）
  "notify_seconds": 3,

  // 退出程序时是否顺便关闭代理
  "disable_on_exit": false,

  // 「编辑配置」使用的编辑器，留空用记事本；也可以填 "code"（VS Code）等
  "editor": "",

  // 代理配置列表，托盘菜单按此顺序展示，点击即切换到该配置并开启。
  "profiles": [
    {
      "name": "本地 Clash",
      // 代理服务器，形式为 host:port；也可以按协议分别指定：
      // "server": "http=127.0.0.1:7890;https=127.0.0.1:7890;socks=127.0.0.1:7891"
      "server": "127.0.0.1:7890",
      // 不走代理的地址（系统代理的“例外”列表，分号分隔），留空用默认值
      "bypass": "` + defaultBypass + `",
      // 生效范围：system = 系统代理；env = 用户环境变量 HTTP_PROXY/HTTPS_PROXY/NO_PROXY；
      //          git = git 全局代理；npm = ~/.npmrc（pnpm 也读它）
      "apply_to": ["system"]
    },
    {
      "name": "公司 PAC（示例）",
      // 自动配置脚本地址（下面这个是示例，请改成你自己的）。填了 pac 后系统代理走 PAC 模式；
      // env / git / npm 不支持 PAC，若同时勾选它们需要再填 server。
      "pac": "http://proxy.example.com/proxy.pac",
      "apply_to": ["system"]
    },
    {
      "name": "抓包 8080",
      "server": "127.0.0.1:8080",
      "bypass": "<local>",
      "apply_to": ["system"]
    }
  ]
}
`

// stripJSONC 去掉 // 与 /* */ 注释，以及对象/数组末尾多余的逗号。
func stripJSONC(src string) string {
	var out strings.Builder
	out.Grow(len(src))
	inStr := false
	esc := false
	i := 0
	for i < len(src) {
		c := src[i]
		if inStr {
			out.WriteByte(c)
			switch {
			case esc:
				esc = false
			case c == '\\':
				esc = true
			case c == '"':
				inStr = false
			}
			i++
			continue
		}
		switch {
		case c == '"':
			inStr = true
			out.WriteByte(c)
			i++
		case c == '/' && i+1 < len(src) && src[i+1] == '/':
			for i < len(src) && src[i] != '\n' {
				i++
			}
		case c == '/' && i+1 < len(src) && src[i+1] == '*':
			i += 2
			for i+1 < len(src) && !(src[i] == '*' && src[i+1] == '/') {
				i++
			}
			i += 2
		case c == ',':
			// 逗号后面（跳过空白和注释）如果是 } 或 ]，就丢掉这个逗号
			j := i + 1
			for j < len(src) {
				if src[j] == ' ' || src[j] == '\t' || src[j] == '\r' || src[j] == '\n' {
					j++
					continue
				}
				if src[j] == '/' && j+1 < len(src) && src[j+1] == '/' {
					for j < len(src) && src[j] != '\n' {
						j++
					}
					continue
				}
				if src[j] == '/' && j+1 < len(src) && src[j+1] == '*' {
					j += 2
					for j+1 < len(src) && !(src[j] == '*' && src[j+1] == '/') {
						j++
					}
					j += 2
					continue
				}
				break
			}
			if j < len(src) && (src[j] == '}' || src[j] == ']') {
				i++
				continue
			}
			out.WriteByte(c)
			i++
		default:
			out.WriteByte(c)
			i++
		}
	}
	return out.String()
}

// parseConfig 解析配置文本并补全默认值、做校验。
func parseConfig(text string) (*Config, error) {
	cfg := defaultConfig()
	text = strings.TrimPrefix(text, "\ufeff") // 记事本可能写入 BOM
	dec := json.NewDecoder(strings.NewReader(stripJSONC(text)))
	if err := dec.Decode(cfg); err != nil {
		return nil, fmt.Errorf("配置文件格式错误：%v", err)
	}
	cfg.Hotkey = strings.TrimSpace(cfg.Hotkey)
	if cfg.NotifySeconds < 0 || cfg.NotifySeconds > maxNotifySeconds {
		return nil, fmt.Errorf("notify_seconds 需要在 0~%d 之间", maxNotifySeconds)
	}
	if len(cfg.Profiles) == 0 {
		return nil, fmt.Errorf("配置里没有任何 profiles")
	}
	seen := map[string]bool{}
	for i := range cfg.Profiles {
		p := &cfg.Profiles[i]
		p.Name = strings.TrimSpace(p.Name)
		p.Server = strings.TrimSpace(p.Server)
		p.PAC = strings.TrimSpace(p.PAC)
		p.Bypass = strings.TrimSpace(p.Bypass)
		p.NoProxy = strings.TrimSpace(p.NoProxy)
		if p.Name == "" {
			return nil, fmt.Errorf("第 %d 个 profile 缺少 name", i+1)
		}
		if seen[strings.ToLower(p.Name)] {
			return nil, fmt.Errorf("profile 名字重复：%s", p.Name)
		}
		seen[strings.ToLower(p.Name)] = true
		if p.Server == "" && p.PAC == "" {
			return nil, fmt.Errorf("profile %q 需要填写 server 或 pac", p.Name)
		}
		if len(p.ApplyTo) == 0 {
			p.ApplyTo = []string{targetSystem}
		}
		for j, t := range p.ApplyTo {
			t = strings.ToLower(strings.TrimSpace(t))
			p.ApplyTo[j] = t
			ok := false
			for _, v := range validTargets {
				if v == t {
					ok = true
				}
			}
			if !ok {
				return nil, fmt.Errorf("profile %q 的 apply_to 含有未知目标 %q（可用：%s）",
					p.Name, t, strings.Join(validTargets, " / "))
			}
			if t != targetSystem && p.Server == "" {
				return nil, fmt.Errorf("profile %q 的 apply_to 含有 %s，但没有填 server（%s 不支持 PAC）", p.Name, t, t)
			}
		}
		if p.Bypass == "" {
			p.Bypass = defaultBypass
		}
		if p.NoProxy == "" {
			p.NoProxy = defaultNoProxy
		}
	}
	if cfg.Hotkey != "" {
		if _, err := parseHotkey(cfg.Hotkey); err != nil {
			return nil, fmt.Errorf("hotkey 无法识别：%v", err)
		}
	}
	return cfg, nil
}

// loadConfig 读取配置文件；文件不存在时写入默认配置并返回 created=true。
func loadConfig(path string) (cfg *Config, created bool, err error) {
	data, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			return nil, false, err
		}
		// 带 UTF-8 BOM 写入：老版本记事本没有 BOM 会把中文注释当成 ANSI 显示成乱码，
		// 而且保存时可能损坏；parseConfig 会自动去掉 BOM。
		if err := os.WriteFile(path, []byte("\xEF\xBB\xBF"+defaultConfigText), 0o644); err != nil {
			return nil, false, err
		}
		data = []byte(defaultConfigText)
		created = true
	} else if err != nil {
		return nil, false, err
	}
	cfg, err = parseConfig(string(data))
	if err != nil {
		return nil, created, err
	}
	return cfg, created, nil
}

// FindProfile 按名字查找（不区分大小写）。
func (c *Config) FindProfile(name string) *Profile {
	for i := range c.Profiles {
		if strings.EqualFold(c.Profiles[i].Name, strings.TrimSpace(name)) {
			return &c.Profiles[i]
		}
	}
	return nil
}

// State 记录运行状态（与用户手写的配置文件分开存放，避免覆盖注释）。
type State struct {
	Profile string `json:"profile"` // 最近选择的配置名
	Enabled bool   `json:"enabled"` // 对不含 system 目标的配置，记录是否处于开启状态
}

func loadState(path string) *State {
	st := &State{}
	data, err := os.ReadFile(path)
	if err != nil {
		return st
	}
	_ = json.Unmarshal(data, st)
	return st
}

func saveState(path string, st *State) error {
	data, err := json.MarshalIndent(st, "", "  ")
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	return os.WriteFile(path, data, 0o644)
}
