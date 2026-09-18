package main

import (
	"fmt"
	"os"
	"strings"
	"sync"
)

// devBackend 是开发 / 自动化测试用的假后端：不碰注册表，只在内存里模拟“开/关”状态，
// 配置读写走真实的配置文件解析逻辑。`go run . --dev-settings` 会用它在任何平台上预览设置页面。
type devBackend struct {
	mu        sync.Mutex
	paths     Paths
	cfg       *Config
	cfgErr    string
	on        bool
	selected  string
	autostart bool
	hotkeyErr string
}

func newDevBackend(paths Paths) *devBackend {
	b := &devBackend{paths: paths}
	cfg, _, err := loadConfig(paths.Config)
	if err != nil {
		b.cfgErr = err.Error()
	} else {
		b.cfg = cfg
		b.selected = cfg.Profiles[0].Name
	}
	return b
}

func (b *devBackend) SettingsState() SettingsState {
	b.mu.Lock()
	defer b.mu.Unlock()
	st := SettingsState{
		Version:     appVersion,
		Paths:       PathsInfo{Dir: b.paths.Dir, Config: b.paths.Config, Log: b.paths.Log, Portable: b.paths.Portable},
		Config:      b.cfg,
		ConfigError: b.cfgErr,
		Autostart:   b.autostart,
		HotkeyError: b.hotkeyErr,
		Defaults:    DefaultsInfo{Bypass: defaultBypass, NoProxy: defaultNoProxy, Hotkey: "Ctrl+Alt+P"},
		Targets:     targetInfos,
		Platform:    "dev",
	}
	if b.cfg != nil {
		st.HotkeyText = b.cfg.Hotkey
		st.Status = StatusInfo{On: b.on, Profile: b.selected}
	}
	return st
}

func (b *devBackend) SaveConfig(cfg *Config) error {
	b.mu.Lock()
	defer b.mu.Unlock()
	if err := writeConfigFile(b.paths.Config, cfg); err != nil {
		return err
	}
	// 重新从文件读一遍，确保写出去的内容能被解析
	loaded, _, err := loadConfig(b.paths.Config)
	if err != nil {
		b.cfgErr = err.Error()
		return err
	}
	b.cfg = loaded
	b.cfgErr = ""
	if b.cfg.FindProfile(b.selected) == nil {
		b.selected = b.cfg.Profiles[0].Name
	}
	return nil
}

func (b *devBackend) DoAction(action, name string) error {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.cfg == nil {
		return fmt.Errorf("配置文件有错误，无法操作")
	}
	switch action {
	case "toggle":
		b.on = !b.on
	case "on":
		b.on = true
	case "off":
		b.on = false
	case "use":
		p := b.cfg.FindProfile(name)
		if p == nil {
			return fmt.Errorf("没有名为 %q 的配置", name)
		}
		b.selected = p.Name
		b.on = true
	}
	return nil
}

func (b *devBackend) SetAutostart(enabled bool) error {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.autostart = enabled
	return nil
}

// runDevSettings 在任意平台上启动设置页面服务，用于开发和测试。
func runDevSettings(args []string) int {
	dir, err := os.MkdirTemp("", "proxyswitch-dev-")
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	for _, a := range args {
		if strings.HasPrefix(a, "--dir=") {
			dir = strings.TrimPrefix(a, "--dir=")
		}
	}
	paths := Paths{Dir: dir, Config: dir + "/" + configFileName, State: dir + "/" + stateFileName, Log: dir + "/" + logFileName}
	backend := newDevBackend(paths)
	srv := newSettingsServer(backend, func(f string, a ...any) { fmt.Fprintf(os.Stderr, f+"\n", a...) })
	url, err := srv.Start()
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	fmt.Println(url)
	select {}
}
