package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"
)

func TestRuleSetsConfig(t *testing.T) {
	// 旧版每个订阅配置自己的规则地址：第一个用到的启用，其余的停用；有配置用内置的大陆直连时也列上（停用）。
	config, err := parseConfig(`{"profiles": [
		{"name": "本机", "server": "127.0.0.1:7890", "rules": "https://ignored.example.com/a.conf"},
		{"name": "机场", "subscription": "https://sub.example.com/a", "rules": " https://johnshall.github.io/Shadowrocket-ADBlock-Rules-Forever/sr_top500_banlist_ad.conf "},
		{"name": "备用", "subscription": "https://sub.example.com/b", "rules": "https://rules.example.com/my.conf"},
		{"name": "内置", "subscription": "https://sub.example.com/c"}
	]}`)
	if err != nil {
		t.Fatal(err)
	}
	want := []RuleSet{
		{Name: "黑名单 + 去广告", Url: rulePresetBase + "sr_top500_banlist_ad.conf"},
		{Name: "my", Url: "https://rules.example.com/my.conf", Disabled: true},
		{Name: "国内直连", Url: builtinChinaDirect, Policy: rulePolicyDirect, Disabled: true},
	}
	if !reflect.DeepEqual(config.RuleSets, want) {
		t.Errorf("旧的规则地址应换成规则集：%+v", config.RuleSets)
	}
	data, _ := marshalConfigFile(config)
	if strings.Contains(string(data), `"rules": "`) || !strings.Contains(string(data), `"rule_sets": [`) || !strings.Contains(string(data), `"final_policy": ""`) {
		t.Errorf("保存时写规则集，不再写每个配置的规则地址：\n%s", data)
	}
	if again, err := parseConfig(string(data)); err != nil || !reflect.DeepEqual(again.RuleSets, config.RuleSets) {
		t.Errorf("规则集保存后应能原样读回：%v %+v", err, again)
	}

	// 都没填规则地址：一条内置的国内直连；新的配置文件里就有它。
	for _, text := range []string{`{"profiles": [{"name": "机场", "subscription": "https://sub.example.com/a"}]}`, `{"profiles": []}`, defaultConfigText} {
		if config, err := parseConfig(text); err != nil || !reflect.DeepEqual(config.RuleSets, []RuleSet{chinaDirectRuleSet()}) || config.FinalPolicy != "" {
			t.Errorf("默认是内置的国内直连：%v %+v", err, config)
		}
	}
	// 已经有 rule_sets 时不再迁移，空列表表示没有规则集。
	config, _ = parseConfig(`{"rule_sets": [], "profiles": [{"name": "机场", "subscription": "https://sub.example.com/a", "rules": "https://x.example.com/a.conf"}]}`)
	if len(config.RuleSets) != 0 || config.Profiles[0].Rules != "" {
		t.Errorf("有 rule_sets 时不再迁移：%+v", config.RuleSets)
	}

	// 整理：名字默认用规则库里的或者文件名，去向、类型不区分大小写，Windows 路径换成 file:// 地址。
	config, err = parseConfig(`{"policy_groups": [{"name": "流媒体"}], "final_policy": " GROUP:流媒体 ", "rule_sets": [
		{"url": " https://raw.githubusercontent.com/blackmatrix7/ios_rule_script/master/rule/Clash/Netflix/Netflix.list ", "policy": "group:流媒体"},
		{"url": "https://example.com/rules/ads", "policy": "Reject", "behavior": " Domain "},
		{"url": "C:\\规则\\my.list"}
	], "profiles": []}`)
	if err != nil {
		t.Fatal(err)
	}
	if sets := config.RuleSets; sets[0].Name != "Netflix" || sets[1].Name != "ads" || sets[1].Policy != rulePolicyReject || sets[1].Behavior != behaviorDomain || sets[2].Url != "file:///C:/规则/my.list" || sets[2].Name != "my" || config.FinalPolicy != "group:流媒体" {
		t.Errorf("规则集整理得不对：%+v %q", sets, config.FinalPolicy)
	}

	// 策略组改名、删除时指向它的规则集和其余流量跟着改。
	copied := config.Clone()
	copied.RetargetGroup("流媒体", "group:视频")
	if copied.RuleSets[0].Policy != "group:视频" || copied.FinalPolicy != "group:视频" || config.RuleSets[0].Policy != "group:流媒体" {
		t.Errorf("策略组改名后去向应跟着改，原配置不变：%+v %q", copied.RuleSets, copied.FinalPolicy)
	}

	for text, problem := range map[string]string{
		`{"rule_sets": [{"url": "ftp://x.example.com/a.list"}]}`:                                            "http",
		`{"rule_sets": [{"url": "builtin://other"}]}`:                                                       "没有内置的规则",
		`{"rule_sets": [{"url": "https://x.example.com/a.list"}, {"url": "https://x.example.com/a.list"}]}`: "重复",
		`{"rule_sets": [{"url": "https://x.example.com/a.list", "policy": "group:没有"}]}`:                    "策略组「没有」不存在",
		`{"rule_sets": [{"url": "https://x.example.com/a.list", "policy": "fast"}]}`:                        `去向 "fast" 不认识`,
		`{"rule_sets": [{"url": "https://x.example.com/a.list", "behavior": "ipv4"}]}`:                      `类型 "ipv4" 不认识`,
		`{"rule_sets": [{"url": "https://x.example.com/a.mrs", "behavior": "classical"}]}`:                  "mrs 格式",
		`{"final_policy": "later"}`: "其余流量",
		`{"rule_sets": [{"name": "一二三四五六七八九十一二三四五六七八九十一二三四五六七八九十一二三四五六七八九十一", "url": "https://x.example.com/a.list"}]}`: "名字太长",
	} {
		if _, err := parseConfig(text); err == nil || !strings.Contains(err.Error(), problem) {
			t.Errorf("%s 应报错「%s」：%v", text, problem, err)
		}
	}
}

func TestRuleSetKinds(t *testing.T) {
	for _, item := range []struct {
		url, kind, format, behavior string
	}{
		{builtinChinaDirect, ruleSetBuiltin, "text", behaviorClassical},
		{libraryBlackmatrix + "Telegram/Telegram.list", ruleSetProvider, "text", behaviorClassical},
		{libraryMetaGeo + "geosite/cn.mrs", ruleSetProvider, "mrs", behaviorDomain},
		{libraryMetaGeo + "geoip/cn.mrs", ruleSetProvider, "mrs", behaviorIpcidr},
		{"https://example.com/rules/apps.YAML?token=1", ruleSetProvider, "yaml", behaviorClassical},
		{"https://example.com/cn_domain.txt", ruleSetProvider, "text", behaviorDomain},
		{rulePresetBase + "sr_cnip.conf", ruleSetConvert, "text", behaviorIpcidr},
		{"https://example.com/sub?rules", ruleSetConvert, "text", behaviorClassical},
	} {
		set := RuleSet{Url: item.url}
		if set.guessKind() != item.kind || set.format() != item.format || set.guessBehavior() != item.behavior {
			t.Errorf("%s 应是 %s / %s / %s：%s / %s / %s", item.url, item.kind, item.format, item.behavior, set.guessKind(), set.format(), set.guessBehavior())
		}
	}
	if id := ruleSetId("https://a.example.com/x.list"); !strings.HasPrefix(id, "rs-") || len(id) != 11 || id != ruleSetId("https://a.example.com/x.list") || id == ruleSetId("https://a.example.com/y.list") {
		t.Errorf("规则集的 id 由地址算出，地址不同 id 不同：%s", id)
	}

	// GitHub 的原始地址换成 jsDelivr 镜像：经代理先试原地址，直连先试镜像。
	address := libraryBlackmatrix + "Apple/Apple.list"
	mirror := "https://testingcf.jsdelivr.net/gh/blackmatrix7/ios_rule_script@master/rule/Clash/Apple/Apple.list"
	if got := ruleMirror(address); got != mirror {
		t.Errorf("镜像地址不对：%s", got)
	}
	if !reflect.DeepEqual(ruleFileCandidates(address, false), []string{address, mirror}) || !reflect.DeepEqual(ruleFileCandidates(address, true), []string{mirror, address}) {
		t.Error("经代理先试原地址，直连先试镜像")
	}
	if ruleMirror("https://example.com/a.list") != "" || ruleMirror("https://raw.githubusercontent.com/only/two") != "" || len(ruleFileCandidates("https://example.com/a.list", true)) != 1 {
		t.Error("不是 GitHub 原始文件的地址没有镜像")
	}

	for text, want := range map[string]bool{
		"[General]\nipv6 = false\n[Rule]\nFINAL,DIRECT\n": true,
		"proxies: []\nrules:\n  - MATCH,DIRECT\n":         true,
		"payload:\n  - DOMAIN,rules.example\n":            false,
		"DOMAIN-SUFFIX,google.com\n":                      false,
		"# rules:\nDOMAIN,a.example\n":                    false,
	} {
		if got := needsConversion([]byte(text)); got != want {
			t.Errorf("%q 是否要转换：%v，应为 %v", text, got, want)
		}
	}
	for text, want := range map[string]struct {
		behavior string
		lines    int
	}{
		"# Telegram\nDOMAIN-SUFFIX,t.me\nIP-CIDR,91.108.8.0/22,no-resolve\n": {behaviorClassical, 2},
		"payload:\n  - '+.openai.com'\n  - \"chatgpt.com\"\n":                {behaviorDomain, 2},
		"1.0.1.0/24\n1.0.2.0/23\n2001:db8::/32\n":                            {behaviorIpcidr, 3},
		".apple.com\nicloud.com\n":                                           {behaviorDomain, 2},
		"# 空的\n":                                                             {behaviorDomain, 0},
	} {
		if behavior, lines := detectRuleListBehavior(text); behavior != want.behavior || lines != want.lines {
			t.Errorf("%q 应认成 %s（%d 行）：%s（%d 行）", text, want.behavior, want.lines, behavior, lines)
		}
	}
}

// 纯规则列表原样保存，交给内核读：格式和类型从内容判断，内容变了文件名跟着变；网页、空文件和不是 mrs 的 .mrs 报错。
func TestFetchRuleLists(t *testing.T) {
	contents := map[string][]byte{
		"/classical.list": []byte("DOMAIN-SUFFIX,t.me\nIP-CIDR,91.108.8.0/22,no-resolve\n"),
		"/domains.list":   []byte("payload:\n  - '+.openai.com'\n  - 'chatgpt.com'\n"),
		"/cn.yaml":        []byte("1.0.1.0/24\n1.0.2.0/23\n"),
		"/geosite.mrs":    append(append([]byte{}, zstdMagic...), 1, 2, 3),
		"/broken.mrs":     []byte("<html>not found</html>"),
		"/page.list":      []byte("<!DOCTYPE html><title>404</title>"),
		"/empty.list":     []byte("# 什么都没有\n"),
		"/config.list":    []byte("[Rule]\nDOMAIN-SUFFIX,google.com,Proxy\nFINAL,DIRECT\n"),
	}
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		if content, found := contents[request.URL.Path]; found {
			_, _ = writer.Write(content)
			return
		}
		writer.WriteHeader(http.StatusNotFound)
	}))
	defer server.Close()
	dir := t.TempDir()
	for path, want := range map[string]ruleSetDownload{
		"/classical.list": {Kind: ruleSetProvider, Format: "text", Behavior: behaviorClassical, Count: 2},
		"/domains.list":   {Kind: ruleSetProvider, Format: "yaml", Behavior: behaviorDomain, Count: 2},
		"/cn.yaml":        {Kind: ruleSetProvider, Format: "text", Behavior: behaviorIpcidr, Count: 2},
		"/geosite.mrs":    {Kind: ruleSetProvider, Format: "mrs", Behavior: behaviorDomain},
	} {
		set := RuleSet{Url: server.URL + path}
		result, err := fetchRuleSet(dir, set, []string{""})
		revision := result.Revision
		result.Revision = ""
		if err != nil || result != want {
			t.Errorf("%s 应认成 %+v：%+v %v", path, want, result, err)
			continue
		}
		data, err := os.ReadFile(filepath.Join(dir, "rules", set.Id(), revision))
		if err != nil || string(data) != string(contents[path]) || !strings.HasSuffix(revision, "."+map[string]string{"text": "txt", "yaml": "yaml", "mrs": "mrs"}[want.Format]) {
			t.Errorf("%s 应原样保存：%s %v", path, revision, err)
		}
	}
	for path, problem := range map[string]string{"/broken.mrs": "不是 mrs 格式", "/page.list": "网页", "/empty.list": "没有规则", "/missing.list": "404"} {
		if _, err := fetchRuleSet(dir, RuleSet{Url: server.URL + path}, []string{""}); err == nil || !strings.Contains(err.Error(), problem) {
			t.Errorf("%s 应报错「%s」：%v", path, problem, err)
		}
	}
	// 扩展名像列表、内容其实是完整配置：转换。
	if result, err := fetchRuleSet(dir, RuleSet{Url: server.URL + "/config.list"}, []string{""}); err != nil || result.Kind != ruleSetConvert || result.Final != rulePolicyDirect || result.Count != 1 {
		t.Errorf("内容是完整配置时应转换：%+v %v", result, err)
	}
	// 本机的文件直接读取。
	local := filepath.Join(t.TempDir(), "my.list")
	if err := os.WriteFile(local, []byte("DOMAIN,local.example\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	set := RuleSet{Url: testFileAddress(local)}
	if result, err := fetchRuleSet(dir, set, nil); err != nil || result.Kind != ruleSetProvider || result.Count != 1 {
		t.Errorf("应能读取本机的规则文件：%+v %v", result, err)
	}
}

func TestEngineRuleSets(t *testing.T) {
	listUrl, confUrl, localDir := "https://rules.example.com/netflix.list", "https://rules.example.com/black.conf", t.TempDir()
	localList := filepath.Join(localDir, "local.list")
	if err := os.WriteFile(localList, []byte("DOMAIN,local.example\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	fixture, core, downloads := newSubscriptionFixture(t, fmt.Sprintf(`{
  "policy_groups": [{"name": "流媒体", "type": "select"}],
  "rule_sets": [
    {"url": %q, "policy": "group:流媒体"},
    {"url": %q},
    {"url": "builtin://china-direct"},
    {"url": %q, "disabled": true}
  ],
  "profiles": [{"name": "机场", "subscription": "https://sub.example.com/a"}]
}`, listUrl, confUrl, testFileAddress(localList)))
	engine := fixture.engine
	now := time.Date(2026, 9, 28, 9, 0, 0, 0, time.Local)
	engine.now = func() time.Time { return now }
	installFakeCoreBinary(t, engine)
	config := engine.Config()
	list, conf := *config.FindRuleSet(listUrl), *config.FindRuleSet(confUrl)

	if due := engine.RuleSetsDue(); len(due) != 2 || due[0].Url != listUrl || due[1].Url != confUrl {
		t.Fatalf("启用的、不是内置的规则集都应下载：%+v", due)
	}
	profile := *config.FindProfile("机场")
	recordTestSubscription(t, engine, &profile)
	if err := engine.UseProfile("机场"); err != nil {
		t.Fatal(err)
	}
	// 还没下载好的规则集先跳过，内置的照用；其余流量没有规则文件的 FINAL 时走节点。
	settings := core.last()
	if want := []string{"GEOSITE,cn,DIRECT", "GEOIP,CN,DIRECT", "MATCH,ProxySwitch"}; !reflect.DeepEqual(settings.Rules, want) || len(settings.RuleProviders) != 0 || !engine.GeoDue() {
		t.Errorf("还没下载好时只用内置的规则：%+v", settings.Rules)
	}

	// 下载失败：记下原因，过一会儿重试。
	if err := engine.RecordRuleSet(listUrl, ruleSetDownload{}, errors.New("连接超时")); err == nil {
		t.Error("下载失败应返回错误")
	}
	if state := engine.ruleSetStates()[listUrl]; state.Error != "连接超时" || state.Downloaded || state.Kind != ruleSetProvider {
		t.Errorf("应记下失败原因：%+v", state)
	}
	if due := engine.RuleSetsDue(); len(due) != 1 || due[0].Url != confUrl {
		t.Errorf("刚失败的先不重试：%+v", due)
	}
	now = now.Add(ruleSetRetry)
	if len(engine.RuleSetsDue()) != 2 {
		t.Error("失败一会儿后应重试")
	}

	// 下载好：列表交给内核读，完整配置用转换好的规则，按规则集的顺序；文件里的 FINAL 决定其余流量。
	dir := engine.paths.Core
	if err := os.MkdirAll(filepath.Join(dir, "rules", list.Id()), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "rules", list.Id(), "abc.txt"), []byte("DOMAIN-SUFFIX,netflix.com\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	before := *downloads
	if err := engine.RecordRuleSet(listUrl, ruleSetDownload{Kind: ruleSetProvider, Revision: "abc.txt", Format: "text", Behavior: behaviorClassical, Count: 1}, nil); err != nil {
		t.Fatal(err)
	}
	parsed, _ := parseRuleConfig([]byte("[Rule]\nDOMAIN-SUFFIX,video.example,流媒体\nDOMAIN-SUFFIX,google.com,Proxy\nGEOIP,US,Proxy\nFINAL,DIRECT\n"))
	converted := convertRules(parsed, nil, conf.Id())
	revision, err := writeConvertedRules(dir, conf.Id(), converted)
	if err != nil {
		t.Fatal(err)
	}
	if err := engine.RecordRuleSet(confUrl, ruleSetDownload{Kind: ruleSetConvert, Revision: revision, Count: converted.Count, Final: converted.Final, FinalName: converted.FinalName, Geo: converted.Geo}, nil); err != nil {
		t.Fatal(err)
	}
	settings = core.last()
	want := []string{
		"RULE-SET," + list.Id() + ",流媒体",
		"DOMAIN-SUFFIX,video.example,流媒体",
		"DOMAIN-SUFFIX,google.com,ProxySwitch",
		"GEOIP,US,ProxySwitch",
		"GEOSITE,cn,DIRECT", "GEOIP,CN,DIRECT",
		"MATCH,DIRECT",
	}
	if !reflect.DeepEqual(settings.Rules, want) {
		t.Errorf("规则应按规则集的顺序，文件里和策略组同名的策略指到那个组：\n%s", strings.Join(settings.Rules, "\n"))
	}
	if provider := settings.RuleProviders[list.Id()]; provider != (CoreRuleProvider{Behavior: behaviorClassical, Format: "text", Path: "rules/" + list.Id() + "/abc.txt", Optional: true}) {
		t.Errorf("规则列表应交给内核读：%+v", settings.RuleProviders)
	}
	if len(engine.RuleSetsDue()) != 0 || *downloads == before {
		t.Error("都下载好后不用再下载；规则集用到了 GEOIP，应请求下载地理数据")
	}
	if state := engine.ruleSetStates()[confUrl]; !state.Downloaded || state.Kind != ruleSetConvert || state.Count != 3 || state.Final != rulePolicyDirect || state.Error != "" {
		t.Errorf("设置页的规则集情况不对：%+v", state)
	}

	// 改规则集的去向、其余流量：不用重新下载，内核直接换。
	updated := engine.Config().Clone()
	updated.FindRuleSet(confUrl).Policy = rulePolicyReject
	updated.FinalPolicy = "group:流媒体"
	if err := engine.SaveConfig(updated); err != nil {
		t.Fatal(err)
	}
	rules := core.last().Rules
	if rules[1] != "DOMAIN-SUFFIX,video.example,REJECT" || rules[2] != "DOMAIN-SUFFIX,google.com,REJECT" || rules[len(rules)-1] != "MATCH,流媒体" || len(engine.RuleSetsDue()) != 0 {
		t.Errorf("规则集设了去向时全部改到那里，其余流量按设置：%v", rules)
	}
	// 全局代理时不用规则集。
	if err := engine.SetMode(profile.Id, "global"); err != nil || core.last().Rules != nil {
		t.Errorf("全局代理时不用规则集：%v %v", err, core.last().Rules)
	}
	if err := engine.SetMode(profile.Id, "rule"); err != nil {
		t.Fatal(err)
	}

	// 每天更新一次；本机的文件改过就重新读。
	now = now.Add(25 * time.Hour)
	if due := engine.RuleSetsDue(); len(due) != 2 {
		t.Errorf("超过一天应更新：%+v", due)
	}
	updated = engine.Config().Clone()
	updated.FindRuleSet(testFileAddress(localList)).Disabled = false
	if err := engine.SaveConfig(updated); err != nil {
		t.Fatal(err)
	}
	local := *engine.Config().FindRuleSet(testFileAddress(localList))
	_ = engine.RecordRuleSet(local.Url, ruleSetDownload{Kind: ruleSetProvider, Revision: "x.txt", Format: "text", Behavior: behaviorDomain, Count: 1}, nil)
	isDue := func() bool {
		for _, set := range engine.RuleSetsDue() {
			if set.Url == local.Url {
				return true
			}
		}
		return false
	}
	if isDue() {
		t.Error("本机的文件没改过不用重新读")
	}
	if err := os.Chtimes(localList, now.Add(time.Minute), now.Add(time.Minute)); err != nil {
		t.Fatal(err)
	}
	now = now.Add(2 * time.Minute)
	if !isDue() {
		t.Error("本机的文件改过应重新读")
	}

	// 删掉规则集：记录和文件一起删；下载期间被删的结果忽略。
	updated = engine.Config().Clone()
	updated.RuleSets = updated.RuleSets[1:]
	if err := engine.SaveConfig(updated); err != nil {
		t.Fatal(err)
	}
	if _, found := engine.state.RuleSets[list.Id()]; found || fileExists(filepath.Join(dir, "rules", list.Id())) {
		t.Error("删掉的规则集的记录和文件应删除")
	}
	if err := engine.RecordRuleSet(listUrl, ruleSetDownload{Kind: ruleSetProvider, Revision: "abc.txt"}, nil); err == nil {
		t.Error("已删除的规则集的下载结果应忽略")
	}

	// 没有订阅配置时用不上规则集，不下载，也不需要地理数据。
	updated = engine.Config().Clone()
	updated.Profiles = nil
	if err := engine.SaveConfig(updated); err != nil {
		t.Fatal(err)
	}
	if len(engine.RuleSetsDue()) != 0 || engine.GeoDue() {
		t.Error("没有订阅配置时不下载规则集")
	}
}

// 旧版按配置下载的规则：换成规则集后删掉记录和文件。
func TestEngineForgetsOldRules(t *testing.T) {
	fixture, _, _ := newSubscriptionFixture(t, subscriptionTestConfig)
	engine := fixture.engine
	old := filepath.Join(engine.paths.Core, "rules", "p12345678", "abc")
	if err := os.MkdirAll(old, 0o755); err != nil {
		t.Fatal(err)
	}
	engine.state.Rules = map[string]json.RawMessage{"p12345678": json.RawMessage(`{"revision":"abc"}`)}
	if err := engine.SaveConfig(engine.Config().Clone()); err != nil {
		t.Fatal(err)
	}
	if engine.state.Rules != nil || fileExists(filepath.Dir(old)) {
		t.Error("旧版按配置下载的规则应删除")
	}
}
