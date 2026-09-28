#!/usr/bin/env python3
"""生成设置页用的字体：思源黑体（Noto Sans SC，SIL OFL 1.1）的常用字子集，保留全部字重（可变字体）。

没装苹方的电脑上设置页用这个字体，整体观感接近 macOS。子集包括：界面和程序提示里用到的全部汉字（设置页的脚本、
程序的 Go 代码、更新日志）、GB2312 的一级字（3755 个常用字，节点名和配置名基本都在里面）、ASCII、常用的标点和符号。
子集里没有的字由系统字体（微软雅黑）补上。

    pip install fonttools brotli
    python3 tools/subset_font.py              # 下载 Noto Sans SC 可变字体，生成 web/NotoSansSC-subset.woff2
    python3 tools/subset_font.py 字体.ttf     # 用本地的 NotoSansSC[wght].ttf
    python3 tools/subset_font.py --check      # 只检查界面里的汉字是否都在已生成的字体里（CI 里运行）

改了界面文字、用到了新的生僻字时，--check 会报出来，重新生成一次即可。
"""

import glob
import os
import sys
import tempfile
import urllib.request

from fontTools import subset
from fontTools.ttLib import TTFont

ROOT = os.path.dirname(os.path.dirname(os.path.abspath(__file__)))
OUTPUT = os.path.join(ROOT, "web", "NotoSansSC-subset.woff2")
SOURCE_URL = "https://raw.githubusercontent.com/google/fonts/main/ofl/notosanssc/NotoSansSC%5Bwght%5D.ttf"

# ASCII、拉丁字母补充、常用标点、箭头、CJK 标点和全角字符。
RANGES = [(0x20, 0x7E), (0xA0, 0xFF), (0x2000, 0x206F), (0x2190, 0x21FF), (0x3000, 0x303F), (0xFF00, 0xFFEF)]
EXTRA = "✓×·…"


def is_cjk(char):
    code = ord(char)
    return 0x4E00 <= code <= 0x9FFF or 0x3400 <= code <= 0x4DBF


def interface_characters():
    """界面里会出现的汉字：设置页、程序的提示和错误（设置页会显示）、更新日志（关于页显示发布说明）。"""
    names = glob.glob(os.path.join(ROOT, "web", "*.js")) + glob.glob(os.path.join(ROOT, "web", "*.html"))
    names += [name for name in glob.glob(os.path.join(ROOT, "*.go")) if not name.endswith("_test.go")]
    names.append(os.path.join(ROOT, "CHANGELOG.md"))
    characters = set()
    for name in names:
        with open(name, encoding="utf-8") as source:
            characters |= {char for char in source.read() if is_cjk(char)}
    return characters


def common_characters():
    """GB2312 的一级字：3755 个最常用的汉字。"""
    characters = set()
    for high in range(0xB0, 0xD8):
        for low in range(0xA1, 0xFF):
            try:
                characters.add(bytes([high, low]).decode("gb2312"))
            except UnicodeDecodeError:
                pass
    return characters


def wanted_codepoints():
    codepoints = {ord(char) for char in interface_characters() | common_characters() | set(EXTRA)}
    for low, high in RANGES:
        codepoints |= set(range(low, high + 1))
    return codepoints


def build(source):
    font = TTFont(source)
    options = subset.Options()
    options.flavor = "woff2"
    options.layout_features = ["*"]
    # 保留全部名字记录：版权和 OFL 许可证写在字体文件里，许可证要求随字体一起分发。
    options.name_IDs = ["*"]
    options.name_languages = ["*"]
    options.notdef_outline = True
    options.hinting = False
    options.desubroutinize = True
    subsetter = subset.Subsetter(options)
    subsetter.populate(unicodes=wanted_codepoints())
    subsetter.subset(font)
    font.flavor = "woff2"
    font.save(OUTPUT)
    print(f"已生成 {os.path.relpath(OUTPUT, ROOT)}：{font['maxp'].numGlyphs} 个字形，{os.path.getsize(OUTPUT) / 1024:.0f} KB")


def check():
    cmap = TTFont(OUTPUT).getBestCmap()
    missing = sorted(char for char in interface_characters() if ord(char) not in cmap)
    if missing:
        print(f"界面里有 {len(missing)} 个字不在 {os.path.relpath(OUTPUT, ROOT)} 里：{''.join(missing)}")
        print("运行 python3 tools/subset_font.py 重新生成字体")
        return 1
    print("界面里的字都在字体里")
    return 0


def main():
    if "--check" in sys.argv[1:]:
        return check()
    if len(sys.argv) > 1:
        source = sys.argv[1]
    else:
        source = os.path.join(tempfile.gettempdir(), "NotoSansSC-wght.ttf")
        if not os.path.exists(source):
            print("下载 Noto Sans SC 可变字体…")
            urllib.request.urlretrieve(SOURCE_URL, source)
    build(source)
    return check()


if __name__ == "__main__":
    sys.exit(main())
