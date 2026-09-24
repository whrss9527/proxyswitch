package main

import "strings"

// 已经打开的终端读不到之后写入的用户环境变量。托盘菜单和设置页提供这些命令，复制后粘贴到终端运行，
// 当前终端窗口就会使用代理。

type TerminalCommand struct {
	Shell   string `json:"shell"`
	Label   string `json:"label"`
	Command string `json:"command"`
}

// terminalCommands 返回 PowerShell、命令提示符和 Bash 下设置代理环境变量的命令；proxyUrl 为空时返回 nil。
func terminalCommands(proxyUrl, noProxy string) []TerminalCommand {
	if proxyUrl == "" {
		return nil
	}
	if noProxy == "" {
		noProxy = defaultNoProxy
	}
	values := [][2]string{{"HTTP_PROXY", proxyUrl}, {"HTTPS_PROXY", proxyUrl}, {"NO_PROXY", noProxy}}

	powershell := make([]string, 0, len(values))
	cmd := make([]string, 0, len(values))
	lower := make([]string, 0, len(values))
	upper := make([]string, 0, len(values))
	for _, value := range values {
		powershell = append(powershell, "$env:"+value[0]+"="+powershellQuote(value[1]))
		// set "名称=值" 的写法不会把 & 前面的空格算进值里。
		cmd = append(cmd, `set "`+value[0]+"="+strings.ReplaceAll(value[1], `"`, "")+`"`)
		// curl 等程序在 Linux 风格的环境里只认小写的 http_proxy，两种都设置。
		lower = append(lower, strings.ToLower(value[0])+"="+bashQuote(value[1]))
		upper = append(upper, value[0]+"="+bashQuote(value[1]))
	}
	return []TerminalCommand{
		{Shell: "powershell", Label: "PowerShell", Command: strings.Join(powershell, "; ")},
		{Shell: "cmd", Label: "命令提示符（CMD）", Command: strings.Join(cmd, " & ")},
		{Shell: "bash", Label: "Git Bash", Command: "export " + strings.Join(append(lower, upper...), " ")},
	}
}

func powershellQuote(value string) string {
	return "'" + strings.ReplaceAll(value, "'", "''") + "'"
}

func bashQuote(value string) string {
	return "'" + strings.ReplaceAll(value, "'", `'\''`) + "'"
}
