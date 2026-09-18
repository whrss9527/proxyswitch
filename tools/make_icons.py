#!/usr/bin/env python3
"""生成托盘图标与程序图标（.ico）。

on.ico  : 绿色圆底 + 白色电源符号（代理已开启）
off.ico : 灰色圆底 + 白色电源符号（代理已关闭）
app.ico : 蓝色圆底 + 白色电源符号（exe 图标）
"""
import math
import os
import sys

from PIL import Image, ImageDraw

HERE = os.path.dirname(os.path.abspath(__file__))
OUT = os.path.join(os.path.dirname(HERE), "assets")

SIZES_TRAY = [16, 20, 24, 32, 40, 48, 64]
SIZES_APP = [16, 24, 32, 48, 64, 128, 256]


def render(size, bg, fg=(255, 255, 255, 255), ring=None):
    """超采样绘制，再缩小到目标尺寸得到平滑边缘。"""
    ss = 8
    S = size * ss
    img = Image.new("RGBA", (S, S), (0, 0, 0, 0))
    d = ImageDraw.Draw(img)

    # 圆形底
    pad = S * 0.03
    d.ellipse([pad, pad, S - pad, S - pad], fill=bg)
    if ring:
        d.ellipse([pad, pad, S - pad, S - pad], outline=ring, width=max(1, S // 24))

    # 电源符号：一个缺口朝上的圆弧 + 一条竖线
    cx = cy = S / 2
    r = S * 0.27
    w = max(2, int(S * 0.085))  # 线宽
    gap = 35  # 缺口半角（度）
    bbox = [cx - r, cy - r, cx + r, cy + r]
    d.arc(bbox, start=270 + gap, end=270 - gap + 360, fill=fg, width=w)
    # 圆弧两端加圆头
    for ang in (270 + gap, 270 - gap):
        a = math.radians(ang)
        px = cx + r * math.cos(a)
        py = cy + r * math.sin(a)
        d.ellipse([px - w / 2, py - w / 2, px + w / 2, py + w / 2], fill=fg)
    # 竖线（圆头）
    top = cy - r - w * 0.15
    bottom = cy - r * 0.15
    d.rounded_rectangle([cx - w / 2, top, cx + w / 2, bottom], radius=w / 2, fill=fg)

    return img.resize((size, size), Image.LANCZOS)


def save_ico(path, sizes, bg, ring=None):
    frames = [render(s, bg, ring=ring) for s in sizes]
    # Pillow 的 ICO 保存：把最大的一帧作为主图，其它尺寸通过 append_images 提供
    frames_sorted = sorted(frames, key=lambda im: im.size[0], reverse=True)
    frames_sorted[0].save(
        path,
        format="ICO",
        sizes=[(s, s) for s in sizes],
        append_images=frames_sorted[1:],
        bitmap_format="bmp",
    )
    print("wrote", path, os.path.getsize(path), "bytes")


def main():
    os.makedirs(OUT, exist_ok=True)
    green = (34, 160, 78, 255)
    gray = (128, 132, 140, 255)
    blue = (37, 99, 235, 255)
    save_ico(os.path.join(OUT, "on.ico"), SIZES_TRAY, green)
    save_ico(os.path.join(OUT, "off.ico"), SIZES_TRAY, gray)
    save_ico(os.path.join(OUT, "app.ico"), SIZES_APP, blue)
    # 顺便导出 PNG 预览
    render(64, green).save(os.path.join(OUT, "preview_on.png"))
    render(64, gray).save(os.path.join(OUT, "preview_off.png"))


if __name__ == "__main__":
    sys.exit(main())
