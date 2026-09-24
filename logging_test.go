package main

import (
	"os"
	"runtime/debug"
	"strings"
	"testing"
	"time"
)

func TestSetupCrashOutput(t *testing.T) {
	paths := pathsIn(t.TempDir(), false)
	// 程序只在启动时调用一次；测试里多次调用，每次之后都取消崩溃输出，释放文件（Windows 上打开的文件不能改名和删除）。
	release := func() { _ = debug.SetCrashOutput(nil, debug.CrashOptions{}) }
	defer release()
	if setupCrashOutput(paths) {
		t.Error("没有崩溃记录时应返回 false")
	}
	release()
	if err := os.WriteFile(paths.Crash, []byte("panic: 测试\n\ngoroutine 1 [running]:\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if !setupCrashOutput(paths) {
		t.Error("上次有崩溃记录时应返回 true")
	}
	release()
	if data, _ := os.ReadFile(paths.Crash); len(data) != 0 {
		t.Errorf("crash.log 应重新开始记录：%q", data)
	}
	crash, when := readPreviousCrash(paths.PreviousCrash, time.Now())
	if !strings.HasPrefix(crash, "panic: 测试") || when.IsZero() {
		t.Errorf("上次的崩溃记录不对：%q %v", crash, when)
	}
	if crash, _ := readPreviousCrash(paths.PreviousCrash, time.Now().Add(8*24*time.Hour)); crash != "" {
		t.Error("超过 7 天的崩溃记录不再显示")
	}
	if setupCrashOutput(paths) {
		t.Error("没有新的崩溃时应返回 false")
	}
}
