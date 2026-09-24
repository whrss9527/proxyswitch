package main

import (
	"archive/zip"
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"time"
)

// 下载内核：Windows 上从 mihomo 的官方发布下载固定版本（coreVersion）的 zip，按发布构建时写入程序的 SHA-256 校验，
// 解压出程序放到内核的工作目录。其他平台（开发模式）不下载，用 --core 或配置里的 core.path 指定内核。

// 官方 zip 的 SHA-256，发布构建时由 -ldflags 写入；为空表示这个版本不能在程序里下载内核。
var (
	coreSha256Amd64 = ""
	coreSha256Arm64 = ""
)

// coreReleaseBase 是官方发布的下载地址前缀，测试时指向本地的模拟服务。
var coreReleaseBase = "https://github.com/MetaCubeX/mihomo/releases/download/"

const (
	coreVersionFile = "mihomo.version"
	maxCoreProgram  = 256 << 20
)

// coreAsset 返回某个平台和架构的官方发布文件名和内置的 SHA-256。x64 用兼容所有处理器的 v1 版本。
func coreAsset(goos, arch string) (name, sha string, ok bool) {
	if goos != "windows" {
		return "", "", false
	}
	switch arch {
	case "amd64":
		return "mihomo-windows-amd64-v1-" + coreVersion + ".zip", coreSha256Amd64, coreSha256Amd64 != ""
	case "arm64":
		return "mihomo-windows-arm64-" + coreVersion + ".zip", coreSha256Arm64, coreSha256Arm64 != ""
	}
	return "", "", false
}

// coreDownloadable 表示本机可以在程序里下载内核。
func coreDownloadable() bool {
	_, _, ok := coreAsset(runtime.GOOS, runtime.GOARCH)
	return ok
}

// installCore 把本机架构的内核下载到 dir。paths 是依次尝试的网络路径（见 fetchSubscription）。
func installCore(dir string, paths []string, progress func(received, total int64)) error {
	name, expected, ok := coreAsset(runtime.GOOS, runtime.GOARCH)
	if !ok {
		return errors.New("这个版本不能在程序里下载内核，请在设置里指定本机的 mihomo 程序")
	}
	return installCoreAsset(dir, coreReleaseBase+coreVersion+"/"+name, expected, paths, progress)
}

func installCoreAsset(dir, address, expected string, paths []string, progress func(received, total int64)) error {
	var data []byte
	var lastErr error
	for _, proxyUrl := range paths {
		var buffer bytes.Buffer
		if lastErr = copyDownload(updateClient(proxyUrl, updateDownloadLimit), address, &buffer, 0, progress); lastErr == nil {
			data = buffer.Bytes()
			break
		}
	}
	if data == nil {
		if lastErr == nil {
			lastErr = errors.New("没有可用的网络路径")
		}
		return lastErr
	}
	sum := sha256.Sum256(data)
	if hex.EncodeToString(sum[:]) != strings.ToLower(expected) {
		return errors.New("下载的内核与内置的校验值不一致，可能没有下载完整，请重试")
	}
	program, err := extractCoreProgram(data)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return fmt.Errorf("无法创建内核的工作目录：%v", err)
	}
	if err := placeCoreProgram(filepath.Join(dir, coreBinaryName()), program); err != nil {
		return err
	}
	return os.WriteFile(filepath.Join(dir, coreVersionFile), []byte(coreVersion+"\n"), 0o644)
}

// extractCoreProgram 从官方 zip 里取出程序（zip 里只有一个 exe）。
func extractCoreProgram(data []byte) ([]byte, error) {
	archive, err := zip.NewReader(bytes.NewReader(data), int64(len(data)))
	if err != nil {
		return nil, errors.New("下载的内核压缩包无法打开")
	}
	for _, file := range archive.File {
		if !strings.HasSuffix(strings.ToLower(file.Name), ".exe") || file.UncompressedSize64 > maxCoreProgram {
			continue
		}
		reader, err := file.Open()
		if err != nil {
			return nil, err
		}
		program, err := io.ReadAll(io.LimitReader(reader, maxCoreProgram))
		reader.Close()
		if err != nil {
			return nil, fmt.Errorf("解压内核失败：%v", err)
		}
		return program, nil
	}
	return nil, errors.New("下载的内核压缩包里没有程序")
}

// placeCoreProgram 把新程序放到 target。旧程序可能正在运行：Windows 允许给运行中的程序改名，先改名再放入新程序，
// 旧程序等内核重启后再删除。
func placeCoreProgram(target string, program []byte) error {
	temporary := target + ".download"
	if err := os.WriteFile(temporary, program, 0o755); err != nil {
		return fmt.Errorf("无法写入内核：%v", err)
	}
	previous := ""
	if fileExists(target) {
		previous = target + ".old"
		if err := os.Remove(previous); err != nil && !os.IsNotExist(err) {
			previous = fmt.Sprintf("%s.%d.old", target, time.Now().UnixNano())
		}
		if err := os.Rename(target, previous); err != nil {
			_ = os.Remove(temporary)
			return fmt.Errorf("无法替换内核：%v", err)
		}
	}
	if err := os.Rename(temporary, target); err != nil {
		if previous != "" {
			_ = os.Rename(previous, target)
		}
		return fmt.Errorf("无法替换内核：%v", err)
	}
	if previous != "" {
		_ = os.Remove(previous)
	}
	return nil
}

// removeOldCorePrograms 删除替换内核时留下的旧程序，在内核启动前调用（这时旧程序已经退出）。
func removeOldCorePrograms(binary string) {
	leftovers, _ := filepath.Glob(binary + ".*.old")
	for _, path := range append(leftovers, binary+".old", binary+".download") {
		_ = os.Remove(path)
	}
}

// readCoreVersion 返回下载的内核的版本，没有记录时返回空字符串。
func readCoreVersion(dir string) string {
	data, err := os.ReadFile(filepath.Join(dir, coreVersionFile))
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(data))
}
