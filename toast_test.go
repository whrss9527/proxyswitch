package main

import (
	"encoding/xml"
	"strings"
	"testing"
)

func TestToastXml(t *testing.T) {
	notice := Notice{
		Level: noticeInfo, Title: "发现新版本 2.1.0", Text: "点这里查看更新内容\n<可以> 一键更新 & 重启", Page: "about",
		Actions: []NoticeAction{{Label: "立即更新", Link: installUpdateLink("00ff")}},
	}
	text := toastXml(notice, `C:\Users\张三\AppData\Local\ProxySwitch\on "x".png`)
	var parsed struct {
		Launch     string   `xml:"launch,attr"`
		Activation string   `xml:"activationType,attr"`
		Duration   string   `xml:"duration,attr"`
		Texts      []string `xml:"visual>binding>text"`
		Image      struct {
			Placement string `xml:"placement,attr"`
			Src       string `xml:"src,attr"`
		} `xml:"visual>binding>image"`
		Actions []struct {
			Content    string `xml:"content,attr"`
			Arguments  string `xml:"arguments,attr"`
			Activation string `xml:"activationType,attr"`
		} `xml:"actions>action"`
	}
	if err := xml.Unmarshal([]byte(text), &parsed); err != nil {
		t.Fatalf("XML 解析失败：%v\n%s", err, text)
	}
	if parsed.Launch != "proxyswitch://settings/about" || parsed.Activation != "protocol" || parsed.Duration != "long" {
		t.Errorf("点通知应打开关于页，带按钮的通知停留久一些：%+v", parsed)
	}
	if len(parsed.Texts) != 2 || parsed.Texts[0] != notice.Title || parsed.Texts[1] != notice.Text {
		t.Errorf("标题和正文：%q", parsed.Texts)
	}
	if parsed.Image.Placement != "appLogoOverride" || parsed.Image.Src != `C:\Users\张三\AppData\Local\ProxySwitch\on "x".png` {
		t.Errorf("图标：%+v", parsed.Image)
	}
	if len(parsed.Actions) != 1 || parsed.Actions[0].Content != "立即更新" || parsed.Actions[0].Arguments != "proxyswitch://update?install=00ff" || parsed.Actions[0].Activation != "protocol" {
		t.Errorf("按钮：%+v", parsed.Actions)
	}
	// 没有按钮、没有正文和图标的通知：点了打开「代理」页，停留时间由系统决定。
	plain := toastXml(Notice{Title: "代理已开启"}, "")
	if !strings.Contains(plain, `launch="proxyswitch://settings/proxies"`) || strings.Contains(plain, "duration") ||
		strings.Contains(plain, "<actions>") || strings.Contains(plain, "<image") || strings.Count(plain, "<text>") != 1 {
		t.Errorf("普通通知：%s", plain)
	}
	if request, err := parseLink(useProfileLink("公司 & 家")); err != nil || request != (LinkRequest{Command: "use", Argument: "公司 & 家"}) {
		t.Errorf("开启配置的链接：%+v %v", request, err)
	}
	if request, err := parseLink(turnOffLink()); err != nil || request.Command != "off" {
		t.Errorf("关闭代理的链接：%+v %v", request, err)
	}
	if request, err := parseLink(settingsLink("about")); err != nil || request != (LinkRequest{Command: "settings", Argument: "about"}) {
		t.Errorf("设置页的链接：%+v %v", request, err)
	}
}
