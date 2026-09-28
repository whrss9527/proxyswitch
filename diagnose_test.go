package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"reflect"
	"strconv"
	"strings"
	"testing"
	"time"
)

func TestNormalizeDiagnoseUrl(t *testing.T) {
	for input, want := range map[string]string{
		"youtube.com":                 "https://youtube.com/",
		" https://WWW.YouTube.com/x ": "https://www.youtube.com/x",
		"http://example.com:8080":     "http://example.com:8080/",
		"1.2.3.4":                     "https://1.2.3.4/",
	} {
		if got, err := normalizeDiagnoseUrl(input); err != nil || got.String() != want {
			t.Errorf("%q 应整理成 %s：%v %v", input, want, got, err)
		}
	}
	for _, input := range []string{"", "you tube.com", "ftp://example.com", "https://"} {
		if _, err := normalizeDiagnoseUrl(input); err == nil {
			t.Errorf("%q 应报错", input)
		}
	}
	target, _ := normalizeDiagnoseUrl("http://example.com")
	if diagnosePort(target) != 80 {
		t.Error("http 默认 80 端口")
	}
}

func TestParseRouteTrace(t *testing.T) {
	cases := map[string]*RouteTrace{
		"[TCP] 127.0.0.1:50000 --> www.youtube.com:443 match RuleSet(proxy-1) using ProxySwitch[香港 01 (IPLC)]": {Host: "www.youtube.com", Port: 443, Rule: "RuleSet(proxy-1)", Chain: "ProxySwitch[香港 01 (IPLC)]"},
		"[TCP] 127.0.0.1:50000(chrome.exe) --> example.com:80 match Match using DIRECT":                        {Host: "example.com", Port: 80, Rule: "Match", Chain: "DIRECT"},
		"[TCP] 127.0.0.1:50000 --> example.com:443 doesn't match any rule using DIRECT":                        {Host: "example.com", Port: 443, Rule: "没有命中任何规则", Chain: "DIRECT"},
		"[TCP] 127.0.0.1:50000 --> [2001:db8::1]:443 using GLOBAL":                                             {Host: "2001:db8::1", Port: 443, Chain: "GLOBAL"},
		"[TCP] dial ProxySwitch (match Match/) 127.0.0.1:50000 --> www.google.com:443 error: connect failed":   {Host: "www.google.com", Port: 443, Rule: "Match", Chain: "ProxySwitch", Error: "connect failed"},
		"[TCP] dial DIRECT (match DomainSuffix/cn) 127.0.0.1:50000 --> a.cn:443 error: i/o timeout":            {Host: "a.cn", Port: 443, Rule: "DomainSuffix(cn)", Chain: "DIRECT", Error: "i/o timeout"},
		"[TCP] dial 上游代理 127.0.0.1:50000 --> Example.COM:443 error: refused":                                   {Host: "example.com", Port: 443, Chain: "上游代理", Error: "refused"},
		"[UDP] 127.0.0.1:50000 --> 8.8.8.8:53 match Match using DIRECT":                                        nil,
		"Start initial configuration in progress":                                                              nil,
	}
	for line, want := range cases {
		if got := parseRouteTrace(line); !reflect.DeepEqual(got, want) {
			t.Errorf("%s\n解析成 %+v，应为 %+v", line, got, want)
		}
	}
	trace := parseRouteTrace("[TCP] 127.0.0.1:1 --> a.com:443 match Match using ProxySwitch[香港 01]")
	if trace.Outbound() != "香港 01" || trace.IsDirect() {
		t.Errorf("出口应是节点：%s", trace.Outbound())
	}
	if trace := parseRouteTrace("[TCP] 127.0.0.1:1 --> a.com:443 match Match using DIRECT"); !trace.IsDirect() {
		t.Error("应是直连")
	}
}

// 访问失败时按原因归类：被拒绝、超时、被中断、TLS 握手失败、域名解析失败。
func TestProbeUrl(t *testing.T) {
	ctx := context.Background()
	website := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		writer.WriteHeader(http.StatusNotFound)
	}))
	defer website.Close()
	target, _ := url.Parse(website.URL + "/missing")
	if probe := probeUrl(ctx, target, nil, 3*time.Second); !probe.Ok || probe.Status != http.StatusNotFound || !strings.HasPrefix(probe.Summary(), "HTTP 404，") {
		t.Errorf("网站有响应就算通：%+v", probe)
	}

	closedPort, _ := freeLocalPort()
	target, _ = url.Parse("http://127.0.0.1:" + strconv.Itoa(closedPort) + "/")
	if probe := probeUrl(ctx, target, nil, 3*time.Second); probe.Ok || probe.Failure != "连接被拒绝" {
		t.Errorf("端口没在监听：%+v", probe)
	}

	silent, _ := net.Listen("tcp", "127.0.0.1:0")
	defer silent.Close()
	go func() {
		for {
			connection, err := silent.Accept()
			if err != nil {
				return
			}
			defer connection.Close()
		}
	}()
	target, _ = url.Parse("http://" + silent.Addr().String() + "/")
	if probe := probeUrl(ctx, target, nil, 300*time.Millisecond); probe.Failure != "超时，没有响应" {
		t.Errorf("没有响应：%+v", probe)
	}

	closing, _ := net.Listen("tcp", "127.0.0.1:0")
	defer closing.Close()
	go func() {
		for {
			connection, err := closing.Accept()
			if err != nil {
				return
			}
			connection.Close()
		}
	}()
	target, _ = url.Parse("https://" + closing.Addr().String() + "/")
	if probe := probeUrl(ctx, target, nil, 3*time.Second); !strings.HasPrefix(probe.Failure, "连接被中断") {
		t.Errorf("连上就被关掉：%+v", probe)
	}

	secure := httptest.NewTLSServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
	defer secure.Close()
	target, _ = url.Parse(secure.URL + "/")
	if probe := probeUrl(ctx, target, nil, 3*time.Second); !strings.HasPrefix(probe.Failure, "证书校验失败") {
		t.Errorf("证书不受信任：%+v", probe)
	}
	plain := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
	defer plain.Close()
	target, _ = url.Parse(strings.Replace(plain.URL, "http://", "https://", 1) + "/")
	if probe := probeUrl(ctx, target, nil, 3*time.Second); !strings.HasPrefix(probe.Failure, "TLS 握手失败") {
		t.Errorf("对方不是 HTTPS：%+v", probe)
	}

	target, _ = url.Parse("https://proxyswitch-diagnose.invalid/")
	if probe := probeUrl(ctx, target, nil, 3*time.Second); probe.Failure != "域名解析失败" {
		t.Errorf("域名不存在：%+v", probe)
	}

	// 经代理：代理连不上网站时返回 502，算不通；代理本身连不上时说明。
	gateway := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		writer.WriteHeader(http.StatusBadGateway)
	}))
	defer gateway.Close()
	proxy, _ := url.Parse(gateway.URL)
	target, _ = url.Parse("http://example.com/")
	if probe := probeUrl(ctx, target, proxy, 3*time.Second); probe.Ok || !strings.Contains(probe.Failure, "HTTP 502") {
		t.Errorf("代理返回 502：%+v", probe)
	}
	proxy, _ = url.Parse("http://127.0.0.1:" + strconv.Itoa(closedPort))
	if probe := probeUrl(ctx, target, proxy, 3*time.Second); !strings.HasPrefix(probe.Failure, "连不上代理") {
		t.Errorf("代理没在运行：%+v", probe)
	}
}

// 经真实的内核访问，从它的日志里找出这次连接的判定；测单个节点的延迟。
func TestCoreTraceConnection(t *testing.T) {
	binary := requireCoreBinary(t)
	// 网站稍慢一点：内核把测得 0 ms 的延迟当作失败。
	website := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		time.Sleep(5 * time.Millisecond)
		writer.WriteHeader(http.StatusNoContent)
	}))
	defer website.Close()
	node := startCountingProxy(t, strings.TrimPrefix(website.URL, "http://"))
	deadPort, _ := freeLocalPort()
	dir := t.TempDir()
	subscription := fmt.Sprintf("proxies:\n  - {name: \"节点 A\", type: http, server: 127.0.0.1, port: %d}\n  - {name: \"坏节点\", type: http, server: 127.0.0.1, port: %d}\n", node.Port(), deadPort)
	if err := writeSubscriptionFile(dir, "pa", []byte(subscription)); err != nil {
		t.Fatal(err)
	}
	core := newCore(nil)
	defer core.Stop()
	port, _ := freeLocalPort()
	testUrl := "http://" + coreTestHost + "/generate_204"
	settings := CoreSettings{Binary: binary, Dir: dir, Port: port, TestUrl: testUrl, Active: "pa", Mode: "rule",
		Subscriptions: []CoreSubscription{{Id: "pa", Node: "节点 A", Revision: "1"}}, CustomRules: []string{"DOMAIN-SUFFIX,blocked." + coreTestHost + ",REJECT"}}
	if err := core.Wait(core.Sync(settings), 30*time.Second); err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	proxy := &url.URL{Scheme: "http", Host: "127.0.0.1:" + strconv.Itoa(port)}
	visit := func(host string) (DiagnoseProbe, *RouteTrace) {
		target, _ := url.Parse("http://" + host + "/")
		return core.TraceConnection(ctx, host, 80, func() DiagnoseProbe { return probeUrl(ctx, target, proxy, 5*time.Second) })
	}
	probe, trace := visit(coreTestHost)
	if !probe.Ok || trace == nil || trace.Chain != "ProxySwitch[节点 A]" || trace.Outbound() != "节点 A" || trace.Rule != "Match" {
		t.Errorf("应找到这次连接走的节点和规则：%+v %+v", probe, trace)
	}
	if delay := core.NodeDelay("pa", "节点 A", testUrl); delay <= 0 {
		t.Errorf("能用的节点应测出延迟：%d", delay)
	}
	probe, trace = visit("blocked." + coreTestHost)
	if probe.Ok || trace == nil || trace.Outbound() != "REJECT" || trace.Rule != "DomainSuffix(blocked."+coreTestHost+")" {
		t.Errorf("被拦截的网站：%+v %+v", probe, trace)
	}

	settings.Subscriptions[0].Node = "坏节点"
	if err := core.Wait(core.Sync(settings), 30*time.Second); err != nil {
		t.Fatal(err)
	}
	probe, trace = visit(coreTestHost)
	if probe.Ok || trace == nil || trace.Error == "" || trace.Chain != "ProxySwitch" {
		t.Errorf("节点连不上时应找到内核的报错：%+v %+v", probe, trace)
	}
	if delay := core.NodeDelay("pa", "坏节点", testUrl); delay != 0 {
		t.Errorf("连不上的节点延迟应为 0：%d", delay)
	}
	core.Stop()
	if probe, trace := visit(coreTestHost); probe.Ok || trace != nil {
		t.Errorf("内核没有运行时只访问：%+v %+v", probe, trace)
	}
}

func TestDiagnoseVerdict(t *testing.T) {
	ok := &DiagnoseProbe{Ok: true, Status: 204, Millis: 120}
	failed := &DiagnoseProbe{Failure: "连接被中断（常见于被屏蔽）"}
	zero, fast := 0, 80
	none, some := 0, 3
	kinds := func(verdict DiagnoseVerdict) string {
		var list []string
		for _, action := range verdict.Actions {
			list = append(list, action.Kind)
		}
		return strings.Join(list, ",")
	}
	cases := []struct {
		name     string
		facts    DiagnoseFacts
		headline string
		actions  string
	}{
		{"设备视角共享没在监听", DiagnoseFacts{Perspective: diagnoseDevice}, "共享入口没在监听", "open_share"},
		{"没开代理直连正常", DiagnoseFacts{Perspective: diagnosePc, Route: "off", Direct: ok}, "直连正常，本机没开代理", "copy_report"},
		{"没开代理直连不通，有订阅", DiagnoseFacts{Perspective: diagnosePc, Route: "off", Direct: failed, TurnOn: "机场"}, "本机没开代理，直连又打不开", "turn_on,copy_report"},
		{"没开代理直连不通，没有订阅", DiagnoseFacts{Perspective: diagnosePc, Route: "off", Direct: failed}, "本机没开代理，直连又打不开", "open_proxies,copy_report"},
		{"链路正常", DiagnoseFacts{Perspective: diagnosePc, Route: "subscription", UsesCore: true, Proxied: ok, ProxiedVia: "订阅配置", Trace: &RouteTrace{Rule: "Match", Chain: "ProxySwitch[香港 01]"}}, "链路正常", "copy_report"},
		{"设备最近没访问", DiagnoseFacts{Perspective: diagnoseDevice, ShareListening: true, Proxied: ok, DeviceConnections: &none}, "从这台电脑看链路是通的，但设备最近没有访问它", "open_share,copy_report"},
		{"设备访问过", DiagnoseFacts{Perspective: diagnoseDevice, ShareListening: true, Proxied: ok, DeviceConnections: &some}, "链路正常", "copy_report"},
		{"规则直连但不通", DiagnoseFacts{Perspective: diagnosePc, Host: "x.com", UsesCore: true, Proxied: failed, Direct: failed, Trace: &RouteTrace{Rule: "GeoIP(CN)", Chain: "DIRECT"}}, "规则把它分到了直连，但直连不通", "pin_to_proxy,copy_report"},
		{"共享直连但不通", DiagnoseFacts{Perspective: diagnoseDevice, ShareListening: true, Proxied: failed, TurnOn: "机场", Trace: &RouteTrace{Rule: "Match", Chain: "DIRECT"}}, "直连不通", "turn_on,copy_report"},
		{"转发给本机的代理失败", DiagnoseFacts{Perspective: diagnoseDevice, ShareListening: true, Proxied: failed, Trace: &RouteTrace{Rule: "Match", Chain: coreUpstreamProxy, Error: "refused"}}, "转发给本机的代理失败", "copy_report"},
		{"被拦截", DiagnoseFacts{Perspective: diagnosePc, Host: "ads.com", UsesCore: true, Proxied: failed, Trace: &RouteTrace{Rule: "RuleSet(reject-1)", Chain: "REJECT"}}, "规则拦截了这个网站", "pin_to_proxy,copy_report"},
		{"节点连不上", DiagnoseFacts{Perspective: diagnosePc, UsesCore: true, Proxied: failed, NodeDelay: &zero, Trace: &RouteTrace{Rule: "Match", Chain: "ProxySwitch", Error: "i/o timeout"}}, "当前节点连不上", "auto_select,test_nodes,copy_report"},
		{"节点通但网站不通", DiagnoseFacts{Perspective: diagnosePc, UsesCore: true, Proxied: failed, NodeDelay: &fast, Trace: &RouteTrace{Rule: "Match", Chain: "ProxySwitch[日本 01]"}}, "节点能通，但这个网站经它打不开", "open_nodes,copy_report"},
		{"内核没记录判定", DiagnoseFacts{Perspective: diagnosePc, UsesCore: true, Proxied: failed}, "经代理访问失败", "copy_report"},
		{"其他代理失败", DiagnoseFacts{Perspective: diagnosePc, Route: "profile", ProxiedVia: "「公司」（10.0.0.1:8080）", Proxied: failed, TurnOn: "机场"}, "经「公司」（10.0.0.1:8080）访问失败", "turn_on,copy_report"},
	}
	for _, item := range cases {
		verdict := diagnoseVerdict(item.facts)
		if verdict.Headline != item.headline || kinds(verdict) != item.actions || verdict.Explanation == "" {
			t.Errorf("%s：%q %s\n%s", item.name, verdict.Headline, kinds(verdict), verdict.Explanation)
		}
	}
	if verdict := diagnoseVerdict(cases[7].facts); verdict.Actions[0].Host != "x.com" || !strings.Contains(verdict.Actions[0].Label, "x.com") {
		t.Errorf("让网站走节点的操作应带上域名：%+v", verdict.Actions[0])
	}
	report := diagnoseReport(DiagnoseJob{Url: "https://x.com/", Perspective: diagnosePc, Rows: []DiagnoseRow{{Title: "直接访问", Outcome: "fail", Summary: "不通", Detail: "细节"}}, Verdict: &DiagnoseVerdict{Headline: "结论", Explanation: "解释"}})
	if !strings.Contains(report, "https://x.com/（这台电脑") || !strings.Contains(report, "✗ 直接访问：不通（细节）") || !strings.Contains(report, "结论：结论\n解释") {
		t.Errorf("诊断报告：\n%s", report)
	}
}

// 从设置页的接口诊断网址：没开代理、用订阅、局域网设备的视角，停止诊断。
func TestSettingsDiagnoseFlow(t *testing.T) {
	sharePort, _ := freeLocalPort()
	corePort, _ := freeLocalPort()
	fixture := newSettingsFixture(t, fmt.Sprintf(`{"core": {"port": %d}, "share": {"port": %d}, "profiles": []}`, corePort, sharePort))
	defer fixture.backend.Close()
	diagnose := func(address, perspective string) DiagnoseJob {
		t.Helper()
		status, data := fixture.request(t, "POST", "/api/diagnose", map[string]string{"url": address, "perspective": perspective}, nil)
		if status != http.StatusOK {
			t.Fatalf("开始诊断失败：%d %s", status, data)
		}
		var job DiagnoseJob
		for deadline := time.Now().Add(40 * time.Second); time.Now().Before(deadline); time.Sleep(100 * time.Millisecond) {
			_, data = fixture.request(t, "GET", "/api/diagnose", nil, nil)
			if err := json.Unmarshal(data, &job); err != nil {
				t.Fatal(err)
			}
			if !job.Running {
				return job
			}
		}
		t.Fatalf("诊断没有结束：%+v", job)
		return job
	}
	row := func(job DiagnoseJob, id string) DiagnoseRow {
		for _, item := range job.Rows {
			if item.Id == id {
				return item
			}
		}
		return DiagnoseRow{}
	}
	if status, data := fixture.request(t, "POST", "/api/diagnose", map[string]string{"url": "not a url"}, nil); status != http.StatusBadRequest || !strings.Contains(string(data), "认不出这个网址") {
		t.Errorf("认不出的网址应报错：%d %s", status, data)
	}
	// 本机没开代理：直连打不开，还没有订阅。
	job := diagnose("http://"+coreTestHost+"/", diagnosePc)
	if job.Verdict == nil || job.Verdict.Headline != "本机没开代理，直连又打不开" || row(job, "status").Outcome != "warn" || row(job, "proxied").Outcome != "skipped" || !strings.Contains(job.Report, "结论：") {
		t.Errorf("本机没开代理时：%+v", job)
	}
	if job := diagnose("http://"+coreTestHost+"/", diagnoseDevice); job.Verdict == nil || job.Verdict.Headline != "共享入口没在监听" || row(job, "status").Summary != "局域网共享没有开启" {
		t.Errorf("没开共享时从设备的视角看：%+v", job)
	}

	binary := requireCoreBinary(t)
	website := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		time.Sleep(5 * time.Millisecond)
		writer.WriteHeader(http.StatusNoContent)
	}))
	defer website.Close()
	nodes := map[string]*countingProxy{"节点 A": startCountingProxy(t, strings.TrimPrefix(website.URL, "http://"))}
	subscription := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		_, _ = io.WriteString(writer, nodesYaml(nodes, "节点 A"))
	}))
	defer subscription.Close()
	if err := linkDevCore(fixture.backend.engine.paths, binary); err != nil {
		t.Fatal(err)
	}
	config := fixture.backend.engine.Config().Clone()
	config.TestUrl = "http://" + coreTestHost + "/generate_204"
	config.Profiles = []Profile{{Name: "机场", Subscription: subscription.URL, ApplyTo: []string{targetSystem}}}
	if status, data := fixture.request(t, "PUT", "/api/config", config, nil); status != http.StatusOK {
		t.Fatalf("保存配置失败：%d %s", status, data)
	}
	profileId := fixture.backend.engine.Config().Profiles[0].Id
	if status, data := fixture.request(t, "POST", "/api/subscriptions/"+profileId+"/update", nil, nil); status != http.StatusOK {
		t.Fatalf("下载订阅失败：%d %s", status, data)
	}
	job = diagnose("http://"+coreTestHost+"/", diagnosePc)
	if job.Verdict == nil || len(job.Verdict.Actions) == 0 || job.Verdict.Actions[0].Kind != "turn_on" || job.Verdict.Actions[0].Profile != "机场" {
		t.Errorf("直连不通、有订阅时应建议开启订阅配置：%+v", job.Verdict)
	}

	// 用订阅：经内核访问，找到命中的规则和节点，测节点的延迟。
	if status, data := fixture.request(t, "POST", "/api/use", map[string]string{"name": "机场"}, nil); status != http.StatusOK {
		t.Fatalf("开启订阅失败：%d %s", status, data)
	}
	job = diagnose("http://"+coreTestHost+"/", diagnosePc)
	if job.Verdict == nil || job.Verdict.Headline != "链路正常" || job.Subscription != profileId {
		t.Errorf("用订阅时链路正常：%+v", job)
	}
	if proxied := row(job, "proxied"); proxied.Outcome != "pass" || !strings.Contains(proxied.Summary, "走 ProxySwitch[节点 A]") {
		t.Errorf("经代理访问应说明走的节点：%+v", proxied)
	}
	if node := row(job, "node"); node.Outcome != "pass" || !strings.HasPrefix(node.Summary, "节点 A：") {
		t.Errorf("应测出当前节点的延迟：%+v", node)
	}
	if status := row(job, "status"); status.Outcome != "pass" || !strings.Contains(status.Summary, "「机场」") || !strings.Contains(status.Summary, "节点 A") {
		t.Errorf("本机代理一行应说明订阅和节点：%+v", status)
	}

	// 局域网设备的视角：经共享入口走一遍，设备最近没有访问过它。
	if status, data := fixture.request(t, "POST", "/api/share", map[string]bool{"enabled": true}, nil); status != http.StatusOK {
		t.Fatalf("开启共享失败：%d %s", status, data)
	}
	job = diagnose("http://"+coreTestHost+"/", diagnoseDevice)
	if job.Verdict == nil || job.Verdict.Headline != "从这台电脑看链路是通的，但设备最近没有访问它" || row(job, "device").Outcome != "warn" || !strings.Contains(row(job, "proxied").Summary, "节点 A") {
		t.Errorf("从设备的视角看：%+v", job)
	}

	// 停止诊断。
	if status, _ := fixture.request(t, "POST", "/api/diagnose", map[string]string{"url": "http://" + coreTestHost + "/", "perspective": diagnosePc}, nil); status != http.StatusOK {
		t.Fatal("开始诊断失败")
	}
	_, data := fixture.request(t, "POST", "/api/diagnose/cancel", nil, nil)
	var stopped DiagnoseJob
	if err := json.Unmarshal(data, &stopped); err != nil || stopped.Running || stopped.Verdict != nil || row(stopped, "node").Summary != "已停止" {
		t.Errorf("停止后不再检查：%s", data)
	}
}
