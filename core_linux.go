//go:build linux

package main

import (
	"os"
	"os/exec"
	"syscall"
)

// 开发模式在 Linux 上运行内核：ProxySwitch 退出时让内核一起退出。

func prepareCoreCommand(command *exec.Cmd) {
	command.SysProcAttr = &syscall.SysProcAttr{Pdeathsig: syscall.SIGKILL}
}

func attachCoreProcess(process *os.Process) {}
