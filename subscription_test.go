package main

import (
	"encoding/base64"
	"errors"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

const testClashSubscription = `# 机场订阅
mixed-port: 7890
proxies:
  - name: "香港 01"
    type: ss
    server: hk.example.com
    port: 443
    cipher: aes-128-gcm
    password: secret
  - {name: 日本 02, type: trojan, server: jp.example.com, port: 443, password: secret}
  - name: 美国 03
    type: vmess
    server: us.example.com
    port: 443
    uuid: 00000000-0000-0000-0000-000000000000
    ws-opts:
      path: /
      headers:
        - Host: us.example.com
proxy-groups:
  - name: 节点选择
    type: select
    proxies: [香港 01, 日本 02, 美国 03]
rules:
  - MATCH,节点选择
`

func TestInspectSubscription(t *testing.T) {
	links := "ss://YWVzLTEyOC1nY206c2VjcmV0@hk.example.com:443#%E9%A6%99%E6%B8%AF\nvless://uuid@jp.example.com:443?security=reality#Japan\ntrojan://secret@us.example.com:443#US\n"
	encoded := base64.StdEncoding.EncodeToString([]byte(links))
	wrapped := encoded[:40] + "\n" + encoded[40:]
	cases := []struct {
		name    string
		content string
		format  string
		nodes   int
		problem string
	}{
		{"Clash 配置", testClashSubscription, "clash", 3, ""},
		{"没有缩进的列表", "proxies:\n- name: a\n  type: ss\n- name: b\n  type: ss\nrules: []\n", "clash", 2, ""},
		{"一行写完的列表", "proxies: [{name: a, type: ss}, {name: b, type: ss}]\n", "clash", 2, ""},
		{"空的列表", "proxies: []\nrules: []\n", "", 0, "没有节点"},
		{"proxies 为空", "proxies:\nrules: []\n", "", 0, "没有节点"},
		{"base64 节点链接", encoded, "links", 3, ""},
		{"带换行的 base64", wrapped, "links", 3, ""},
		{"URL 安全的 base64", base64.RawURLEncoding.EncodeToString([]byte(links)), "links", 3, ""},
		{"明文节点链接", links, "links", 3, ""},
		{"带 BOM", "\xef\xbb\xbf" + links, "links", 3, ""},
		{"网页", "<!DOCTYPE html><html><body>登录</body></html>", "", 0, "网页"},
		{"空内容", "  \n", "", 0, "空的"},
		{"看不懂的内容", "hello world", "", 0, "既不是"},
	}
	for _, item := range cases {
		format, nodes, err := inspectSubscription([]byte(item.content))
		if item.problem != "" {
			if err == nil || !strings.Contains(err.Error(), item.problem) || !errors.Is(err, errBadSubscription) {
				t.Errorf("%s：应报错「%s」，得到 %v", item.name, item.problem, err)
			}
			continue
		}
		if err != nil || format != item.format || nodes != item.nodes {
			t.Errorf("%s：得到 %q %d %v，应为 %q %d", item.name, format, nodes, err, item.format, item.nodes)
		}
	}
}

func TestParseSubscriptionUserinfo(t *testing.T) {
	info := parseSubscriptionUserinfo("upload=1073741824; download=5368709120; Total=107374182400; expire=1798761600; other=abc")
	if info.Upload != 1<<30 || info.Download != 5<<30 || info.Total != 100<<30 || info.Expire != 1798761600 {
		t.Errorf("解析结果不对：%+v", info)
	}
	if info := parseSubscriptionUserinfo("upload=1.5e3;download=;total=x"); info.Upload != 1500 || info.Download != 0 || info.Total != 0 {
		t.Errorf("小数和错误的值：%+v", info)
	}
	if info := parseSubscriptionUserinfo(""); info != (SubscriptionInfo{}) {
		t.Errorf("没有这个响应头时应为空：%+v", info)
	}
}

func TestSubscriptionNameFromHeader(t *testing.T) {
	cases := map[string]string{
		"attachment; filename*=UTF-8''%E6%88%91%E7%9A%84%E6%9C%BA%E5%9C%BA.yaml": "我的机场",
		`attachment; filename="Example Cloud.yml"`:                               "Example Cloud",
		"attachment; filename=nodes":                                             "nodes",
		"inline":                                                                 "",
		"":                                                                       "",
		"attachment; filename*=bad''%zz":                                         "",
	}
	for header, wanted := range cases {
		if got := subscriptionNameFromHeader(header); got != wanted {
			t.Errorf("%q 得到 %q，应为 %q", header, got, wanted)
		}
	}
	long := "attachment; filename=" + strings.Repeat("a", 60)
	if got := subscriptionNameFromHeader(long); len([]rune(got)) != maxProfileNameLength {
		t.Errorf("太长的名字应截断：%q", got)
	}
}

func TestFetchSubscription(t *testing.T) {
	var userAgent string
	status := http.StatusOK
	content := testClashSubscription
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		userAgent = request.Header.Get("User-Agent")
		writer.Header().Set("subscription-userinfo", "upload=100; download=200; total=1000; expire=1798761600")
		writer.Header().Set("Content-Disposition", "attachment; filename*=UTF-8''%E6%B5%8B%E8%AF%95%E6%9C%BA%E5%9C%BA")
		writer.WriteHeader(status)
		_, _ = writer.Write([]byte(content))
	}))
	defer server.Close()

	result, err := fetchSubscription(server.URL+"/sub?token=abc", []string{""})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(userAgent, "clash.meta/") {
		t.Errorf("User-Agent 应让机场返回 Clash 格式：%q", userAgent)
	}
	if result.Format != "clash" || result.Nodes != 3 || result.Name != "测试机场" || string(result.Content) != testClashSubscription {
		t.Errorf("下载结果不对：%+v", result)
	}
	if result.Info.Upload != 100 || result.Info.Download != 200 || result.Info.Total != 1000 || result.Info.Expire != 1798761600 {
		t.Errorf("流量信息不对：%+v", result.Info)
	}

	// 走不通的网络路径换下一条；服务器返回错误状态或内容不对时不再重试。
	closed, _ := net.Listen("tcp", "127.0.0.1:0")
	deadProxy := "http://" + closed.Addr().String()
	closed.Close()
	if _, err := fetchSubscription(server.URL, []string{deadProxy, ""}); err != nil {
		t.Errorf("第一条路径连不上时应换下一条：%v", err)
	}
	status = http.StatusForbidden
	if _, err := fetchSubscription(server.URL, []string{"", deadProxy}); err == nil || !strings.Contains(err.Error(), "HTTP 403") || !strings.Contains(err.Error(), "失效") {
		t.Errorf("HTTP 403 应说明订阅可能失效：%v", err)
	}
	status = http.StatusOK
	content = "<html>请先登录</html>"
	if _, err := fetchSubscription(server.URL, []string{""}); err == nil || !errors.Is(err, errBadSubscription) {
		t.Errorf("返回网页时应报错：%v", err)
	}
	if _, err := fetchSubscription("ftp://example.com/sub", []string{""}); err == nil || !strings.Contains(err.Error(), "http") {
		t.Errorf("订阅地址不是 http(s) 时应报错：%v", err)
	}
	if _, err := fetchSubscription(server.URL, []string{deadProxy}); err == nil {
		t.Error("所有路径都连不上时应报错")
	}
}

func TestWriteSubscriptionFile(t *testing.T) {
	dir := t.TempDir()
	if err := writeSubscriptionFile(dir, "p1234", []byte("proxies: []\n")); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(filepath.Join(dir, "subscriptions", "p1234.yaml"))
	if err != nil || string(data) != "proxies: []\n" {
		t.Errorf("订阅文件内容不对：%q %v", data, err)
	}
	if subscriptionSource("https://a.example/sub") == subscriptionSource("https://b.example/sub") || subscriptionSource(" https://a.example/sub ") != subscriptionSource("https://a.example/sub") {
		t.Error("订阅地址的摘要应只随地址变化")
	}
}
