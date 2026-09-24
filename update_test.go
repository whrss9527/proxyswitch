package main

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
)

// fakeRelease 模拟 GitHub 的最新发布接口和附件下载，以及发布页的跳转和固定的下载地址。
type fakeRelease struct {
	tag       string
	program   []byte
	checksums string
	withSums  bool
	// apiStatus 不为 0 时接口返回这个状态码，例如 403 模拟超过了访问次数限制。
	apiStatus int
	// pageRequests 是发布页被访问的次数。
	pageRequests atomic.Int32
}

func startFakeRelease(t *testing.T, release *fakeRelease) {
	t.Helper()
	var server *httptest.Server
	server = httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		downloads := "/owner/repo/releases/download/" + release.tag + "/"
		switch request.URL.Path {
		case "/releases/latest":
			if release.apiStatus != 0 {
				writer.Header().Set("X-RateLimit-Remaining", "0")
				writer.WriteHeader(release.apiStatus)
				return
			}
			assets := []map[string]any{
				{"name": updateAssetName(runtime.GOARCH), "browser_download_url": server.URL + "/download/program", "size": len(release.program)},
			}
			if release.withSums {
				assets = append(assets, map[string]any{"name": checksumsAssetName, "browser_download_url": server.URL + "/download/sums", "size": len(release.checksums)})
			}
			_ = json.NewEncoder(writer).Encode(map[string]any{
				"tag_name": release.tag, "html_url": server.URL + "/release", "published_at": "2026-10-01T00:00:00Z", "body": "- 更新内容", "assets": assets,
			})
		case "/download/program", downloads + updateAssetName(runtime.GOARCH):
			// 与 GitHub 一样给出文件大小，没有发布信息里的大小时进度按它显示。
			writer.Header().Set("Content-Length", strconv.Itoa(len(release.program)))
			_, _ = writer.Write(release.program)
		case "/download/sums", downloads + checksumsAssetName:
			if !release.withSums {
				writer.WriteHeader(http.StatusNotFound)
				return
			}
			_, _ = writer.Write([]byte(release.checksums))
		case "/owner/repo/releases/latest":
			release.pageRequests.Add(1)
			http.Redirect(writer, request, "/owner/repo/releases/tag/"+release.tag, http.StatusFound)
		default:
			writer.WriteHeader(http.StatusNotFound)
		}
	}))
	originalApi, originalPage := releaseApiUrl, releasePageUrl
	releaseApiUrl = server.URL + "/releases/latest"
	releasePageUrl = server.URL + "/owner/repo/releases/latest"
	t.Cleanup(func() {
		releaseApiUrl, releasePageUrl = originalApi, originalPage
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

// GitHub 接口拒绝访问（超过访问次数限制）时从发布页找到最新版本，附件按固定的地址下载，同样用校验文件校验。
func TestLatestReleaseFallback(t *testing.T) {
	program := []byte(strings.Repeat("fallback program ", 10000))
	sum := sha256.Sum256(program)
	release := &fakeRelease{
		tag:       "v99.1.0",
		program:   program,
		checksums: hex.EncodeToString(sum[:]) + "  " + updateAssetName(runtime.GOARCH) + "\n",
		withSums:  true,
		apiStatus: http.StatusForbidden,
	}
	startFakeRelease(t, release)

	info, err := checkLatestRelease("")
	if err != nil {
		t.Fatal(err)
	}
	if !info.Newer || !info.CanInstall || info.Latest != "99.1.0" || !strings.HasSuffix(info.Url, "/owner/repo/releases/tag/v99.1.0") {
		t.Fatalf("发布信息不对：%+v", info)
	}
	destination := filepath.Join(t.TempDir(), "update.download")
	var lastTotal int64
	if _, err := downloadLatestRelease("", destination, func(received, total int64) { lastTotal = total }); err != nil {
		t.Fatal(err)
	}
	if data, _ := os.ReadFile(destination); string(data) != string(program) {
		t.Error("下载的内容不对")
	}
	if lastTotal != int64(len(program)) {
		t.Errorf("不知道文件大小时应按下载的长度显示进度：%d", lastTotal)
	}

	release.apiStatus = http.StatusTooManyRequests
	if info, err := checkLatestRelease(""); err != nil || !info.CanInstall {
		t.Errorf("HTTP 429 时也应改从发布页查找：%+v %v", info, err)
	}
	release.withSums = false
	if info, err := checkLatestRelease(""); err != nil || !info.Newer || info.CanInstall {
		t.Errorf("没有校验文件时只能到发布页下载：%+v %v", info, err)
	}
	release.tag = "v" + appVersion
	if info, err := checkLatestRelease(""); err != nil || info.Newer {
		t.Errorf("已经是最新版本时不应提示更新：%+v %v", info, err)
	}

	// 连不上接口时不改从发布页查找：网页多半同样连不上，免得多等一次。
	apiUrl := releaseApiUrl
	closed, _ := net.Listen("tcp", "127.0.0.1:0")
	releaseApiUrl = "http://" + closed.Addr().String() + "/releases/latest"
	closed.Close()
	requests := release.pageRequests.Load()
	if _, err := checkLatestRelease(""); err == nil {
		t.Error("连不上接口时应报错")
	}
	if release.pageRequests.Load() != requests {
		t.Error("连不上接口时不应访问发布页")
	}

	// 发布页也不能用时报告接口的错误。
	releaseApiUrl = apiUrl
	releasePageUrl = apiUrl + "/missing"
	release.apiStatus = http.StatusForbidden
	if _, err := checkLatestRelease(""); err == nil || !strings.Contains(err.Error(), "访问次数") {
		t.Errorf("超过访问次数限制时应说明原因：%v", err)
	}
	release.apiStatus = http.StatusBadGateway
	if _, err := checkLatestRelease(""); err == nil || !strings.Contains(err.Error(), "HTTP 502") {
		t.Errorf("应报告接口返回的状态：%v", err)
	}
}

func TestUpdateAssetName(t *testing.T) {
	if updateAssetName("amd64") != "ProxySwitch.exe" || updateAssetName("arm64") != "ProxySwitch-arm64.exe" {
		t.Error("附件名称应与 Makefile 的输出一致")
	}
}
