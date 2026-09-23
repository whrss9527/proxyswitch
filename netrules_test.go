package main

import "testing"

var officeNetwork = NetworkInfo{
	Ssids: []string{"Office-5G"},
	Adapters: []NetworkAdapter{
		{Name: "WLAN", DnsSuffix: "corp.example.com.", Gateway: "10.1.0.1", GatewayMac: "00-1A-2B-3C-4D-5E", Wireless: true},
	},
}

func TestRuleMatches(t *testing.T) {
	cases := []struct {
		rule   NetRule
		wanted bool
	}{
		{NetRule{Match: "ssid", Value: "office-5g"}, true},
		{NetRule{Match: "ssid", Value: "Office"}, false},
		{NetRule{Match: "dns_suffix", Value: "corp.example.com"}, true},
		{NetRule{Match: "dns_suffix", Value: ".example.com"}, true},
		{NetRule{Match: "dns_suffix", Value: "ample.com"}, false},
		{NetRule{Match: "gateway", Value: "10.1.0.1"}, true},
		{NetRule{Match: "gateway", Value: "00:1a:2b:3c:4d:5e"}, true},
		{NetRule{Match: "gateway", Value: "001a.2b3c.4d5e"}, true},
		{NetRule{Match: "gateway", Value: "00:1a:2b:3c:4d:5f"}, false},
		{NetRule{Match: "gateway", Value: "10.1.0.2"}, false},
		{NetRule{Match: "ssid", Value: " "}, false},
	}
	for _, item := range cases {
		if got := ruleMatches(item.rule, officeNetwork); got != item.wanted {
			t.Errorf("%+v 匹配结果为 %v，应为 %v", item.rule, got, item.wanted)
		}
	}
}

func TestDecideNetworkAction(t *testing.T) {
	autoSwitch := AutoSwitch{
		Rules: []NetRule{
			{Match: "ssid", Value: "Home", Action: "off"},
			{Match: "dns_suffix", Value: "corp.example.com", Action: "use", Profile: "公司"},
			{Match: "ssid", Value: "Office-5G", Action: "use", Profile: "不会用到"},
		},
		DefaultAction:  "use",
		DefaultProfile: "默认",
	}
	decision := decideNetworkAction(autoSwitch, officeNetwork)
	if decision.RuleIndex != 1 || decision.Action != "use" || decision.Profile != "公司" {
		t.Errorf("应命中第 2 条规则：%+v", decision)
	}
	decision = decideNetworkAction(autoSwitch, NetworkInfo{Ssids: []string{"Cafe"}})
	if decision.RuleIndex != -1 || decision.Profile != "默认" {
		t.Errorf("没有命中时应走默认动作：%+v", decision)
	}
}

func TestNetworkInfoHelpers(t *testing.T) {
	reordered := NetworkInfo{
		Ssids: []string{"Office-5G"},
		Adapters: []NetworkAdapter{
			{Name: "以太网", Gateway: "192.168.0.1"},
			officeNetwork.Adapters[0],
		},
	}
	withEthernet := NetworkInfo{
		Ssids:    officeNetwork.Ssids,
		Adapters: append([]NetworkAdapter{officeNetwork.Adapters[0]}, NetworkAdapter{Name: "以太网", Gateway: "192.168.0.1"}),
	}
	if reordered.Signature() != withEthernet.Signature() {
		t.Error("网卡顺序不同不应改变网络特征")
	}
	if officeNetwork.Signature() == withEthernet.Signature() {
		t.Error("多了一块网卡应改变网络特征")
	}
	if (NetworkInfo{}).Online() || !officeNetwork.Online() {
		t.Error("Online 结果不对")
	}
	if officeNetwork.Describe() != "Wi-Fi「Office-5G」" {
		t.Errorf("描述不对：%s", officeNetwork.Describe())
	}
	wired := NetworkInfo{Adapters: []NetworkAdapter{{Name: "以太网", DnsSuffix: "corp.local"}}}
	if wired.Describe() != "网络「corp.local」" || (NetworkInfo{}).Describe() != "未连接网络" {
		t.Errorf("有线网络描述不对：%s", wired.Describe())
	}
	if normalizeMac("AA-BB-CC-DD-EE-FF") != "aabbccddeeff" || normalizeMac("192.168.1.1") != "" || normalizeMac("zz:bb:cc:dd:ee:ff") != "" {
		t.Error("normalizeMac 结果不对")
	}
}
