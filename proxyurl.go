package main

import (
	"strings"
)

// 不同目标对代理地址的写法不一样：
//   - Windows 系统代理（WinINET）：host:port，或 http=host:port;https=host:port;socks=host:port
//   - 环境变量 / git / npm：http://host:port 或 socks5://host:port
// 这里做两边的互相转换，用户在配置里两种写法都可以。

// serverToWinINET 把用户填写的 server 规范成 WinINET 的 ProxyServer 格式。
func serverToWinINET(server string) string {
	s := strings.TrimSpace(server)
	if s == "" {
		return ""
	}
	if strings.Contains(s, "=") {
		// 已经是 http=..;https=.. 形式，去掉多余空白
		var parts []string
		for _, p := range strings.Split(s, ";") {
			p = strings.TrimSpace(p)
			if p != "" {
				parts = append(parts, p)
			}
		}
		return strings.Join(parts, ";")
	}
	lower := strings.ToLower(s)
	switch {
	case strings.HasPrefix(lower, "http://"):
		return strings.TrimSuffix(s[len("http://"):], "/")
	case strings.HasPrefix(lower, "https://"):
		return strings.TrimSuffix(s[len("https://"):], "/")
	case strings.HasPrefix(lower, "socks5h://"):
		return "socks=" + strings.TrimSuffix(s[len("socks5h://"):], "/")
	case strings.HasPrefix(lower, "socks5://"):
		return "socks=" + strings.TrimSuffix(s[len("socks5://"):], "/")
	case strings.HasPrefix(lower, "socks://"):
		return "socks=" + strings.TrimSuffix(s[len("socks://"):], "/")
	}
	return s
}

// serverToURL 把 server 转成环境变量 / git / npm 能用的 URL。
// 优先取 https= 项，其次 http=，再次 socks=（转为 socks5://）。
func serverToURL(server string) string {
	s := strings.TrimSpace(server)
	if s == "" {
		return ""
	}
	if strings.Contains(s, "=") {
		entries := map[string]string{}
		for _, p := range strings.Split(s, ";") {
			p = strings.TrimSpace(p)
			i := strings.Index(p, "=")
			if i <= 0 {
				continue
			}
			entries[strings.ToLower(strings.TrimSpace(p[:i]))] = strings.TrimSpace(p[i+1:])
		}
		if v := entries["https"]; v != "" {
			return "http://" + stripScheme(v)
		}
		if v := entries["http"]; v != "" {
			return "http://" + stripScheme(v)
		}
		if v := entries["socks"]; v != "" {
			return "socks5://" + stripScheme(v)
		}
		return ""
	}
	if strings.Contains(s, "://") {
		return strings.TrimSuffix(s, "/")
	}
	return "http://" + s
}

func stripScheme(s string) string {
	if i := strings.Index(s, "://"); i >= 0 {
		s = s[i+3:]
	}
	return strings.TrimSuffix(s, "/")
}

// sameServer 比较两个 WinINET 格式的服务器串是否等价（忽略大小写、空白与顺序无关的分号项）。
func sameServer(a, b string) bool {
	na := normalizeServer(a)
	nb := normalizeServer(b)
	return na != "" && na == nb
}

func normalizeServer(s string) string {
	s = strings.ToLower(serverToWinINET(s))
	if !strings.Contains(s, "=") {
		return s
	}
	parts := strings.Split(s, ";")
	// 简单的插入排序，避免引入 sort 包的额外依赖也可以，这里直接用 sort
	sortStrings(parts)
	return strings.Join(parts, ";")
}

func sortStrings(a []string) {
	for i := 1; i < len(a); i++ {
		for j := i; j > 0 && a[j] < a[j-1]; j-- {
			a[j], a[j-1] = a[j-1], a[j]
		}
	}
}
