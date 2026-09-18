//go:build !windows

package main

import (
	"fmt"
	"os"
)

func main() {
	fmt.Fprintln(os.Stderr, appName+" 只支持 Windows。请用 GOOS=windows 交叉编译：\n  GOOS=windows GOARCH=amd64 go build -ldflags=\"-H windowsgui -s -w\" -o ProxySwitch.exe .")
	os.Exit(1)
}
