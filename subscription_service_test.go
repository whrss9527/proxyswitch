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
