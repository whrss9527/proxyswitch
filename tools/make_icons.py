#!/usr/bin/env python3
"""生成程序图标 assets/app.ico 和文档用的 docs/logo.png。

图标是蓝色圆角方块上的一个“开关”：白色轨道、绿色滑块在右侧，与托盘图标的开关造型一致。
托盘图标在程序运行时按状态和配置颜色绘制，不需要图标文件。

用法：python3 tools/make_icons.py（需要 Pillow）
"""
import os

from PIL import Image, ImageDraw

ROOT = os.path.dirname(os.path.dirname(os.path.abspath(__file__)))
SIZES = [16, 20, 24, 32, 40, 48, 64, 128, 256]
SUPERSAMPLING = 8


def vertical_gradient(size, top, bottom):
    gradient = Image.new("RGBA", (1, size))
    for y in range(size):
        ratio = y / max(1, size - 1)
        gradient.putpixel((0, y), tuple(round(top[i] + (bottom[i] - top[i]) * ratio) for i in range(3)) + (255,))
    return gradient.resize((size, size))


def render(size):
    canvas_size = size * SUPERSAMPLING
    canvas = Image.new("RGBA", (canvas_size, canvas_size), (0, 0, 0, 0))

    # 圆角方块底：小尺寸时留边更少，保证清晰
    margin = canvas_size * (0.02 if size <= 24 else 0.06)
    radius = canvas_size * 0.22
    mask = Image.new("L", (canvas_size, canvas_size), 0)
    ImageDraw.Draw(mask).rounded_rectangle([margin, margin, canvas_size - margin, canvas_size - margin], radius=radius, fill=255)
    tile = vertical_gradient(canvas_size, (59, 130, 246), (29, 78, 216))
    canvas.paste(tile, (0, 0), mask)

    # 开关：白色轨道 + 绿色滑块
    draw = ImageDraw.Draw(canvas)
    track_width = canvas_size * (0.74 if size <= 24 else 0.66)
    track_height = track_width * 0.56
    left = (canvas_size - track_width) / 2
    top = (canvas_size - track_height) / 2
    draw.rounded_rectangle([left, top, left + track_width, top + track_height], radius=track_height / 2, fill=(255, 255, 255, 255))
    inset = track_height * 0.14
    knob = track_height - inset * 2
    knob_left = left + track_width - inset - knob
    draw.ellipse([knob_left, top + inset, knob_left + knob, top + inset + knob], fill=(22, 163, 74, 255))

    return canvas.resize((size, size), Image.LANCZOS)


def main():
    frames = [render(size) for size in SIZES]
    largest = frames[-1]
    largest.save(os.path.join(ROOT, "assets", "app.ico"), sizes=[(size, size) for size in SIZES], append_images=frames[:-1])
    os.makedirs(os.path.join(ROOT, "docs"), exist_ok=True)
    render(128).save(os.path.join(ROOT, "docs", "logo.png"))
    print("已生成 assets/app.ico 和 docs/logo.png")


if __name__ == "__main__":
    main()
