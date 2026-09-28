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
		"<html><body>404</body></html>":                 "网页",
		"[General]\nipv6 = false\n":                     "没有找到分流规则",
		"port: 7890\nproxies: []\nrules:\n  - PASS,x\n": "Clash 配置里没有找到分流规则",
	}
	for content, problem := range failures {
		if _, err := parseRuleConfig([]byte(content)); err == nil || !strings.Contains(err.Error(), problem) {
			t.Errorf("%q 应报错「%s」：%v", content, problem, err)
		}
	}
}

// Clash（mihomo）的配置：策略组按第一个选项归类，RULE-SET 引用 rule-providers 里的规则集。
func TestParseClashRuleConfig(t *testing.T) {
	content := `mixed-port: 7890
proxies: []
proxy-groups:
  - name: 🚀 节点选择
    type: select
    proxies: [♻️ 自动选择, DIRECT]
  - {name: ♻️ 自动选择, type: url-test, use: [机场]}
  - {name: 🎯 全球直连, type: select, proxies: [DIRECT, 🚀 节点选择]}
  - {name: 🛑 广告拦截, type: select, proxies: [REJECT, DIRECT]}
  - {name: 🐟 漏网之鱼, type: select, proxies: [🚀 节点选择, 🎯 全球直连]}
rule-providers:
  reject: {type: http, behavior: domain, url: "https://example.com/reject.txt", path: ./reject.yaml}
  cncidr: {type: http, behavior: ipcidr, format: text, url: https://example.com/cncidr.txt}
  apps: {type: http, behavior: classical, url: https://example.com/apps.yaml}
  binary: {type: http, behavior: domain, format: mrs, url: https://example.com/a.mrs}
  local: {type: file, behavior: domain, path: ./local.yaml}
  mine:
    type: inline
    behavior: domain
    payload:
      - '+.mine.example'
      - exact.example
rules:
  - RULE-SET,reject,🛑 广告拦截
  - RULE-SET,mine,🎯 全球直连
  - DOMAIN-SUFFIX,google.com,🚀 节点选择
  - GEOSITE,category-ads-all,🛑 广告拦截
  - GEOSITE,geolocation-!cn,🚀 节点选择
  - RULE-SET,binary,🛑 广告拦截
  - RULE-SET,local,DIRECT
  - RULE-SET,missing,DIRECT
  - PROCESS-NAME,Telegram.exe,🚀 节点选择
  - DOMAIN,skip.example,PASS
  - RULE-SET,apps,🚀 节点选择
  - RULE-SET,cncidr,🎯 全球直连,no-resolve
  - GEOIP,CN,🎯 全球直连
  - MATCH,🐟 漏网之鱼
  - DOMAIN,after.match,DIRECT
`
	config, err := parseRuleConfig([]byte(content))
	if err != nil {
		t.Fatal(err)
	}
	type line struct {
		kind, value, set string
		domainSet        bool
		policy           string
	}
	var got []line
	for _, item := range config.Lines {
		got = append(got, line{item.Entry.Kind, item.Entry.Value, item.Set, item.DomainSet, item.Policy})
	}
	want := []line{
		{"", "", "https://example.com/reject.txt", true, rulePolicyReject},
		{"DOMAIN-SUFFIX", "mine.example", "", false, rulePolicyDirect},
		{"DOMAIN", "exact.example", "", false, rulePolicyDirect},
		{"DOMAIN-SUFFIX", "google.com", "", false, rulePolicyProxy},
		{"GEOSITE", "category-ads-all", "", false, rulePolicyReject},
		{"GEOSITE", "geolocation-!cn", "", false, rulePolicyProxy},
		{"PROCESS-NAME", "Telegram.exe", "", false, rulePolicyProxy},
		{"", "", "https://example.com/apps.yaml", false, rulePolicyProxy},
		{"", "", "https://example.com/cncidr.txt", false, rulePolicyDirect},
		{"GEOIP", "CN", "", false, rulePolicyDirect},
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("Clash 规则转换不对：\n%+v", got)
	}
	// 漏网之鱼 → 节点选择 → 自动选择（url-test）：走节点。
	if config.Final != rulePolicyProxy || config.Skipped != 4 {
		t.Errorf("MATCH 的去向或跳过的规则数不对（mrs、file、不存在的规则集、PASS）：%q %d", config.Final, config.Skipped)
	}

	if !config.Lines[8].NoResolve || config.Lines[7].NoResolve {
		t.Error("RULE-SET 后面的 no-resolve 应记下")
	}
	sets := map[ruleSetSource][]ruleEntry{{"https://example.com/cncidr.txt", false}: {{Kind: "IP-CIDR", Value: "1.0.1.0/24"}}}
	converted := convertRules(config, sets, "r")
	lines := templateLines(converted.Rules, "", nil)
	if !containsString(lines, "PROCESS-NAME,Telegram.exe,ProxySwitch") {
		t.Errorf("PROCESS-NAME 规则应交给内核：%v", lines)
	}
	if !converted.Geo || !containsString(lines, "GEOSITE,category-ads-all,REJECT") || !containsString(lines, "GEOSITE,geolocation-!cn,ProxySwitch") {
		t.Errorf("GEOSITE 规则应原样交给内核，并下载地理数据：%v", lines)
	}
	if !containsString(lines, "IP-CIDR,1.0.1.0/24,DIRECT,no-resolve") {
		t.Errorf("规则集里的 IP 段按 RULE-SET 的 no-resolve 不解析域名：%v", lines)
	}
	// 文件里写的策略名和某个策略组同名时指到那个组，规则集设了去向时全部改到那里。
	if lines := templateLines(converted.Rules, "", []string{"🚀 节点选择"}); !containsString(lines, "DOMAIN-SUFFIX,google.com,🚀 节点选择") || !containsString(lines, "GEOSITE,category-ads-all,REJECT") {
		t.Errorf("和策略组同名的策略应指到那个组：%v", lines)
	}
	if lines := templateLines(converted.Rules, rulePolicyDirect, []string{"🚀 节点选择"}); containsString(lines, "DOMAIN-SUFFIX,google.com,🚀 节点选择") || !containsString(lines, "GEOSITE,category-ads-all,DIRECT") {
		t.Errorf("规则集设了去向时全部改到那里：%v", lines)
	}
}

// templateLines 是转换结果在内核里的写法。
func templateLines(rules []ruleTemplate, forced string, groups []string) []string {
	var lines []string
	for _, rule := range rules {
		lines = append(lines, rule.line(forced, groups))
	}
	return lines
}

func TestParseRuleList(t *testing.T) {
	surge := "# Telegram\nDOMAIN-SUFFIX,t.me\nDOMAIN,telegram.org\nIP-CIDR,91.108.8.0/22,no-resolve\nIP-CIDR6,2001:67c:4e8::/48,no-resolve\nUSER-AGENT,Telegram*\nPROCESS-NAME,Telegram\n"
	entries, skipped, err := parseRuleList([]byte(surge), false)
	if err != nil || len(entries) != 5 || skipped != 1 || !entries[2].NoResolve || entries[3].Kind != "IP-CIDR6" || entries[4] != (ruleEntry{Kind: "PROCESS-NAME", Value: "Telegram"}) {
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
	converted := convertRules(config, sets, "rs-1")
	want := []string{
		"RULE-SET,rs-1-1,REJECT",
		"DOMAIN-SUFFIX,google.com,ProxySwitch",
		"DOMAIN-KEYWORD,youtube,ProxySwitch",
		"IP-CIDR,91.108.4.0/22,ProxySwitch,no-resolve",
		"GEOIP,US,ProxySwitch",
		"DOMAIN-SUFFIX,cn.example.com,DIRECT",
		"GEOIP,CN,DIRECT",
		"RULE-SET,rs-1-2,ProxySwitch",
	}
	if lines := templateLines(converted.Rules, "", nil); !reflect.DeepEqual(lines, want) {
		t.Errorf("转换后的规则不对：\n%s", strings.Join(lines, "\n"))
	}
	if len(converted.Providers) != 2 || converted.Providers[0].Behavior != "domain" || len(converted.Providers[0].Lines) != ruleSetMinimum+1 ||
		converted.Providers[0].Lines[0] != "+.ad0.example.com" || converted.Providers[0].Lines[ruleSetMinimum] != "ad0.example.com" ||
		converted.Providers[1].Behavior != "ipcidr" || len(converted.Providers[1].Lines) != ruleSetMinimum {
		t.Errorf("规则集不对：%+v", converted.Providers)
	}
	if converted.Count != ruleSetMinimum+8+ruleSetMinimum || converted.Final != rulePolicyDirect || !converted.Geo {
		t.Errorf("规则数、其余网站的去向或地理数据标记不对：%d %s %v", converted.Count, converted.Final, converted.Geo)
	}

	// 没有 FINAL 时不记（其余流量由规则集的设置决定）；没下载到的规则列表按空处理。
	converted = convertRules(ruleConfig{Lines: []ruleLine{{Set: "https://missing.example.com/list", Policy: rulePolicyReject}}}, nil, "rs-2")
	if len(converted.Rules) != 0 || converted.Final != "" || converted.Geo {
		t.Errorf("没有 FINAL、规则列表也没下载到时应没有规则：%+v", converted)
	}

	// 策略名不同的连续规则分开：它们可能指到不同的策略组。
	config, _ = parseRuleConfig([]byte("[Rule]\nDOMAIN-SUFFIX,netflix.com,Netflix\nDOMAIN-SUFFIX,google.com,Proxy\nFINAL,Proxy\n"))
	converted = convertRules(config, nil, "rs-3")
	if lines := templateLines(converted.Rules, "", []string{"netflix"}); !reflect.DeepEqual(lines, []string{"DOMAIN-SUFFIX,netflix.com,netflix", "DOMAIN-SUFFIX,google.com,ProxySwitch"}) || converted.FinalName != "Proxy" {
		t.Errorf("策略名不同的规则应分开，同名的策略组不区分大小写：%v %+v", lines, converted)
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
	set := RuleSet{Name: "规则", Url: server.URL + "/rules.conf"}
	id := set.Id()

	// 规则列表下载失败，也没有上次的副本：跳过它，其余规则照常使用。
	result, err := fetchRuleSet(dir, set, []string{""})
	if err != nil {
		t.Fatal(err)
	}
	if result.Kind != ruleSetConvert || result.Sets != 2 || result.FailedSets != 1 || result.Count != 3 || result.Skipped != 1 || result.Final != rulePolicyDirect || result.FinalName != "DIRECT" || result.Revision == "" {
		t.Errorf("第一次下载的结果不对：%+v", result)
	}
	first := result.Revision

	setAvailable.Store(true)
	result, err = fetchRuleSet(dir, set, []string{""})
	if err != nil || result.FailedSets != 0 || result.Count != 3+ruleSetMinimum || result.Skipped != 2 || result.Revision == first {
		t.Fatalf("规则列表能下载后结果不对：%+v %v", result, err)
	}
	manifest, err := readRuleManifest(dir, id, result.Revision)
	if err != nil {
		t.Fatal(err)
	}
	if len(manifest.Providers) != 1 || manifest.Final != rulePolicyDirect || len(manifest.Rules) != 3 {
		t.Errorf("保存的规则不对：%+v", manifest)
	}
	for _, provider := range manifest.Providers {
		data, err := os.ReadFile(filepath.Join(dir, filepath.FromSlash(provider.Path)))
		if err != nil || !strings.HasPrefix(string(data), "+.google.com\n+.t0.me\n") || provider.Behavior != "domain" || !strings.HasPrefix(provider.Path, "rules/"+id+"/"+result.Revision+"/") {
			t.Errorf("规则集文件不对：%+v %q %v", provider, data, err)
		}
	}

	// 规则列表再次下载失败时用上次的副本，内容没变，版本也不变。
	setAvailable.Store(false)
	again, err := fetchRuleSet(dir, set, []string{""})
	if err != nil || again.FailedSets != 0 || again.Revision != result.Revision || listDownloads.Load() != 3 {
		t.Errorf("应使用上次下载的规则列表：%+v %v", again, err)
	}

	removeRuleRevisions(dir, id, result.Revision)
	entries, _ := os.ReadDir(filepath.Join(dir, "rules", id))
	if len(entries) != 2 {
		t.Errorf("应只留下在用的版本和规则列表的副本：%v", entries)
	}
	removeRuleRevisions(dir, id, "")
	if fileExists(filepath.Join(dir, "rules", id)) {
		t.Error("应删除这个规则集的全部文件")
	}

	if _, err := fetchRuleSet(dir, RuleSet{Url: server.URL + "/missing.conf"}, []string{""}); err == nil || !strings.Contains(err.Error(), "404") {
		t.Errorf("规则地址不存在时应报错：%v", err)
	}
}

// 下载 Clash 配置：rule-providers 里的规则集一起下载，yaml 的 payload 和 text 格式都能读。
func TestFetchClashRules(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		switch request.URL.Path {
		case "/clash.yaml":
			fmt.Fprintf(writer, "proxy-groups:\n  - {name: 节点选择, type: select, proxies: [自动选择, DIRECT]}\n  - {name: 自动选择, type: url-test, use: [a]}\n"+
				"rule-providers:\n  reject:\n    type: http\n    behavior: domain\n    url: http://%s/reject.yaml\n  cn:\n    type: http\n    behavior: ipcidr\n    format: text\n    url: http://%s/cn.txt\n"+
				"rules:\n  - RULE-SET,reject,REJECT\n  - DOMAIN-SUFFIX,google.com,节点选择\n  - RULE-SET,cn,DIRECT,no-resolve\n  - MATCH,节点选择\n", request.Host, request.Host)
		case "/reject.yaml":
			_, _ = writer.Write([]byte("payload:\n  - '+.ads.example.com'\n  - 'tracker.example.com'\n"))
		case "/cn.txt":
			_, _ = writer.Write([]byte("# 大陆 IP\n1.0.1.0/24\n1.0.2.0/23\n"))
		default:
			writer.WriteHeader(http.StatusNotFound)
		}
	}))
	defer server.Close()
	dir := t.TempDir()
	// 地址是 .yaml（按扩展名是规则列表），内容却是 Clash 的完整配置：认出来后转换。
	set := RuleSet{Url: server.URL + "/clash.yaml"}
	result, err := fetchRuleSet(dir, set, []string{""})
	if err != nil {
		t.Fatal(err)
	}
	if result.Kind != ruleSetConvert || result.Sets != 2 || result.FailedSets != 0 || result.Count != 5 || result.Final != rulePolicyProxy || result.FinalName != "节点选择" {
		t.Errorf("Clash 配置的下载结果不对：%+v", result)
	}
	manifest, err := readRuleManifest(dir, set.Id(), result.Revision)
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"DOMAIN-SUFFIX,ads.example.com,REJECT", "DOMAIN,tracker.example.com,REJECT", "DOMAIN-SUFFIX,google.com,ProxySwitch", "IP-CIDR,1.0.1.0/24,DIRECT,no-resolve", "IP-CIDR,1.0.2.0/23,DIRECT,no-resolve"}
	if lines := templateLines(manifest.Rules, "", nil); !reflect.DeepEqual(lines, want) {
		t.Errorf("转换后的规则不对：%v", lines)
	}
}
