//go:build !windows && !linux

package main

import (
	"os"
	"os/exec"
)

// 开发模式在其他平台上运行内核，不做额外设置。

func prepareCoreCommand(command *exec.Cmd) {}

func attachCoreProcess(process *os.Process) {}
