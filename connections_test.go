package main

import (
	"bufio"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"
)

func testConnection(id, host, start string, upload, download int64, chains ...string) CoreConnection {
	var connection CoreConnection
	connection.Id, connection.Start, connection.Upload, connection.Download, connection.Chains = id, start, upload, download, chains
	connection.Metadata.Host, connection.Metadata.DestinationPort, connection.Metadata.Network = host, "443", "tcp"
	connection.Metadata.SourceIp = "127.0.0.1"
	return connection
}

func recordIds(records []ConnectionRecord) []string {
	ids := []string{}
	for _, record := range records {
		ids = append(ids, record.Id)
	}
	return ids
}

func TestConnectionMonitor(t *testing.T) {
	path := filepath.Join(t.TempDir(), trafficFileName)
	now := time.Date(2026, 9, 28, 10, 0, 0, 0, time.UTC)
	monitor := newConnectionMonitor(path, func() time.Time { return now })

	shared := testConnection("s", "", "2026-09-28T10:00:00Z", 1, 1, "DIRECT")
	shared.Metadata.DestinationIp, shared.Metadata.InboundName, shared.Metadata.SourceIp = "1.2.3.4", coreShareListener, "192.168.1.20"
	program := testConnection("a", "a.example", "2026-09-28T10:00:01Z", 100, 1000, "香港 01", "p1", "ProxySwitch")
	program.Metadata.Process, program.Rule, program.RulePayload = "chrome.exe", "RuleSet", "rs-1234"
	monitor.ingest(CoreConnections{UploadTotal: 300, DownloadTotal: 3000, Connections: []CoreConnection{
		program, testConnection("b", "b.example", "2026-09-28T10:00:02Z", 200, 2000, "DIRECT"), shared,
	}}, true)
	view := monitor.view()
	if !view.Running || !reflect.DeepEqual(recordIds(view.Active), []string{"b", "a", "s"}) || !reflect.DeepEqual(recordIds(view.Recent), []string{"b", "a", "s"}) {
		t.Fatalf("正在进行的和最近的连接应按开始时间从新到旧：%+v", view)
	}
	if view.Session != (TrafficTotal{Upload: 300, Download: 3000}) {
		t.Errorf("内核这次运行的总流量：%+v", view.Session)
	}
	record := view.Active[1]
	if record.Process != "chrome.exe" || record.Rule != "RuleSet rs-1234" || record.Network != "TCP" || record.Host != "a.example" || record.Share || !reflect.DeepEqual(record.Chains, []string{"香港 01", "p1", "ProxySwitch"}) {
		t.Errorf("连接的信息不对：%+v", record)
	}
	if device := view.Active[2]; !device.Share || device.Host != "1.2.3.4" || device.Client != "192.168.1.20" {
		t.Errorf("经共享入口的连接应标出来，没有域名时用目标 IP：%+v", device)
	}
	want := []OutboundTraffic{{"DIRECT", TrafficTotal{201, 2001}}, {"香港 01", TrafficTotal{100, 1000}}}
	if !reflect.DeepEqual(view.Traffic.Outbounds, want) || view.Traffic.Since != "2026-09-28T10:00:00Z" {
		t.Errorf("按出口累计的流量：%+v", view.Traffic)
	}

	// 第二次只算新增的流量；没有出口的算直连；关掉的连接留在最近的连接里。
	program.Upload, program.Download = 150, 1500
	monitor.ingest(CoreConnections{Connections: []CoreConnection{program, testConnection("c", "c.example", "2026-09-28T10:00:03Z", 10, 10)}}, true)
	view = monitor.view()
	if !reflect.DeepEqual(recordIds(view.Active), []string{"c", "a"}) || !reflect.DeepEqual(recordIds(view.Recent), []string{"c", "b", "a", "s"}) {
		t.Fatalf("新连接排在最前，关掉的留在最近的连接里：%+v %+v", recordIds(view.Active), recordIds(view.Recent))
	}
	if view.Recent[2].Upload != 150 {
		t.Errorf("最近的连接里还开着的连接应更新流量：%+v", view.Recent[2])
	}
	want = []OutboundTraffic{{"DIRECT", TrafficTotal{211, 2011}}, {"香港 01", TrafficTotal{150, 1500}}}
	if !reflect.DeepEqual(view.Traffic.Outbounds, want) {
		t.Errorf("只累计新增的流量：%+v", view.Traffic.Outbounds)
	}
	if fileExists(path) {
		t.Error("流量统计不应每次都写文件")
	}

	// 隔一段时间保存；重新启动后接着算。
	now = now.Add(trafficSaveInterval)
	program.Upload = 160
	monitor.ingest(CoreConnections{Connections: []CoreConnection{program}}, true)
	reloaded := newConnectionMonitor(path, func() time.Time { return now })
	if view := reloaded.view(); !reflect.DeepEqual(view.Traffic.Outbounds, []OutboundTraffic{{"DIRECT", TrafficTotal{211, 2011}}, {"香港 01", TrafficTotal{160, 1500}}}) || view.Traffic.Since != "2026-09-28T10:00:00Z" {
		t.Errorf("保存的流量统计应能读回来：%+v", view.Traffic)
	}

	// 内核停了：正在进行的连接和这次运行的流量清掉，最近的连接留着；再启动时连接的流量从头算。
	monitor.ingest(CoreConnections{}, false)
	if view := monitor.view(); view.Running || len(view.Active) != 0 || view.Session != (TrafficTotal{}) || len(view.Recent) != 4 {
		t.Errorf("内核停了之后：%+v", view)
	}
	monitor.ingest(CoreConnections{Connections: []CoreConnection{testConnection("d", "d.example", "2026-09-28T10:01:00Z", 5, 5, "香港 01")}}, true)
	if total := monitor.view().Traffic.Outbounds[1]; total.Name != "香港 01" || total.TrafficTotal != (TrafficTotal{165, 1505}) {
		t.Errorf("内核重启后接着累计：%+v", total)
	}

	// 最近的连接最多留 connectionHistoryLimit 条。
	var many []CoreConnection
	for index := 0; index < connectionHistoryLimit+50; index++ {
		many = append(many, testConnection(fmt.Sprintf("m%03d", index), "m.example", fmt.Sprintf("2026-09-28T11:%02d:%02dZ", index/60, index%60), 0, 0))
	}
	monitor.ingest(CoreConnections{Connections: many}, true)
	if view := monitor.view(); len(view.Recent) != connectionHistoryLimit || view.Recent[0].Id != fmt.Sprintf("m%03d", connectionHistoryLimit+49) {
		t.Errorf("最近的连接应只留最新的 %d 条：%d %s", connectionHistoryLimit, len(view.Recent), view.Recent[0].Id)
	}

	monitor.forget("m000")
	if view := monitor.view(); len(view.Active) != connectionHistoryLimit+49 {
		t.Errorf("断开的连接应从正在进行的连接里去掉：%d", len(view.Active))
	}
	monitor.forget("")
	monitor.clearRecent()
	now = now.Add(time.Hour)
	monitor.resetTraffic()
	view = monitor.view()
	if len(view.Active) != 0 || len(view.Recent) != 0 || len(view.Traffic.Outbounds) != 0 || view.Traffic.Since != now.Format(time.RFC3339) {
		t.Errorf("清空和清零之后：%+v", view)
	}
	if reloaded := newConnectionMonitor(path, time.Now); len(reloaded.view().Traffic.Outbounds) != 0 {
		t.Error("清零应立即保存")
	}
	// 文件坏了时重新开始统计。
	if err := os.WriteFile(path, []byte("{"), 0o644); err != nil {
		t.Fatal(err)
	}
	if view := newConnectionMonitor(path, time.Now).view(); len(view.Traffic.Outbounds) != 0 || view.Traffic.Since == "" {
		t.Errorf("流量统计的文件坏了时应重新开始：%+v", view.Traffic)
	}
}

func TestParseExitInfo(t *testing.T) {
	for text, want := range map[string]ExitInfo{
		`{"ip":"203.0.113.7","country_code":"JP","country":"Japan","city":"Tokyo","isp":"Example ISP","organization":"Example Org"}`: {Ip: "203.0.113.7", CountryCode: "JP", Country: "Japan", City: "Tokyo", Organization: "Example ISP"},
		`{"ip":"198.51.100.2","country":"US","city":"Los Angeles","org":"AS64500 Example"}`:                                          {Ip: "198.51.100.2", CountryCode: "US", City: "Los Angeles", Organization: "AS64500 Example"},
		`{"ip":"192.0.2.9","country_code":"hk","country_name":"Hong Kong","city":"Hong Kong","org":"Example HK"}`:                    {Ip: "192.0.2.9", CountryCode: "HK", Country: "Hong Kong", City: "Hong Kong", Organization: "Example HK"},
		`{"query":"192.0.2.10","countryCode":"SG","country":"Singapore","city":"","isp":"Example SG"}`:                               {Ip: "192.0.2.10", CountryCode: "SG", Country: "Singapore", Organization: "Example SG"},
	} {
		if info, ok := parseExitInfo([]byte(text)); !ok || info != want {
			t.Errorf("%s 应解析成 %+v：%+v", text, want, info)
		}
	}
	for _, text := range []string{`{}`, `{"ip": ""}`, `not json`, `[]`} {
		if _, ok := parseExitInfo([]byte(text)); ok {
			t.Errorf("%s 不应解析出出口 IP", text)
		}
	}
	if host, port := exitEndpointHost("https://api.ip.sb/geoip"); host != "api.ip.sb" || port != 443 {
		t.Errorf("查询接口的主机和端口：%s %d", host, port)
	}
	if host, port := exitEndpointHost("http://127.0.0.1:8080/json"); host != "127.0.0.1" || port != 8080 {
		t.Errorf("查询接口的主机和端口：%s %d", host, port)
	}
}

// exitIpServer 模拟查询出口 IP 的接口：/geoip 返回 ip.sb 的格式，/broken 返回 500。
func exitIpServer(t *testing.T) *httptest.Server {
	t.Helper()
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		if request.URL.Path == "/broken" {
			writer.WriteHeader(http.StatusInternalServerError)
			return
		}
		_, _ = io.WriteString(writer, `{"ip":"203.0.113.7","country_code":"JP","country":"Japan","city":"Tokyo","isp":"Example ISP"}`)
	}))
	t.Cleanup(server.Close)
	return server
}

func setExitIpEndpoints(t *testing.T, endpoints ...string) {
	t.Helper()
	previous := exitIpEndpoints
	exitIpEndpoints = endpoints
	t.Cleanup(func() { exitIpEndpoints = previous })
}

func TestSettingsConnectionsWithoutCore(t *testing.T) {
	server := exitIpServer(t)
	fixture := newSettingsFixture(t, "")
	defer fixture.backend.Close()
	fixture.backend.pollConnections()
	status, data := fixture.request(t, "GET", "/api/connections", nil, nil)
	var view ConnectionsView
	if err := json.Unmarshal(data, &view); status != http.StatusOK || err != nil || view.Running || view.Active == nil || view.Traffic.Outbounds == nil {
		t.Fatalf("内核没有运行时的连接页：%d %s", status, data)
	}
	if status, data := fixture.request(t, "POST", "/api/exit/check", map[string]bool{"direct": false}, nil); status != http.StatusConflict || !strings.Contains(string(data), "内核没有运行") {
		t.Errorf("内核没有运行时不能查经节点的出口：%d %s", status, data)
	}
	if status, data := fixture.request(t, "POST", "/api/connections/close", map[string]string{"id": ""}, nil); status != http.StatusConflict {
		t.Errorf("内核没有运行时不能断开连接：%d %s", status, data)
	}

	// 直连的出口：第一个接口坏了换下一个。
	setExitIpEndpoints(t, server.URL+"/broken", server.URL+"/geoip")
	status, data = fixture.request(t, "POST", "/api/exit/check", map[string]bool{"direct": true}, nil)
	if err := json.Unmarshal(data, &view); status != http.StatusOK || err != nil {
		t.Fatalf("查直连的出口失败：%d %s", status, data)
	}
	if info := view.Exit.Direct.Info; info == nil || info.Ip != "203.0.113.7" || info.CountryCode != "JP" || info.City != "Tokyo" || info.Checked == "" || view.Exit.Direct.Checking || view.Exit.Direct.Error != "" {
		t.Errorf("直连的出口：%+v", view.Exit.Direct)
	}
	setExitIpEndpoints(t, server.URL+"/broken")
	_, data = fixture.request(t, "POST", "/api/exit/check", map[string]bool{"direct": true}, nil)
	var failed ConnectionsView
	if err := json.Unmarshal(data, &failed); err != nil || failed.Exit.Direct.Info != nil || !strings.Contains(failed.Exit.Direct.Error, "500") {
		t.Errorf("接口都不能用时说明原因：%+v", failed.Exit.Direct)
	}

	if status, _ := fixture.request(t, "POST", "/api/traffic/reset", nil, nil); status != http.StatusOK || !fileExists(trafficPath(fixture.backend.engine.paths)) {
		t.Error("清零流量统计失败")
	}
	if status, _ := fixture.request(t, "POST", "/api/connections/clear", nil, nil); status != http.StatusOK {
		t.Error("清空最近的连接失败")
	}
}

// 经内核的连接：本机经内核上网时列出连接（走的节点、命中的规则、流量），可以断开；按节点累计流量；经节点查出口 IP 时
// 说明这次查询走的出口。
func TestSettingsConnectionsWithCore(t *testing.T) {
	binary := requireCoreBinary(t)
	website := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		if request.URL.Path == "/geoip" {
			_, _ = io.WriteString(writer, `{"ip":"203.0.113.7","country_code":"JP","country":"Japan","city":"Tokyo"}`)
			return
		}
		_, _ = io.WriteString(writer, "hello")
	}))
	defer website.Close()
	node := startCountingProxy(t, strings.TrimPrefix(website.URL, "http://"))
	subscription := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		_, _ = io.WriteString(writer, nodesYaml(map[string]*countingProxy{"节点 A": node}, "节点 A"))
	}))
	defer subscription.Close()
	port, _ := freeProxyPort()
	fixture := newSettingsFixture(t, fmt.Sprintf(`{"test_url": "http://%s/", "core": {"port": %d}, "profiles": [
		{"name": "机场", "subscription": %q, "apply_to": ["system"]}
	]}`, coreTestHost, port, subscription.URL))
	defer fixture.backend.Close()
	if err := linkDevCore(fixture.backend.engine.paths, binary); err != nil {
		t.Fatal(err)
	}
	profileId := fixture.backend.engine.Config().Profiles[0].Id
	if status, data := fixture.request(t, "POST", "/api/subscriptions/"+profileId+"/update", nil, nil); status != http.StatusOK {
		t.Fatalf("下载订阅失败：%d %s", status, data)
	}
	if status, data := fixture.request(t, "POST", "/api/on", nil, nil); status != http.StatusOK {
		t.Fatalf("开启失败：%d %s", status, data)
	}

	// 经内核开一条隧道到测试域名，发一个请求，隧道一直开着。
	tunnel, err := net.DialTimeout("tcp", fmt.Sprintf("127.0.0.1:%d", port), 3*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	defer tunnel.Close()
	_ = tunnel.SetDeadline(time.Now().Add(20 * time.Second))
	reader := bufio.NewReader(tunnel)
	fmt.Fprintf(tunnel, "CONNECT %s:80 HTTP/1.1\r\nHost: %s:80\r\n\r\n", coreTestHost, coreTestHost)
	if response, err := http.ReadResponse(reader, nil); err != nil || response.StatusCode != http.StatusOK {
		t.Fatalf("经内核建立隧道失败：%v", err)
	}
	fmt.Fprintf(tunnel, "GET / HTTP/1.1\r\nHost: %s\r\n\r\n", coreTestHost)
	if response, err := http.ReadResponse(reader, nil); err != nil || response.StatusCode != http.StatusOK {
		t.Fatalf("经隧道访问失败：%v", err)
	} else {
		_, _ = io.Copy(io.Discard, io.LimitReader(response.Body, response.ContentLength))
	}

	var view ConnectionsView
	var found *ConnectionRecord
	deadline := time.Now().Add(10 * time.Second)
	for found == nil && time.Now().Before(deadline) {
		fixture.backend.pollConnections()
		_, data := fixture.request(t, "GET", "/api/connections", nil, nil)
		if err := json.Unmarshal(data, &view); err != nil {
			t.Fatal(err)
		}
		for index := range view.Active {
			if view.Active[index].Host == coreTestHost && view.Active[index].Download > 0 {
				found = &view.Active[index]
			}
		}
		if found == nil {
			time.Sleep(200 * time.Millisecond)
		}
	}
	if found == nil || !view.Running {
		t.Fatalf("正在进行的连接里应有经隧道的连接：%+v", view)
	}
	if len(found.Chains) == 0 || found.Chains[0] != "节点 A" || found.Chains[len(found.Chains)-1] != coreTopGroup || found.Port != "80" || found.Network != "TCP" || found.Share || found.Rule == "" {
		t.Errorf("连接的出口、规则不对：%+v", found)
	}
	if view.Session.Download == 0 {
		t.Errorf("应有内核这次运行的总流量：%+v", view.Session)
	}
	if len(view.Traffic.Outbounds) == 0 || view.Traffic.Outbounds[0].Name != "节点 A" || view.Traffic.Outbounds[0].Download == 0 {
		t.Errorf("流量应算在节点上：%+v", view.Traffic.Outbounds)
	}

	// 断开这条连接：隧道随之关掉，正在进行的连接里没有它，最近的连接里还有。
	status, data := fixture.request(t, "POST", "/api/connections/close", map[string]string{"id": found.Id}, nil)
	if err := json.Unmarshal(data, &view); status != http.StatusOK || err != nil {
		t.Fatalf("断开连接失败：%d %s", status, data)
	}
	for _, record := range view.Active {
		if record.Id == found.Id {
			t.Error("断开的连接不应还在正在进行的连接里")
		}
	}
	if !reflect.DeepEqual(recordIds(view.Recent)[:1], []string{found.Id}) {
		t.Errorf("断开的连接应留在最近的连接里：%v", recordIds(view.Recent))
	}
	_ = tunnel.SetReadDeadline(time.Now().Add(5 * time.Second))
	if _, err := reader.ReadByte(); err == nil {
		t.Error("断开后隧道应关掉")
	}

	// 经节点查出口 IP：查询经内核走节点（模拟的节点把请求转给本机的测试网站），说明走的是哪个节点。
	setExitIpEndpoints(t, fmt.Sprintf("http://%s/geoip", coreTestHost))
	status, data = fixture.request(t, "POST", "/api/exit/check", map[string]bool{"direct": false}, nil)
	if err := json.Unmarshal(data, &view); status != http.StatusOK || err != nil {
		t.Fatalf("查经节点的出口失败：%d %s", status, data)
	}
	if info := view.Exit.Proxy.Info; info == nil || info.Ip != "203.0.113.7" || info.Outbound != "节点 A" || info.Node != "节点 A" {
		t.Errorf("经节点的出口：%+v %+v", view.Exit.Proxy, info)
	}

	// 内核停了（删掉订阅配置）：正在进行的连接清空，经节点的出口也忘掉。
	config := fixture.backend.engine.Config().Clone()
	config.Profiles = nil
	if status, data := fixture.request(t, "PUT", "/api/config", config, nil); status != http.StatusOK {
		t.Fatalf("删掉订阅配置失败：%d %s", status, data)
	}
	deadline = time.Now().Add(10 * time.Second)
	for fixture.backend.core.Status().Running && time.Now().Before(deadline) {
		time.Sleep(100 * time.Millisecond)
	}
	fixture.backend.pollConnections()
	if view := fixture.backend.Connections(); view.Running || len(view.Active) != 0 || view.Exit.Proxy.Info != nil || len(view.Recent) == 0 {
		t.Errorf("内核停了之后：%+v", view)
	}
}
