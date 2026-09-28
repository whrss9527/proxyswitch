//go:build windows

package main

import "testing"

// TestWinHttpProxy 经 netsh 设置 WinHTTP 的代理再读回来，结束后恢复原来的设置。以管理员身份运行时不会弹出确认。
func TestWinHttpProxy(t *testing.T) {
	proxy, bypass, err := readWinHttpProxy()
	if err != nil {
		t.Fatal(err)
	}
	t.Logf("WinHTTP 的代理：%q，不走代理：%q", proxy, bypass)
	requireRegistryTests(t)
	t.Cleanup(func() {
		if err := writeWinHttpProxy(proxy, bypass); err != nil {
			t.Errorf("恢复 WinHTTP 的代理失败：%v", err)
		}
	})
	if err := writeWinHttpProxy("http=127.0.0.1:1;https=127.0.0.1:2", "<local>;*.example.com"); err != nil {
		t.Fatal(err)
	}
	if got, gotBypass, err := readWinHttpProxy(); err != nil || got != "http=127.0.0.1:1;https=127.0.0.1:2" || gotBypass != "<local>;*.example.com" {
		t.Errorf("设置后读到 %q %q %v", got, gotBypass, err)
	}
	if err := writeWinHttpProxy("", ""); err != nil {
		t.Fatal(err)
	}
	if got, _, err := readWinHttpProxy(); err != nil || got != "" {
		t.Errorf("改回直连后读到 %q %v", got, err)
	}
}
