package main

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"
	"time"
)

func TestPolicyGroupsConfig(t *testing.T) {
	config, err := parseConfig(`{
		"policy_groups": [
			{"name": " 流媒体 ", "type": "", "filter": " 港|HK ", "node": " 香港 01 "},
			{"name": "Telegram", "type": "URL_TEST", "node": "香港 01"},
			{"name": "备用", "type": "available"},
			{"name": "分流", "type": "loadBalance"}
		],
		"custom_rules": [
			{"value": "netflix.com", "policy": " GROUP:流媒体 "},
			{"value": "t.me", "policy": "group:Telegram"}
		],
		"profiles": []
	}`)
	if err != nil {
		t.Fatal(err)
	}
	want := []PolicyGroup{
		{Name: "流媒体", Type: groupSelect, Filter: "港|HK", Node: "香港 01"},
		{Name: "Telegram", Type: groupUrlTest},
		{Name: "备用", Type: groupFallback},
		{Name: "分流", Type: groupLoadBalance},
	}
	if !reflect.DeepEqual(config.PolicyGroups, want) {
		t.Errorf("策略组应整理类型和空白，自动挑选的组不记选中的节点：%+v", config.PolicyGroups)
	}
	if config.CustomRules[0].Policy != "group:流媒体" || config.CustomRules[1].Policy != "group:Telegram" {
		t.Errorf("指向策略组的去向应保留组名：%+v", config.CustomRules)
	}

	copied := config.Clone()
	copied.PolicyGroups[0].Node = ""
	if config.PolicyGroups[0].Node != "香港 01" {
		t.Error("修改副本的策略组不应影响原配置")
	}

	// 改名、删除后指向它的规则跟着改。
	copied.PolicyGroups[0].Name = "视频"
	copied.RetargetGroup("流媒体", "group:视频")
	copied.PolicyGroups = copied.PolicyGroups[1:]
	copied.RetargetGroup("视频", rulePolicyProxy)
	if copied.CustomRules[0].Policy != rulePolicyProxy || copied.CustomRules[1].Policy != "group:Telegram" {
		t.Errorf("删除策略组后指向它的规则应改为走节点：%+v", copied.CustomRules)
	}

	data, _ := marshalConfigFile(config)
	again, err := parseConfig(string(data))
	if err != nil || !reflect.DeepEqual(again.PolicyGroups, config.PolicyGroups) {
		t.Errorf("策略组保存后应能原样读回：%v\n%s", err, data)
	}
	if empty, _ := parseConfig(`{"profiles": []}`); empty.PolicyGroups == nil || !strings.Contains(string(mustMarshal(t, empty)), `"policy_groups": []`) {
		t.Error("没有策略组时写成空列表")
	}
}

func mustMarshal(t *testing.T, config *Config) []byte {
	t.Helper()
	data, err := marshalConfigFile(config)
	if err != nil {
		t.Fatal(err)
	}
	return data
}

func TestPolicyGroupValidation(t *testing.T) {
	profiles := `"profiles": [{"id": "pa1", "name": "机场", "subscription": "https://example.com/sub"}]`
	for _, item := range []struct {
		groups string
		rules  string
		want   string
	}{
		{`{"name": ""}`, ``, "请填写策略组的名字"},
		{`{"name": "一二三四五六七八九十一二三四五六七八九十一"}`, ``, "名字太长"},
		{`{"name": "港,台"}`, ``, "不能有逗号"},
		{`{"name": "港，台"}`, ``, "不能有逗号"},
		{`{"name": "direct"}`, ``, "内核保留的名字"},
		{`{"name": "ProxySwitch"}`, ``, "内核保留的名字"},
		{`{"name": "自动选择"}`, ``, "内核保留的名字"},
		{`{"name": "流媒体"}, {"name": "流媒体", "type": "url-test"}`, ``, "有两个策略组都叫「流媒体」"},
		{`{"name": "pa1-auto"}`, ``, "和配置「机场」的 id 重名"},
		{`{"name": "流媒体", "type": "random"}`, ``, `类型 "random" 不认识`},
		{`{"name": "流媒体", "filter": "[港"}`, ``, "不是正确的正则表达式"},
		{`{"name": "流媒体", "filter": "(?P<地区>港)"}`, ``, "不是正确的正则表达式"},
		{`{"name": "流媒体", "filter": "\\Q港\\E"}`, ``, "不是正确的正则表达式"},
		{`{"name": "流媒体", "filter": "港` + "`" + `HK"}`, ``, "反引号"},
		{`{"name": "流媒体"}`, `{"value": "netflix.com", "policy": "group:视频"}`, "策略组「视频」不存在"},
		{`{"name": "流媒体"}`, `{"value": "netflix.com", "policy": "group:流媒體"}`, "策略组「流媒體」不存在"},
		{`{"name": "流媒体"}`, `{"value": "netflix.com", "policy": "node"}`, `去向 "node" 不认识`},
	} {
		text := `{"policy_groups": [` + item.groups + `], "custom_rules": [` + item.rules + `], ` + profiles + `}`
		if _, err := parseConfig(text); err == nil || !strings.Contains(err.Error(), item.want) {
			t.Errorf("%s %s 应报错「%s」：%v", item.groups, item.rules, item.want, err)
		}
	}
	// 内核支持的写法：环视（常用来排除节点）、Unicode 类别、自己写的标志。
	for _, filter := range []string{`^(?!.*(游戏|Game)).*$`, `(?<=香港)\d+`, `\p{Han}+`, `(?-i)HK`, `[(?P<]`, `日本|JP|🇯🇵`} {
		if err := validateGroupFilter(filter); err != nil {
			t.Errorf("筛选 %q 应该可以用：%v", filter, err)
		}
	}
}

func TestCoreRegexpSyntax(t *testing.T) {
	for pattern, want := range map[string]string{
		`港|HK`:                    `港|HK`,
		`^(?!.*游戏).*$`:            `^(?:.*游戏).*$`,
		`(?<=a)b(?<!c)(?=d)(?>e)`: `(?:a)b(?:c)(?:d)(?:e)`,
		`\(?!x)`:                  `\(?!x)`,
		`[(?=]`:                   `[(?=]`,
		`[]a](?=b)`:               `[]a](?:b)`,
		`(?<name>港)`:              `(?<name>港)`,
	} {
		if got, ok := coreRegexpSyntax(pattern); !ok || got != want {
			t.Errorf("%q 应改写成 %q：%q %v", pattern, want, got, ok)
		}
	}
	for _, pattern := range []string{`(?P<x>a)`, `\Qa.b\E`} {
		if _, ok := coreRegexpSyntax(pattern); ok {
			t.Errorf("%q 内核不认，应拒绝", pattern)
		}
	}
	if coreGroupFilter("港|HK") != "(?i)港|HK" || coreGroupFilter("(?-i)HK") != "(?-i)HK" || coreGroupFilter("") != "" {
		t.Error("筛选默认不区分大小写，自己写了标志时照用")
	}
}

func TestPolicyGroupCoreConfig(t *testing.T) {
	groups := []PolicyGroup{
		{Name: "流媒体", Type: groupSelect, Filter: "港|HK", Node: "香港 01"},
		{Name: "Telegram", Type: groupUrlTest},
		{Name: "备用", Type: groupFallback, Filter: "(?-i)JP"},
		{Name: "分流", Type: groupLoadBalance},
	}
	names := []string{"流媒体", "Telegram", "备用", "分流"}
	settings := CoreSettings{
		Port: 17890, TestUrl: "https://example.com/generate_204", Mode: "global", Active: "pb2",
		Subscriptions: []CoreSubscription{{Id: "pa1"}, {Id: "pb2"}},
		PolicyGroups:  groups, GroupSource: "pb2",
		CustomRules: customRuleLines([]CustomRule{
			{Value: "netflix.com", Policy: "group:流媒体"},
			{Value: "t.me", Policy: "group:Telegram"},
			{Value: "example.org", Policy: "group:已删除"},
		}, names),
	}
	var config coreConfigFile
	if err := json.Unmarshal(coreConfigText(settings, "127.0.0.1:9090", "secret"), &config); err != nil {
		t.Fatal(err)
	}
	byName := map[string]coreGroup{}
	var order []string
	for _, group := range config.ProxyGroups {
		byName[group.Name] = group
		order = append(order, group.Name)
	}
	if strings.Join(order[len(order)-4:], ",") != "流媒体,Telegram,备用,分流" {
		t.Errorf("策略组应按配置里的顺序排在后面：%v", order)
	}
	if group := byName["流媒体"]; group.Type != "select" || strings.Join(group.Proxies, ",") != "ProxySwitch,pb2-auto,DIRECT" || strings.Join(group.Use, ",") != "pb2-nodes" || group.Filter != "(?i)港|HK" {
		t.Errorf("手动选择的组：跟随节点、自动选择、直连，加上正在使用的订阅里筛出来的节点：%+v", group)
	}
	if group := byName["Telegram"]; group.Type != "url-test" || len(group.Proxies) != 0 || group.Url != settings.TestUrl || group.Interval != groupHealthInterval || group.Tolerance != groupTolerance || !group.Lazy || group.Filter != "" {
		t.Errorf("自动选择的组只在节点里挑：%+v", group)
	}
	if group := byName["备用"]; group.Type != "fallback" || group.Filter != "(?-i)JP" || group.Url == "" {
		t.Errorf("故障转移的组：%+v", group)
	}
	if group := byName["分流"]; group.Type != "load-balance" || group.Strategy != "round-robin" {
		t.Errorf("负载均衡的组轮流用节点：%+v", group)
	}
	for _, rule := range []string{"DOMAIN-SUFFIX,netflix.com,流媒体", "DOMAIN-SUFFIX,t.me,Telegram", "DOMAIN-SUFFIX,example.org,ProxySwitch"} {
		if !contains(config.Rules, rule) {
			t.Errorf("自定义规则应指向策略组，组已删除时走节点：缺少 %s\n%v", rule, config.Rules)
		}
	}

	// 选中的成员在内核里的名字，和读回来时的写法。
	for node, member := range map[string]string{"": "ProxySwitch", "自动选择": "pb2-auto", "DIRECT": "DIRECT", "香港 01": "香港 01"} {
		if got := coreGroupMember(node, "pb2"); got != member {
			t.Errorf("%q 在内核里应是 %q：%q", node, member, got)
		}
		if got := groupNodeOf(member, "pb2"); got != node {
			t.Errorf("内核里的 %q 在配置里应写成 %q：%q", member, node, got)
		}
	}

	// 只为局域网共享运行（没有订阅）时不需要策略组。
	settings.Subscriptions, settings.GroupSource = nil, ""
	settings.Share = &CoreShare{Port: 17892, Allowed: []string{"192.168.1.0/24"}, Upstream: ShareUpstream{Kind: shareUpstreamDirect}}
	config = coreConfigFile{}
	if err := json.Unmarshal(coreConfigText(settings, "127.0.0.1:9090", "secret"), &config); err != nil {
		t.Fatal(err)
	}
	if len(config.ProxyGroups) != 0 || !reflect.DeepEqual(config.Rules, []string{"MATCH,DIRECT"}) {
		t.Errorf("没有订阅时不生成策略组：%+v %v", config.ProxyGroups, config.Rules)
	}
}

func TestEnginePolicyGroups(t *testing.T) {
	fixture, core, _ := newSubscriptionFixture(t, `{
  "policy_groups": [{"name": "流媒体", "type": "select", "filter": "港"}, {"name": "Telegram", "type": "url-test"}],
  "custom_rules": [{"value": "netflix.com", "policy": "group:流媒体"}],
  "profiles": [
    {"name": "本机", "server": "127.0.0.1:7890", "apply_to": ["system"]},
    {"name": "机场", "subscription": "https://sub.example.com/a"},
    {"name": "备用机场", "subscription": "https://sub.example.com/b"}
  ]
}`)
	engine := fixture.engine
	installFakeCoreBinary(t, engine)
	if engine.groupSource() != "" || len(engine.coreSettings().PolicyGroups) != 0 {
		t.Error("订阅还没下载时没有节点来源，不生成策略组")
	}
	first, second := *engine.Config().FindProfile("机场"), *engine.Config().FindProfile("备用机场")
	recordTestSubscription(t, engine, &second)
	recordTestSubscription(t, engine, &first)
	if err := engine.UseProfile("本机"); err != nil {
		t.Fatal(err)
	}
	// 用的不是订阅时，节点来自第一个已下载的订阅（内核的 ProxySwitch 组这时默认选它）。
	settings := core.last()
	if settings.GroupSource != first.Id || len(settings.PolicyGroups) != 2 || !contains(settings.CustomRules, "DOMAIN-SUFFIX,netflix.com,流媒体") {
		t.Errorf("策略组应交给内核，节点来自第一个订阅：%+v", settings)
	}
	if state := engine.settingsState(); state.Groups.Source != first.Id || state.Groups.States == nil {
		t.Errorf("设置页应知道节点来源：%+v", state.Groups)
	}
	// 用哪个订阅，策略组的节点就来自哪个订阅。
	if err := engine.UseProfile("备用机场"); err != nil {
		t.Fatal(err)
	}
	if source := core.last().GroupSource; source != second.Id {
		t.Errorf("换到另一个订阅后策略组的节点应来自它：%q", source)
	}

	// 手动选择的组记下选中的成员，内核随之切换；自动挑选的组不能手动选。
	if err := engine.SelectGroupNode("流媒体", "香港 01"); err != nil {
		t.Fatal(err)
	}
	reloaded, _, _ := loadConfig(engine.paths.Config)
	if reloaded.FindGroup("流媒体").Node != "香港 01" || core.last().PolicyGroups[0].Node != "香港 01" {
		t.Errorf("选中的成员应保存并交给内核：%+v", core.last().PolicyGroups)
	}
	syncs := len(core.history)
	if err := engine.SelectGroupNode("流媒体", "香港 01"); err != nil || len(core.history) != syncs+1 {
		t.Errorf("选中同一个成员时只让内核再切换一次：%v", err)
	}
	if err := engine.SelectGroupNode("Telegram", "香港 01"); err == nil || !strings.Contains(err.Error(), "不能手动选择") {
		t.Errorf("自动挑选的组不能手动选：%v", err)
	}
	if err := engine.SelectGroupNode("不存在", ""); err == nil {
		t.Error("不存在的组应报错")
	}
}

// 用真实的内核测策略组：规则指向组时流量经过组里选中的节点；跟随节点、自动选择、直连三个候选；选中的节点筛不到时跟随节点；
// 换订阅后组里的节点跟着换。
func TestCoreWithPolicyGroups(t *testing.T) {
	binary := requireCoreBinary(t)
	website := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		_, _ = io.WriteString(writer, "hello")
	}))
	defer website.Close()
	target := strings.TrimPrefix(website.URL, "http://")
	nodes := map[string]*countingProxy{}
	for _, name := range []string{"香港 01", "香港 02", "日本 01", "美国 01"} {
		nodes[name] = startCountingProxy(t, target)
	}
	dir := t.TempDir()
	if err := writeSubscriptionFile(dir, "pa", []byte(nodesYaml(nodes, "香港 01", "香港 02", "日本 01"))); err != nil {
		t.Fatal(err)
	}
	if err := writeSubscriptionFile(dir, "pb", []byte(nodesYaml(nodes, "美国 01"))); err != nil {
		t.Fatal(err)
	}
	core := newCore(nil)
	defer core.Stop()
	port, _ := freeProxyPort()
	groups := []PolicyGroup{{Name: "流媒体", Type: groupSelect, Filter: "香港", Node: "香港 02"}, {Name: "日本", Type: groupUrlTest, Filter: "日本|JP"}}
	settings := CoreSettings{
		Binary: binary, Dir: dir, Port: port, TestUrl: "http://" + coreTestHost + "/", Active: "pa", Mode: "rule",
		Subscriptions: []CoreSubscription{{Id: "pa", Node: "日本 01", Revision: "1"}, {Id: "pb", Revision: "1"}},
		PolicyGroups:  groups, GroupSource: "pa",
		CustomRules: customRuleLines([]CustomRule{{Value: coreTestHost, Policy: "group:流媒体"}}, []string{"流媒体", "日本"}),
	}
	used := func(name string) bool {
		before := map[string]int32{}
		for node, proxy := range nodes {
			before[node] = proxy.connections.Load()
		}
		status, body, err := requestThroughCore(port, coreTestHost)
		for node, proxy := range nodes {
			if (node == name) != (proxy.connections.Load() != before[node]) {
				t.Errorf("应只经过 %q，%q 的连接数 %d→%d（%d %q %v）", name, node, before[node], proxy.connections.Load(), status, body, err)
				return false
			}
		}
		return name == "" || status == http.StatusOK && body == "hello"
	}
	apply := func() {
		t.Helper()
		if err := core.Wait(core.Sync(settings), 30*time.Second); err != nil {
			t.Fatal(err)
		}
	}
	apply()
	if !used("香港 02") {
		t.Error("规则指向策略组时应经过组里选中的节点")
	}
	states, err := core.GroupStates(groups, "pa", coreApiTimeout)
	if err != nil || len(states) != 2 {
		t.Fatalf("应读到两个策略组：%+v %v", states, err)
	}
	var values, labels []string
	for _, member := range states[0].Members {
		values, labels = append(values, member.Value), append(labels, member.Label)
	}
	if states[0].Now != "香港 02" || states[0].Current != "香港 02" || strings.Join(values, ",") != ",自动选择,DIRECT,香港 01,香港 02" || strings.Join(labels, ",") != "跟随节点,自动选择,直连,香港 01,香港 02" {
		t.Errorf("手动选择的组的状态不对：%+v", states[0])
	}
	if states[1].Type != groupUrlTest || states[1].Current != "日本 01" || len(states[1].Members) != 1 || !states[1].Members[0].Node {
		t.Errorf("自动选择的组只有筛出来的节点：%+v", states[1])
	}

	// 跟随节点：和订阅选中的节点一样。
	settings.PolicyGroups[0].Node = ""
	apply()
	if !used("日本 01") {
		t.Error("跟随节点时应经过订阅选中的节点")
	}
	if states, _ := core.GroupStates(settings.PolicyGroups, "pa", coreApiTimeout); states[0].Now != "" || states[0].Current != "日本 01" {
		t.Errorf("跟随节点时应报告实际在用的节点：%+v", states[0])
	}
	// 自动选择：订阅的自动选择挑中的节点。
	settings.PolicyGroups[0].Node = groupMemberAuto
	apply()
	if states, _ := core.GroupStates(settings.PolicyGroups, "pa", coreApiTimeout); states[0].Now != groupMemberAuto || states[0].Current == "" || states[0].Current == coreAutoGroup("pa") {
		t.Errorf("自动选择时应报告它挑中的节点：%+v", states[0])
	}
	// 直连：不经过任何节点（测试域名直连解析不了，访问失败）。
	settings.PolicyGroups[0].Node = groupMemberDirect
	apply()
	used("")
	// 选中的节点筛不到（或已下线）：跟随节点，不算出错。
	settings.PolicyGroups[0].Node = "日本 01"
	apply()
	if !used("日本 01") {
		t.Error("选中的节点不在组里时应跟随节点")
	}
	if states, _ := core.GroupStates(settings.PolicyGroups, "pa", coreApiTimeout); states[0].Now != "" {
		t.Errorf("选中的节点不在组里时应改为跟随节点：%+v", states[0])
	}

	// 换订阅：组里的节点来自新的订阅，选中的节点还在时照用。
	settings.Active, settings.GroupSource = "pb", "pb"
	settings.PolicyGroups[0] = PolicyGroup{Name: "流媒体", Type: groupSelect, Node: "美国 01"}
	apply()
	if !used("美国 01") {
		t.Error("换订阅后策略组应用新订阅里的节点")
	}
}
