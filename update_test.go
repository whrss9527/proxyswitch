package main

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

// fakeRelease 模拟 GitHub 的最新发布接口和附件下载。
type fakeRelease struct {
	tag       string
	program   []byte
	checksums string
	withSums  bool
}

func startFakeRelease(t *testing.T, release *fakeRelease) {
	t.Helper()
	var server *httptest.Server
	server = httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		switch request.URL.Path {
		case "/releases/latest":
			assets := []map[string]any{
				{"name": updateAssetName(runtime.GOARCH), "browser_download_url": server.URL + "/download/program", "size": len(release.program)},
			}
			if release.withSums {
				assets = append(assets, map[string]any{"name": checksumsAssetName, "browser_download_url": server.URL + "/download/sums", "size": len(release.checksums)})
			}
			_ = json.NewEncoder(writer).Encode(map[string]any{
				"tag_name": release.tag, "html_url": server.URL + "/release", "published_at": "2026-10-01T00:00:00Z", "body": "- 更新内容", "assets": assets,
			})
		case "/download/program":
			_, _ = writer.Write(release.program)
		case "/download/sums":
			_, _ = writer.Write([]byte(release.checksums))
		default:
			writer.WriteHeader(http.StatusNotFound)
		}
	}))
	original := releaseApiUrl
	releaseApiUrl = server.URL + "/releases/latest"
	t.Cleanup(func() {
		releaseApiUrl = original
		server.Close()
	})
}

func TestDownloadLatestRelease(t *testing.T) {
	program := []byte(strings.Repeat("new program ", 20000))
	sum := sha256.Sum256(program)
	release := &fakeRelease{
		tag:       "v99.0.0",
		program:   program,
		checksums: strings.Repeat("0", 64) + "  proxyswitch-99.0.0-src.zip\n" + hex.EncodeToString(sum[:]) + " *" + updateAssetName(runtime.GOARCH) + "\n",
		withSums:  true,
	}
	startFakeRelease(t, release)

	info, err := checkLatestRelease("")
	if err != nil {
		t.Fatal(err)
	}
	if !info.Newer || !info.CanInstall || info.Latest != "99.0.0" || info.AssetSize != int64(len(program)) {
		t.Fatalf("发布信息不对：%+v", info)
	}

	destination := filepath.Join(t.TempDir(), "update.download")
	var lastReceived, lastTotal int64
	if _, err := downloadLatestRelease("", destination, func(received, total int64) { lastReceived, lastTotal = received, total }); err != nil {
		t.Fatal(err)
	}
	if data, _ := os.ReadFile(destination); string(data) != string(program) {
		t.Error("下载的内容不对")
	}
	if lastReceived != int64(len(program)) || lastTotal != int64(len(program)) {
		t.Errorf("进度不对：%d / %d", lastReceived, lastTotal)
	}

	release.checksums = strings.Repeat("a", 64) + "  " + updateAssetName(runtime.GOARCH) + "\n"
	if _, err := downloadLatestRelease("", destination, nil); err == nil || !strings.Contains(err.Error(), "校验值不一致") {
		t.Errorf("校验值不一致时应报错：%v", err)
	}
	if _, err := os.Stat(destination); !os.IsNotExist(err) {
		t.Error("校验不通过的文件应删除")
	}

	release.checksums = "没有这个文件的校验值\n"
	if _, err := downloadLatestRelease("", destination, nil); err == nil || !strings.Contains(err.Error(), "校验文件里没有") {
		t.Errorf("校验文件里找不到时应报错：%v", err)
	}

	release.withSums = false
	if info, _ := checkLatestRelease(""); info.CanInstall {
		t.Error("没有校验文件时不能在程序里更新")
	}
	if _, err := downloadLatestRelease("", destination, nil); err == nil {
		t.Error("没有校验文件时下载应报错")
	}

	release.tag = "v" + appVersion
	release.withSums = true
	if _, err := downloadLatestRelease("", destination, nil); err == nil || !strings.Contains(err.Error(), "最新版本") {
		t.Errorf("已经是最新版本时应报错：%v", err)
	}
}

func TestUpdateAssetName(t *testing.T) {
	if updateAssetName("amd64") != "ProxySwitch.exe" || updateAssetName("arm64") != "ProxySwitch-arm64.exe" {
		t.Error("附件名称应与 Makefile 的输出一致")
	}
}
