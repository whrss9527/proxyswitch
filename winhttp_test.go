package main

import "testing"

func TestWinHttpProxyFor(t *testing.T) {
	for server, want := range map[string]string{
		"127.0.0.1:7890":                           "127.0.0.1:7890",
		"http://127.0.0.1:7890/":                   "127.0.0.1:7890",
		"http=10.0.0.1:8080;https=10.0.0.1:8443":   "http=10.0.0.1:8080;https=10.0.0.1:8443",
		"https=http://10.0.0.1:8443;socks=1.2.3.4": "https=10.0.0.1:8443",
	} {
		if got, err := winHttpProxyFor(server); err != nil || got != want {
			t.Errorf("%s 应转成 %s：%s %v", server, want, got, err)
		}
	}
	for _, server := range []string{"socks5://127.0.0.1:1080", "socks=127.0.0.1:1080", "https://proxy.example.com:443", ""} {
		if got, err := winHttpProxyFor(server); err == nil {
			t.Errorf("%s 不能用于 WinHTTP：%s", server, got)
		}
	}
}

func TestWinHttpSuggestion(t *testing.T) {
	fixture := newEngineFixture(t, engineTestConfig)
	engine := fixture.engine
	if proxy, _, reason := engine.WinHttpSuggestion(); proxy != "" || reason == "" {
		t.Errorf("代理没开时不能设：%q %q", proxy, reason)
	}
	for name, want := range map[string]string{"本机": "127.0.0.1:7890", "公司": "http=10.0.0.1:8080;https=10.0.0.1:8080", "PAC": "", "只改终端": ""} {
		if err := engine.UseProfile(name); err != nil {
			t.Fatal(err)
		}
		proxy, bypass, reason := engine.WinHttpSuggestion()
		if proxy != want || (want == "") != (reason != "") {
			t.Errorf("%s：%q %q，应为 %q", name, proxy, reason, want)
		}
		if want != "" && bypass != engine.Config().FindProfile(name).Bypass {
			t.Errorf("%s 不走代理的地址应跟配置一样：%q", name, bypass)
		}
	}
	if !engine.WinHttpUsesCore(coreServer(engine.Config().Core.Port)) || engine.WinHttpUsesCore("127.0.0.1:7890") || engine.WinHttpUsesCore("") {
		t.Error("只有内置内核的地址算用内核")
	}
}
