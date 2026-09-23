//go:build !windows

package main

import (
	"fmt"
	"os"
)

func main() {
	if len(os.Args) > 1 && os.Args[1] == "--dev-settings" {
		// 开发用：在任意平台预览设置页（系统设置用内存模拟），例如 go run . --dev-settings --dir=/tmp/ps --web=web
		os.Exit(runDevSettings(os.Args[2:]))
	}
	fmt.Fprintln(os.Stderr, appName+" 只支持 Windows，请用 make windows 交叉编译；开发预览设置页：go run . --dev-settings")
	os.Exit(1)
}
