package main

import (
	"bytes"
	"encoding/json"
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
	if contains(config.Rules, "GEOIP,CN,DIRECT") {
		t.Error("地理数据还没下载时不能用大陆直连规则，否则内核会在启动时去下载")
	}

	settings.GeoReady = true
	rules := rulesOf(t, coreConfigText(settings, "127.0.0.1:9090", "secret"))
	if !contains(rules, "GEOSITE,cn,DIRECT") || !contains(rules, "GEOIP,CN,DIRECT") || rules[len(rules)-1] != "MATCH,ProxySwitch" {
		t.Errorf("大陆直连模式应让国内地址直连：%v", rules)
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
