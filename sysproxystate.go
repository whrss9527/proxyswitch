package main

import "fmt"

// SystemProxyState 是 Windows 系统代理（WinINET）的一份完整快照。
// Server / Pac 在对应开关关闭时仍可能保留地址，是否生效只看 ProxyEnabled / PacEnabled。
type SystemProxyState struct {
	ProxyEnabled bool   `json:"proxy_enabled"`
	PacEnabled   bool   `json:"pac_enabled"`
	AutoDetect   bool   `json:"auto_detect"`
	Server       string `json:"server"`
	Bypass       string `json:"bypass"`
	Pac          string `json:"pac"`
}

func (state SystemProxyState) Active() bool {
	return state.ProxyEnabled || state.PacEnabled
}

// Describe 用于菜单、通知里描述当前生效的系统代理。
func (state SystemProxyState) Describe() string {
	switch {
	case state.PacEnabled && state.ProxyEnabled:
		return fmt.Sprintf("PAC %s + %s", state.Pac, state.Server)
	case state.PacEnabled:
		return "PAC " + state.Pac
	case state.ProxyEnabled:
		return state.Server
	}
	return "未开启"
}
