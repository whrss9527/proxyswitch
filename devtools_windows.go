//go:build windows

package main

import (
	"errors"
	"fmt"
	"os/exec"
	"strings"
	"syscall"
	"time"
)

// git 全局代理：调用 git 本身来改 ~/.gitconfig，避免自己解析 gitconfig。

const createNoWindow = 0x08000000

// runHidden 在后台运行命令（不弹出黑色控制台窗口）。
func runHidden(timeout time.Duration, name string, args ...string) (string, int, error) {
	cmd := exec.Command(name, args...)
	cmd.SysProcAttr = &syscall.SysProcAttr{HideWindow: true, CreationFlags: createNoWindow}
	done := make(chan struct{})
	var out []byte
	var err error
	go func() {
		out, err = cmd.CombinedOutput()
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(timeout):
		if cmd.Process != nil {
			_ = cmd.Process.Kill()
		}
		<-done
		return string(out), -1, fmt.Errorf("%s 执行超时", name)
	}
	code := 0
	if err != nil {
		var ee *exec.ExitError
		if errors.As(err, &ee) {
			code = ee.ExitCode()
		} else {
			return string(out), -1, err
		}
	}
	return string(out), code, nil
}

func gitPath() (string, error) {
	p, err := exec.LookPath("git")
	if err != nil {
		return "", fmt.Errorf("没有找到 git（不在 PATH 里）")
	}
	return p, nil
}

func setGitProxy(proxyURL string) error {
	git, err := gitPath()
	if err != nil {
		return err
	}
	for _, key := range []string{"http.proxy", "https.proxy"} {
		out, code, err := runHidden(15*time.Second, git, "config", "--global", key, proxyURL)
		if err != nil {
			return err
		}
		if code != 0 {
			return fmt.Errorf("git config %s 失败(%d): %s", key, code, strings.TrimSpace(out))
		}
	}
	return nil
}

func clearGitProxy() error {
	git, err := gitPath()
	if err != nil {
		return err
	}
	for _, key := range []string{"http.proxy", "https.proxy"} {
		out, code, err := runHidden(15*time.Second, git, "config", "--global", "--unset-all", key)
		if err != nil {
			return err
		}
		// 退出码 5 = 这个键本来就不存在，不算错误
		if code != 0 && code != 5 {
			return fmt.Errorf("git config --unset %s 失败(%d): %s", key, code, strings.TrimSpace(out))
		}
	}
	return nil
}

// readGitProxy 返回当前 git 全局 https.proxy（或 http.proxy）。
func readGitProxy() string {
	git, err := gitPath()
	if err != nil {
		return ""
	}
	for _, key := range []string{"https.proxy", "http.proxy"} {
		out, code, err := runHidden(10*time.Second, git, "config", "--global", "--get", key)
		if err == nil && code == 0 && strings.TrimSpace(out) != "" {
			return strings.TrimSpace(out)
		}
	}
	return ""
}

// openWithEditor 用配置的编辑器（默认记事本）打开文件。
func openWithEditor(editor, path string) error {
	if strings.TrimSpace(editor) == "" {
		editor = "notepad.exe"
	}
	exe, err := exec.LookPath(editor)
	if err != nil {
		return fmt.Errorf("找不到编辑器 %q：%v", editor, err)
	}
	cmd := exec.Command(exe, path)
	// 只用 CREATE_NO_WINDOW 抑制 .cmd/.bat 启动器的黑窗口；不能用 HideWindow，
	// 否则记事本这类 GUI 程序的主窗口会被隐藏。
	cmd.SysProcAttr = &syscall.SysProcAttr{CreationFlags: createNoWindow}
	if err := cmd.Start(); err != nil {
		return err
	}
	go func() { _ = cmd.Wait() }()
	return nil
}
