//go:build windows

package main

import (
	"fmt"
	"image"
	"image/color"
	"math"
	"unsafe"
)

// 把运行时绘制的图像转成 Windows 图标（HICON）和菜单位图（HBITMAP）。

// createDibSection 创建 32 位自上而下的 DIB，返回位图句柄和像素内存（BGRA）。
func createDibSection(width, height int) (uintptr, []byte, error) {
	header := bitmapInfoHeader{width: int32(width), height: -int32(height), planes: 1, bitCount: 32}
	header.size = uint32(unsafe.Sizeof(header))
	var bits unsafe.Pointer
	bitmap, _, err := procCreateDIBSection.Call(0, uintptr(unsafe.Pointer(&header)), 0, uintptr(unsafe.Pointer(&bits)), 0, 0)
	if bitmap == 0 || bits == nil {
		return 0, nil, fmt.Errorf("CreateDIBSection：%v", err)
	}
	return bitmap, unsafe.Slice((*byte)(bits), width*height*4), nil
}

// createIcon 把非预乘 alpha 的图像转成 HICON，用完需要 DestroyIcon。
func createIcon(picture *image.NRGBA) (uintptr, error) {
	width, height := picture.Rect.Dx(), picture.Rect.Dy()
	colorBitmap, pixels, err := createDibSection(width, height)
	if err != nil {
		return 0, err
	}
	defer procDeleteObject.Call(colorBitmap)
	for y := 0; y < height; y++ {
		for x := 0; x < width; x++ {
			source := picture.NRGBAAt(x, y)
			offset := (y*width + x) * 4
			pixels[offset], pixels[offset+1], pixels[offset+2], pixels[offset+3] = source.B, source.G, source.R, source.A
		}
	}
	// 单色掩码全为 0，透明度完全由颜色位图的 alpha 决定。
	maskStride := (width + 15) / 16 * 2
	mask := make([]byte, maskStride*height)
	maskBitmap, _, err := procCreateBitmap.Call(uintptr(width), uintptr(height), 1, 1, uintptr(unsafe.Pointer(&mask[0])))
	if maskBitmap == 0 {
		return 0, fmt.Errorf("CreateBitmap：%v", err)
	}
	defer procDeleteObject.Call(maskBitmap)
	info := iconInfo{isIcon: 1, mask: maskBitmap, color: colorBitmap}
	icon, _, err := procCreateIconIndirect.Call(uintptr(unsafe.Pointer(&info)))
	if icon == 0 {
		return 0, fmt.Errorf("CreateIconIndirect：%v", err)
	}
	return icon, nil
}

// createMenuBitmap 把图像转成菜单项用的位图（预乘 alpha），用完需要 DeleteObject。
func createMenuBitmap(picture *image.NRGBA) (uintptr, error) {
	width, height := picture.Rect.Dx(), picture.Rect.Dy()
	bitmap, pixels, err := createDibSection(width, height)
	if err != nil {
		return 0, err
	}
	for y := 0; y < height; y++ {
		for x := 0; x < width; x++ {
			source := picture.NRGBAAt(x, y)
			alpha := uint32(source.A)
			offset := (y*width + x) * 4
			pixels[offset] = byte(uint32(source.B) * alpha / 255)
			pixels[offset+1] = byte(uint32(source.G) * alpha / 255)
			pixels[offset+2] = byte(uint32(source.R) * alpha / 255)
			pixels[offset+3] = source.A
		}
	}
	return bitmap, nil
}

// renderDot 画一个实心圆点，用作菜单里配置的颜色标记。
func renderDot(size int, fill color.NRGBA) *image.NRGBA {
	canvas := image.NewNRGBA(image.Rect(0, 0, size, size))
	center := float64(size) / 2
	radius := float64(size) * 0.34
	const samples = 4
	for y := 0; y < size; y++ {
		for x := 0; x < size; x++ {
			covered := 0
			for subY := 0; subY < samples; subY++ {
				for subX := 0; subX < samples; subX++ {
					pointX := float64(x) + (float64(subX)+0.5)/samples
					pointY := float64(y) + (float64(subY)+0.5)/samples
					if math.Hypot(pointX-center, pointY-center) <= radius {
						covered++
					}
				}
			}
			if covered > 0 {
				canvas.SetNRGBA(x, y, color.NRGBA{fill.R, fill.G, fill.B, uint8(int(fill.A) * covered / (samples * samples))})
			}
		}
	}
	return canvas
}
