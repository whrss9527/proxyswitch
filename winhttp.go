package main

import (
	"errors"
	"strings"
)

// WinHTTP 的代理：Windows 更新、Microsoft Store 的下载、BITS 等系统服务不看系统代理（WinINET），用的是整台电脑的
// WinHTTP 代理设置（netsh winhttp show proxy）。修改要管理员权限，所以它不跟随代理的开关，由用户在系统集成页里
// 设为当前代理或改回直连。Windows 上的部分见 winhttp_windows.go。

// WinHttpInfo 是 WinHTTP 的代理设置：Proxy 为空表示直接连接，Bypass 是不走代理的地址。Suggested 是现在可以设给
// 它的代理（正在使用的配置的 HTTP 代理，订阅配置是内置内核的端口），SuggestedBypass 是一起设的不走代理的地址；
// 不能设时 Suggested 为空，Unsupported 说明原因。
type WinHttpInfo struct {
	Proxy           string `json:"proxy"`
	Bypass          string `json:"bypass"`
	Suggested       string `json:"suggested"`
	SuggestedBypass string `json:"suggested_bypass"`
	Unsupported     string `json:"unsupported,omitempty"`
	Error           string `json:"error,omitempty"`
}

var errWinHttpSocks = errors.New("WinHTTP 只能用 HTTP 代理，这个配置用的是 SOCKS 代理")

// winHttpProxyFor 把配置的代理地址转成 WinHTTP 的写法：一个地址，或者按协议分别指定的「http=…;https=…」。
func winHttpProxyFor(server string) (string, error) {
	server = strings.TrimSpace(server)
	if strings.Contains(server, "=") {
		entries := parseProtocolEntries(server)
		var parts []string
		for _, protocol := range []string{"http", "https"} {
			if address := stripScheme(entries[protocol]); address != "" {
				parts = append(parts, protocol+"="+address)
			}
		}
		if len(parts) == 0 {
			return "", errWinHttpSocks
		}
		return strings.Join(parts, ";"), nil
	}
	scheme, address := splitScheme(server)
	if scheme != "" && scheme != "http" {
		return "", errWinHttpSocks
	}
	if address == "" {
		return "", errors.New("配置里没有代理服务器地址")
	}
	return address, nil
}

// WinHttpSuggestion 是现在可以设给 WinHTTP 的代理和不走代理的地址；不能设时 proxy 为空，reason 说明原因。
func (engine *Engine) WinHttpSuggestion() (proxy, bypass, reason string) {
	status := engine.Status()
	if status.State != statusOn || status.Profile == nil {
		return "", "", "开启代理后可以设为当前代理"
	}
	if status.Profile.Server == "" {
		return "", "", "WinHTTP 不支持 PAC 脚本，这个配置用的是 PAC"
	}
	proxy, err := winHttpProxyFor(status.Profile.Server)
	if err != nil {
		return "", "", err.Error()
	}
	if strings.ContainsAny(proxy+status.Profile.Bypass, `" `) {
		return "", "", "代理地址或不走代理的地址里有空格或引号，WinHTTP 设置不了"
	}
	return proxy, status.Profile.Bypass, ""
}

// WinHttpUsesCore 表示 WinHTTP 的代理是内置内核的地址。内核在 ProxySwitch 运行期间一直在（有订阅配置时），
// 退出 ProxySwitch 后 Windows 更新等系统服务就连不上了。
func (engine *Engine) WinHttpUsesCore(proxy string) bool {
	return engine.config != nil && proxy != "" && proxy == coreServer(engine.config.Core.Port)
}
