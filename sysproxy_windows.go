//go:build windows

package main

import (
	"fmt"
	"strings"
)

// Windows 系统代理（“设置 → 网络和 Internet → 代理”）的读写。
// 做法与 Clash Verge / v2rayN 等工具一致：写 HKCU 下 Internet Settings 的
// ProxyEnable / ProxyServer / ProxyOverride / AutoConfigURL，然后通知 WinINET 刷新。

const inetSettingsKey = `Software\Microsoft\Windows\CurrentVersion\Internet Settings`

// SystemProxyState 是当前系统代理的快照。
type SystemProxyState struct {
	Enabled bool   // ProxyEnable == 1（手动代理开关）
	Server  string // ProxyServer
	Bypass  string // ProxyOverride
	PAC     string // AutoConfigURL（非空表示“使用设置脚本”已打开）
}

// Active 表示系统代理是否处于生效状态（手动代理或 PAC 任一开启）。
func (s SystemProxyState) Active() bool {
	return s.Enabled || strings.TrimSpace(s.PAC) != ""
}

// Describe 用于在菜单/通知里展示当前生效的代理。
func (s SystemProxyState) Describe() string {
	switch {
	case s.PAC != "" && s.Enabled:
		return fmt.Sprintf("PAC %s + %s", s.PAC, s.Server)
	case s.PAC != "":
		return "PAC " + s.PAC
	case s.Enabled:
		return s.Server
	}
	return "未开启"
}

func readSystemProxy() (SystemProxyState, error) {
	var st SystemProxyState
	k, err := regOpen(hkeyCurrentUser, inetSettingsKey, keyRead)
	if err != nil {
		return st, err
	}
	defer k.close()

	if v, err := k.getDWORD("ProxyEnable"); err == nil {
		st.Enabled = v != 0
	}
	if v, err := k.getString("ProxyServer"); err == nil {
		st.Server = strings.TrimSpace(v)
	}
	if v, err := k.getString("ProxyOverride"); err == nil {
		st.Bypass = v
	}
	if v, err := k.getString("AutoConfigURL"); err == nil {
		st.PAC = strings.TrimSpace(v)
	}
	if st.Enabled && st.Server == "" {
		// ProxyEnable=1 但没有服务器，等于没开
		st.Enabled = false
	}
	return st, nil
}

// setSystemProxy 开启系统代理。server 为 WinINET 格式（host:port 或 http=..;https=..），
// pac 非空时同时开启“使用设置脚本”。两者至少一个非空。
func setSystemProxy(server, bypass, pac string) error {
	server = strings.TrimSpace(server)
	pac = strings.TrimSpace(pac)
	if server == "" && pac == "" {
		return fmt.Errorf("server 与 pac 不能同时为空")
	}
	k, err := regCreate(hkeyCurrentUser, inetSettingsKey)
	if err != nil {
		return err
	}
	defer k.close()

	var errs []string
	if server != "" {
		if err := k.setString("ProxyServer", server); err != nil {
			errs = append(errs, err.Error())
		}
		if err := k.setString("ProxyOverride", bypass); err != nil {
			errs = append(errs, err.Error())
		}
		if err := k.setDWORD("ProxyEnable", 1); err != nil {
			errs = append(errs, err.Error())
		}
	} else {
		if err := k.setDWORD("ProxyEnable", 0); err != nil {
			errs = append(errs, err.Error())
		}
	}
	if pac != "" {
		if err := k.setString("AutoConfigURL", pac); err != nil {
			errs = append(errs, err.Error())
		}
	} else {
		if err := k.deleteValue("AutoConfigURL"); err != nil {
			errs = append(errs, err.Error())
		}
	}
	refreshWinINET()
	if len(errs) > 0 {
		return fmt.Errorf("%s", strings.Join(errs, "; "))
	}
	return nil
}

// disableSystemProxy 关闭系统代理（手动代理与 PAC 一起关），保留 ProxyServer 便于系统设置里回显。
func disableSystemProxy() error {
	k, err := regCreate(hkeyCurrentUser, inetSettingsKey)
	if err != nil {
		return err
	}
	defer k.close()
	var errs []string
	if err := k.setDWORD("ProxyEnable", 0); err != nil {
		errs = append(errs, err.Error())
	}
	if err := k.deleteValue("AutoConfigURL"); err != nil {
		errs = append(errs, err.Error())
	}
	refreshWinINET()
	if len(errs) > 0 {
		return fmt.Errorf("%s", strings.Join(errs, "; "))
	}
	return nil
}

// refreshWinINET 通知系统代理设置已变更，让浏览器等 WinINET/WinHTTP 客户端立即生效。
func refreshWinINET() {
	procInternetSetOptionW.Call(0, internetOptionSettingsChanged, 0, 0)
	procInternetSetOptionW.Call(0, internetOptionProxySettingsChanged, 0, 0)
	procInternetSetOptionW.Call(0, internetOptionRefresh, 0, 0)
}
