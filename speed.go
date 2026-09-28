package main

import (
	"fmt"
	"time"
)

// 实时网速：每隔几秒读一次累计收发的字节数——系统里所有物理网卡的，或者只算内置代理内核的——算出这段时间的平均速度，
// 显示在托盘图标的提示和设置页里。

const (
	speedSystem = "system"
	speedCore   = "core"
	speedNone   = "none"
)

// SpeedInfo 是实时网速（字节/秒），Mode 见 Config.SpeedDisplay；Ready 表示已经有两次采样、速度可以显示。
type SpeedInfo struct {
	Mode     string `json:"mode"`
	Ready    bool   `json:"ready"`
	Upload   int64  `json:"upload"`
	Download int64  `json:"download"`
}

// speedMeter 按两次采样之间累计字节数的变化算速度。
type speedMeter struct {
	info         SpeedInfo
	lastTime     time.Time
	lastSent     uint64
	lastReceived uint64
	sampled      bool
}

// sample 记下 mode 下的一次累计字节数（ok 为 false 表示这次没读到），更新速度。换了统计方式、计数变小
// （网卡重置、内核重启）时从这次重新算起。
func (meter *speedMeter) sample(mode string, now time.Time, received, sent uint64, ok bool) {
	if mode != meter.info.Mode {
		*meter = speedMeter{info: SpeedInfo{Mode: mode}}
	}
	if mode == speedNone || !ok {
		meter.sampled = false
		meter.info.Ready, meter.info.Upload, meter.info.Download = mode == speedNone, 0, 0
		return
	}
	seconds := now.Sub(meter.lastTime).Seconds()
	if meter.sampled && seconds > 0.2 && received >= meter.lastReceived && sent >= meter.lastSent {
		meter.info.Ready = true
		meter.info.Download = int64(float64(received-meter.lastReceived) / seconds)
		meter.info.Upload = int64(float64(sent-meter.lastSent) / seconds)
	}
	meter.lastTime, meter.lastReceived, meter.lastSent, meter.sampled = now, received, sent, true
}

// Info 是现在的网速。
func (meter *speedMeter) Info() SpeedInfo {
	return meter.info
}

// Text 是托盘提示里的一行，例如「↑ 12.3 KB/s  ↓ 1.2 MB/s」；不显示或者还没算出来时为空。
func (info SpeedInfo) Text() string {
	if info.Mode == speedNone || !info.Ready {
		return ""
	}
	text := "↑ " + formatSpeed(info.Upload) + "  ↓ " + formatSpeed(info.Download)
	if info.Mode == speedCore {
		text += "（内置代理）"
	}
	return text
}

// formatSpeed 把字节/秒写成 0 B/s、512 B/s、12.3 KB/s、1.2 MB/s 这样的文字。
func formatSpeed(bytesPerSecond int64) string {
	value := float64(max(bytesPerSecond, 0))
	for _, unit := range []string{"B/s", "KB/s", "MB/s", "GB/s"} {
		switch {
		case value < 1024 && unit == "B/s":
			return fmt.Sprintf("%.0f %s", value, unit)
		case value < 10:
			return fmt.Sprintf("%.2f %s", value, unit)
		case value < 100:
			return fmt.Sprintf("%.1f %s", value, unit)
		case value < 1000 || unit == "GB/s":
			return fmt.Sprintf("%.0f %s", value, unit)
		}
		value /= 1024
	}
	return ""
}
