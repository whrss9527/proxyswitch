package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"strings"
)

// 链接：proxyswitch:// 命令（和 macOS 版相同），以及机场网站「一键导入」按钮用的 clash://install-config。
// Windows 版把这两种链接登记给自己（见 links_windows.go）：浏览器或脚本打开链接时启动 ProxySwitch.exe，
// 链接换成和命令行一样的命令交给托盘程序执行。

const (
	linkScheme      = "proxyswitch"
	clashLinkScheme = "clash"
)

// LinkRequest 是解析后的链接。Command 和 Argument 与命令行转交给托盘程序的命令相同（见 main_windows.go 的
// onCopyData）：on、off、toggle、use（配置名）、share（on / off / 空表示切换）、settings（页面）、update（通知上的随机数）、
// diagnose（网址\x00视角），以及 import（订阅地址和名字的 JSON，打开添加订阅的对话框）。
type LinkRequest struct {
	Command  string
	Argument string
}

// isLink 表示命令行参数是链接，而不是命令。
func isLink(text string) bool {
	scheme, _, found := strings.Cut(strings.TrimSpace(text), ":")
	return found && (strings.EqualFold(scheme, linkScheme) || strings.EqualFold(scheme, clashLinkScheme))
}

// parseLink 解析 proxyswitch:// 或 clash://install-config 链接。
func parseLink(text string) (LinkRequest, error) {
	parsed, err := url.Parse(strings.TrimSpace(text))
	if err != nil {
		return LinkRequest{}, fmt.Errorf("链接格式不对：%v", err)
	}
	// proxyswitch://share/on 的命令在主机名的位置，proxyswitch:share/on 这样的写法在 Opaque 里。
	command, rest := strings.ToLower(parsed.Host), parsed.Path
	if parsed.Opaque != "" {
		command, rest, _ = strings.Cut(parsed.Opaque, "/")
		command = strings.ToLower(command)
	}
	rest = strings.Trim(rest, "/")
	query := parsed.Query()
	switch strings.ToLower(parsed.Scheme) {
	case clashLinkScheme:
		if command != "install-config" {
			return LinkRequest{}, fmt.Errorf("不认识的 clash:// 链接：%s", text)
		}
		return importLink(query)
	case linkScheme:
	default:
		return LinkRequest{}, fmt.Errorf("不认识的链接：%s", text)
	}
	switch command {
	case "on", "enable", "start":
		return LinkRequest{Command: "on"}, nil
	case "off", "disable", "stop":
		return LinkRequest{Command: "off"}, nil
	case "toggle":
		return LinkRequest{Command: "toggle"}, nil
	case "use", "switch":
		name := strings.TrimSpace(query.Get("name"))
		if name == "" {
			name = strings.TrimSpace(rest)
		}
		if name == "" {
			return LinkRequest{}, errors.New("链接里没有配置名，例如 proxyswitch://use?name=公司代理")
		}
		return LinkRequest{Command: "use", Argument: name}, nil
	case "share", "lan":
		state := strings.ToLower(query.Get("state"))
		if state == "" {
			state = strings.ToLower(rest)
		}
		switch state {
		case "on", "enable", "1", "true":
			return LinkRequest{Command: "share", Argument: "on"}, nil
		case "off", "disable", "0", "false":
			return LinkRequest{Command: "share", Argument: "off"}, nil
		}
		return LinkRequest{Command: "share"}, nil
	case "settings", "preferences":
		page := strings.ToLower(query.Get("page"))
		if page == "" {
			page = strings.ToLower(rest)
		}
		for _, char := range page {
			if (char < 'a' || char > 'z') && char != '-' {
				return LinkRequest{}, fmt.Errorf("不认识的设置页：%s", page)
			}
		}
		return LinkRequest{Command: "settings", Argument: page}, nil
	case "update", "upgrade":
		// 通知上「立即更新」的链接带着这次运行的随机数（见 installUpdateLink），其他的只检查更新。
		token := strings.ToLower(query.Get("install"))
		if !validNoticeToken(token) {
			token = ""
		}
		return LinkRequest{Command: "update", Argument: token}, nil
	case "diagnose", "check":
		address := strings.TrimSpace(query.Get("url"))
		if address != "" {
			if _, err := normalizeDiagnoseUrl(address); err != nil {
				return LinkRequest{}, err
			}
		}
		perspective := diagnosePc
		if strings.EqualFold(query.Get("from"), "device") {
			perspective = diagnoseDevice
		}
		return LinkRequest{Command: "diagnose", Argument: address + "\x00" + perspective}, nil
	case "install-config", "import", "subscribe":
		return importLink(query)
	}
	return LinkRequest{}, fmt.Errorf("不认识的链接：%s", text)
}

// importLink 是添加订阅的链接：url 是订阅地址，name 是机场给的名字（可以没有）。网页上的链接只能添加网上的订阅，
// 不能指向这台电脑上的文件。
func importLink(query url.Values) (LinkRequest, error) {
	address := strings.TrimSpace(query.Get("url"))
	parsed, err := url.Parse(address)
	if address == "" || err != nil || (parsed.Scheme != "http" && parsed.Scheme != "https") || parsed.Host == "" {
		return LinkRequest{}, errors.New("链接里的订阅地址不对，应以 http:// 或 https:// 开头")
	}
	name := strings.TrimSpace(query.Get("name"))
	if runes := []rune(name); len(runes) > maxProfileNameLength {
		name = string(runes[:maxProfileNameLength])
	}
	argument, _ := json.Marshal(map[string]string{"url": address, "name": name})
	return LinkRequest{Command: "import", Argument: string(argument)}, nil
}
