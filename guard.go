package main

import (
	"fmt"
	"log/slog"
	"time"
)

// 守护系统代理（guard_proxy）：代理开启期间，其他程序或 Windows 设置改掉了 ProxySwitch 设置的系统代理，
// 就在下一次状态检查时改回来。短时间里被改掉太多次，说明有程序在和它抢（例如另一个代理软件开着「自动设置系统代理」），
// 这时停下来提示，下次开启代理时再继续守护。

const (
	guardWindow = 10 * time.Minute
	guardLimit  = 3
)

const noticeTagGuard = "guard"

// proxyGuard 记下最近几次改回的时间；stopped 表示被反复修改，暂停守护。
type proxyGuard struct {
	restores []time.Time
	stopped  bool
}

// GuardSystemProxy 检查系统代理有没有被其他程序改掉，改掉了就改回来。在状态定时器里调用。
func (engine *Engine) GuardSystemProxy() {
	if engine.config == nil || !engine.config.GuardProxy || !engine.state.Enabled || engine.guard.stopped {
		return
	}
	profile := engine.selectedProfile()
	if profile == nil || !profile.Has(targetSystem) {
		return
	}
	status := engine.Status()
	if status.SystemError != nil || (status.State == statusOn && status.Profile != nil && status.Profile.Id == profile.Id) {
		return
	}
	changed := "系统代理被关掉了"
	switch {
	case status.State == statusExternal:
		changed = "系统代理被改成了 " + status.External
	case status.State == statusOn && status.Profile != nil:
		changed = fmt.Sprintf("系统代理被改成了配置「%s」的设置", status.Profile.Name)
	}
	now := engine.now()
	recent := engine.guard.restores[:0]
	for _, restored := range engine.guard.restores {
		if now.Sub(restored) < guardWindow {
			recent = append(recent, restored)
		}
	}
	engine.guard.restores = recent
	if len(recent) >= guardLimit {
		engine.guard.stopped = true
		slog.Warn("系统代理被反复修改，暂停守护", "change", changed)
		engine.notify(Notice{
			Level: noticeWarning,
			Title: "有程序在反复修改系统代理",
			Text:  changed + "\n已停止改回：请关掉那个程序的「自动设置系统代理」，然后重新开启一次代理",
			Tag:   noticeTagGuard,
		})
		return
	}
	if err := engine.system.WriteSystemProxy(systemStateFor(profile, status.System)); err != nil {
		slog.Warn("改回系统代理失败", "err", err)
		return
	}
	engine.guard.restores = append(engine.guard.restores, now)
	slog.Info("系统代理被其他程序修改，已改回", "change", changed, "profile", profile.Name)
	engine.notify(Notice{
		Level: noticeInfo,
		Title: "已改回系统代理",
		Text:  changed + "，已改回「" + profile.Name + "」",
		Icon:  iconStateOn,
		Color: profile.Color,
		Tag:   noticeTagGuard,
	})
}
