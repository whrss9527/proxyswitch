package main

import (
	"sort"
	"strings"
)

// 微软商店应用（UWP）的回环豁免：商店应用运行在 AppContainer 里，默认不能连本机的 127.0.0.1。系统代理指向本机的
// 代理（例如内置的内核）时，商店、邮件、Xbox 这些应用就上不了网；加上回环豁免（和 CheckNetIsolation LoopbackExempt
// 相同）后就能连了。Windows 上的实现见 loopback_windows.go。

// LoopbackApp 是一个商店应用：Sid 是它的 AppContainer SID，Package 是包名，Exempt 表示已经允许它连接本机。
type LoopbackApp struct {
	Sid     string `json:"sid"`
	Name    string `json:"name"`
	Package string `json:"package"`
	Exempt  bool   `json:"exempt"`
}

// LoopbackInfo 是这台电脑上的商店应用，按名字排序。
type LoopbackInfo struct {
	Apps []LoopbackApp `json:"apps"`
}

// appContainerSidPrefix 是 AppContainer SID 的开头，回环豁免只接受这种 SID。
const appContainerSidPrefix = "S-1-15-2-"

// mergeLoopback 算出修改后有回环豁免的 SID：已安装的应用按 exempt 决定，没有安装（列表里没有）的保留原样，
// 免得删掉其他工具为已卸载或其他用户的应用加的豁免。结果排好序，没有重复。
func mergeLoopback(current []string, installed []LoopbackApp, exempt []string) []string {
	isInstalled := map[string]bool{}
	for _, app := range installed {
		isInstalled[strings.ToUpper(app.Sid)] = true
	}
	result := map[string]string{}
	for _, sid := range current {
		if !isInstalled[strings.ToUpper(sid)] {
			result[strings.ToUpper(sid)] = sid
		}
	}
	for _, sid := range exempt {
		if isInstalled[strings.ToUpper(sid)] {
			result[strings.ToUpper(sid)] = sid
		}
	}
	merged := make([]string, 0, len(result))
	for _, sid := range result {
		merged = append(merged, sid)
	}
	sort.Strings(merged)
	return merged
}

// sameSids 表示两组 SID 相同（不分大小写和顺序）。
func sameSids(first, second []string) bool {
	if len(first) != len(second) {
		return false
	}
	counts := map[string]int{}
	for _, sid := range first {
		counts[strings.ToUpper(sid)]++
	}
	for _, sid := range second {
		counts[strings.ToUpper(sid)]--
	}
	for _, count := range counts {
		if count != 0 {
			return false
		}
	}
	return true
}

// sortLoopbackApps 按名字排序，名字相同时按包名。
func sortLoopbackApps(apps []LoopbackApp) {
	sort.SliceStable(apps, func(i, j int) bool {
		first, second := strings.ToLower(apps[i].Name), strings.ToLower(apps[j].Name)
		if first != second {
			return first < second
		}
		return apps[i].Package < apps[j].Package
	})
}
