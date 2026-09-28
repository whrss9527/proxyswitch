//go:build !windows

package main

import "errors"

// 开发模式不能以管理员身份运行内核，TUN 模式只在 Windows 上可用。
func startElevatedCoreProcess(binary, dir, configPath, logPath string) (coreProcess, error) {
	return nil, errors.New("TUN 模式只在 Windows 上可用")
}
