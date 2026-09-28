package main

import (
	"strings"
	"testing"
	"time"
)

const usageGB = int64(1) << 30

func TestUsageLevels(t *testing.T) {
	now := time.Date(2026, 9, 28, 12, 0, 0, 0, time.Local)
	days := func(count float64) int64 { return now.Add(time.Duration(count * 24 * float64(time.Hour))).Unix() }
	cases := []struct {
		info            SubscriptionInfo
		traffic, expire string
	}{
		{SubscriptionInfo{Upload: 1 * usageGB, Download: 50 * usageGB, Total: 100 * usageGB, Expire: days(30)}, "", ""},
		{SubscriptionInfo{Upload: 10 * usageGB, Download: 80 * usageGB, Total: 100 * usageGB}, usageTrafficLow, ""},
		{SubscriptionInfo{Download: 100 * usageGB, Total: 100 * usageGB}, usageTrafficOut, ""},
		{SubscriptionInfo{Expire: days(6.5)}, "", usageExpireSoon},
		{SubscriptionInfo{Expire: days(-1)}, "", usageExpired},
		// 机场没给总流量和到期时间：不提醒。
		{SubscriptionInfo{Download: 500 * usageGB}, "", ""},
	}
	for _, item := range cases {
		traffic, expire := usageLevels(&item.info, now)
		if traffic != item.traffic || expire != item.expire {
			t.Errorf("%+v：得到 %q %q，应为 %q %q", item.info, traffic, expire, item.traffic, item.expire)
		}
	}
}

func TestUsageNoticeOnlyWhenWorse(t *testing.T) {
	now := time.Date(2026, 9, 28, 12, 0, 0, 0, time.Local)
	profile := &Profile{Name: "机场", Color: "#16a34a"}
	info := &SubscriptionInfo{Upload: 2 * usageGB, Download: 90 * usageGB, Total: 100 * usageGB}

	notice, current, notify := usageNotice(profile, info, "", now)
	if !notify || notice.Title != "订阅流量快用完了：机场" || !strings.Contains(notice.Text, "已用 92.0 GB / 100 GB（92%）") || notice.Page != "proxies" || notice.Level != noticeWarning {
		t.Fatalf("流量用了九成要提醒：%+v", notice)
	}
	if _, again, notify := usageNotice(profile, info, current, now); notify || again != current {
		t.Fatal("同样的情况只提醒一次")
	}
	info.Download = 99 * usageGB
	info.Upload = 1 * usageGB
	notice, current, notify = usageNotice(profile, info, current, now)
	if !notify || notice.Title != "订阅流量已用完：机场" || !strings.Contains(notice.Text, "连不上") {
		t.Fatalf("流量用完要再提醒：%+v", notice)
	}
	// 流量重置：不提醒，记下现在的情况，下次用到九成再提醒。
	info.Upload, info.Download = 0, 1*usageGB
	if _, reset, notify := usageNotice(profile, info, current, now); notify || reset != "|" {
		t.Fatalf("流量重置后不提醒：%q", reset)
	} else {
		current = reset
	}
	info.Download = 95 * usageGB
	if _, _, notify := usageNotice(profile, info, current, now); !notify {
		t.Fatal("重置后再用到九成要提醒")
	}

	// 快到期和已到期。
	expiring := &SubscriptionInfo{Expire: now.Add(50 * time.Hour).Unix()}
	notice, current, notify = usageNotice(profile, expiring, "", now)
	if !notify || notice.Title != "订阅快到期了：机场" || !strings.Contains(notice.Text, "还剩 3 天") {
		t.Fatalf("快到期要提醒：%+v", notice)
	}
	notice, _, notify = usageNotice(profile, expiring, current, now.Add(51*time.Hour))
	if !notify || notice.Title != "订阅已到期：机场" {
		t.Fatalf("到期要再提醒：%+v", notice)
	}

	// 流量快用完、同时已经到期：标题用更严重的，两件事都写上。
	both := &SubscriptionInfo{Download: 95 * usageGB, Total: 100 * usageGB, Expire: now.Add(-time.Hour).Unix()}
	notice, _, _ = usageNotice(profile, both, "", now)
	if notice.Title != "订阅已到期：机场" || !strings.Contains(notice.Text, "已用 95.0 GB") || !strings.Contains(notice.Text, "到期") {
		t.Fatalf("两件事都要写上：%+v", notice)
	}
}

func TestEngineCheckSubscriptionUsage(t *testing.T) {
	fixture := newEngineFixture(t, `{
  "profiles": [
    {"id": "pa1", "name": "机场", "subscription": "https://example.com/sub"},
    {"name": "本机", "server": "127.0.0.1:7890"}
  ]
}`)
	engine := fixture.engine
	engine.now = func() time.Time { return time.Date(2026, 9, 28, 12, 0, 0, 0, time.Local) }
	engine.state.Subscriptions = map[string]*SubscriptionInfo{
		"pa1": {Source: subscriptionSource("https://example.com/sub"), Updated: "2026-09-28T11:00:00Z", Download: 95 * usageGB, Total: 100 * usageGB},
	}
	engine.CheckSubscriptionUsage()
	if len(fixture.notices) != 1 || fixture.notices[0].Title != "订阅流量快用完了：机场" {
		t.Fatalf("应该提醒一次：%+v", fixture.notices)
	}
	if engine.state.UsageNotified["pa1"] != "low|" || loadState(engine.paths.State).UsageNotified["pa1"] != "low|" {
		t.Fatalf("提醒过的情况要记进状态文件：%+v", engine.state.UsageNotified)
	}
	engine.CheckSubscriptionUsage()
	if len(fixture.notices) != 1 {
		t.Fatal("不应该重复提醒")
	}

	// 删掉订阅后不再记着。
	config := engine.Config().Clone()
	config.Profiles = config.Profiles[1:]
	if err := engine.SaveConfig(config); err != nil {
		t.Fatal(err)
	}
	engine.CheckSubscriptionUsage()
	if _, found := engine.state.UsageNotified["pa1"]; found {
		t.Fatal("删掉的订阅要清掉提醒记录")
	}
}
