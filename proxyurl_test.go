package main

import "testing"

func TestServerConversions(t *testing.T) {
	cases := []struct {
		server, winInet, url string
	}{
		{"127.0.0.1:7890", "127.0.0.1:7890", "http://127.0.0.1:7890"},
		{" http://127.0.0.1:7890/ ", "127.0.0.1:7890", "http://127.0.0.1:7890"},
		{"https://proxy.corp:443", "proxy.corp:443", "https://proxy.corp:443"},
		{"socks5://127.0.0.1:1080", "socks=127.0.0.1:1080", "socks5://127.0.0.1:1080"},
		{"socks://127.0.0.1:1080", "socks=127.0.0.1:1080", "socks5://127.0.0.1:1080"},
		{"socks5h://127.0.0.1:1080", "socks=127.0.0.1:1080", "socks5h://127.0.0.1:1080"},
		{"http=a:1; https=b:2 ;", "http=a:1;https=b:2", "http://b:2"},
		{"http=a:1;socks=c:3", "http=a:1;socks=c:3", "http://a:1"},
		{"socks=c:3", "socks=c:3", "socks5://c:3"},
		{"[::1]:7890", "[::1]:7890", "http://[::1]:7890"},
		{"", "", ""},
	}
	for _, item := range cases {
		if got := serverToWinInet(item.server); got != item.winInet {
			t.Errorf("serverToWinInet(%q) = %q，应为 %q", item.server, got, item.winInet)
		}
		if got := serverToUrl(item.server); got != item.url {
			t.Errorf("serverToUrl(%q) = %q，应为 %q", item.server, got, item.url)
		}
	}
}

func TestProxyUrlForTarget(t *testing.T) {
	cases := []struct {
		server, scheme, wanted string
	}{
		{"127.0.0.1:7890", "https", "http://127.0.0.1:7890"},
		{"socks5h://127.0.0.1:1080", "https", "socks5://127.0.0.1:1080"},
		{"http=a:1;https=b:2", "https", "http://b:2"},
		{"http=a:1;https=b:2", "http", "http://a:1"},
		{"https=b:2", "http", "http://b:2"},
		{"socks=c:3", "http", "socks5://c:3"},
	}
	for _, item := range cases {
		proxyUrl, err := proxyUrlForTarget(item.server, item.scheme)
		if err != nil || proxyUrl.String() != item.wanted {
			t.Errorf("proxyUrlForTarget(%q, %q) = %v %v，应为 %q", item.server, item.scheme, proxyUrl, err, item.wanted)
		}
	}
	if _, err := proxyUrlForTarget("ftp=a:1", "https"); err == nil {
		t.Error("没有可用协议时应报错")
	}
	if serverEndpoint("socks5://127.0.0.1:1080") != "127.0.0.1:1080" || serverEndpoint("") != "" {
		t.Error("serverEndpoint 结果不对")
	}
}

func TestValidateServer(t *testing.T) {
	valid := []string{"127.0.0.1:7890", "proxy.corp.com:8080", "socks5://127.0.0.1:1080", "http=a:1;https=b:2;socks=c:3", "[::1]:8080", "https://p:443"}
	for _, server := range valid {
		if err := validateServer(server); err != nil {
			t.Errorf("%q 应当合法：%v", server, err)
		}
	}
	invalid := []string{"127.0.0.1", "127.0.0.1:0", "127.0.0.1:65536", "a b:1", "ftp://a:1", "http=a", "gopher=a:1", ":8080", "user@host:1", "a:port"}
	for _, server := range invalid {
		if err := validateServer(server); err == nil {
			t.Errorf("%q 应当不合法", server)
		}
	}
}

func TestSameServer(t *testing.T) {
	if !sameServer("127.0.0.1:7890", "http://127.0.0.1:7890") {
		t.Error("有无 http:// 应视为相同")
	}
	if !sameServer("https=b:2;http=a:1", "HTTP=a:1;https=b:2") {
		t.Error("分号项顺序和大小写不同应视为相同")
	}
	if !sameServer("socks5://c:3", "socks=c:3") {
		t.Error("socks5:// 与 socks= 应视为相同")
	}
	if sameServer("a:1", "a:2") || sameServer("", "") {
		t.Error("不同地址或空地址不应视为相同")
	}
	if !isSocksServer("socks5h://a:1") || isSocksServer("a:1") || isSocksServer("http=a:1") {
		t.Error("isSocksServer 结果不对")
	}
}
