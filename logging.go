package main

import (
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
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
