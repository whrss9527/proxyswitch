package main

import (
	"fmt"
	"sort"
	"strings"
)

// NetworkAdapter 是一块已连接、有默认网关的网卡。
type NetworkAdapter struct {
	Name       string `json:"name"`
	DnsSuffix  string `json:"dns_suffix"`
	Gateway    string `json:"gateway"`
	GatewayMac string `json:"gateway_mac"`
	Wireless   bool   `json:"wireless"`
}

// NetworkInfo 是当前所在网络的特征，用于按网络自动切换。
type NetworkInfo struct {
	Ssids     []string         `json:"ssids"`
	SsidError string           `json:"ssid_error,omitempty"`
	Adapters  []NetworkAdapter `json:"adapters"`
}

// Signature 在网络没变时保持不变，变化时（换 Wi-Fi、插拔网线、连 VPN）随之改变。
func (info NetworkInfo) Signature() string {
	var parts []string
	for _, ssid := range info.Ssids {
		parts = append(parts, "ssid:"+ssid)
	}
	for _, adapter := range info.Adapters {
		parts = append(parts, fmt.Sprintf("net:%s|%s|%s", strings.ToLower(adapter.DnsSuffix), adapter.Gateway, adapter.GatewayMac))
	}
	sort.Strings(parts)
	return strings.Join(parts, ";")
}

// Online 表示至少有一个可用网络；断网期间不做自动切换。
func (info NetworkInfo) Online() bool {
	return len(info.Ssids) > 0 || len(info.Adapters) > 0
}

// Describe 是当前网络的简短描述，用于通知和设置页。
func (info NetworkInfo) Describe() string {
	if len(info.Ssids) > 0 {
		return "Wi-Fi「" + strings.Join(info.Ssids, "、") + "」"
	}
	for _, adapter := range info.Adapters {
		if adapter.DnsSuffix != "" {
			return "网络「" + adapter.DnsSuffix + "」"
		}
	}
	if len(info.Adapters) > 0 {
		return "网络「" + info.Adapters[0].Name + "」"
	}
	return "未连接网络"
}

func ruleMatches(rule NetRule, info NetworkInfo) bool {
	value := strings.TrimSpace(rule.Value)
	if value == "" {
		return false
	}
	switch rule.Match {
	case "ssid":
		for _, ssid := range info.Ssids {
			if strings.EqualFold(strings.TrimSpace(ssid), value) {
				return true
			}
		}
	case "dns_suffix":
		wanted := strings.ToLower(strings.Trim(value, "."))
		for _, adapter := range info.Adapters {
			suffix := strings.ToLower(strings.Trim(adapter.DnsSuffix, "."))
			if suffix != "" && (suffix == wanted || strings.HasSuffix(suffix, "."+wanted)) {
				return true
			}
		}
	case "gateway":
		wantedMac := normalizeMac(value)
		for _, adapter := range info.Adapters {
			if adapter.Gateway == value || (wantedMac != "" && normalizeMac(adapter.GatewayMac) == wantedMac) {
				return true
			}
		}
	}
	return false
}

// normalizeMac 把 aa:bb:cc:dd:ee:ff、AA-BB-CC-DD-EE-FF、aabb.ccdd.eeff 统一成 12 位小写十六进制；不是 MAC 时返回空串。
func normalizeMac(text string) string {
	var digits strings.Builder
	for _, char := range strings.ToLower(text) {
		switch {
		case (char >= '0' && char <= '9') || (char >= 'a' && char <= 'f'):
			digits.WriteRune(char)
		case char == ':' || char == '-' || char == '.':
		default:
			return ""
		}
	}
	if digits.Len() != 12 {
		return ""
	}
	return digits.String()
}

// SwitchDecision 是按网络规则得出的动作。
type SwitchDecision struct {
	Action    string
	Profile   string
	RuleIndex int
	Reason    string
}

// decideNetworkAction 从上到下找第一条匹配的规则；没有匹配时用默认动作。RuleIndex 为 -1 表示走的默认动作。
func decideNetworkAction(autoSwitch AutoSwitch, info NetworkInfo) SwitchDecision {
	for index, rule := range autoSwitch.Rules {
		if ruleMatches(rule, info) {
			return SwitchDecision{Action: rule.Action, Profile: rule.Profile, RuleIndex: index, Reason: describeRule(rule)}
		}
	}
	return SwitchDecision{
		Action:    autoSwitch.DefaultAction,
		Profile:   autoSwitch.DefaultProfile,
		RuleIndex: -1,
		Reason:    "其他网络",
	}
}

func describeRule(rule NetRule) string {
	switch rule.Match {
	case "ssid":
		return "Wi-Fi「" + rule.Value + "」"
	case "dns_suffix":
		return "DNS 后缀「" + rule.Value + "」"
	case "gateway":
		return "网关「" + rule.Value + "」"
	}
	return rule.Value
}
