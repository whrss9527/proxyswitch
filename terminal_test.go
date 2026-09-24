package main

import (
	"strings"
	"testing"
)

func TestTerminalCommands(t *testing.T) {
	if commands := terminalCommands("", ""); commands != nil {
		t.Errorf("没有代理地址时应返回 nil：%v", commands)
	}
	commands := terminalCommands("http://127.0.0.1:7890", "localhost,.corp")
	wanted := map[string]string{
		"powershell": `$env:HTTP_PROXY='http://127.0.0.1:7890'; $env:HTTPS_PROXY='http://127.0.0.1:7890'; $env:NO_PROXY='localhost,.corp'`,
		"cmd":        `set "HTTP_PROXY=http://127.0.0.1:7890" & set "HTTPS_PROXY=http://127.0.0.1:7890" & set "NO_PROXY=localhost,.corp"`,
		"bash":       `export http_proxy='http://127.0.0.1:7890' https_proxy='http://127.0.0.1:7890' no_proxy='localhost,.corp' HTTP_PROXY='http://127.0.0.1:7890' HTTPS_PROXY='http://127.0.0.1:7890' NO_PROXY='localhost,.corp'`,
	}
	if len(commands) != len(wanted) {
		t.Fatalf("应有 %d 条命令，得到 %d", len(wanted), len(commands))
	}
	for _, command := range commands {
		if command.Command != wanted[command.Shell] {
			t.Errorf("%s 命令不对：\n得到 %s\n应为 %s", command.Shell, command.Command, wanted[command.Shell])
		}
		if command.Label == "" {
			t.Errorf("%s 缺少显示名称", command.Shell)
		}
	}

	quoted := terminalCommands("socks5://127.0.0.1:1080", "it's")
	if quoted[0].Command != `$env:HTTP_PROXY='socks5://127.0.0.1:1080'; $env:HTTPS_PROXY='socks5://127.0.0.1:1080'; $env:NO_PROXY='it''s'` {
		t.Errorf("PowerShell 引号转义不对：%s", quoted[0].Command)
	}
	if !strings.Contains(quoted[2].Command, `no_proxy='it'\''s'`) {
		t.Errorf("Bash 引号转义不对：%s", quoted[2].Command)
	}
	if defaults := terminalCommands("http://a:1", ""); !strings.Contains(defaults[0].Command, defaultNoProxy) {
		t.Errorf("NO_PROXY 为空时应使用默认值：%s", defaults[0].Command)
	}
}
