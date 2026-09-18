@echo off
rem 在 Windows 上编译 ProxySwitch.exe（需要安装 Go 1.21+，纯 Go 无需 CGO / gcc）
setlocal
cd /d "%~dp0"

rem 可选：重新生成图标 / 版本信息 / 清单资源（仓库里已带 .syso，没装 goversioninfo 也能编译）
where goversioninfo >nul 2>nul && goversioninfo -64 -o resource_windows_amd64.syso versioninfo.json

set GOOS=windows
set GOARCH=amd64
set CGO_ENABLED=0
go build -trimpath -ldflags="-H windowsgui -s -w" -o ProxySwitch.exe .
if errorlevel 1 (
  echo 编译失败
  exit /b 1
)
echo 已生成 %cd%\ProxySwitch.exe
