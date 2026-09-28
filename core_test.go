package main

import (
	"bufio"
	"encoding/base64"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// 用真实的 mihomo 测内核管理：设置 PROXYSWITCH_CORE 为 mihomo 程序的路径才运行（CI 会下载固定版本的内核）。
func requireCoreBinary(t *testing.T) string {
	t.Helper()
	binary := os.Getenv("PROXYSWITCH_CORE")
	if binary == "" {
		t.Skip("设置 PROXYSWITCH_CORE 为 mihomo 程序的路径才运行内核测试")
	}
	return binary
}

// 测试访问的是假域名：内核规则里本机和局域网地址直连，访问本机的测试网站不会经过节点。
const coreTestHost = "proxyswitch.test"

// countingProxy 充当订阅里的节点：记录连接次数，不管请求哪个地址都转给本机的测试网站。
type countingProxy struct {
	*fakeProxy
	connections atomic.Int32
}

func startCountingProxy(t *testing.T, website string) *countingProxy {
	t.Helper()
	proxy := &countingProxy{}
	fake, err := startFakeProxy(func(connection net.Conn, delay time.Duration) {
		proxy.connections.Add(1)
		serveRedirectingProxy(connection, website)
	}, 0)
	if err != nil {
		t.Fatal(err)
	}
	proxy.fakeProxy = fake
	t.Cleanup(fake.Stop)
	return proxy
}

func serveRedirectingProxy(client net.Conn, website string) {
	defer client.Close()
	_ = client.SetDeadline(time.Now().Add(30 * time.Second))
	reader := bufio.NewReader(client)
	request, err := http.ReadRequest(reader)
	if err != nil {
		return
	}
	upstream, err := net.DialTimeout("tcp", website, 5*time.Second)
	if err != nil {
		_, _ = io.WriteString(client, "HTTP/1.1 502 Bad Gateway\r\nContent-Length: 0\r\n\r\n")
		return
	}
	defer upstream.Close()
	if request.Method == http.MethodConnect {
		_, _ = io.WriteString(client, "HTTP/1.1 200 Connection Established\r\n\r\n")
		relay(client, reader, upstream)
		return
	}
	request.Close = true
	if request.Write(upstream) == nil {
		_, _ = io.Copy(client, upstream)
	}
}

func corePid(core *Core) int {
	core.mutex.Lock()
	defer core.mutex.Unlock()
	if core.process == nil {
		return 0
	}
	return core.process.Pid()
}

func nodesYaml(nodes map[string]*countingProxy, order ...string) string {
	var builder strings.Builder
	builder.WriteString("proxies:\n")
	for _, name := range order {
		fmt.Fprintf(&builder, "  - {name: %q, type: http, server: 127.0.0.1, port: %d}\n", name, nodes[name].Port())
	}
	return builder.String()
}

// getThroughCore 经内核的代理端口访问测试域名。
func getThroughCore(t *testing.T, port int) string {
	t.Helper()
	status, body, err := requestThroughCore(port, coreTestHost)
	if err != nil {
		t.Fatalf("经内核访问失败：%v", err)
	}
	if status != http.StatusOK {
		t.Errorf("经内核访问返回 HTTP %d", status)
	}
	return body
}

// requestThroughCore 经内核的代理端口访问 host，返回状态码和内容。
func requestThroughCore(port int, host string) (int, string, error) {
	connection, err := net.DialTimeout("tcp", fmt.Sprintf("127.0.0.1:%d", port), 3*time.Second)
	if err != nil {
		return 0, "", fmt.Errorf("连不上内核的代理端口：%v", err)
	}
	defer connection.Close()
	_ = connection.SetDeadline(time.Now().Add(10 * time.Second))
	fmt.Fprintf(connection, "GET http://%s/ HTTP/1.1\r\nHost: %s\r\nConnection: close\r\n\r\n", host, host)
	response, err := http.ReadResponse(bufio.NewReader(connection), nil)
	if err != nil {
		return 0, "", err
	}
	defer response.Body.Close()
	body, _ := io.ReadAll(response.Body)
	return response.StatusCode, string(body), nil
}

func TestCoreWithMihomo(t *testing.T) {
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
	testUrl := "http://" + coreTestHost + "/generate_204"
	nodes := map[string]*countingProxy{"节点 A": startCountingProxy(t, target), "节点 B": startCountingProxy(t, target), "节点 C": startCountingProxy(t, target)}

	dir := t.TempDir()
	if err := writeSubscriptionFile(dir, "pa", []byte(nodesYaml(nodes, "节点 A", "节点 B"))); err != nil {
		t.Fatal(err)
	}
	var errorsMutex sync.Mutex
	var reported []string
	core := newCore(func(message string) {
		errorsMutex.Lock()
		reported = append(reported, message)
		errorsMutex.Unlock()
	})
	defer core.Stop()
	port, _ := freeLocalPort()
	settings := CoreSettings{
		Binary: binary, Dir: dir, Port: port, TestUrl: testUrl, Active: "pa", Mode: "rule",
		Subscriptions: []CoreSubscription{{Id: "pa", Node: "节点 B", Revision: "1"}},
	}
	if err := core.Wait(core.Sync(settings), 30*time.Second); err != nil {
		t.Fatalf("内核没能启动：%v", err)
	}
	status := core.Status()
	if !status.Running || status.Port != port {
		t.Fatalf("状态不对：%+v", status)
	}

	list, err := core.Nodes("pa")
	if err != nil || len(list.Nodes) != 2 || list.Nodes[0].Name != "节点 A" || list.Selected != "节点 B" || list.Current != "节点 B" {
		t.Fatalf("节点列表不对：%+v %v", list, err)
	}
	// 比较请求前后的连接数：内核启动时的健康检查也会连一次各个节点。
	beforeA, beforeB := nodes["节点 A"].connections.Load(), nodes["节点 B"].connections.Load()
	if body := getThroughCore(t, port); body != "hello" || nodes["节点 B"].connections.Load() == beforeB || nodes["节点 A"].connections.Load() != beforeA {
		t.Errorf("流量应经过选中的节点 B：%q A=%d→%d B=%d→%d", body, beforeA, nodes["节点 A"].connections.Load(), beforeB, nodes["节点 B"].connections.Load())
	}
	if err := core.Select("pa", "节点 A"); err != nil {
		t.Fatal(err)
	}
	before := nodes["节点 A"].connections.Load()
	if getThroughCore(t, port); nodes["节点 A"].connections.Load() == before {
		t.Error("切换后流量应经过节点 A")
	}

	delays, err := core.TestDelays("pa", testUrl)
	if _, okA := delays["节点 A"]; err != nil || !okA || len(delays) != 2 {
		t.Errorf("测速结果不对：%v %v", delays, err)
	}
	if list, _ := core.Nodes("pa"); !list.Nodes[0].Tested || !list.Nodes[0].Alive {
		t.Errorf("测速后节点应记下结果：%+v", list.Nodes)
	}
	nodes["节点 B"].Stop()
	delays, _ = core.TestDelays("pa", testUrl)
	if _, okB := delays["节点 B"]; okB {
		t.Errorf("连不上的节点不应有延迟：%v", delays)
	}
	if list, _ := core.Nodes("pa"); list.Nodes[1].Alive || !list.Nodes[1].Tested {
		t.Errorf("连不上的节点应标记为不可用：%+v", list.Nodes[1])
	}
	_ = nodes["节点 B"].Start()

	// 自动选择：选延迟最低且能用的节点。
	settings.Subscriptions[0].Node = ""
	if err := core.Wait(core.Sync(settings), 10*time.Second); err != nil {
		t.Fatal(err)
	}
	if list, _ := core.Nodes("pa"); list.Selected != "" || list.Current == "" || list.Current == "pa-auto" {
		t.Errorf("自动选择时应报告实际使用的节点：%+v", list)
	}

	// 订阅文件更新后，改 Revision 让内核重新读取；进程不重启。
	pid := corePid(core)
	if err := writeSubscriptionFile(dir, "pa", []byte(nodesYaml(nodes, "节点 A", "节点 B", "节点 C"))); err != nil {
		t.Fatal(err)
	}
	settings.Subscriptions[0].Revision = "2"
	if err := core.Wait(core.Sync(settings), 10*time.Second); err != nil {
		t.Fatal(err)
	}
	if list, _ := core.Nodes("pa"); len(list.Nodes) != 3 {
		t.Errorf("更新订阅后应读到 3 个节点：%+v", list.Nodes)
	}

	// 增加订阅：重新加载配置，进程不重启；切换正在使用的订阅。
	if err := writeSubscriptionFile(dir, "pb", []byte(nodesYaml(nodes, "节点 C"))); err != nil {
		t.Fatal(err)
	}
	settings.Subscriptions = append(settings.Subscriptions, CoreSubscription{Id: "pb", Revision: "1"})
	settings.Active = "pb"
	if err := core.Wait(core.Sync(settings), 10*time.Second); err != nil {
		t.Fatal(err)
	}
	if corePid(core) != pid {
		t.Error("只是配置变了，不应重启内核")
	}
	before = nodes["节点 C"].connections.Load()
	if getThroughCore(t, port); nodes["节点 C"].connections.Load() == before {
		t.Error("切换到第二个订阅后流量应经过它的节点 C")
	}

	// 选中的节点已不在订阅里：改用自动选择，不算出错。
	settings.Subscriptions[1].Node = "已经下线的节点"
	if err := core.Wait(core.Sync(settings), 10*time.Second); err != nil {
		t.Errorf("选中的节点不存在时不应出错：%v", err)
	}
	if list, _ := core.Nodes("pb"); list.Selected != "" || list.Current != "节点 C" {
		t.Errorf("选中的节点不存在时应自动选择：%+v", list)
	}

	// 内核意外退出后自动重启。
	if process, err := os.FindProcess(corePid(core)); err == nil {
		_ = process.Kill()
	}
	deadline := time.Now().Add(15 * time.Second)
	for time.Now().Before(deadline) {
		if status := core.Status(); status.Running && core.request(http.MethodGet, "/version", nil, nil, time.Second) == nil {
			break
		}
		time.Sleep(100 * time.Millisecond)
	}
	if body := getThroughCore(t, port); body != "hello" {
		t.Error("内核意外退出后应自动重启并恢复代理")
	}

	// 端口变了：重启内核。
	newPort, _ := freeLocalPort()
	settings.Port = newPort
	if err := core.Wait(core.Sync(settings), 30*time.Second); err != nil {
		t.Fatal(err)
	}
	if body := getThroughCore(t, newPort); body != "hello" {
		t.Error("换端口后应在新端口提供代理")
	}

	// 端口被占用时说明原因。
	occupied, _ := net.Listen("tcp", "127.0.0.1:0")
	defer occupied.Close()
	settings.Port = occupied.Addr().(*net.TCPAddr).Port
	if err := core.Wait(core.Sync(settings), 30*time.Second); err == nil || !strings.Contains(err.Error(), "被其他程序占用") {
		t.Errorf("端口被占用时应报错：%v", err)
	}
	errorsMutex.Lock()
	if len(reported) == 0 || !strings.Contains(reported[len(reported)-1], "被其他程序占用") {
		t.Errorf("出错时应通知：%v", reported)
	}
	errorsMutex.Unlock()

	// 没有订阅时停止内核。
	if err := core.Wait(core.Sync(CoreSettings{}), 10*time.Second); err != nil || core.Status().Running {
		t.Errorf("没有订阅时应停止内核：%v", err)
	}
	if _, err := os.Stat(filepath.Join(dir, coreConfigName)); err != nil {
		t.Errorf("应在工作目录写入内核配置：%v", err)
	}
}

// 很多机场默认返回 base64 编码的节点链接（没有识别出 Clash 时），内核也要能读出节点和名字。
func TestCoreWithLinkSubscription(t *testing.T) {
	binary := requireCoreBinary(t)
	website := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		_, _ = io.WriteString(writer, "hello")
	}))
	defer website.Close()
	target := strings.TrimPrefix(website.URL, "http://")
	nodes := map[string]*countingProxy{"香港 01": startCountingProxy(t, target), "日本 02": startCountingProxy(t, target)}
	var links strings.Builder
	for _, name := range []string{"香港 01", "日本 02"} {
		fmt.Fprintf(&links, "http://127.0.0.1:%d#%s\n", nodes[name].Port(), url.PathEscape(name))
	}
	content := []byte(base64.StdEncoding.EncodeToString([]byte(links.String())))
	if format, count, err := inspectSubscription(content); format != "links" || count != 2 || err != nil {
		t.Fatalf("应识别为节点链接：%s %d %v", format, count, err)
	}
	dir := t.TempDir()
	if err := writeSubscriptionFile(dir, "pa", content); err != nil {
		t.Fatal(err)
	}
	core := newCore(nil)
	defer core.Stop()
	port, _ := freeLocalPort()
	settings := CoreSettings{
		Binary: binary, Dir: dir, Port: port, TestUrl: "http://" + coreTestHost + "/", Active: "pa", Mode: "global",
		Subscriptions: []CoreSubscription{{Id: "pa", Node: "日本 02", Revision: "1"}},
	}
	if err := core.Wait(core.Sync(settings), 30*time.Second); err != nil {
		t.Fatalf("内核没能启动：%v", err)
	}
	if list, err := core.Nodes("pa"); err != nil || len(list.Nodes) != 2 || list.Nodes[0].Name != "香港 01" || list.Current != "日本 02" {
		t.Fatalf("节点列表不对：%+v %v", list, err)
	}
	before := nodes["日本 02"].connections.Load()
	if body := getThroughCore(t, port); body != "hello" || nodes["日本 02"].connections.Load() == before {
		t.Errorf("流量应经过选中的节点：%q", body)
	}
}

func TestCoreMissingBinary(t *testing.T) {
	core := newCore(nil)
	defer core.Stop()
	settings := CoreSettings{Binary: filepath.Join(t.TempDir(), coreBinaryName()), Dir: t.TempDir(), Port: 17890, Subscriptions: []CoreSubscription{{Id: "pa"}}}
	if err := core.Wait(core.Sync(settings), 5*time.Second); err != errCoreMissing {
		t.Errorf("没有内核程序时应报告还没下载：%v", err)
	}
	if status := core.Status(); status.Running || status.Error != errCoreMissing.Error() {
		t.Errorf("状态应记下错误：%+v", status)
	}
}

// 局域网共享：内核多开一个入口给局域网设备，流量跟着本机走（直连、转发给本机在用的代理、走同样的节点）。
func TestCoreWithShare(t *testing.T) {
	binary := requireCoreBinary(t)
	website := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		_, _ = io.WriteString(writer, "hello")
	}))
	defer website.Close()
	target := strings.TrimPrefix(website.URL, "http://")
	node, upstream := startCountingProxy(t, target), startCountingProxy(t, target)
	dir := t.TempDir()
	core := newCore(nil)
	defer core.Stop()
	corePort, _ := freeLocalPort()
	sharePort, _ := freeLocalPort()
	allowed := []string{"127.0.0.0/8", "::1/128", "10.0.0.0/8", "172.16.0.0/12", "192.168.0.0/16"}
	through := func(host string) (int32, int32, string) {
		t.Helper()
		nodeBefore, upstreamBefore := node.connections.Load(), upstream.connections.Load()
		status, body, err := requestThroughCore(sharePort, host)
		return node.connections.Load() - nodeBefore, upstream.connections.Load() - upstreamBefore, fmt.Sprintf("%d %s %v", status, body, err)
	}

	// 只为共享运行：本机的代理端口不监听，共享的设备直连。
	settings := CoreSettings{Binary: binary, Dir: dir, Port: corePort, TestUrl: "http://" + coreTestHost + "/", Mode: "rule",
		Share: &CoreShare{Port: sharePort, Allowed: allowed, Upstream: ShareUpstream{Kind: shareUpstreamDirect}}}
	if err := core.Wait(core.Sync(settings), 30*time.Second); err != nil {
		t.Fatalf("只为共享也应启动内核：%v", err)
	}
	if status := core.Status(); !status.Running || status.Port != 0 || !status.Share.Listening || status.Share.Port != sharePort {
		t.Errorf("状态不对：%+v", status)
	}
	if connection, err := net.DialTimeout("tcp", fmt.Sprintf("127.0.0.1:%d", corePort), time.Second); err == nil {
		connection.Close()
		t.Error("只为共享运行时本机的代理端口不应监听")
	}
	if viaNode, viaUpstream, result := through(target); viaNode != 0 || viaUpstream != 0 || result != "200 hello <nil>" {
		t.Errorf("本机没开代理时共享的设备应直连：%s", result)
	}

	// 不在允许名单里的来源连不上共享入口（这里去掉了本机回环）；重新加载时已经建立的连接不断。
	open, err := net.DialTimeout("tcp", fmt.Sprintf("127.0.0.1:%d", sharePort), 3*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	defer open.Close()
	openReader := bufio.NewReader(open)
	fmt.Fprintf(open, "CONNECT %s HTTP/1.1\r\nHost: %s\r\n\r\n", target, target)
	if response, err := http.ReadResponse(openReader, nil); err != nil || response.StatusCode != http.StatusOK {
		t.Fatalf("经共享入口建立隧道失败：%v", err)
	}
	settings.Share = &CoreShare{Port: sharePort, Allowed: []string{"192.168.1.20/32"}, Upstream: ShareUpstream{Kind: shareUpstreamDirect}}
	if err := core.Wait(core.Sync(settings), 30*time.Second); err != nil {
		t.Fatal(err)
	}
	if _, _, result := through(target); result == "200 hello <nil>" {
		t.Errorf("不在允许名单里的来源不应能用共享入口：%s", result)
	}
	_ = open.SetDeadline(time.Now().Add(5 * time.Second))
	fmt.Fprintf(open, "GET / HTTP/1.1\r\nHost: %s\r\n\r\n", target)
	if response, err := http.ReadResponse(openReader, nil); err != nil || response.StatusCode != http.StatusOK {
		t.Errorf("重新加载后已经建立的连接应继续可用：%v", err)
	}
	open.Close()

	// 本机用其他代理：共享的流量转发给它，内核只重新加载。
	pid := corePid(core)
	settings.Share = &CoreShare{Port: sharePort, Allowed: allowed, Upstream: ShareUpstream{Kind: shareUpstreamProxy, Proxy: "http://" + upstream.Address()}}
	if err := core.Wait(core.Sync(settings), 30*time.Second); err != nil {
		t.Fatal(err)
	}
	if _, viaUpstream, result := through(coreTestHost); viaUpstream == 0 || result != "200 hello <nil>" {
		t.Errorf("共享的流量应转发给本机在用的代理：%s", result)
	}
	if corePid(core) != pid {
		t.Error("只是去向变了，不应重启内核")
	}

	// 经共享入口的连接能在连接列表里看到来源和入口。
	tunnel, err := net.DialTimeout("tcp", fmt.Sprintf("127.0.0.1:%d", sharePort), 3*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	fmt.Fprintf(tunnel, "CONNECT %s HTTP/1.1\r\nHost: %s\r\n\r\n", target, target)
	if response, err := http.ReadResponse(bufio.NewReader(tunnel), nil); err != nil || response.StatusCode != http.StatusOK {
		t.Fatalf("经共享入口建立隧道失败：%v", err)
	}
	// 内核先答应 CONNECT 再去拨号，拨通后连接才出现在列表里。
	var connections []CoreConnection
	found := false
	for deadline := time.Now().Add(5 * time.Second); !found && time.Now().Before(deadline); time.Sleep(50 * time.Millisecond) {
		connections, err = core.Connections()
		for _, connection := range connections {
			if connection.Metadata.InboundName == coreShareListener && connection.Metadata.SourceIp == "127.0.0.1" && connection.Target() != "" && connection.Outbound() != "" {
				found = true
			}
		}
	}
	tunnel.Close()
	if err != nil || !found {
		t.Errorf("连接列表里应有经共享入口的连接：%+v %v", connections, err)
	}

	// 加上订阅、本机用订阅：共享的设备走同样的节点，本机的代理端口开始监听。
	if err := writeSubscriptionFile(dir, "p1", []byte(nodesYaml(map[string]*countingProxy{"节点": node}, "节点"))); err != nil {
		t.Fatal(err)
	}
	settings.Subscriptions = []CoreSubscription{{Id: "p1", Node: "节点", Revision: "1"}}
	settings.Active = "p1"
	settings.Share = &CoreShare{Port: sharePort, Allowed: allowed, Upstream: ShareUpstream{Kind: shareUpstreamCore}}
	if err := core.Wait(core.Sync(settings), 30*time.Second); err != nil {
		t.Fatal(err)
	}
	if status := core.Status(); status.Port != corePort || !status.Share.Listening {
		t.Errorf("有订阅后本机端口应监听，共享照常：%+v", status)
	}
	if viaNode, _, result := through(coreTestHost); viaNode == 0 || result != "200 hello <nil>" {
		t.Errorf("本机用订阅时共享的设备应走同样的节点：%s", result)
	}

	// 共享端口被占用：共享报错，内核照常运行。
	occupied, _ := net.Listen("tcp", "127.0.0.1:0")
	defer occupied.Close()
	settings.Share = &CoreShare{Port: occupied.Addr().(*net.TCPAddr).Port, Allowed: allowed, Upstream: ShareUpstream{Kind: shareUpstreamCore}}
	if err := core.Wait(core.Sync(settings), 30*time.Second); err != nil {
		t.Errorf("共享端口被占用不算内核出错：%v", err)
	}
	if status := core.Status(); !status.Running || status.Share.Listening || !strings.Contains(status.Share.Error, "被其他程序占用") {
		t.Errorf("共享端口被占用时应说明：%+v", status)
	}

	// 关掉共享：共享入口不再监听。
	settings.Share = nil
	if err := core.Wait(core.Sync(settings), 30*time.Second); err != nil {
		t.Fatal(err)
	}
	if connection, err := net.DialTimeout("tcp", fmt.Sprintf("127.0.0.1:%d", sharePort), time.Second); err == nil {
		connection.Close()
		t.Error("关掉共享后共享入口不应再监听")
	}
	if status := core.Status(); status.Share != (CoreShareStatus{}) {
		t.Errorf("关掉共享后状态应清空：%+v", status.Share)
	}
}

// 重新加载配置期间有人查询「自动选择」（设置页每两秒查一次在用的节点）时，内核会把 COMPATIBLE（相当于直连）缓存十秒；
// 加载完后要让它重新挑节点，否则走节点的流量会直连。
func TestCoreAutoGroupAfterReload(t *testing.T) {
	binary := requireCoreBinary(t)
	website := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		_, _ = io.WriteString(writer, "hello")
	}))
	defer website.Close()
	node := startCountingProxy(t, strings.TrimPrefix(website.URL, "http://"))
	dir := t.TempDir()
	if err := writeSubscriptionFile(dir, "pa", []byte(nodesYaml(map[string]*countingProxy{"节点 A": node}, "节点 A"))); err != nil {
		t.Fatal(err)
	}
	core := newCore(nil)
	defer core.Stop()
	port, _ := freeLocalPort()
	settings := CoreSettings{Binary: binary, Dir: dir, Port: port, TestUrl: "http://" + coreTestHost + "/", Active: "pa", Mode: "rule",
		Subscriptions: []CoreSubscription{{Id: "pa", Revision: "1"}}}
	if err := core.Wait(core.Sync(settings), 30*time.Second); err != nil {
		t.Fatal(err)
	}
	var stop atomic.Bool
	polled := make(chan struct{})
	go func() {
		defer close(polled)
		for !stop.Load() {
			_ = core.request(http.MethodGet, "/proxies/"+url.PathEscape(coreAutoGroup("pa")), nil, nil, time.Second)
		}
	}()
	defer func() {
		stop.Store(true)
		<-polled
	}()
	for round := 0; round < 10; round++ {
		settings.CustomRules = []string{fmt.Sprintf("DOMAIN-SUFFIX,round%d.invalid,ProxySwitch", round)}
		if err := core.Wait(core.Sync(settings), 30*time.Second); err != nil {
			t.Fatal(err)
		}
		if status, body, err := requestThroughCore(port, "other.invalid"); status != http.StatusOK || body != "hello" {
			t.Fatalf("第 %d 次重新加载后应仍经过节点：%d %q %v（自动选择：%s）", round+1, status, body, err, core.CurrentNode("pa"))
		}
	}
}
