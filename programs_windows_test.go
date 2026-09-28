//go:build windows

package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestRunningPrograms(t *testing.T) {
	programs := runningPrograms()
	executable, _ := os.Executable()
	own := strings.ToLower(filepath.Base(executable))
	found := false
	for _, name := range programs {
		if strings.ToLower(name) == own {
			found = true
		}
		if strings.EqualFold(name, "svchost.exe") {
			t.Error("不应列出系统进程")
		}
	}
	if !found {
		t.Errorf("应列出正在运行的测试程序 %s：%v", own, programs)
	}
}
