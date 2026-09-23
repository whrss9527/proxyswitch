package main

import (
	"errors"
	"fmt"
	"net"
	"net/url"
	"sort"
	"strconv"
	"strings"
)

// 不同目标对代理地址的写法不一样：
//   - Windows 系统代理（WinINET）：host:port，或 http=host:port;https=host:port;socks=host:port
//   - 环境变量 / git / npm / 测速：http://host:port 或 socks5://host:port
// 配置里两种写法都接受，这里负责互相转换。

// serverToWinInet 把配置里的 server 转成 WinINET 的 ProxyServer 格式。
func serverToWinInet(server string) string {
	server = strings.TrimSpace(server)
	if server == "" {
		return ""
	}
	if strings.Contains(server, "=") {
		var entries []string
		for _, entry := range strings.Split(server, ";") {
			if entry = strings.TrimSpace(entry); entry != "" {
				entries = append(entries, entry)
			}
		}
		return strings.Join(entries, ";")
	}
	scheme, rest := splitScheme(server)
	switch scheme {
	case "socks", "socks5", "socks5h":
		return "socks=" + rest
	}
	return rest
}

// serverToUrl 把 server 转成环境变量 / git / npm 能用的 URL。
// 按协议分别指定时优先取 https 项，其次 http，再次 socks（转为 socks5://）。
func serverToUrl(server string) string {
	server = strings.TrimSpace(server)
	if server == "" {
		return ""
	}
	if strings.Contains(server, "=") {
		entries := parseProtocolEntries(server)
		switch {
		case entries["https"] != "":
			return "http://" + stripScheme(entries["https"])
		case entries["http"] != "":
			return "http://" + stripScheme(entries["http"])
		case entries["socks"] != "":
			return "socks5://" + stripScheme(entries["socks"])
		}
		return ""
	}
	scheme, rest := splitScheme(server)
	switch scheme {
	case "":
		return "http://" + rest
	case "socks":
		return "socks5://" + rest
	}
	return scheme + "://" + rest
}

// serverUrlScheme 返回 serverToUrl 结果的协议：http / https / socks5 / socks5h，无法识别时为空。
func serverUrlScheme(server string) string {
	scheme, _ := splitScheme(serverToUrl(server))
	return scheme
}

func isSocksServer(server string) bool {
	scheme := serverUrlScheme(server)
	return scheme == "socks5" || scheme == "socks5h"
}

// proxyUrlForTarget 返回访问 targetScheme（http / https）地址时应使用的代理，用于测速和连通检查。
func proxyUrlForTarget(server, targetScheme string) (*url.URL, error) {
	server = strings.TrimSpace(server)
	if server == "" {
		return nil, errors.New("没有填写代理服务器地址")
	}
	var chosen string
	if strings.Contains(server, "=") {
		entries := parseProtocolEntries(server)
		switch {
		case targetScheme == "https" && entries["https"] != "":
			chosen = "http://" + stripScheme(entries["https"])
		case entries["http"] != "":
			chosen = "http://" + stripScheme(entries["http"])
		case entries["https"] != "":
			chosen = "http://" + stripScheme(entries["https"])
		case entries["socks"] != "":
			chosen = "socks5://" + stripScheme(entries["socks"])
		default:
			return nil, errors.New("没有可用的 http / https / socks 项")
		}
	} else {
		chosen = serverToUrl(server)
		// Go 的 socks 拨号器本来就把域名交给代理解析，与 socks5h 语义一致。
		if strings.HasPrefix(chosen, "socks5h://") {
			chosen = "socks5://" + strings.TrimPrefix(chosen, "socks5h://")
		}
	}
	parsed, err := url.Parse(chosen)
	if err != nil || parsed.Host == "" {
		return nil, fmt.Errorf("代理地址 %q 格式不对", server)
	}
	return parsed, nil
}

// serverEndpoint 返回代理服务器的 host:port，用于检查代理是否还能连上。
func serverEndpoint(server string) string {
	proxyUrl, err := proxyUrlForTarget(server, "https")
	if err != nil {
		return ""
	}
	return proxyUrl.Host
}

func splitScheme(value string) (scheme, rest string) {
	value = strings.TrimSuffix(strings.TrimSpace(value), "/")
	if index := strings.Index(value, "://"); index >= 0 {
		return strings.ToLower(value[:index]), value[index+3:]
	}
	return "", value
}

func stripScheme(value string) string {
	_, rest := splitScheme(value)
	return rest
}

func parseProtocolEntries(server string) map[string]string {
	entries := map[string]string{}
	for _, entry := range strings.Split(server, ";") {
		key, value, found := strings.Cut(strings.TrimSpace(entry), "=")
		if !found {
			continue
		}
		entries[strings.ToLower(strings.TrimSpace(key))] = strings.TrimSpace(value)
	}
	return entries
}

func validateServer(server string) error {
	if strings.ContainsAny(server, " \t") {
		return errors.New("不能包含空格")
	}
	if strings.Contains(server, "=") {
		for _, entry := range strings.Split(server, ";") {
			entry = strings.TrimSpace(entry)
			if entry == "" {
				continue
			}
			key, value, found := strings.Cut(entry, "=")
			if !found {
				return fmt.Errorf("「%s」应写成 协议=主机:端口", entry)
			}
			key = strings.ToLower(strings.TrimSpace(key))
			if !containsString([]string{"http", "https", "ftp", "socks"}, key) {
				return fmt.Errorf("「%s」里的协议 %s 不认识，可用：http / https / ftp / socks", entry, key)
			}
			if err := validateHostPort(stripScheme(value)); err != nil {
				return fmt.Errorf("「%s」%v", entry, err)
			}
		}
		return nil
	}
	scheme, rest := splitScheme(server)
	if scheme != "" && !containsString([]string{"http", "https", "socks", "socks5", "socks5h"}, scheme) {
		return fmt.Errorf("不支持 %s:// 开头的地址，可用：http:// 或 socks5://", scheme)
	}
	return validateHostPort(rest)
}

func validateHostPort(value string) error {
	host, port, err := net.SplitHostPort(value)
	if err != nil {
		return errors.New("应写成 主机:端口，例如 127.0.0.1:7890")
	}
	if host == "" {
		return errors.New("缺少主机地址")
	}
	if strings.ContainsAny(host, "/?#@") {
		return errors.New("主机地址里不能有 / ? # @")
	}
	number, err := strconv.Atoi(port)
	if err != nil || number < 1 || number > 65535 {
		return errors.New("端口需要是 1~65535 之间的数字")
	}
	return nil
}

// sameServer 判断两个地址是否指向同一代理（忽略 scheme、大小写和分号项顺序）。
func sameServer(first, second string) bool {
	normalized := normalizeServer(first)
	return normalized != "" && normalized == normalizeServer(second)
}

func normalizeServer(server string) string {
	normalized := strings.ToLower(serverToWinInet(server))
	if !strings.Contains(normalized, "=") {
		return normalized
	}
	entries := strings.Split(normalized, ";")
	sort.Strings(entries)
	return strings.Join(entries, ";")
}
