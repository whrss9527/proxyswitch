package main

import (
	"strings"
	"testing"
	"time"
)

func TestGuardSystemProxy(t *testing.T) {
	fixture := newEngineFixture(t, engineTestConfig)
	engine := fixture.engine
	now := time.Date(2026, 9, 28, 10, 0, 0, 0, time.UTC)
	engine.now = func() time.Time { return now }
	if err := engine.UseProfile("本机"); err != nil {
		t.Fatal(err)
	}
	external := SystemProxyState{ProxyEnabled: true, Server: "192.168.1.9:3128"}
	fixture.system.System = external
	engine.GuardSystemProxy()
	if fixture.system.System.Server != external.Server {
		t.Fatal("没开守护时不应改回")
	}

	engine.config.GuardProxy = true
	engine.GuardSystemProxy()
	if status := engine.Status(); status.State != statusOn || status.Profile.Name != "本机" {
		t.Fatalf("其他程序改了系统代理后应改回：%+v", fixture.system.System)
	}
	if notice := fixture.lastNotice(t); notice.Title != "已改回系统代理" || !strings.Contains(notice.Text, "192.168.1.9:3128") || notice.Tag != noticeTagGuard {
		t.Errorf("改回后应通知：%+v", notice)
	}
	writes := fixture.system.Writes
	engine.GuardSystemProxy()
	if fixture.system.Writes != writes {
		t.Error("系统代理没被改时不应再写")
	}

	// 被其他程序关掉也改回；十分钟里第四次被改时停下来提示，不再改回。
	for round := 2; round <= 3; round++ {
		now = now.Add(time.Minute)
		fixture.system.System.ProxyEnabled = false
		engine.GuardSystemProxy()
		if !fixture.system.System.ProxyEnabled || !strings.Contains(fixture.lastNotice(t).Text, "被关掉") {
			t.Fatalf("第 %d 次被关掉后应改回", round)
		}
	}
	now = now.Add(time.Minute)
	fixture.system.System = external
	engine.GuardSystemProxy()
	if fixture.system.System.Server != external.Server || fixture.lastNotice(t).Level != noticeWarning {
		t.Fatalf("反复被改时应停下来提示：%+v %+v", fixture.system.System, fixture.lastNotice(t))
	}
	now = now.Add(time.Hour)
	engine.GuardSystemProxy()
	if fixture.system.System.Server != external.Server {
		t.Error("停下来后，重新开启代理之前不再改回")
	}

	// 重新开启后继续守护；已经过了十分钟，之前的次数不再算。
	if err := engine.UseProfile("本机"); err != nil {
		t.Fatal(err)
	}
	fixture.system.System = external
	engine.GuardSystemProxy()
	if engine.Status().State != statusOn {
		t.Error("重新开启代理后应继续守护")
	}

	// 用 ProxySwitch 关掉代理后不管。
	if err := engine.TurnOff(); err != nil {
		t.Fatal(err)
	}
	fixture.system.System = external
	engine.GuardSystemProxy()
	if fixture.system.System.Server != external.Server {
		t.Error("代理关着时不应改系统代理")
	}
}
