//go:build windows

package main

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"
)

// 测试程序也可以当作以管理员身份运行的内核宿主（startElevatedCoreProcess 启动的是 os.Executable()）。
func init() {
	testHelpers = append(testHelpers, func(arguments []string) (int, bool) {
		if len(arguments) > 0 && arguments[0] == coreHostArgument {
			return runCoreHost(arguments[1:]), true
		}
		return 0, false
	})
}

// ProxySwitch 下载的内核要和发布时写入的 SHA-256 一致才以管理员身份运行；配置里指定的内核不校验。
func TestStartVerifiedCore(t *testing.T) {
	binary := requireCoreBinary(t)
	dir := t.TempDir()
	managed := filepath.Join(dir, coreBinaryName())
	data, err := os.ReadFile(binary)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(managed, data, 0o755); err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256(data)
	original := coreExeSha256Amd64
	originalArm := coreExeSha256Arm64
	t.Cleanup(func() { coreExeSha256Amd64, coreExeSha256Arm64 = original, originalArm })
	start := func(program string) error {
		command := exec.Command(program, "-v")
		command.Stdout, command.Stderr = io.Discard, io.Discard
		if err := startVerifiedCore(command, dir); err != nil {
			return err
		}
		return command.Wait()
	}

	coreExeSha256Amd64, coreExeSha256Arm64 = strings.Repeat("0", 64), strings.Repeat("0", 64)
	if err := start(managed); !errors.Is(err, errCoreMismatch) {
		t.Errorf("和发布时不一致的内核不应运行：%v", err)
	}
	if err := start(binary); err != nil {
		t.Errorf("配置里指定的内核不校验：%v", err)
	}
	coreExeSha256Amd64, coreExeSha256Arm64 = hex.EncodeToString(sum[:]), hex.EncodeToString(sum[:])
	if err := start(managed); err != nil {
		t.Errorf("一致的内核应能运行：%v", err)
	}
}

// 以管理员身份运行的宿主进程启动内核，收到停止信号时结束内核。CI 已经以管理员身份运行，不会弹出确认框。
// 这里的配置不开 TUN，不改这台电脑的路由。
func TestCoreHostProcess(t *testing.T) {
	requireRegistryTests(t)
	binary := requireCoreBinary(t)
	dir := t.TempDir()
	if err := writeSubscriptionFile(dir, "pa", []byte("proxies:\n  - {name: a, type: http, server: 127.0.0.1, port: 1}\n")); err != nil {
		t.Fatal(err)
	}
	apiPort, _ := freeLocalPort()
	port, _ := freeLocalPort()
	controller := "127.0.0.1:" + strconv.Itoa(apiPort)
	settings := CoreSettings{Binary: binary, Dir: dir, Port: port, Active: "pa", Mode: "rule", Subscriptions: []CoreSubscription{{Id: "pa"}}}
	configPath := filepath.Join(dir, coreConfigName)
	if err := os.WriteFile(configPath, coreConfigText(settings, controller, "secret"), 0o600); err != nil {
		t.Fatal(err)
	}
	logPath := filepath.Join(dir, coreLogName)
	process, err := startElevatedCoreProcess(binary, dir, configPath, logPath)
	if err != nil {
		t.Fatal(err)
	}
	exited := make(chan error, 1)
	go func() { exited <- process.Wait() }()
	if !process.Elevated() || process.Pid() == 0 {
		t.Errorf("应是以管理员身份运行的宿主：%d", process.Pid())
	}
	client := &http.Client{Transport: &http.Transport{Proxy: nil}, Timeout: time.Second}
	ready := false
	for deadline := time.Now().Add(15 * time.Second); time.Now().Before(deadline); time.Sleep(100 * time.Millisecond) {
		request, _ := http.NewRequest(http.MethodGet, "http://"+controller+"/version", nil)
		request.Header.Set("Authorization", "Bearer secret")
		if response, err := client.Do(request); err == nil {
			response.Body.Close()
			ready = response.StatusCode == http.StatusOK
			if ready {
				break
			}
		}
	}
	if !ready {
		log, _ := os.ReadFile(logPath)
		t.Fatalf("宿主启动的内核没有就绪：\n%s", log)
	}
	if err := process.Kill(); err != nil {
		t.Fatal(err)
	}
	select {
	case err := <-exited:
		if err != nil {
			t.Errorf("收到停止信号后宿主应正常退出：%v", err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("宿主没有退出")
	}
	if response, err := client.Get("http://" + controller + "/version"); err == nil {
		response.Body.Close()
		t.Error("宿主退出后内核也应结束")
	}
}
