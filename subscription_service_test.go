package main

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// 从设置页的接口一路到真实的内核：下载订阅、列出节点、开启、切换节点、测速、检查订阅地址。
func TestSettingsSubscriptionFlow(t *testing.T) {
	binary := requireCoreBinary(t)
	website := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		if request.URL.Path == "/generate_204" {
			writer.WriteHeader(http.StatusNoContent)
			return
		}
		_, _ = io.WriteString(writer, "hello")
	}))
	defer website.Close()
	target := strings.TrimPrefix(website.URL, "http://")
	nodes := map[string]*countingProxy{"节点 A": startCountingProxy(t, target), "节点 B": startCountingProxy(t, target)}
	subscription := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		writer.Header().Set("subscription-userinfo", "upload=1024; download=2048; total=1073741824; expire=1798761600")
		writer.Header().Set("Content-Disposition", "attachment; filename*=UTF-8''%E6%B5%8B%E8%AF%95%E6%9C%BA%E5%9C%BA.yaml")
		_, _ = io.WriteString(writer, nodesYaml(nodes, "节点 A", "节点 B"))
	}))
	defer subscription.Close()

	port, _ := freeLocalPort()
	configText := fmt.Sprintf(`{"test_url": "http://%s/generate_204", "core": {"port": %d}, "profiles": [
		{"name": "机场", "subscription": %q, "node": "节点 B", "mode": "global", "apply_to": ["system"]}
	]}`, coreTestHost, port, subscription.URL+"/sub?token=abc")
	fixture := newSettingsFixture(t, configText)
	defer fixture.backend.Close()
	if err := linkDevCore(fixture.backend.engine.paths, binary); err != nil {
		t.Fatal(err)
	}
	profileId := fixture.backend.engine.Config().Profiles[0].Id
	base := "/api/subscriptions/" + profileId

	// 还没下载订阅时不能开启。
	status, data := fixture.request(t, "POST", "/api/on", nil, nil)
	if status != http.StatusConflict || !strings.Contains(string(data), "订阅") {
		t.Errorf("订阅还没下载时开启应失败：%d %s", status, data)
	}

	status, data = fixture.request(t, "POST", base+"/update", nil, nil)
	if status != http.StatusOK {
		t.Fatalf("下载订阅失败：%d %s", status, data)
	}
	state := fixture.state(t, data)
	if info := state.Subscriptions[profileId]; info.Nodes != 2 || info.Total != 1<<30 || info.Error != "" {
		t.Errorf("订阅信息不对：%+v", info)
	}
	if !state.Core.Installed || !state.Core.Running || state.Core.Port != port || state.Core.Downloadable {
		t.Errorf("内核状态不对：%+v", state.Core)
	}

	var list CoreNodes
	status, data = fixture.request(t, "GET", base+"/nodes", nil, nil)
	if err := json.Unmarshal(data, &list); status != http.StatusOK || err != nil || len(list.Nodes) != 2 || list.Selected != "节点 B" {
		t.Fatalf("节点列表不对：%d %s", status, data)
	}

	status, data = fixture.request(t, "POST", "/api/on", nil, nil)
	if state := fixture.state(t, data); status != http.StatusOK || state.Status.State != "on" {
		t.Fatalf("开启失败：%d %s", status, data)
	}
	if fixture.backend.system.System.Server != fmt.Sprintf("127.0.0.1:%d", port) {
		t.Errorf("系统代理应指向内核：%+v", fixture.backend.system.System)
	}
	before := nodes["节点 B"].connections.Load()
	if body := getThroughCore(t, port); body != "hello" || nodes["节点 B"].connections.Load() == before {
		t.Errorf("流量应经过选中的节点 B：%q", body)
	}

	status, data = fixture.request(t, "POST", base+"/select", map[string]string{"node": "节点 A"}, nil)
	if err := json.Unmarshal(data, &list); status != http.StatusOK || err != nil || list.Selected != "节点 A" || list.Current != "节点 A" {
		t.Fatalf("切换节点失败：%d %s", status, data)
	}
	if fixture.backend.engine.Config().Profiles[0].Node != "节点 A" {
		t.Error("选中的节点应保存到配置")
	}
	before = nodes["节点 A"].connections.Load()
	if getThroughCore(t, port); nodes["节点 A"].connections.Load() == before {
		t.Error("切换后流量应经过节点 A")
	}

	status, data = fixture.request(t, "POST", base+"/test", nil, nil)
	if err := json.Unmarshal(data, &list); status != http.StatusOK || err != nil || !list.Nodes[0].Tested || !list.Nodes[0].Alive {
		t.Errorf("测速结果不对：%d %s", status, data)
	}

	var check SubscriptionCheck
	status, data = fixture.request(t, "POST", "/api/subscriptions/check", map[string]string{"url": subscription.URL + "/other"}, nil)
	if err := json.Unmarshal(data, &check); status != http.StatusOK || err != nil || check.Nodes != 2 || check.Format != "clash" || check.Name != "测试机场" || check.Total != 1<<30 {
		t.Errorf("检查订阅的结果不对：%d %s", status, data)
	}
	if status, data := fixture.request(t, "POST", "/api/subscriptions/check", map[string]string{"url": website.URL + "/"}, nil); status != http.StatusBadGateway || !strings.Contains(string(data), "既不是") {
		t.Errorf("不是订阅内容时应说明：%d %s", status, data)
	}
	if status, data := fixture.request(t, "POST", "/api/core/install", nil, nil); status != http.StatusConflict || !strings.Contains(string(data), "不能在程序里下载") {
		t.Errorf("开发模式不能下载内核：%d %s", status, data)
	}
	if status, _ := fixture.request(t, "GET", "/api/subscriptions/nothing/nodes", nil, nil); status != http.StatusConflict {
		t.Errorf("不存在的订阅应报错：%d", status)
	}
}

func TestDelaysNotice(t *testing.T) {
	profile := Profile{Name: "机场", Color: "#16a34a"}
	notice := delaysNotice(profile, map[string]int{"香港 01": 80, "日本 01": 35, "美国 01": 35}, nil)
	if notice.Level != noticeInfo || notice.Title != "机场：3 个节点能用" || notice.Text != "最快：日本 01 35 ms" {
		t.Errorf("测速结果的通知不对：%+v", notice)
	}
	if notice := delaysNotice(profile, map[string]int{"本机": 0}, nil); notice.Text != "最快：本机 1 ms" {
		t.Errorf("本机测得 0 ms 时应显示 1 ms：%+v", notice)
	}
	if notice := delaysNotice(profile, map[string]int{}, nil); notice.Level != noticeWarning || !strings.Contains(notice.Title, "没有能用的节点") {
		t.Errorf("没有能用的节点时应提醒：%+v", notice)
	}
	if notice := delaysNotice(profile, nil, fmt.Errorf("代理内核没有运行")); notice.Level != noticeWarning || !strings.Contains(notice.Text, "代理内核没有运行") {
		t.Errorf("测速失败时应说明原因：%+v", notice)
	}
}

// 分流规则：检查规则地址；下载规则和它引用的规则列表，内核按规则把流量交给节点、拦截或直连；切换到全局代理后全部走节点。
func TestSettingsRulesFlow(t *testing.T) {
	binary := requireCoreBinary(t)
	website := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		_, _ = io.WriteString(writer, "hello")
	}))
	defer website.Close()
	node := startCountingProxy(t, strings.TrimPrefix(website.URL, "http://"))
	rules := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		switch request.URL.Path {
		case "/rules.conf":
			fmt.Fprintf(writer, "[General]\nipv6 = false\n[Rule]\nDOMAIN,blocked.%s,Reject\nRULE-SET,http://%s/proxy.list,Proxy\nUSER-AGENT,Telegram*,PROXY\nFINAL,direct\n", coreTestHost, request.Host)
		case "/proxy.list":
			// 条数够多，转换成规则集文件，检验内核能读取它。
			for index := 0; index < ruleSetMinimum; index++ {
				fmt.Fprintf(writer, "DOMAIN-SUFFIX,site%d.example\n", index)
			}
			fmt.Fprintf(writer, "DOMAIN-SUFFIX,%s\n", coreTestHost)
		default:
			writer.WriteHeader(http.StatusNotFound)
		}
	}))
	defer rules.Close()
	subscription := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		_, _ = io.WriteString(writer, nodesYaml(map[string]*countingProxy{"节点 A": node}, "节点 A"))
	}))
	defer subscription.Close()

	port, _ := freeLocalPort()
	configText := fmt.Sprintf(`{"test_url": "http://%s/", "core": {"port": %d}, "profiles": [
		{"name": "机场", "subscription": %q, "rules": %q, "apply_to": ["system"]}
	]}`, coreTestHost, port, subscription.URL, rules.URL+"/rules.conf")
	fixture := newSettingsFixture(t, configText)
	defer fixture.backend.Close()
	if err := linkDevCore(fixture.backend.engine.paths, binary); err != nil {
		t.Fatal(err)
	}
	profileId := fixture.backend.engine.Config().Profiles[0].Id
	base := "/api/subscriptions/" + profileId

	var check RulesCheck
	status, data := fixture.request(t, "POST", "/api/rules/check", map[string]string{"url": rules.URL + "/rules.conf"}, nil)
	if err := json.Unmarshal(data, &check); status != http.StatusOK || err != nil || check.Rules != 1 || check.Sets != 1 || check.Skipped != 1 || check.Final != rulePolicyDirect {
		t.Errorf("检查规则的结果不对：%d %s", status, data)
	}
	if status, data := fixture.request(t, "POST", "/api/rules/check", map[string]string{"url": rules.URL + "/missing.conf"}, nil); status != http.StatusBadGateway || !strings.Contains(string(data), "404") {
		t.Errorf("规则地址不存在时应说明：%d %s", status, data)
	}

	if status, data := fixture.request(t, "POST", base+"/update", nil, nil); status != http.StatusOK {
		t.Fatalf("下载订阅失败：%d %s", status, data)
	}
	status, data = fixture.request(t, "POST", base+"/rules", nil, nil)
	if status != http.StatusOK {
		t.Fatalf("下载规则失败：%d %s", status, data)
	}
	state := fixture.state(t, data)
	if info := state.Rules[profileId]; info.Rules != 2+ruleSetMinimum || info.Sets != 1 || info.FailedSets != 0 || info.Skipped != 1 || info.Final != rulePolicyDirect || info.Error != "" || info.Updated == "" {
		t.Errorf("规则信息不对：%+v", info)
	}
	if len(state.RulePresets) == 0 || !strings.HasSuffix(state.RulePresets[0].Url, ".conf") {
		t.Errorf("应提供可以直接选的规则：%+v", state.RulePresets)
	}
	if status, data := fixture.request(t, "POST", "/api/on", nil, nil); status != http.StatusOK {
		t.Fatalf("开启失败：%d %s", status, data)
	}

	through := func(host string) (bool, string) {
		t.Helper()
		before := node.connections.Load()
		status, body, err := requestThroughCore(port, host)
		return node.connections.Load() != before, fmt.Sprintf("%d %s %v", status, body, err)
	}
	if viaNode, result := through(coreTestHost); !viaNode || result != "200 hello <nil>" {
		t.Errorf("规则列表里的网站应经过节点：%s", result)
	}
	if viaNode, result := through("blocked." + coreTestHost); viaNode || strings.Contains(result, "hello") {
		t.Errorf("被拦截的网站不应经过节点：%s", result)
	}
	if viaNode, _ := through("other.invalid"); viaNode {
		t.Error("其余网站按 FINAL 直连，不应经过节点")
	}

	status, data = fixture.request(t, "POST", base+"/mode", map[string]string{"mode": "global"}, nil)
	if state := fixture.state(t, data); status != http.StatusOK || state.Config.Profiles[0].Mode != "global" {
		t.Fatalf("切换到全局代理失败：%d %s", status, data)
	}
	if viaNode, result := through("other.invalid"); !viaNode || result != "200 hello <nil>" {
		t.Errorf("全局代理时所有网站都应经过节点：%s", result)
	}
	if status, _ := fixture.request(t, "POST", base+"/mode", map[string]string{"mode": "rule"}, nil); status != http.StatusOK {
		t.Fatal("切回按规则分流失败")
	}
	if viaNode, _ := through("blocked." + coreTestHost); viaNode {
		t.Error("切回按规则分流后应重新拦截")
	}
	if status, data := fixture.request(t, "POST", base+"/mode", map[string]string{"mode": "fast"}, nil); status == http.StatusOK || !strings.Contains(string(data), "fast") {
		t.Errorf("不认识的模式应报错：%d %s", status, data)
	}
}
