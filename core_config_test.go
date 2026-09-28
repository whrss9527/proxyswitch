package main

import (
	"bytes"
	"encoding/json"
	"reflect"
	"strings"
	"testing"
)

func TestCoreConfigText(t *testing.T) {
	settings := CoreSettings{
		Port:    17890,
		TestUrl: "https://example.com/generate_204",
		Mode:    "rule",
		Subscriptions: []CoreSubscription{
			{Id: "pa1", Node: "香港 01"},
			{Id: "pb2"},
		},
	}
	text := coreConfigText(settings, "127.0.0.1:9090", "secret")
	var config coreConfigFile
	if err := json.Unmarshal(text, &config); err != nil {
		t.Fatalf("生成的配置不是合法的 JSON（也就不是合法的 YAML）：%v", err)
	}
	if config.MixedPort != 17890 || config.BindAddress != "127.0.0.1" || config.AllowLan || config.ExternalController != "127.0.0.1:9090" || config.Secret != "secret" {
		t.Errorf("端口和 API 设置不对：%+v", config)
	}
	provider, ok := config.ProxyProviders["pa1-nodes"]
	if !ok || provider.Type != "file" || provider.Path != "subscriptions/pa1.yaml" || provider.HealthCheck.Url != settings.TestUrl {
		t.Errorf("订阅应是读本地文件的 provider：%+v", config.ProxyProviders)
	}
	groups := map[string]coreGroup{}
	for _, group := range config.ProxyGroups {
		groups[group.Name] = group
	}
	if group := groups["pa1"]; group.Type != "select" || strings.Join(group.Proxies, ",") != "pa1-auto" || strings.Join(group.Use, ",") != "pa1-nodes" {
		t.Errorf("订阅的组应以自动选择为第一项：%+v", group)
	}
	if group := groups["pb2-auto"]; group.Type != "url-test" || group.Url != settings.TestUrl {
		t.Errorf("自动选择应按延迟选节点：%+v", group)
	}
	if group := groups[coreTopGroup]; strings.Join(group.Proxies, ",") != "pa1,pb2" {
		t.Errorf("顶层组应包含所有订阅：%+v", group)
	}
	last := config.Rules[len(config.Rules)-1]
	if last != "MATCH,ProxySwitch" || !contains(config.Rules, "IP-CIDR,192.168.0.0/16,DIRECT,no-resolve") {
		t.Errorf("规则应让局域网直连、其余交给顶层组：%v", config.Rules)
	}
	// 分流规则（引擎按规则集拼好的，最后一条是 MATCH）：地理数据还没下载时先跳过 GEOIP 和 GEOSITE，否则内核会拒绝配置。
	settings.Rules = append(builtinRules(builtinChinaDirect, "DIRECT"), "MATCH,DIRECT")
	if rules := rulesOf(t, coreConfigText(settings, "127.0.0.1:9090", "secret")); contains(rules, "GEOIP,CN,DIRECT") || rules[len(rules)-1] != "MATCH,DIRECT" {
		t.Errorf("地理数据还没下载时不能用大陆直连规则，否则内核会在启动时去下载：%v", rules)
	}

	settings.GeoReady = true
	rules := rulesOf(t, coreConfigText(settings, "127.0.0.1:9090", "secret"))
	if !contains(rules, "GEOSITE,cn,DIRECT") || !contains(rules, "GEOIP,CN,DIRECT") || rules[len(rules)-1] != "MATCH,DIRECT" {
		t.Errorf("大陆直连应让国内地址直连，其余按规则集的最后一条：%v", rules)
	}
	settings.Mode = "global"
	if rules := rulesOf(t, coreConfigText(settings, "127.0.0.1:9090", "secret")); contains(rules, "GEOIP,CN,DIRECT") {
		t.Errorf("全局模式不应有大陆直连规则：%v", rules)
	}
	if !bytes.Equal(coreConfigText(settings, "a", "b"), coreConfigText(settings, "a", "b")) {
		t.Error("相同的设置应生成相同的配置，才能判断是否需要重新加载")
	}
}

func rulesOf(t *testing.T, text []byte) []string {
	t.Helper()
	var config coreConfigFile
	if err := json.Unmarshal(text, &config); err != nil {
		t.Fatal(err)
	}
	return config.Rules
}

func contains(list []string, value string) bool {
	return containsString(list, value)
}

// 局域网共享：共享入口监听所有网卡，只放行允许的来源，按同名的 sub-rules 分流。
func TestCoreConfigShare(t *testing.T) {
	parse := func(settings CoreSettings) coreConfigFile {
		t.Helper()
		var config coreConfigFile
		if err := json.Unmarshal(coreConfigText(settings, "127.0.0.1:9090", "secret"), &config); err != nil {
			t.Fatal(err)
		}
		return config
	}
	allowed := []string{"127.0.0.0/8", "::1/128", "192.168.1.20/32"}
	settings := CoreSettings{Port: 17890, Mode: "rule", Share: &CoreShare{Port: 17892, Allowed: allowed, Upstream: ShareUpstream{Kind: shareUpstreamDirect}}}

	// 没有订阅时只为共享运行：本机的代理端口不监听，没有节点。
	config := parse(settings)
	if config.MixedPort != 0 || len(config.ProxyGroups) != 0 || !reflect.DeepEqual(config.Rules, []string{"MATCH,DIRECT"}) {
		t.Errorf("只为共享运行时本机不经内核：%+v", config)
	}
	if want := []coreListener{{Name: coreShareListener, Type: "mixed", Listen: "0.0.0.0", Port: 17892, Rule: coreShareListener}}; !reflect.DeepEqual(config.Listeners, want) || !reflect.DeepEqual(config.LanAllowedIps, allowed) {
		t.Errorf("共享入口监听所有网卡、只放行允许的来源：%+v %v", config.Listeners, config.LanAllowedIps)
	}
	if !reflect.DeepEqual(config.SubRules[coreShareListener], []string{"MATCH,DIRECT"}) || !config.Sniffer.Enable {
		t.Errorf("本机没开代理时共享的设备直连，并嗅探域名：%+v", config.SubRules)
	}

	// 本机用其他代理：局域网直连，其余转发给它；用户名、密码和 HTTPS 代理都带上。
	settings.Share.Upstream = ShareUpstream{Kind: shareUpstreamProxy, Proxy: "socks5://user:p%40ss@10.0.0.2:1080"}
	config = parse(settings)
	if want := []coreProxy{{Name: coreUpstreamProxy, Type: "socks5", Server: "10.0.0.2", Port: 1080, Username: "user", Password: "p@ss"}}; !reflect.DeepEqual(config.Proxies, want) {
		t.Errorf("上游代理不对：%+v", config.Proxies)
	}
	rules := config.SubRules[coreShareListener]
	if rules[len(rules)-1] != "MATCH,"+coreUpstreamProxy || !contains(rules, "IP-CIDR,192.168.0.0/16,DIRECT,no-resolve") {
		t.Errorf("本机用其他代理时局域网直连、其余转发给它：%v", rules)
	}
	settings.Share.Upstream.Proxy = "https://proxy.example.com:8443"
	if proxies := parse(settings).Proxies; len(proxies) != 1 || proxies[0].Type != "http" || !proxies[0].Tls || proxies[0].Server != "proxy.example.com" {
		t.Errorf("HTTPS 代理：%+v", proxies)
	}
	settings.Share.Upstream.Proxy = "ftp://10.0.0.2"
	if config := parse(settings); len(config.Proxies) != 0 || !reflect.DeepEqual(config.SubRules[coreShareListener], []string{"MATCH,DIRECT"}) {
		t.Errorf("认不出的上游改为直连：%+v", config)
	}

	// 本机用订阅：共享的设备和本机完全一样。
	settings.Subscriptions = []CoreSubscription{{Id: "p1"}}
	settings.Active = "p1"
	settings.CustomRules = []string{"DOMAIN-SUFFIX,youtube.com,ProxySwitch"}
	settings.Share.Upstream = ShareUpstream{Kind: shareUpstreamCore}
	config = parse(settings)
	if config.MixedPort != 17890 || !reflect.DeepEqual(config.SubRules[coreShareListener], config.Rules) || !contains(config.Rules, "DOMAIN-SUFFIX,youtube.com,ProxySwitch") {
		t.Errorf("本机用订阅时共享的设备用同样的规则：%+v", config.SubRules)
	}

	settings.Share = nil
	text := string(coreConfigText(settings, "127.0.0.1:9090", "secret"))
	if strings.Contains(text, "listeners") || strings.Contains(text, "sub-rules") || strings.Contains(text, "lan-allowed-ips") {
		t.Errorf("没开共享时不应有共享入口：\n%s", text)
	}
}
