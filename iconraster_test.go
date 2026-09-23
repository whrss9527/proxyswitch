package main

import (
	"image"
	"image/color"
	"image/draw"
	"image/png"
	"os"
	"testing"
)

func TestParseHexColor(t *testing.T) {
	parsed, ok := parseHexColor("#16a34a")
	if !ok || parsed != (color.NRGBA{0x16, 0xa3, 0x4a, 0xff}) {
		t.Fatalf("解析 #16a34a 得到 %v %v", parsed, ok)
	}
	for _, invalid := range []string{"", "#fff", "#gggggg", "16a34a0"} {
		if _, ok := parseHexColor(invalid); ok {
			t.Errorf("%q 不应解析成功", invalid)
		}
	}
}

func TestRenderToggleIcon(t *testing.T) {
	for _, size := range []int{16, 20, 24, 32, 48, 64} {
		on := renderToggleIcon(size, trayIconStyle(iconStateOn, "#2563eb"))
		off := renderToggleIcon(size, trayIconStyle(iconStateOff, ""))
		// 角落透明
		if on.NRGBAAt(0, 0).A != 0 || off.NRGBAAt(size-1, size-1).A != 0 {
			t.Errorf("%dpx 角落应透明", size)
		}
		middle := size / 2
		// 开启：滑块在右侧（白色），左侧是轨道色
		rightKnob := on.NRGBAAt(size-size*5/16, middle)
		if rightKnob.R < 240 || rightKnob.G < 240 || rightKnob.B < 240 {
			t.Errorf("%dpx 开启状态右侧应是白色滑块，得到 %v", size, rightKnob)
		}
		leftTrack := on.NRGBAAt(size/6, middle)
		if leftTrack.B < 200 || leftTrack.A < 200 {
			t.Errorf("%dpx 开启状态左侧应是蓝色轨道，得到 %v", size, leftTrack)
		}
		// 关闭：滑块在左侧
		leftKnob := off.NRGBAAt(size*5/16-1, middle)
		if leftKnob.R < 240 {
			t.Errorf("%dpx 关闭状态左侧应是白色滑块，得到 %v", size, leftKnob)
		}
		rightTrack := off.NRGBAAt(size-size/6, middle)
		if rightTrack != iconGray && rightTrack.A < 200 {
			t.Errorf("%dpx 关闭状态右侧应是灰色轨道，得到 %v", size, rightTrack)
		}
	}
	warn := renderToggleIcon(32, trayIconStyle(iconStateWarn, "#16a34a"))
	knobCenter := warn.NRGBAAt(32-32*5/16, 16)
	if knobCenter.R < 200 || knobCenter.G > 80 {
		t.Errorf("警告状态滑块中心应为红色，得到 %v", knobCenter)
	}
}

// 设置 ICON_PREVIEW=路径 时输出各状态、各尺寸在浅色和深色任务栏上的预览图，方便肉眼检查。
func TestIconPreview(t *testing.T) {
	path := os.Getenv("ICON_PREVIEW")
	if path == "" {
		t.Skip("设置 ICON_PREVIEW 才生成预览图")
	}
	sizes := []int{16, 20, 24, 32, 48}
	states := []struct{ state, color string }{
		{iconStateOff, ""}, {iconStateOn, "#16a34a"}, {iconStateOn, "#2563eb"}, {iconStateOn, "#7c3aed"},
		{iconStateOn, "#db2777"}, {iconStateOn, "#ea580c"}, {iconStateOn, "#0891b2"},
		{iconStateExternal, ""}, {iconStateWarn, "#16a34a"}, {iconStateError, ""},
	}
	cell := 56
	sheet := image.NewNRGBA(image.Rect(0, 0, cell*len(sizes)*2, cell*len(states)))
	backgrounds := []color.NRGBA{{0xf3, 0xf3, 0xf3, 0xff}, {0x20, 0x20, 0x20, 0xff}}
	for row, item := range states {
		for column, size := range sizes {
			for variant, background := range backgrounds {
				originX := (variant*len(sizes) + column) * cell
				originY := row * cell
				draw.Draw(sheet, image.Rect(originX, originY, originX+cell, originY+cell), &image.Uniform{background}, image.Point{}, draw.Src)
				icon := renderToggleIcon(size, trayIconStyle(item.state, item.color))
				offset := (cell - size) / 2
				draw.Draw(sheet, image.Rect(originX+offset, originY+offset, originX+offset+size, originY+offset+size), icon, image.Point{}, draw.Over)
			}
		}
	}
	file, err := os.Create(path)
	if err != nil {
		t.Fatal(err)
	}
	defer file.Close()
	if err := png.Encode(file, sheet); err != nil {
		t.Fatal(err)
	}
}
