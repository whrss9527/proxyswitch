#!/bin/bash
# 编译并组装 ProxySwitch.app（通用二进制），ad-hoc 签名后打成 dist/ProxySwitch-macos.zip。
#   VERSION=1.0.0 Scripts/build-app.sh          发布构建
#   CONFIG=debug ARCHS="" Scripts/build-app.sh   本机架构的调试构建
set -euo pipefail
cd "$(dirname "$0")/.."

VERSION="${VERSION:-0.1.0}"
BUILD="${BUILD:-$(date +%Y%m%d%H%M)}"
CONFIG="${CONFIG:-release}"
ARCHS="${ARCHS---arch arm64 --arch x86_64}"

# shellcheck disable=SC2086
swift build -c "$CONFIG" $ARCHS --product ProxySwitch
# shellcheck disable=SC2086
BIN_DIR="$(swift build -c "$CONFIG" $ARCHS --show-bin-path)"

APP="dist/ProxySwitch.app"
rm -rf "$APP"
mkdir -p "$APP/Contents/MacOS" "$APP/Contents/Resources"
sed -e "s/__VERSION__/$VERSION/g" -e "s/__BUILD__/$BUILD/g" Resources/Info.plist > "$APP/Contents/Info.plist"
cp "$BIN_DIR/ProxySwitch" "$APP/Contents/MacOS/ProxySwitch"
cp Resources/AppIcon.icns "$APP/Contents/Resources/AppIcon.icns"
printf 'APPL????' > "$APP/Contents/PkgInfo"
# 没有开发者证书时用 ad-hoc 签名，Apple 芯片上必须有签名才能运行。
codesign --force --deep --sign "${CODESIGN_IDENTITY:--}" "$APP"

(cd dist && rm -f ProxySwitch-macos.zip && ditto -c -k --keepParent ProxySwitch.app ProxySwitch-macos.zip)
echo "已生成 $APP 和 dist/ProxySwitch-macos.zip（版本 $VERSION）"
