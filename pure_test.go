package main

import (
	"strings"
	"testing"
)

func TestParseHotkey(t *testing.T) {
	cases := []struct {
		in   string
		mods uint32
		vk   uint32
		text string
		err  bool
	}{
		{"Ctrl+Alt+P", hkModControl | hkModAlt, 'P', "Ctrl+Alt+P", false},
		{"ctrl + shift + f9", hkModControl | hkModShift, 0x78, "Ctrl+Shift+F9", false},
		{"Win+Space", hkModWin, 0x20, "Win+Space", false},
		{"Alt+1", hkModAlt, '1', "Alt+1", false},
		{"Ctrl+Alt+Numpad0", hkModControl | hkModAlt, 0x60, "Ctrl+Alt+Numpad0", false},
		{"Ctrl++", hkModControl, 0xBB, "Ctrl++", false},
		{"Ctrl+Alt+`", hkModControl | hkModAlt, 0xC0, "Ctrl+Alt+`", false},
		{"P", 0, 0, "", true},            // 没有修饰键
		{"Ctrl+Alt", 0, 0, "", true},     // 没有主键
		{"Ctrl+A+B", 0, 0, "", true},     // 两个主键
		{"Ctrl+Alt+F25", 0, 0, "", true}, // 不存在的键
		{"", 0, 0, "", true},
	}
	for _, c := range cases {
		hk, err := parseHotkey(c.in)
		if c.err {
			if err == nil {
				t.Errorf("%q: 期望报错，得到 %+v", c.in, hk)
			}
			continue
		}
		if err != nil {
			t.Errorf("%q: 意外错误 %v", c.in, err)
			continue
		}
		if hk.Mods != c.mods || hk.VK != c.vk || hk.Text != c.text {
			t.Errorf("%q: 得到 mods=%#x vk=%#x text=%q，期望 mods=%#x vk=%#x text=%q",
				c.in, hk.Mods, hk.VK, hk.Text, c.mods, c.vk, c.text)
		}
	}
}

func TestStripJSONC(t *testing.T) {
	in := `{
  // 注释
  "a": "http://x/y", /* 块注释 */
  "b": [1, 2, 3,],
  "c": "字符串里的 // 不是注释",
  "d": "转义 \" 引号 // 也不是",
}`
	got := stripJSONC(in)
	if strings.Contains(got, "注释") && !strings.Contains(got, "不是注释") {
		t.Fatalf("注释没有去干净: %s", got)
	}
	if strings.Contains(got, "块注释") {
		t.Fatalf("块注释没有去掉: %s", got)
	}
	if !strings.Contains(got, `"http://x/y"`) {
		t.Fatalf("字符串里的 // 被误删: %s", got)
	}
	if strings.Contains(got, "3,]") || strings.Contains(got, ",\n}") {
		t.Fatalf("末尾逗号没有去掉: %s", got)
	}
	cfg, err := parseConfig(`{"profiles":[{"name":"x","server":"127.0.0.1:1"}],}`)
	if err != nil {
		t.Fatalf("末尾逗号应当被容忍: %v", err)
	}
	if cfg.Profiles[0].Bypass != defaultBypass || cfg.Profiles[0].ApplyTo[0] != targetSystem {
		t.Fatalf("默认值没有补全: %+v", cfg.Profiles[0])
	}
}

func TestDefaultConfigParses(t *testing.T) {
	cfg, err := parseConfig(defaultConfigText)
	if err != nil {
		t.Fatalf("默认配置解析失败: %v", err)
	}
	if len(cfg.Profiles) != 3 {
		t.Fatalf("期望 3 套配置，得到 %d", len(cfg.Profiles))
	}
	if cfg.Hotkey != "Ctrl+Alt+P" || !cfg.Notify || cfg.NotifySeconds != 3 {
		t.Fatalf("默认值不对: %+v", cfg)
	}
	// 没写 notify_seconds 时默认 3；写 0 表示跟随系统；超范围报错
	if c, err := parseConfig(`{"profiles":[{"name":"a","server":"a:1"}]}`); err != nil || c.NotifySeconds != 3 {
		t.Fatalf("notify_seconds 默认值应为 3: %v %+v", err, c)
	}
	if c, err := parseConfig(`{"notify_seconds":0,"profiles":[{"name":"a","server":"a:1"}]}`); err != nil || c.NotifySeconds != 0 {
		t.Fatalf("notify_seconds=0 应被接受: %v", err)
	}
	if _, err := parseConfig(`{"notify_seconds":61,"profiles":[{"name":"a","server":"a:1"}]}`); err == nil {
		t.Fatalf("notify_seconds=61 应报错")
	}
	if cfg.FindProfile("公司 pac（示例）") == nil {
		t.Fatalf("FindProfile 应该不区分大小写")
	}
	// 带 BOM 也要能解析（记事本另存为 UTF-8 可能带 BOM）
	if _, err := parseConfig("\ufeff" + defaultConfigText); err != nil {
		t.Fatalf("带 BOM 的配置解析失败: %v", err)
	}
}

func TestConfigValidation(t *testing.T) {
	bad := []string{
		`{"profiles":[]}`,
		`{"profiles":[{"name":"","server":"a:1"}]}`,
		`{"profiles":[{"name":"a","server":"a:1"},{"name":"A","server":"b:1"}]}`,
		`{"profiles":[{"name":"a"}]}`,
		`{"profiles":[{"name":"a","pac":"http://p","apply_to":["env"]}]}`,
		`{"profiles":[{"name":"a","server":"a:1","apply_to":["docker"]}]}`,
		`{"hotkey":"P","profiles":[{"name":"a","server":"a:1"}]}`,
		`{"profiles":[{"name":"a","server":"a:1"}] oops`,
	}
	for _, b := range bad {
		if _, err := parseConfig(b); err == nil {
			t.Errorf("应当报错但没有: %s", b)
		}
	}
	good := `{"notify": false, "hotkey": "", "profiles":[{"name":"a","server":"http://a:1","apply_to":["System","ENV"]}]}`
	cfg, err := parseConfig(good)
	if err != nil {
		t.Fatalf("意外错误: %v", err)
	}
	if cfg.Notify || cfg.Hotkey != "" {
		t.Fatalf("显式设置的值被覆盖: %+v", cfg)
	}
	if cfg.Profiles[0].ApplyTo[0] != "system" || cfg.Profiles[0].ApplyTo[1] != "env" {
		t.Fatalf("apply_to 应当被规范成小写: %v", cfg.Profiles[0].ApplyTo)
	}
}

func TestServerConversions(t *testing.T) {
	cases := []struct{ in, wininet, url string }{
		{"127.0.0.1:7890", "127.0.0.1:7890", "http://127.0.0.1:7890"},
		{"http://127.0.0.1:7890", "127.0.0.1:7890", "http://127.0.0.1:7890"},
		{"http://127.0.0.1:7890/", "127.0.0.1:7890", "http://127.0.0.1:7890"},
		{"socks5://127.0.0.1:7891", "socks=127.0.0.1:7891", "socks5://127.0.0.1:7891"},
		{"http=127.0.0.1:7890;https=127.0.0.1:7890;socks=127.0.0.1:7891", "http=127.0.0.1:7890;https=127.0.0.1:7890;socks=127.0.0.1:7891", "http://127.0.0.1:7890"},
		{"socks=127.0.0.1:7891", "socks=127.0.0.1:7891", "socks5://127.0.0.1:7891"},
		{" proxy.corp.com:8080 ", "proxy.corp.com:8080", "http://proxy.corp.com:8080"},
		{"", "", ""},
	}
	for _, c := range cases {
		if got := serverToWinINET(c.in); got != c.wininet {
			t.Errorf("serverToWinINET(%q) = %q, 期望 %q", c.in, got, c.wininet)
		}
		if got := serverToURL(c.in); got != c.url {
			t.Errorf("serverToURL(%q) = %q, 期望 %q", c.in, got, c.url)
		}
	}
	if !sameServer("http://127.0.0.1:7890", "127.0.0.1:7890") {
		t.Error("sameServer 应当忽略 scheme")
	}
	if !sameServer("https=a:1;http=a:1", "http=a:1;https=a:1") {
		t.Error("sameServer 应当忽略顺序")
	}
	if sameServer("", "") {
		t.Error("空串不应相等")
	}
	if sameServer("127.0.0.1:7890", "127.0.0.1:7891") {
		t.Error("不同端口不应相等")
	}
}

func TestUpdateNpmrc(t *testing.T) {
	orig := "registry=https://registry.npmmirror.com\r\nproxy=http://old:1\r\n# comment\r\nhttps-proxy=http://old:1\r\n"
	got := updateNpmrc(orig, "http://127.0.0.1:7890", "localhost,127.0.0.1")
	want := "registry=https://registry.npmmirror.com\r\nproxy=http://127.0.0.1:7890\r\n# comment\r\nhttps-proxy=http://127.0.0.1:7890\r\nnoproxy=localhost,127.0.0.1\r\n"
	if got != want {
		t.Fatalf("设置代理结果不对:\n%q\n期望:\n%q", got, want)
	}
	got = updateNpmrc(got, "", "")
	want = "registry=https://registry.npmmirror.com\r\n# comment\r\n"
	if got != want {
		t.Fatalf("移除代理结果不对:\n%q\n期望:\n%q", got, want)
	}
	if got := updateNpmrc("", "", ""); got != "" {
		t.Fatalf("空文件移除代理应得到空串，得到 %q", got)
	}
	if got := updateNpmrc("", "http://a:1", ""); got != "proxy=http://a:1\nhttps-proxy=http://a:1\n" {
		t.Fatalf("空文件设置代理结果不对: %q", got)
	}
	// 重复行只保留一条，其它写法（https_proxy / 冒号分隔）也能识别
	got = updateNpmrc("proxy = x\nproxy=y\nhttps_proxy: z\n", "http://b:2", "")
	if got != "proxy=http://b:2\nhttps-proxy=http://b:2\n" {
		t.Fatalf("重复行处理不对: %q", got)
	}
}
