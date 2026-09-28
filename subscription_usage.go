package main

import (
	"fmt"
	"strings"
	"time"
)

// 订阅的流量和到期提醒：机场在订阅的响应头里给出已用流量、总流量和到期时间（「代理」页的订阅卡片上显示）。
// 流量用了九成、用完，或者七天内到期、已经到期时发一次通知；同一种情况只提醒一次，流量重置或续费后重新开始。

const (
	usageTrafficLow = "low"
	usageTrafficOut = "out"
	usageExpireSoon = "soon"
	usageExpired    = "expired"
	// usageWarnRatio 是提醒「流量快用完了」的比例，usageExpireDays 是提前几天提醒到期。
	usageWarnRatio  = 0.9
	usageExpireDays = 7
	noticeTagUsage  = "usage"
)

var usageRanks = map[string]int{"": 0, usageTrafficLow: 1, usageTrafficOut: 2, usageExpireSoon: 1, usageExpired: 2}

// usageLevels 是订阅现在的流量情况（low / out）和到期情况（soon / expired），都正常时为空。
func usageLevels(info *SubscriptionInfo, now time.Time) (traffic, expire string) {
	used := info.Upload + info.Download
	switch {
	case info.Total > 0 && used >= info.Total:
		traffic = usageTrafficOut
	case info.Total > 0 && float64(used) >= float64(info.Total)*usageWarnRatio:
		traffic = usageTrafficLow
	}
	if info.Expire > 0 {
		expires := time.Unix(info.Expire, 0)
		switch {
		case !now.Before(expires):
			expire = usageExpired
		case expires.Sub(now) < usageExpireDays*24*time.Hour:
			expire = usageExpireSoon
		}
	}
	return traffic, expire
}

// formatBytes 把字节数写成 512 B、12.3 MB、1.50 GB 这样的文字。
func formatBytes(bytes int64) string {
	return strings.TrimSuffix(formatSpeed(bytes), "/s")
}

// usageNotice 比较订阅现在的情况和上次提醒时的情况（previous，「流量|到期」），有更严重的情况时返回要发的通知。
// 返回的 current 是现在的情况，记下来供下次比较。
func usageNotice(profile *Profile, info *SubscriptionInfo, previous string, now time.Time) (notice Notice, current string, notify bool) {
	traffic, expire := usageLevels(info, now)
	current = traffic + "|" + expire
	oldTraffic, oldExpire, _ := strings.Cut(previous, "|")
	title, severity := "", 0
	var lines []string
	if usageRanks[traffic] > usageRanks[oldTraffic] {
		used := info.Upload + info.Download
		if traffic == usageTrafficOut {
			title, severity = "订阅流量已用完", 2
			lines = append(lines, fmt.Sprintf("已用 %s / %s", formatBytes(used), formatBytes(info.Total)))
		} else {
			title, severity = "订阅流量快用完了", 1
			lines = append(lines, fmt.Sprintf("已用 %s / %s（%d%%）", formatBytes(used), formatBytes(info.Total), used*100/info.Total))
		}
	}
	if usageRanks[expire] > usageRanks[oldExpire] {
		date := time.Unix(info.Expire, 0).Format("2006-01-02")
		if expire == usageExpired {
			if severity < 2 {
				title, severity = "订阅已到期", 2
			}
			lines = append(lines, date+" 到期")
		} else {
			if severity < 1 {
				title, severity = "订阅快到期了", 1
			}
			days := int(time.Unix(info.Expire, 0).Sub(now).Hours()/24) + 1
			lines = append(lines, fmt.Sprintf("%s 到期，还剩 %d 天", date, days))
		}
	}
	if len(lines) == 0 {
		return Notice{}, current, false
	}
	if severity == 2 {
		lines = append(lines, "节点可能连不上了，到机场网站续费或更换订阅")
	} else {
		lines = append(lines, "到机场网站续费，免得到时候断网")
	}
	return Notice{Level: noticeWarning, Title: title + "：" + profile.Name, Text: strings.Join(lines, "\n"), Color: profile.Color, Page: "proxies", Tag: noticeTagUsage}, current, true
}

// CheckSubscriptionUsage 检查每个订阅的流量和到期情况，有更严重的情况时提醒一次。下载订阅之后和后台定时检查时调用。
func (engine *Engine) CheckSubscriptionUsage() {
	if engine.config == nil {
		return
	}
	changed := false
	now := engine.now()
	for index := range engine.config.Profiles {
		profile := &engine.config.Profiles[index]
		info := engine.state.Subscriptions[profile.Id]
		if !profile.IsSubscription() || info == nil || info.Updated == "" || info.Source != subscriptionSource(profile.Subscription) {
			continue
		}
		previous := engine.state.UsageNotified[profile.Id]
		notice, current, notify := usageNotice(profile, info, previous, now)
		if current == previous {
			continue
		}
		if engine.state.UsageNotified == nil {
			engine.state.UsageNotified = map[string]string{}
		}
		if current == "|" {
			delete(engine.state.UsageNotified, profile.Id)
		} else {
			engine.state.UsageNotified[profile.Id] = current
		}
		changed = true
		if notify {
			engine.notify(notice)
		}
	}
	// 已经删除的订阅不再记着。
	for profileId := range engine.state.UsageNotified {
		if profile := engine.config.FindProfileById(profileId); profile == nil || !profile.IsSubscription() {
			delete(engine.state.UsageNotified, profileId)
			changed = true
		}
	}
	if changed {
		engine.saveState()
	}
}
