package main

import (
	"runtime"
	"strings"
	"testing"
	"time"
)

func TestSpeedMeter(t *testing.T) {
	var meter speedMeter
	start := time.Date(2026, 9, 28, 12, 0, 0, 0, time.UTC)
	meter.sample(speedSystem, start, 1000, 500, true)
	if info := meter.Info(); info.Ready || info.Mode != speedSystem {
		t.Errorf("第一次采样还算不出速度：%+v", info)
	}
	meter.sample(speedSystem, start.Add(2*time.Second), 1000+4096, 500+2048, true)
	if info := meter.Info(); !info.Ready || info.Download != 2048 || info.Upload != 1024 || info.Text() != "↑ 1.00 KB/s  ↓ 2.00 KB/s" {
		t.Errorf("两次采样之间的平均速度：%+v %q", info, info.Text())
	}
	// 计数变小（内核重启）：这次不算，下一次接着算。
	meter.sample(speedSystem, start.Add(4*time.Second), 10, 10, true)
	if info := meter.Info(); info.Download != 2048 {
		t.Errorf("计数变小时保留上次的速度：%+v", info)
	}
	meter.sample(speedSystem, start.Add(6*time.Second), 10+2*1024*1024, 10, true)
	if info := meter.Info(); info.Download != 1024*1024 || info.Upload != 0 {
		t.Errorf("之后接着算：%+v", info)
	}
	meter.sample(speedCore, start.Add(8*time.Second), 5, 5, true)
	if info := meter.Info(); info.Ready || info.Mode != speedCore || info.Text() != "" {
		t.Errorf("换了统计方式从头算：%+v", info)
	}
	meter.sample(speedCore, start.Add(10*time.Second), 5+1000, 5, true)
	if text := meter.Info().Text(); text != "↑ 0 B/s  ↓ 500 B/s（内置代理）" {
		t.Errorf("只算内核时注明：%q", text)
	}
	meter.sample(speedCore, start.Add(12*time.Second), 0, 0, false)
	if info := meter.Info(); info.Ready {
		t.Errorf("读不到时不显示：%+v", info)
	}
	meter.sample(speedNone, start.Add(14*time.Second), 0, 0, false)
	if info := meter.Info(); info.Text() != "" || info.Mode != speedNone {
		t.Errorf("不显示网速：%+v", info)
	}
}

func TestFormatSpeed(t *testing.T) {
	for value, want := range map[int64]string{
		-1: "0 B/s", 0: "0 B/s", 512: "512 B/s", 1023: "1023 B/s", 1024: "1.00 KB/s", 12_700: "12.4 KB/s",
		512 * 1024: "512 KB/s", 1000 * 1024: "0.98 MB/s", 3 * 1024 * 1024 / 2: "1.50 MB/s", 5 << 30: "5.00 GB/s",
	} {
		if got := formatSpeed(value); got != want {
			t.Errorf("%d 应写成 %s，实际 %s", value, want, got)
		}
	}
}

func TestSpeedConfig(t *testing.T) {
	if config, _ := parseConfig(`{"profiles": []}`); config.SpeedDisplay != speedSystem {
		t.Errorf("默认显示系统网络总速度：%q", config.SpeedDisplay)
	}
	if config, err := parseConfig(`{"speed_display": " Core ", "profiles": []}`); err != nil || config.SpeedDisplay != speedCore {
		t.Errorf("整理大小写和空白：%v %v", config, err)
	}
	if _, err := parseConfig(`{"speed_display": "fast"}`); err == nil || !strings.Contains(err.Error(), "speed_display") {
		t.Errorf("不认识的取值应报错：%v", err)
	}
	if !strings.Contains(defaultConfigText, `"speed_display": "system"`) {
		t.Error("默认配置文件应写出 speed_display")
	}
}

func TestReadInterfaceTotalsOnLinux(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("开发模式在 Linux 上读 /proc/net/dev")
	}
	if _, _, ok := readInterfaceTotals(); !ok {
		t.Error("应读到网卡的收发字节数")
	}
}
