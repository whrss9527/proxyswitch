package main

import (
	"reflect"
	"strings"
	"testing"
)

func TestNormalizeRuleTarget(t *testing.T) {
	cases := map[string]string{
		"https://www.YouTube.com/watch?v=1": "www.youtube.com",
		"*.youtube.com":                     "youtube.com",
		".youtube.com.":                     "youtube.com",
		"youtube.com:443":                   "youtube.com",
		" 8.8.8.8 ":                         "8.8.8.8",
		"10.1.2.3/8":                        "10.0.0.0/8",
		"[2001:db8::1]:443":                 "2001:db8::1",
		"2001:DB8::/32":                     "2001:db8::/32",
		"socks5://1.2.3.4:1080":             "1.2.3.4",
	}
	for input, want := range cases {
		if got := normalizeRuleTarget(input); got != want {
			t.Errorf("%q 应整理成 %q，实际 %q", input, want, got)
		}
	}
}

func TestCustomRuleLines(t *testing.T) {
	rules := []CustomRule{
		{Value: "youtube.com", Policy: rulePolicyProxy},
		{Value: "intranet.example.com", Policy: rulePolicyDirect},
		{Value: "ads.example.com", Policy: rulePolicyReject, Disabled: true},
		{Value: "8.8.8.8", Policy: rulePolicyProxy},
		{Value: "2001:db8::/32", Policy: rulePolicyReject},
		{Value: "不是域名", Policy: rulePolicyProxy},
	}
	want := []string{
		"DOMAIN-SUFFIX,youtube.com,ProxySwitch",
		"DOMAIN-SUFFIX,intranet.example.com,DIRECT",
		"IP-CIDR,8.8.8.8/32,ProxySwitch,no-resolve",
		"IP-CIDR6,2001:db8::/32,REJECT,no-resolve",
	}
	if got := customRuleLines(rules); !reflect.DeepEqual(got, want) {
		t.Errorf("自定义规则在内核里的写法不对：\n%s", strings.Join(got, "\n"))
	}
}

func TestCustomRulesConfig(t *testing.T) {
	config, err := parseConfig(`{"custom_rules": [
		{"value": "https://www.Google.com/search", "policy": "Direct"},
		{"value": "192.168.1.9"}
	], "profiles": []}`)
	if err != nil {
		t.Fatal(err)
	}
	want := []CustomRule{{Value: "www.google.com", Policy: rulePolicyDirect}, {Value: "192.168.1.9", Policy: rulePolicyProxy}}
	if !reflect.DeepEqual(config.CustomRules, want) {
		t.Errorf("自定义规则应整理地址、补上默认去向：%+v", config.CustomRules)
	}
	copied := config.Clone()
	copied.CustomRules[0].Policy = rulePolicyReject
	if config.CustomRules[0].Policy != rulePolicyDirect {
		t.Error("修改副本不应影响原来的配置")
	}
	if config, _ := parseConfig(`{"profiles": []}`); config.CustomRules == nil {
		t.Error("没有自定义规则时应是空列表")
	}
	for text, problem := range map[string]string{
		`{"custom_rules": [{"value": ""}], "profiles": []}`:                            "第 1 条自定义规则：请填写",
		`{"custom_rules": [{"value": "youtube"}], "profiles": []}`:                     "认不出「youtube」",
		`{"custom_rules": [{"value": "a.com"}, {"value": "b.com", "policy": "fast"}]}`: "第 2 条",
	} {
		if _, err := parseConfig(text); err == nil || !strings.Contains(err.Error(), problem) {
			t.Errorf("%s 应报错「%s」：%v", text, problem, err)
		}
	}
}

// 自定义规则排在分流规则前面，全局代理时也生效。
func TestCoreConfigCustomRules(t *testing.T) {
	settings := CoreSettings{Port: 17890, Active: "p1", Mode: "rule", GeoReady: true, Subscriptions: []CoreSubscription{{Id: "p1"}},
		CustomRules: []string{"DOMAIN-SUFFIX,youtube.com,ProxySwitch"}}
	rules := func(settings CoreSettings) []string {
		text := string(coreConfigText(settings, "127.0.0.1:9090", "secret"))
		start := strings.Index(text, `"rules": [`)
		return strings.Split(text[start:], "\n")
	}
	joined := strings.Join(rules(settings), "\n")
	if strings.Index(joined, "youtube.com") > strings.Index(joined, "GEOSITE,cn") || strings.Index(joined, "192.168.0.0") > strings.Index(joined, "youtube.com") {
		t.Errorf("自定义规则应在局域网规则之后、分流规则之前：\n%s", joined)
	}
	settings.Mode = "global"
	if joined := strings.Join(rules(settings), "\n"); !strings.Contains(joined, "youtube.com") || strings.Contains(joined, "GEOSITE") {
		t.Errorf("全局代理时自定义规则也生效：\n%s", joined)
	}
}

func TestEngineCustomRules(t *testing.T) {
	fixture, core, _ := newSubscriptionFixture(t, subscriptionTestConfig)
	engine := fixture.engine
	updated := engine.Config().Clone()
	updated.CustomRules = []CustomRule{{Value: "YouTube.com", Policy: "proxy"}, {Value: "10.0.0.0/8", Policy: "direct", Disabled: true}}
	if err := engine.SaveConfig(updated); err != nil {
		t.Fatal(err)
	}
	if got := core.last().CustomRules; !reflect.DeepEqual(got, []string{"DOMAIN-SUFFIX,youtube.com,ProxySwitch"}) {
		t.Errorf("保存后应把启用的自定义规则交给内核：%v", got)
	}
	if reloaded, _, _ := loadConfig(engine.paths.Config); len(reloaded.CustomRules) != 2 || reloaded.CustomRules[0].Value != "youtube.com" || !reloaded.CustomRules[1].Disabled {
		t.Errorf("自定义规则应写进配置文件：%+v", reloaded.CustomRules)
	}
}
