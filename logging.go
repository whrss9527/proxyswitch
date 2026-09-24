package main

import (
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"runtime/debug"
	"strings"
	"time"
)

const maxLogBytes = 1 << 20

// setupLogger 把 slog 默认输出指向日志文件；文件超过 1MB 时轮转一次。
func setupLogger(path string) {
	_ = os.MkdirAll(filepath.Dir(path), 0o755)
	if info, err := os.Stat(path); err == nil && info.Size() > maxLogBytes {
		_ = os.Remove(path + ".1")
		_ = os.Rename(path, path+".1")
	}
	var writer io.Writer = io.Discard
	if file, err := os.OpenFile(path, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o644); err == nil {
		writer = file
	}
	slog.SetDefault(slog.New(slog.NewTextHandler(writer, &slog.HandlerOptions{Level: slog.LevelInfo})))
}

// readLogTail 返回日志文件最后 maxLines 行。
func readLogTail(path string, maxLines int) string {
	data, err := os.ReadFile(path)
	if err != nil {
		return ""
	}
	lines := strings.Split(strings.TrimRight(string(data), "\n"), "\n")
	if len(lines) > maxLines {
		lines = lines[len(lines)-maxLines:]
	}
	return strings.Join(lines, "\n")
}

// setupCrashOutput 让程序崩溃（没有处理的 panic 等致命错误）时的输出写入 crash.log，托盘程序没有控制台，否则就丢了。
// 上次运行留下的崩溃记录先改名为 crash-previous.log，返回是否有这样的记录。
func setupCrashOutput(paths Paths) bool {
	crashed := false
	flags := os.O_CREATE | os.O_TRUNC | os.O_WRONLY
	if info, err := os.Stat(paths.Crash); err == nil && info.Size() > 0 {
		_ = os.Remove(paths.PreviousCrash)
		if os.Rename(paths.Crash, paths.PreviousCrash) == nil {
			crashed = true
		} else {
			// 改名失败时不能清空，保留原来的记录，新的崩溃接在后面。
			flags = os.O_CREATE | os.O_APPEND | os.O_WRONLY
		}
	}
	file, err := os.OpenFile(paths.Crash, flags, 0o644)
	if err != nil {
		return crashed
	}
	defer file.Close()
	if err := debug.SetCrashOutput(file, debug.CrashOptions{}); err != nil {
		slog.Warn("无法记录崩溃信息", "err", err)
	}
	return crashed
}

// readPreviousCrash 返回 7 天内的崩溃记录（最多 8KB）和它的时间，没有时返回空字符串。
func readPreviousCrash(path string, now time.Time) (string, time.Time) {
	info, err := os.Stat(path)
	if err != nil || now.Sub(info.ModTime()) > 7*24*time.Hour {
		return "", time.Time{}
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return "", time.Time{}
	}
	if len(data) > 8<<10 {
		data = data[:8<<10]
	}
	return strings.TrimSpace(strings.ToValidUTF8(string(data), "")), info.ModTime()
}
