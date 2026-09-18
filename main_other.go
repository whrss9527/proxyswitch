//go:build !windows

package main

import (
	"fmt"
	"os"
)

func main() {
	if len(os.Args) > 1 && os.Args[1] == "--dev-settings" {
		// 开发用：在任意平台预览设置页面（不碰系统代理），例如 go run . --dev-settings
		os.Exit(runDevSettings(os.Args[2:]))
	}
	fmt.Fprintln(os.Stderr, appName+" 只支持 Windows。请用 GOOS=windows 交叉编译：\n  GOOS=windows GOARCH=amd64 go build -ldflags=\"-H windowsgui -s -w\" -o ProxySwitch.exe .")
	os.Exit(1)
}
