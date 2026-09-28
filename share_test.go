package main

import (
	"bufio"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"
	"time"
)

func TestShareConfig(t *testing.T) {
	config, err := parseConfig(`{"profiles": []}`)
	if err != nil {
		t.Fatal(err)
	}
	if want := (ShareConfig{Port: defaultSharePort, KeepAwake: true}); config.Share != want {
		t.Errorf("局域网共享默认关闭、端口 %d、共享期间保持唤醒：%+v", defaultSharePort, config.Share)
	}
	config, err = parseConfig(`{"share": {"enabled": true, "port": 0, "allowed": " 192.168.1.20, 10.0.0.0/8 "}, "profiles": []}`)
	if err != nil {
		t.Fatal(err)
	}
	if config.Share.Port != defaultSharePort || config.Share.Allowed != "192.168.1.20, 10.0.0.0/8" || !config.Share.KeepAwake {
		t.Errorf("没填的项用默认值，允许的设备去掉首尾空白：%+v", config.Share)
	}
	if !strings.Contains(defaultConfigText, `"share": { "enabled": false, "port": 17892`) {
		t.Error("默认配置文件应写出局域网共享的设置")
	}
	for text, problem := range map[string]string{
		`{"share": {"port": 80}}`:                             "1024~65535",
		`{"share": {"port": 18000}, "core": {"port": 18000}}`: "不能和代理内核的端口 18000 相同",
		`{"share": {"allowed": "192.168.1.20, ps5"}}`:         "认不出这些地址：ps5",
	} {
		if _, err := parseConfig(text); err == nil || !strings.Contains(err.Error(), problem) {
			t.Errorf("%s 应报错「%s」：%v", text, problem, err)
		}
	}
}

func TestShareAllowedPrefixes(t *testing.T) {
	prefixes, invalid := parseShareClients("192.168.1.20，192.168.2.0/24; 192.168.1.20\n10.1.2.3/8 fe80::1 abc")
	if want := []string{"192.168.1.20/32", "192.168.2.0/24", "10.0.0.0/8", "fe80::1/128"}; !reflect.DeepEqual(prefixes, want) {
		t.Errorf("应拆出不重复的网段：%v", prefixes)
	}
	if !reflect.DeepEqual(invalid, []string{"abc"}) {
		t.Errorf("认不出的地址：%v", invalid)
	}
	if got := (ShareConfig{}).allowedPrefixes(); !reflect.DeepEqual(got, append(append([]string{}, shareLoopbackPrefixes...), shareLanPrefixes...)) {
		t.Errorf("没填设备时允许本机和局域网：%v", got)
	}
	if got := (ShareConfig{Allowed: "192.168.1.20"}).allowedPrefixes(); !reflect.DeepEqual(got, []string{"127.0.0.0/8", "::1/128", "192.168.1.20/32"}) {
		t.Errorf("填了设备时只允许本机和这些设备：%v", got)
	}
}

func TestShareCommandTarget(t *testing.T) {
	for _, item := range []struct {
		argument string
		current  bool
		want     bool
	}{{"on", false, true}, {"ON", true, true}, {"off", true, false}, {"", false, true}, {"toggle", true, false}} {
		if got, err := shareCommandTarget(item.argument, item.current); err != nil || got != item.want {
			t.Errorf("share %q（现在 %v）应设成 %v：%v %v", item.argument, item.current, item.want, got, err)
		}
	}
	if _, err := shareCommandTarget("maybe", false); err == nil {
		t.Error("不认识的参数应报错")
	}
}

func TestShareAwake(t *testing.T) {
	share := ShareConfig{Enabled: true, KeepAwake: true}
	if shareAwake(share, false) != "holding" || shareAwake(share, true) != "paused" {
		t.Error("默认接着电源时保持唤醒，用电池时暂停")
	}
	share.KeepAwakeOnBattery = true
	if shareAwake(share, true) != "holding" {
		t.Error("设置了用电池时也保持")
	}
	if shareAwake(ShareConfig{Enabled: true}, false) != "off" || shareAwake(ShareConfig{KeepAwake: true}, false) != "off" {
		t.Error("没开共享或没开保持唤醒时不阻止睡眠")
	}
}

// 共享的流量跟着本机走：本机用订阅就走内核的节点和规则，用其他代理就转发给它，没开代理就直连。
func TestEngineShare(t *testing.T) {
	fixture, core, _ := newSubscriptionFixture(t, subscriptionTestConfig)
	engine := fixture.engine
	if core.last().Share != nil {
		t.Fatal("默认不开局域网共享")
	}
	if err := engine.SetShareEnabled(true); err == nil || !strings.Contains(err.Error(), "下载内核") {
		t.Errorf("还没下载内核时不能开共享：%v", err)
	}
	installFakeCoreBinary(t, engine)
	if err := engine.SetShareEnabled(true); err != nil {
		t.Fatal(err)
	}
	upstream := func() ShareUpstream {
		t.Helper()
		share := core.last().Share
		if share == nil {
			t.Fatal("开了共享应交给内核")
		}
		return share.Upstream
	}
	if share := core.last().Share; share.Port != defaultSharePort || !reflect.DeepEqual(share.Allowed, (ShareConfig{}).allowedPrefixes()) || share.Upstream.Kind != shareUpstreamDirect {
		t.Errorf("本机没开代理时共享的设备直连：%+v", share)
	}
	if !engine.Config().Share.Enabled {
		t.Error("开关应保存到配置")
	}

	if err := engine.UseProfile("本机"); err != nil {
		t.Fatal(err)
	}
	if got := upstream(); got.Kind != shareUpstreamProxy || got.Proxy != "http://127.0.0.1:7890" {
		t.Errorf("本机用其他代理时转发给它：%+v", got)
	}
	if err := engine.TurnOff(); err != nil {
		t.Fatal(err)
	}
	if got := upstream(); got.Kind != shareUpstreamDirect {
		t.Errorf("关掉代理后共享的设备直连：%+v", got)
	}

	// 其他程序改了系统代理：定时检查时跟着变，没变化时不打扰内核。
	fixture.system.System = SystemProxyState{ProxyEnabled: true, Server: "socks=127.0.0.1:7891"}
	engine.RefreshCore(engine.Status())
	if got := upstream(); got.Kind != shareUpstreamProxy || got.Proxy != "socks5://127.0.0.1:7891" {
		t.Errorf("其他程序设置的代理也转发给它：%+v", got)
	}
	synced := len(core.history)
	engine.RefreshCore(engine.Status())
	if len(core.history) != synced {
		t.Error("去向没变时不应让内核重新加载")
	}
	fixture.system.System = SystemProxyState{PacEnabled: true, Pac: "http://10.0.0.1/proxy.pac"}
	engine.RefreshCore(engine.Status())
	if got := upstream(); got.Kind != shareUpstreamUnsupported || !strings.Contains(got.Reason, "PAC") {
		t.Errorf("PAC 没法转发：%+v", got)
	}
	// 指回共享入口自己的代理不能转发，否则绕圈。
	for _, server := range []string{"127.0.0.1:17892", "localhost:17892"} {
		fixture.system.System = SystemProxyState{ProxyEnabled: true, Server: server}
		engine.RefreshCore(engine.Status())
		if got := upstream(); got.Kind != shareUpstreamDirect {
			t.Errorf("%s 指回共享入口，应直连：%+v", server, got)
		}
	}
	if addresses := localAddresses(); len(addresses) > 0 && !isLocalHost(addresses[0].Ip) {
		t.Errorf("本机网卡的地址也是本机：%s", addresses[0].Ip)
	}
	if isLocalHost("10.255.255.1") || isLocalHost("proxy.example.com") {
		t.Error("其他地址不是本机")
	}
	fixture.system.System = SystemProxyState{}

	// 本机用订阅：共享的设备走同样的节点和规则。
	profile := *engine.Config().FindProfile("机场")
	recordTestSubscription(t, engine, &profile)
	if err := engine.UseProfile("机场"); err != nil {
		t.Fatal(err)
	}
	if got := upstream(); got.Kind != shareUpstreamCore {
		t.Errorf("本机用订阅时共享的设备和本机一样：%+v", got)
	}
	if info := engine.settingsState().Share; info.Upstream.Kind != shareUpstreamCore || info.Awake != "off" || info.Addresses == nil {
		t.Errorf("设置页应显示共享的去向：%+v", info)
	}

	updated := engine.Config().Clone()
	updated.Share.Allowed = "192.168.1.20"
	updated.Share.Port = 18892
	if err := engine.SaveConfig(updated); err != nil {
		t.Fatal(err)
	}
	if share := core.last().Share; share.Port != 18892 || !reflect.DeepEqual(share.Allowed, []string{"127.0.0.0/8", "::1/128", "192.168.1.20/32"}) {
		t.Errorf("修改端口和允许的设备后交给内核：%+v", share)
	}
	if err := engine.SetShareEnabled(false); err != nil {
		t.Fatal(err)
	}
	if core.last().Share != nil || engine.Config().Share.Enabled {
		t.Error("关掉共享后内核不再监听共享入口")
	}
	if info := engine.settingsState().Share; info.Upstream.Kind != shareUpstreamDirect {
		t.Errorf("没开共享时不显示去向：%+v", info)
	}

	// 命令行在托盘程序没运行时执行：内核随命令退出，不能开共享，但可以关。
	engine.core = nil
	if err := engine.SetShareEnabled(true); err == nil || !strings.Contains(err.Error(), "保持运行") {
		t.Errorf("没有内核时不能开共享：%v", err)
	}
}

func TestShareHistory(t *testing.T) {
	connection := func(id, source, host, start string, upload int64) CoreConnection {
		item := CoreConnection{Id: id, Upload: upload, Download: upload * 10, Start: start, Chains: []string{"香港 01", "ProxySwitch"}, Rule: "RuleSet", RulePayload: "proxy-1"}
		item.Metadata.SourceIp, item.Metadata.Host, item.Metadata.DestinationPort, item.Metadata.InboundName = source, host, "443", coreShareListener
		return item
	}
	local := connection("local", "127.0.0.1", "example.com", "2026-09-28T10:00:00Z", 1)
	local.Metadata.InboundName = "DEFAULT-MIXED"
	var history shareHistory
	activity := history.record([]CoreConnection{
		connection("b", "192.168.1.20", "www.youtube.com", "2026-09-28T10:00:02Z", 5),
		connection("a", "192.168.1.20", "psn.example.com", "2026-09-28T10:00:01Z", 3),
		connection("c", "192.168.1.30", "", "2026-09-28T10:00:03Z", 1),
		local,
	})
	if len(activity.Clients) != 2 || activity.Clients[0].Ip != "192.168.1.20" || activity.Clients[0].Connections != 2 || activity.Clients[0].Upload != 8 || activity.Clients[0].Download != 80 || activity.Clients[0].LastHost != "www.youtube.com" || activity.Clients[0].LastOutbound != "香港 01" {
		t.Errorf("应按来源 IP 归并共享入口的连接：%+v", activity.Clients)
	}
	if len(activity.Recent) != 3 || activity.Recent[0].Id != "c" || activity.Recent[2].Id != "a" || activity.Recent[1].Rule != "RuleSet proxy-1" || activity.Recent[1].Port != "443" {
		t.Errorf("最近的连接应是新的在前、只有共享入口的：%+v", activity.Recent)
	}
	// 连接关掉后仍留在「最近的连接」里，不重复记录。
	activity = history.record([]CoreConnection{connection("b", "192.168.1.20", "www.youtube.com", "2026-09-28T10:00:02Z", 9)})
	if len(activity.Clients) != 1 || activity.Clients[0].Upload != 9 || len(activity.Recent) != 3 {
		t.Errorf("设备只统计还开着的连接，最近的连接保留：%+v", activity)
	}
	if history.recentFor("youtube.com") != 1 || history.recentFor("example.com") != 1 || history.recentFor("google.com") != 0 {
		t.Error("应能数出访问某个网站的连接")
	}
	for index := 0; index < shareHistoryLimit+10; index++ {
		history.record([]CoreConnection{connection(strings.Repeat("x", index+1), "192.168.1.20", "a.com", "2026-09-28T10:01:00Z", 1)})
	}
	if activity := history.record(nil); len(activity.Recent) != shareHistoryLimit || len(activity.Clients) != 0 {
		t.Errorf("最近的连接最多 %d 条：%d", shareHistoryLimit, len(activity.Recent))
	}
	history.clear()
	if activity := history.record([]CoreConnection{connection("b", "192.168.1.20", "www.youtube.com", "2026-09-28T10:00:02Z", 9)}); len(activity.Recent) != 0 || len(activity.Clients) != 1 || activity.Recent == nil {
		t.Errorf("清空后还开着的连接不再列进最近的连接，列表是空的而不是 null：%+v", activity)
	}
}

// 从设置页的接口开关局域网共享：真实的内核监听共享入口，设备经它上网，页面看到正在使用的设备。
func TestSettingsShareFlow(t *testing.T) {
	corePort, _ := freeLocalPort()
	sharePort, _ := freeLocalPort()
	fixture := newSettingsFixture(t, fmt.Sprintf(`{"core": {"port": %d}, "share": {"port": %d}, "profiles": []}`, corePort, sharePort))
	defer fixture.backend.Close()
	setShare := func(enabled bool) (int, []byte) {
		return fixture.request(t, "POST", "/api/share", map[string]bool{"enabled": enabled}, nil)
	}
	if status, data := setShare(true); status != http.StatusConflict || !strings.Contains(string(data), "下载内核") {
		t.Errorf("还没下载内核时不能开共享：%d %s", status, data)
	}

	binary := requireCoreBinary(t)
	website := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		_, _ = io.WriteString(writer, "hello")
	}))
	defer website.Close()
	target := strings.TrimPrefix(website.URL, "http://")
	if err := linkDevCore(fixture.backend.engine.paths, binary); err != nil {
		t.Fatal(err)
	}
	status, data := setShare(true)
	state := fixture.state(t, data)
	if status != http.StatusOK || !state.Config.Share.Enabled || !state.Core.Running || !state.Core.Share.Listening || state.Core.Share.Port != sharePort || state.Share.Upstream.Kind != shareUpstreamDirect || state.Share.Awake != "holding" {
		t.Fatalf("开共享后内核应监听共享入口：%d %s", status, data)
	}
	if status, body, err := requestThroughCore(sharePort, target); status != http.StatusOK || body != "hello" {
		t.Errorf("本机没开代理时经共享入口直连：%d %q %v", status, body, err)
	}

	// 开着的连接能在「正在使用的设备」里看到，关掉后留在「最近的连接」里。
	tunnel, err := net.DialTimeout("tcp", fmt.Sprintf("127.0.0.1:%d", sharePort), 3*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	defer tunnel.Close()
	fmt.Fprintf(tunnel, "CONNECT %s HTTP/1.1\r\nHost: %s\r\n\r\n", target, target)
	if response, err := http.ReadResponse(bufio.NewReader(tunnel), nil); err != nil || response.StatusCode != http.StatusOK {
		t.Fatalf("经共享入口建立隧道失败：%v", err)
	}
	var activity ShareActivity
	for deadline := time.Now().Add(5 * time.Second); time.Now().Before(deadline); time.Sleep(100 * time.Millisecond) {
		_, data := fixture.request(t, "GET", "/api/share/activity", nil, nil)
		if err := json.Unmarshal(data, &activity); err != nil {
			t.Fatal(err)
		}
		if len(activity.Clients) > 0 {
			break
		}
	}
	if len(activity.Clients) != 1 || activity.Clients[0].Ip != "127.0.0.1" || activity.Clients[0].LastOutbound != "DIRECT" || len(activity.Recent) == 0 {
		t.Errorf("应看到经共享入口的连接：%+v", activity)
	}
	if _, data := fixture.request(t, "POST", "/api/share/clear", nil, nil); !strings.Contains(string(data), `"recent":[]`) {
		t.Errorf("清空后没有最近的连接：%s", data)
	}
	if status, _ := fixture.request(t, "POST", "/api/share/firewall", nil, nil); status != http.StatusOK || fixture.backend.firewallAllowed != 1 {
		t.Errorf("开发模式下添加防火墙例外只记一次：%d", status)
	}

	status, data = setShare(false)
	if state := fixture.state(t, data); status != http.StatusOK || state.Config.Share.Enabled || state.Core.Running || state.Core.Share.Listening {
		t.Errorf("关掉共享后没有订阅时内核也停止：%d %s", status, data)
	}
}
