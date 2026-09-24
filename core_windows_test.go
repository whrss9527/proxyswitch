//go:build windows

package main

import (
	"bufio"
	"fmt"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"testing"
	"time"
)

const jobTestRole = "PROXYSWITCH_JOB_TEST"

// ProxySwitch 被强行结束（崩溃、在任务管理器里结束）时，作业对象让内核跟着结束，不会留在后台占着端口。
// 测试程序自己扮演两个角色：子进程当 ProxySwitch，按启动内核的方式启动孙进程并放进作业对象；
// 结束子进程后，孙进程也应随之结束。
func TestCoreProcessEndsWithProxySwitch(t *testing.T) {
	switch os.Getenv(jobTestRole) {
	case "core":
		time.Sleep(time.Minute)
		return
	case "app":
		core := exec.Command(os.Args[0], "-test.run=^TestCoreProcessEndsWithProxySwitch$")
		core.Env = append(os.Environ(), jobTestRole+"=core")
		prepareCoreCommand(core)
		if err := core.Start(); err != nil {
			fmt.Println("无法启动内核：", err)
			os.Exit(1)
		}
		attachCoreProcess(core.Process)
		fmt.Printf("pid=%d\n", core.Process.Pid)
		time.Sleep(time.Minute)
		return
	}

	app := exec.Command(os.Args[0], "-test.run=^TestCoreProcessEndsWithProxySwitch$")
	app.Env = append(os.Environ(), jobTestRole+"=app")
	output, err := app.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	if err := app.Start(); err != nil {
		t.Fatal(err)
	}
	defer app.Process.Kill()
	pid := 0
	var lines []string
	scanner := bufio.NewScanner(output)
	for pid == 0 && scanner.Scan() {
		lines = append(lines, scanner.Text())
		if value, found := strings.CutPrefix(scanner.Text(), "pid="); found {
			pid, _ = strconv.Atoi(value)
		}
	}
	if pid == 0 {
		t.Fatalf("扮演 ProxySwitch 的子进程没有启动内核：%q", lines)
	}
	core, err := os.FindProcess(pid)
	if err != nil {
		t.Fatalf("找不到内核进程：%v", err)
	}
	exited := make(chan struct{})
	go func() {
		_, _ = core.Wait()
		close(exited)
	}()
	select {
	case <-exited:
		t.Fatal("内核不应提前退出")
	case <-time.After(500 * time.Millisecond):
	}

	if err := app.Process.Kill(); err != nil {
		t.Fatal(err)
	}
	_ = app.Wait()
	select {
	case <-exited:
	case <-time.After(10 * time.Second):
		_ = core.Kill()
		t.Fatal("ProxySwitch 被结束后内核应跟着结束")
	}
}
