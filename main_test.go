package main

import (
	"io"
	"log/slog"
	"os"
	"testing"
)

// testHelpers 让测试程序可以被当作辅助程序启动（例如 TUN 模式以管理员身份运行的内核宿主）：handled 为 true 时
// 不运行测试，按 code 退出。
var testHelpers []func(arguments []string) (code int, handled bool)

func TestMain(m *testing.M) {
	for _, helper := range testHelpers {
		if code, handled := helper(os.Args[1:]); handled {
			os.Exit(code)
		}
	}
	slog.SetDefault(slog.New(slog.NewTextHandler(io.Discard, nil)))
	os.Exit(m.Run())
}
