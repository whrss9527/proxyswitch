#!/usr/bin/env bash
# 在 Linux / macOS 上交叉编译出 Windows 版 ProxySwitch.exe（纯 Go，无需 CGO）。
set -euo pipefail
cd "$(dirname "$0")"

# 可选：重新生成 exe 的图标 / 版本信息 / 清单资源（仓库里已经带了一份 .syso，没装也能编译）
if command -v goversioninfo >/dev/null 2>&1; then
  goversioninfo -64 -o resource_windows_amd64.syso versioninfo.json
fi

GOOS=windows GOARCH=amd64 CGO_ENABLED=0 \
  go build -trimpath -ldflags="-H windowsgui -s -w" -o ProxySwitch.exe .

echo "已生成 $(pwd)/ProxySwitch.exe"
