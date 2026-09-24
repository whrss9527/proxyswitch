package main

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync/atomic"
	"testing"
)

const testRuleConfig = `# 小火箭规则
[General]
bypass-system = true
dns-server = system

[Proxy Group]
Spotify = select,DIRECT,PROXY,香港节点,policy-select-name=PROXY
苹果服务 = select,DIRECT,PROXY,policy-select-name=DIRECT
哔哩哔哩 = select,DIRECT,PROXY
香港节点 = url-test,url=http://www.gstatic.com/generate_204,policy-regex-filter=HK|香港
广告 = select,拦截
拦截 = select,REJECT-TINYGIF
循环 = select,环
环 = select,循环

[Rule]
DOMAIN-SUFFIX,Ads.Example.com,Reject
DOMAIN,tracker.example.com,REJECT-DICT
DOMAIN-SUFFIX,google.com,Proxy # Google 走代理
DOMAIN-KEYWORD,youtube,proxy
USER-AGENT,Instagram*,PROXY
URL-REGEX,^http://example\.com/ad,REJECT
IP-ASN,13335,PROXY,no-resolve
AND,((DOMAIN,a.com),(DST-PORT,443)),DIRECT
DOMAIN-SUFFIX,spotify.com,SPOTIFY
DOMAIN-SUFFIX,apple.com,苹果服务
DOMAIN-SUFFIX,bilibili.com,哔哩哔哩
DOMAIN-SUFFIX,hk.example.com,香港节点
DOMAIN-SUFFIX,ad2.example.com,广告
DOMAIN-SUFFIX,loop.example.com,循环
DOMAIN-SUFFIX,node.example.com,香港 01
IP-CIDR,91.108.4.1/22,Proxy,no-resolve
IP-CIDR,2001:b28:f23d::/48,PROXY
RULE-SET,https://rules.example.com/Telegram.list,PROXY
DOMAIN-SET,https://rules.example.com/ads.txt,REJECT
RULE-SET,LAN,DIRECT
RULE-SET,SYSTEM,DIRECT
GEOIP,cn,DIRECT
FINAL,Direct,dns-failed
DOMAIN-SUFFIX,after-final.com,PROXY

[URL Rewrite]
^https?://(www.)?g.cn https://www.google.com 302
`

func TestParseRuleConfig(t *testing.T) {
	config, err := parseRuleConfig([]byte("\xef\xbb\xbf" + testRuleConfig))
	if err != nil {
		t.Fatal(err)
	}
	type line struct{ kind, value, policy string }
	var got []line
	for _, rule := range config.Lines {
		if rule.Set != "" {
			got = append(got, line{"SET", rule.Set, rule.Policy})
			continue
		}
		value := rule.Entry.Value
		if rule.Entry.NoResolve {
			value += " no-resolve"
		}
		got = append(got, line{rule.Entry.Kind, value, rule.Policy})
	}
	want := []line{
		{"DOMAIN-SUFFIX", "ads.example.com", "reject"},
		{"DOMAIN", "tracker.example.com", "reject"},
		{"DOMAIN-SUFFIX", "google.com", "proxy"},
		{"DOMAIN-KEYWORD", "youtube", "proxy"},
		{"DOMAIN-SUFFIX", "spotify.com", "proxy"},
		{"DOMAIN-SUFFIX", "apple.com", "direct"},
		{"DOMAIN-SUFFIX", "bilibili.com", "direct"},
		{"DOMAIN-SUFFIX", "hk.example.com", "proxy"},
		{"DOMAIN-SUFFIX", "ad2.example.com", "reject"},
		{"DOMAIN-SUFFIX", "loop.example.com", "proxy"},
		{"DOMAIN-SUFFIX", "node.example.com", "proxy"},
		{"IP-CIDR", "91.108.4.0/22 no-resolve", "proxy"},
		{"IP-CIDR6", "2001:b28:f23d::/48", "proxy"},
		{"SET", "https://rules.example.com/Telegram.list", "proxy"},
		{"SET", "https://rules.example.com/ads.txt", "reject"},
		{"GEOIP", "CN", "direct"},
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("解析结果不对：\n%v\n应为\n%v", got, want)
	}
	// USER-AGENT、URL-REGEX、IP-ASN、AND、内置的 SYSTEM 规则集跳过；LAN 本来就直连，不算跳过。
	if config.Final != rulePolicyDirect || config.Skipped != 5 {
		t.Errorf("FINAL 或跳过的规则数不对：%q %d", config.Final, config.Skipped)
	}
	if sets := config.Sets(); len(sets) != 2 || sets[0].DomainSet || !sets[1].DomainSet {
		t.Errorf("引用的规则列表不对：%+v", sets)
	}

	// 没有分段的文件整个当作规则；没有 FINAL 时为空。
	if config, err := parseRuleConfig([]byte("DOMAIN-SUFFIX,a.com,PROXY\nIP-CIDR,1.1.1.1,DIRECT\n")); err != nil || len(config.Lines) != 2 || config.Final != "" || config.Lines[1].Entry.Value != "1.1.1.1/32" {
		t.Errorf("没有分段的规则解析不对：%+v %v", config, err)
	}
	failures := map[string]string{
		"<html><body>404</body></html>":                       "网页",
		"[General]\nipv6 = false\n":                           "没有找到分流规则",
		"port: 7890\nproxies: []\nrules:\n  - MATCH,DIRECT\n": "Clash",
	}
	for content, problem := range failures {
		if _, err := parseRuleConfig([]byte(content)); err == nil || !strings.Contains(err.Error(), problem) {
			t.Errorf("%q 应报错「%s」：%v", content, problem, err)
		}
	}
}

func TestParseRuleList(t *testing.T) {
	surge := "# Telegram\nDOMAIN-SUFFIX,t.me\nDOMAIN,telegram.org\nIP-CIDR,91.108.8.0/22,no-resolve\nIP-CIDR6,2001:67c:4e8::/48,no-resolve\nUSER-AGENT,Telegram*\nPROCESS-NAME,Telegram\n"
	entries, skipped, err := parseRuleList([]byte(surge), false)
	if err != nil || len(entries) != 4 || skipped != 2 || !entries[2].NoResolve || entries[3].Kind != "IP-CIDR6" {
		t.Errorf("Surge 规则列表解析不对：%+v %d %v", entries, skipped, err)
	}
	quantumult := "HOST,apps.apple.com,Apple\nHOST-SUFFIX,mzstatic.com,Apple\nhost-keyword,apple,Apple\nHOST-WILDCARD,*.apple.com.edgekey.net,Apple\nIP6-CIDR,2403:300::/32,Apple\nUSER-AGENT,*com.apple*,Apple\n"
	entries, skipped, _ = parseRuleList([]byte(quantumult), false)
	kinds := []string{}
	for _, entry := range entries {
		kinds = append(kinds, entry.Kind)
	}
	if strings.Join(kinds, " ") != "DOMAIN DOMAIN-SUFFIX DOMAIN-KEYWORD DOMAIN-WILDCARD IP-CIDR6" || skipped != 1 {
		t.Errorf("QuantumultX 规则列表解析不对：%v %d", kinds, skipped)
	}
	clash := "payload:\n  - DOMAIN-SUFFIX,openai.com\n  - '+.chatgpt.com'\n  - \"1.2.3.0/24\"\n"
	entries, _, _ = parseRuleList([]byte(clash), false)
	if len(entries) != 3 || entries[1].Kind != "DOMAIN-SUFFIX" || entries[1].Value != "chatgpt.com" || entries[2].Kind != "IP-CIDR" {
		t.Errorf("Clash 规则集解析不对：%+v", entries)
	}
	domains := ".apple.com\nicloud.com\n*.cdn-apple.com\nbad domain\n"
	entries, skipped, _ = parseRuleList([]byte(domains), true)
	if len(entries) != 3 || entries[0].Kind != "DOMAIN-SUFFIX" || entries[1].Kind != "DOMAIN" || entries[2].Kind != "DOMAIN-WILDCARD" || skipped != 1 {
		t.Errorf("DOMAIN-SET 解析不对：%+v %d", entries, skipped)
	}
	if _, _, err := parseRuleList([]byte("<!DOCTYPE html>"), false); err == nil {
		t.Error("网页不是规则列表")
	}
}

func TestResolvePolicy(t *testing.T) {
	groups := parseProxyGroups(strings.Split("A = select, B\nB = select,Reject\nC = select\nD = fallback,DIRECT", "\n"))
	cases := map[string]string{"a": rulePolicyReject, "C": rulePolicyProxy, "D": rulePolicyProxy, "DIRECT": rulePolicyDirect, "REJECT-DROP": rulePolicyReject, "": rulePolicyProxy, "某个节点": rulePolicyProxy}
	for name, want := range cases {
		if got := resolvePolicy(name, groups); got != want {
			t.Errorf("%q 应归为 %s，实际 %s", name, want, got)
		}
	}
}

func TestConvertRules(t *testing.T) {
	var lines []string
	for index := 0; index < ruleSetMinimum; index++ {
		lines = append(lines, fmt.Sprintf("DOMAIN-SUFFIX,ad%d.example.com,REJECT", index))
	}
	lines = append(lines,
		"DOMAIN,ad0.example.com,REJECT", // 与上面的后缀规则同属拦截，保留在同一个规则集里
		"DOMAIN-SUFFIX,google.com,PROXY",
		"DOMAIN-KEYWORD,youtube,PROXY",
		"IP-CIDR,91.108.4.0/22,PROXY,no-resolve",
		"GEOIP,US,PROXY",
		"DOMAIN-SUFFIX,google.com,PROXY", // 重复
		"DOMAIN-SUFFIX,cn.example.com,DIRECT",
		"GEOIP,CN,DIRECT",
		"RULE-SET,https://rules.example.com/list,PROXY",
		"FINAL,DIRECT",
	)
	config, err := parseRuleConfig([]byte("[Rule]\n" + strings.Join(lines, "\n")))
	if err != nil {
		t.Fatal(err)
	}
	var setEntries []ruleEntry
	for index := 0; index < ruleSetMinimum; index++ {
		setEntries = append(setEntries, ruleEntry{Kind: "IP-CIDR", Value: fmt.Sprintf("10.%d.0.0/16", index)})
	}
	sets := map[ruleSetSource][]ruleEntry{{Url: "https://rules.example.com/list"}: setEntries}
	converted := convertRules(config, sets, "p1-rules")
	want := []string{
		"RULE-SET,p1-rules-1,REJECT",
		"DOMAIN-SUFFIX,google.com,ProxySwitch",
		"DOMAIN-KEYWORD,youtube,ProxySwitch",
		"IP-CIDR,91.108.4.0/22,ProxySwitch,no-resolve",
		"GEOIP,US,ProxySwitch",
		"DOMAIN-SUFFIX,cn.example.com,DIRECT",
		"GEOIP,CN,DIRECT",
		"RULE-SET,p1-rules-2,ProxySwitch",
		"MATCH,DIRECT",
	}
	if !reflect.DeepEqual(converted.Rules, want) {
		t.Errorf("转换后的规则不对：\n%s", strings.Join(converted.Rules, "\n"))
	}
	if len(converted.Providers) != 2 || converted.Providers[0].Behavior != "domain" || len(converted.Providers[0].Lines) != ruleSetMinimum+1 ||
		converted.Providers[0].Lines[0] != "+.ad0.example.com" || converted.Providers[0].Lines[ruleSetMinimum] != "ad0.example.com" ||
		converted.Providers[1].Behavior != "ipcidr" || len(converted.Providers[1].Lines) != ruleSetMinimum {
		t.Errorf("规则集不对：%+v", converted.Providers)
	}
	if converted.Count != ruleSetMinimum+8+ruleSetMinimum || converted.Final != rulePolicyDirect || !converted.Geo {
		t.Errorf("规则数、其余网站的去向或地理数据标记不对：%d %s %v", converted.Count, converted.Final, converted.Geo)
	}

	// 没有 FINAL 时其余网站走代理；没下载到的规则列表按空处理。
	converted = convertRules(ruleConfig{Lines: []ruleLine{{Set: "https://missing.example.com/list", Policy: rulePolicyReject}}}, nil, "p2-rules")
	if !reflect.DeepEqual(converted.Rules, []string{"MATCH,ProxySwitch"}) || converted.Final != rulePolicyProxy || converted.Geo {
		t.Errorf("没有 FINAL 时应走代理：%v", converted.Rules)
	}
}

func TestFetchRules(t *testing.T) {
	var setAvailable atomic.Bool
	var listDownloads atomic.Int32
	var telegram strings.Builder
	for index := 0; index < ruleSetMinimum; index++ {
		fmt.Fprintf(&telegram, "DOMAIN-SUFFIX,t%d.me\n", index)
	}
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		switch request.URL.Path {
		case "/rules.conf":
			fmt.Fprintf(writer, "[Rule]\nDOMAIN-SUFFIX,google.com,PROXY\nRULE-SET,http://%s/telegram.list,PROXY\nDOMAIN-SET,http://%s/ads.txt,REJECT\nUSER-AGENT,x,PROXY\nFINAL,DIRECT\n", request.Host, request.Host)
		case "/telegram.list":
			listDownloads.Add(1)
			if !setAvailable.Load() {
				writer.WriteHeader(http.StatusServiceUnavailable)
				return
			}
			_, _ = writer.Write([]byte(telegram.String() + "USER-AGENT,Telegram*\n"))
		case "/ads.txt":
			_, _ = writer.Write([]byte(".ads.example.com\ntracker.example.com\n"))
		default:
			writer.WriteHeader(http.StatusNotFound)
		}
	}))
	defer server.Close()
	dir := t.TempDir()

	// 规则列表下载失败，也没有上次的副本：跳过它，其余规则照常使用。
	result, err := fetchRules(dir, "p1", server.URL+"/rules.conf", []string{""})
	if err != nil {
		t.Fatal(err)
	}
	if result.Sets != 2 || result.FailedSets != 1 || result.Count != 3 || result.Skipped != 1 || result.Final != rulePolicyDirect || result.Revision == "" {
		t.Errorf("第一次下载的结果不对：%+v", result)
	}
	first := result.Revision

	setAvailable.Store(true)
	result, err = fetchRules(dir, "p1", server.URL+"/rules.conf", []string{""})
	if err != nil || result.FailedSets != 0 || result.Count != 3+ruleSetMinimum || result.Skipped != 2 || result.Revision == first {
		t.Fatalf("规则列表能下载后结果不对：%+v %v", result, err)
	}
	manifest, err := readRuleManifest(dir, "p1", result.Revision)
	if err != nil {
		t.Fatal(err)
	}
	if len(manifest.Providers) != 1 || manifest.Rules[len(manifest.Rules)-1] != "MATCH,DIRECT" {
		t.Errorf("保存的规则不对：%+v", manifest)
	}
	for _, provider := range manifest.Providers {
		data, err := os.ReadFile(filepath.Join(dir, filepath.FromSlash(provider.Path)))
		if err != nil || !strings.HasPrefix(string(data), "+.google.com\n+.t0.me\n") || provider.Behavior != "domain" || !strings.HasPrefix(provider.Path, "rules/p1/"+result.Revision+"/") {
			t.Errorf("规则集文件不对：%+v %q %v", provider, data, err)
		}
	}

	// 规则列表再次下载失败时用上次的副本，内容没变，版本也不变。
	setAvailable.Store(false)
	again, err := fetchRules(dir, "p1", server.URL+"/rules.conf", []string{""})
	if err != nil || again.FailedSets != 0 || again.Revision != result.Revision || listDownloads.Load() != 3 {
		t.Errorf("应使用上次下载的规则列表：%+v %v", again, err)
	}

	removeRuleRevisions(dir, "p1", result.Revision)
	entries, _ := os.ReadDir(filepath.Join(dir, "rules", "p1"))
	if len(entries) != 2 {
		t.Errorf("应只留下在用的版本和规则列表的副本：%v", entries)
	}
	removeRuleRevisions(dir, "p1", "")
	if fileExists(filepath.Join(dir, "rules", "p1")) {
		t.Error("应删除这个配置的全部规则")
	}

	if _, err := fetchRules(dir, "p1", server.URL+"/missing.conf", []string{""}); err == nil || !strings.Contains(err.Error(), "404") {
		t.Errorf("规则地址不存在时应报错：%v", err)
	}
	check, err := checkRuleConfig(server.URL+"/rules.conf", []string{""})
	if err != nil || check.Rules != 1 || check.Sets != 2 || check.Skipped != 1 || check.Final != rulePolicyDirect {
		t.Errorf("检查规则的结果不对：%+v %v", check, err)
	}
	if _, err := checkRuleConfig("ftp://example.com/rules.conf", []string{""}); err == nil {
		t.Error("只支持 http 和 https 地址")
	}
}
