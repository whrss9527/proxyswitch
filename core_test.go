package main

import (
	"bufio"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
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
	return core.process.Process.Pid
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
	connection, err := net.DialTimeout("tcp", fmt.Sprintf("127.0.0.1:%d", port), 3*time.Second)
	if err != nil {
		t.Fatalf("连不上内核的代理端口：%v", err)
	}
	defer connection.Close()
	_ = connection.SetDeadline(time.Now().Add(10 * time.Second))
	fmt.Fprintf(connection, "GET http://%s/ HTTP/1.1\r\nHost: %s\r\nConnection: close\r\n\r\n", coreTestHost, coreTestHost)
	response, err := http.ReadResponse(bufio.NewReader(connection), nil)
	if err != nil {
		t.Fatalf("经内核访问失败：%v", err)
	}
	defer response.Body.Close()
	body, _ := io.ReadAll(response.Body)
	return string(body)
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
