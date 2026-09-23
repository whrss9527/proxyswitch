package main

import (
	"image"
	"image/color"
	"math"
	"strconv"
	"strings"
)

// 托盘图标是一个“开关”造型：圆角轨道 + 圆形滑块，滑块在右表示开启。
// 纯 Go 超采样绘制，任意尺寸、任意颜色都能生成，不依赖图标文件。

type iconStyle struct {
	Track     color.NRGBA
	Knob      color.NRGBA
	KnobRing  color.NRGBA
	KnobRight bool
}

var (
	iconGray  = color.NRGBA{0x8b, 0x91, 0x9a, 0xff}
	iconAmber = color.NRGBA{0xd9, 0x77, 0x06, 0xff}
	iconRed   = color.NRGBA{0xdc, 0x26, 0x26, 0xff}
	iconWhite = color.NRGBA{0xff, 0xff, 0xff, 0xff}
)

// parseHexColor 解析 #rrggbb。
func parseHexColor(text string) (color.NRGBA, bool) {
	text = strings.TrimPrefix(strings.TrimSpace(text), "#")
	if len(text) != 6 {
		return color.NRGBA{}, false
	}
	value, err := strconv.ParseUint(text, 16, 32)
	if err != nil {
		return color.NRGBA{}, false
	}
	return color.NRGBA{uint8(value >> 16), uint8(value >> 8), uint8(value), 0xff}, true
}

const iconSupersampling = 8

// renderToggleIcon 按尺寸绘制开关图标，返回非预乘 alpha 的 RGBA 图像。
func renderToggleIcon(size int, style iconStyle) *image.NRGBA {
	canvas := image.NewNRGBA(image.Rect(0, 0, size, size))
	side := float64(size)
	trackHeight := math.Round(side * 0.625)
	top := math.Floor((side - trackHeight) / 2)
	bottom := top + trackHeight
	left := math.Max(0.5, side*0.03)
	right := side - left
	radius := trackHeight / 2
	knobRadius := radius - math.Max(1.15, side*0.085)
	knobCenterY := top + radius
	knobCenterX := left + radius
	if style.KnobRight {
		knobCenterX = right - radius
	}
	ringWidth := 0.0
	if style.KnobRing.A != 0 {
		ringWidth = math.Max(1, side*0.06)
	}

	samples := iconSupersampling * iconSupersampling
	for pixelY := 0; pixelY < size; pixelY++ {
		for pixelX := 0; pixelX < size; pixelX++ {
			var red, green, blue, alpha float64
			for subY := 0; subY < iconSupersampling; subY++ {
				for subX := 0; subX < iconSupersampling; subX++ {
					pointX := float64(pixelX) + (float64(subX)+0.5)/iconSupersampling
					pointY := float64(pixelY) + (float64(subY)+0.5)/iconSupersampling
					var paint color.NRGBA
					knobDistance := math.Hypot(pointX-knobCenterX, pointY-knobCenterY)
					switch {
					case knobDistance <= knobRadius-ringWidth:
						paint = style.Knob
					case knobDistance <= knobRadius:
						paint = style.KnobRing
					case insidePill(pointX, pointY, left, top, right, bottom, radius):
						paint = style.Track
					default:
						continue
					}
					weight := float64(paint.A) / 255
					red += float64(paint.R) * weight
					green += float64(paint.G) * weight
					blue += float64(paint.B) * weight
					alpha += weight
				}
			}
			if alpha == 0 {
				continue
			}
			canvas.SetNRGBA(pixelX, pixelY, color.NRGBA{
				R: uint8(math.Round(red / alpha)),
				G: uint8(math.Round(green / alpha)),
				B: uint8(math.Round(blue / alpha)),
				A: uint8(math.Round(alpha / float64(samples) * 255)),
			})
		}
	}
	return canvas
}

func insidePill(pointX, pointY, left, top, right, bottom, radius float64) bool {
	if pointY < top || pointY > bottom || pointX < left || pointX > right {
		return false
	}
	centerY := top + radius
	switch {
	case pointX < left+radius:
		return math.Hypot(pointX-(left+radius), pointY-centerY) <= radius
	case pointX > right-radius:
		return math.Hypot(pointX-(right-radius), pointY-centerY) <= radius
	}
	return true
}

// 托盘图标的几种状态
const (
	iconStateOff      = "off"
	iconStateOn       = "on"
	iconStateExternal = "external"
	iconStateWarn     = "warn"
	iconStateError    = "error"
)

// trayIconStyle 把状态和配置颜色映射成图标样式。
func trayIconStyle(state, profileColor string) iconStyle {
	accent, ok := parseHexColor(profileColor)
	if !ok {
		accent, _ = parseHexColor(profilePalette[0])
	}
	switch state {
	case iconStateOn:
		return iconStyle{Track: accent, Knob: iconWhite, KnobRight: true}
	case iconStateExternal:
		return iconStyle{Track: iconAmber, Knob: iconWhite, KnobRight: true}
	case iconStateWarn:
		return iconStyle{Track: accent, Knob: iconRed, KnobRing: iconWhite, KnobRight: true}
	case iconStateError:
		return iconStyle{Track: iconRed, Knob: iconWhite}
	}
	return iconStyle{Track: iconGray, Knob: iconWhite}
}
