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

// git 全局代理：调用 git 本身修改 ~/.gitconfig，不自己解析 gitconfig。

const createNoWindow = 0x08000000

var errGitNotFound = errors.New("没有找到 git（不在 PATH 里）")

// runHidden 在后台运行命令，不弹出控制台窗口。
func runHidden(timeout time.Duration, name string, args ...string) (output string, exitCode int, err error) {
	command := exec.Command(name, args...)
	command.SysProcAttr = &syscall.SysProcAttr{HideWindow: true, CreationFlags: createNoWindow}
	done := make(chan struct{})
	var data []byte
	go func() {
		data, err = command.CombinedOutput()
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(timeout):
		if command.Process != nil {
			_ = command.Process.Kill()
		}
		<-done
		return string(data), -1, fmt.Errorf("%s 执行超时", name)
	}
	if err != nil {
		var exitError *exec.ExitError
		if errors.As(err, &exitError) {
			return string(data), exitError.ExitCode(), nil
		}
		return string(data), -1, err
	}
	return string(data), 0, nil
}

func gitPath() (string, error) {
	path, err := exec.LookPath("git")
	if err != nil {
		return "", errGitNotFound
	}
	return path, nil
}

func setGitProxy(proxyUrl string) error {
	git, err := gitPath()
	if err != nil {
		return err
	}
	for _, key := range []string{"http.proxy", "https.proxy"} {
		output, exitCode, err := runHidden(15*time.Second, git, "config", "--global", key, proxyUrl)
		if err != nil {
			return err
		}
		if exitCode != 0 {
			return fmt.Errorf("git config %s 失败：%s", key, strings.TrimSpace(output))
		}
	}
	return nil
}

// clearGitProxy 清除 git 全局代理；没装 git 时不算错误。
func clearGitProxy() error {
	git, err := gitPath()
	if err != nil {
		return nil
	}
	for _, key := range []string{"http.proxy", "https.proxy"} {
		output, exitCode, err := runHidden(15*time.Second, git, "config", "--global", "--unset-all", key)
		if err != nil {
			return err
		}
		// 退出码 5 表示这个键本来就不存在。
		if exitCode != 0 && exitCode != 5 {
			return fmt.Errorf("git config --unset %s 失败：%s", key, strings.TrimSpace(output))
		}
	}
	return nil
}

func readGitStatus() GitStatus {
	git, err := gitPath()
	if err != nil {
		return GitStatus{}
	}
	status := GitStatus{Available: true, Path: git}
	for key, target := range map[string]*string{"http.proxy": &status.HttpProxy, "https.proxy": &status.HttpsProxy} {
		output, exitCode, err := runHidden(10*time.Second, git, "config", "--global", "--get", key)
		if err == nil && exitCode == 0 {
			*target = strings.TrimSpace(output)
		}
	}
	return status
}

// openWithEditor 用配置的编辑器（默认记事本）打开文件。
func openWithEditor(editor, path string) error {
	if strings.TrimSpace(editor) == "" {
		editor = "notepad.exe"
	}
	executable, err := exec.LookPath(editor)
	if err != nil {
		return fmt.Errorf("找不到编辑器 %q", editor)
	}
	command := exec.Command(executable, path)
	// 只用 CREATE_NO_WINDOW 抑制 .cmd 启动器的黑窗口；不能设 HideWindow，否则记事本的窗口也会被隐藏。
	command.SysProcAttr = &syscall.SysProcAttr{CreationFlags: createNoWindow}
	if err := command.Start(); err != nil {
		return err
	}
	go func() { _ = command.Wait() }()
	return nil
}
