package main

import (
	"net"
	"net/http"
	"strconv"
	"strings"
	"testing"
	"time"
)

func startTestFakes(t *testing.T) (httpProxy, socks *fakeProxy, testUrl string) {
	t.Helper()
	httpProxy, err := startFakeProxy(serveFakeHttpProxy, 0)
	if err != nil {
		t.Fatal(err)
	}
	socks, err = startFakeProxy(serveFakeSocksProxy, 0)
	if err != nil {
		t.Fatal(err)
	}
	testUrl, stop, err := startFakeTestServer()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		httpProxy.Stop()
		socks.Stop()
		stop()
	})
	return httpProxy, socks, testUrl
}

func TestDetectProxies(t *testing.T) {
	httpProxy, socks, _ := startTestFakes(t)
	// 一个普通网站端口，不应被当成代理。
	website, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer website.Close()
	go func() {
		_ = http.Serve(website, http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
			writer.WriteHeader(http.StatusBadRequest)
		}))
	}()
	websitePort := website.Addr().(*net.TCPAddr).Port

	listeners := []Listener{
		{Address: "0.0.0.0", Port: httpProxy.Port(), Pid: 100, Process: "proxy-a.exe"},
		{Address: "::", Port: httpProxy.Port(), Pid: 100, Process: "proxy-a.exe"},
		{Address: "127.0.0.1", Port: socks.Port(), Pid: 101, Process: "proxy-b.exe"},
		{Address: "127.0.0.1", Port: websitePort, Pid: 102, Process: "web.exe"},
		{Address: "127.0.0.1", Port: 445, Pid: 4, Process: "System"},
		{Address: "127.0.0.1", Port: 50000, Pid: 103, Process: "svchost.exe"},
	}
	started := time.Now()
	detected := detectProxies(listeners, 1500*time.Millisecond)
	if elapsed := time.Since(started); elapsed > 5*time.Second {
		t.Errorf("检测太慢：%v", elapsed)
	}
	if len(detected) != 2 {
		t.Fatalf("应检测到 2 个代理，得到 %+v", detected)
	}
	byPort := map[int]DetectedProxy{}
	for _, proxy := range detected {
		byPort[proxy.Port] = proxy
	}
	httpResult := byPort[httpProxy.Port()]
	if !httpResult.Http || httpResult.Socks || httpResult.Process != "proxy-a.exe" || httpResult.Server != "127.0.0.1:"+strconv.Itoa(httpProxy.Port()) {
		t.Errorf("HTTP 代理检测结果不对：%+v", httpResult)
	}
	socksResult := byPort[socks.Port()]
	if socksResult.Http || !socksResult.Socks || socksResult.Server != "socks5://127.0.0.1:"+strconv.Itoa(socks.Port()) {
		t.Errorf("SOCKS5 代理检测结果不对：%+v", socksResult)
	}
}

func TestProxyCandidates(t *testing.T) {
	candidates := proxyCandidates([]Listener{
		{Address: "::", Port: 7890, Pid: 10},
		{Address: "0.0.0.0", Port: 7890, Pid: 10},
		{Address: "192.168.1.5", Port: 8080, Pid: 11},
		{Address: "127.0.0.1", Port: 80, Pid: 12},
		{Address: "127.0.0.1", Port: 9000, Pid: 4},
	})
	if len(candidates) != 2 || candidates[0].Address != "127.0.0.1" || candidates[0].Port != 7890 || candidates[1].Address != "192.168.1.5" {
		t.Errorf("候选端口不对：%+v", candidates)
	}
}

func TestTestProxyServer(t *testing.T) {
	httpProxy, socks, testUrl := startTestFakes(t)
	for _, server := range []string{httpProxy.Address(), "http://" + httpProxy.Address(), "socks5://" + socks.Address(), "http=" + httpProxy.Address() + ";socks=" + socks.Address()} {
		result := testProxyServer(server, testUrl, 3*time.Second)
		if !result.Ok || result.Status != http.StatusNoContent {
			t.Errorf("经 %s 测速应成功：%+v", server, result)
		}
	}
	closed, _ := net.Listen("tcp", "127.0.0.1:0")
	closedAddress := closed.Addr().String()
	closed.Close()
	result := testProxyServer(closedAddress, testUrl, time.Second)
	if result.Ok || !strings.Contains(result.Message, "连不上代理") {
		t.Errorf("端口没有监听时应报连不上：%+v", result)
	}
}

func TestTestPacUrl(t *testing.T) {
	server, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer server.Close()
	go func() {
		_ = http.Serve(server, http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
			switch request.URL.Path {
			case "/proxy.pac":
				_, _ = writer.Write([]byte(`function FindProxyForURL(url, host) { return "DIRECT"; }`))
			case "/page":
				_, _ = writer.Write([]byte("<html></html>"))
			default:
				writer.WriteHeader(http.StatusNotFound)
			}
		}))
	}()
	base := "http://" + server.Addr().String()
	if result := testPacUrl(base+"/proxy.pac", time.Second); !result.Ok {
		t.Errorf("PAC 应可下载：%+v", result)
	}
	if result := testPacUrl(base+"/page", time.Second); result.Ok || !strings.Contains(result.Message, "FindProxyForURL") {
		t.Errorf("不是 PAC 的内容应提示：%+v", result)
	}
	if result := testPacUrl(base+"/missing", time.Second); result.Ok || result.Status != http.StatusNotFound {
		t.Errorf("404 应提示：%+v", result)
	}
}

func TestFriendlyNetError(t *testing.T) {
	closed, _ := net.Listen("tcp", "127.0.0.1:0")
	address := closed.Addr().String()
	closed.Close()
	_, err := net.DialTimeout("tcp", address, time.Second)
	if err == nil {
		t.Skip("端口意外可连")
	}
	if message := friendlyNetError(err); !strings.Contains(message, "连接被拒绝") {
		t.Errorf("连接被拒绝的提示不对：%s", message)
	}
}
