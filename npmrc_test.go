package main

import "testing"

func TestUpdateNpmrc(t *testing.T) {
	cases := []struct {
		explain, content, proxyUrl, noProxy, wanted string
	}{
		{"空文件写入", "", "http://127.0.0.1:7890", "localhost", "proxy=http://127.0.0.1:7890\nhttps-proxy=http://127.0.0.1:7890\nnoproxy=localhost\n"},
		{
			"保留其他内容并替换旧值",
			"registry=https://registry.npmmirror.com\n# 注释\nproxy = http://old:1\nHTTPS_PROXY=http://old:1\nsave-exact=true\n",
			"http://new:2", "",
			"registry=https://registry.npmmirror.com\n# 注释\nproxy=http://new:2\nhttps-proxy=http://new:2\nsave-exact=true\n",
		},
		{"移除代理", "proxy=http://a:1\nhttps-proxy=http://a:1\nnoproxy=x\nfund=false\n", "", "", "fund=false\n"},
		{"只有代理时移除后为空", "proxy=http://a:1\n", "", "", ""},
		{"保留 CRLF", "fund=false\r\nproxy=http://a:1\r\n", "http://b:2", "", "fund=false\r\nproxy=http://b:2\r\nhttps-proxy=http://b:2\r\n"},
		{"重复的键只保留一个", "proxy=http://a:1\nproxy=http://a:2\n", "http://b:2", "", "proxy=http://b:2\nhttps-proxy=http://b:2\n"},
		{"注释里的键不动", "; proxy=http://keep\n", "", "", "; proxy=http://keep\n"},
	}
	for _, item := range cases {
		if got := updateNpmrc(item.content, item.proxyUrl, item.noProxy); got != item.wanted {
			t.Errorf("%s：\n得到 %q\n应为 %q", item.explain, got, item.wanted)
		}
	}
}
