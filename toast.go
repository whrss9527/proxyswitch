package main

import (
	"crypto/rand"
	"encoding/hex"
	"net/url"
	"strings"
)

// 系统通知（Windows 的 toast）：可以带按钮，留在通知中心里之后还能点。点通知本身和按钮都打开 proxyswitch:// 链接，
// 由链接交给托盘程序执行（和在浏览器里点链接一样），所以只在登记了链接时使用，否则仍用托盘气泡。
// 这里把通知写成 toast 的 XML，显示见 toast_windows.go。

// toastXml 把通知写成 toast 的 XML。logo 是通知图标的图片路径，为空时用登记的程序图标。
// 带按钮的通知多停留一会儿，免得来不及点。
func toastXml(notice Notice, logo string) string {
	page := notice.Page
	if page == "" {
		page = "proxies"
	}
	var builder strings.Builder
	builder.WriteString(`<toast activationType="protocol" launch="` + xmlEscape(settingsLink(page)) + `"`)
	if len(notice.Actions) > 0 {
		builder.WriteString(` duration="long"`)
	}
	builder.WriteString(`><visual><binding template="ToastGeneric"><text>` + xmlEscape(notice.Title) + `</text>`)
	if notice.Text != "" {
		builder.WriteString(`<text>` + xmlEscape(notice.Text) + `</text>`)
	}
	if logo != "" {
		builder.WriteString(`<image placement="appLogoOverride" src="` + xmlEscape(logo) + `"/>`)
	}
	builder.WriteString(`</binding></visual>`)
	if len(notice.Actions) > 0 {
		builder.WriteString(`<actions>`)
		for _, action := range notice.Actions {
			builder.WriteString(`<action activationType="protocol" content="` + xmlEscape(action.Label) + `" arguments="` + xmlEscape(action.Link) + `"/>`)
		}
		builder.WriteString(`</actions>`)
	}
	builder.WriteString(`</toast>`)
	return builder.String()
}

var xmlEscaper = strings.NewReplacer("&", "&amp;", "<", "&lt;", ">", "&gt;", `"`, "&quot;", "'", "&apos;")

func xmlEscape(text string) string {
	return xmlEscaper.Replace(text)
}

// settingsLink 是打开设置窗口某一页的链接。
func settingsLink(page string) string {
	return linkScheme + "://settings/" + page
}

// useProfileLink 是开启某个配置的链接。
func useProfileLink(name string) string {
	return linkScheme + "://use?name=" + url.QueryEscape(name)
}

// turnOffLink 是关闭代理的链接。
func turnOffLink() string {
	return linkScheme + "://off"
}

// installUpdateLink 是通知上「立即更新」的链接：token 是这次运行生成的随机数，链接带着它才直接安装，
// 网页上的链接只能打开「关于」页检查更新。
func installUpdateLink(token string) string {
	return linkScheme + "://update?install=" + token
}

// newNoticeToken 生成通知链接用的随机数，生成不了时为空（通知上就不放「立即更新」）。
func newNoticeToken() string {
	buffer := make([]byte, 16)
	if _, err := rand.Read(buffer); err != nil {
		return ""
	}
	return hex.EncodeToString(buffer)
}

// validNoticeToken 检查链接里的随机数的格式：十六进制，不超过 64 个字符。
func validNoticeToken(token string) bool {
	if token == "" || len(token) > 64 {
		return false
	}
	for _, char := range token {
		if (char < '0' || char > '9') && (char < 'a' || char > 'f') {
			return false
		}
	}
	return true
}
