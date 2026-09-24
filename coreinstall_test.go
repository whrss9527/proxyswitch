package main

import (
	"archive/zip"
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func zipWith(t *testing.T, name string, content []byte) []byte {
	t.Helper()
	var buffer bytes.Buffer
	writer := zip.NewWriter(&buffer)
	file, err := writer.Create(name)
	if err != nil {
		t.Fatal(err)
	}
	_, _ = file.Write(content)
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}
	return buffer.Bytes()
}

func sha256Hex(data []byte) string {
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])
}

func TestInstallCoreAsset(t *testing.T) {
	program := []byte("fake mihomo program")
	files := map[string][]byte{
		"/core.zip":    zipWith(t, "mihomo-windows-amd64-v1.exe", program),
		"/noexe.zip":   zipWith(t, "README.md", []byte("readme")),
		"/notzip.zip":  []byte("<html>not found</html>"),
		"/missing.zip": nil,
	}
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		data, found := files[request.URL.Path]
		if !found || data == nil {
			writer.WriteHeader(http.StatusNotFound)
			return
		}
		_, _ = writer.Write(data)
	}))
	defer server.Close()
	closed, _ := net.Listen("tcp", "127.0.0.1:0")
	deadProxy := "http://" + closed.Addr().String()
	closed.Close()

	dir := t.TempDir()
	binary := filepath.Join(dir, coreBinaryName())
	if err := os.WriteFile(binary, []byte("old program"), 0o755); err != nil {
		t.Fatal(err)
	}
	var lastReceived int64
	err := installCoreAsset(dir, server.URL+"/core.zip", sha256Hex(files["/core.zip"]), []string{deadProxy, ""}, func(received, total int64) { lastReceived = received })
	if err != nil {
		t.Fatal(err)
	}
	if data, _ := os.ReadFile(binary); string(data) != string(program) {
		t.Errorf("应换成新下载的内核：%q", data)
	}
	if readCoreVersion(dir) != coreVersion || lastReceived != int64(len(files["/core.zip"])) {
		t.Errorf("应记下版本号并报告进度：%q %d", readCoreVersion(dir), lastReceived)
	}
	leftovers, _ := filepath.Glob(binary + ".*.old")
	for _, path := range append(leftovers, binary+".old", binary+".download") {
		if fileExists(path) {
			t.Errorf("不应留下临时文件：%s", path)
		}
	}

	failures := map[string]string{
		"/noexe.zip":   "没有程序",
		"/notzip.zip":  "无法打开",
		"/missing.zip": "HTTP 404",
	}
	for path, problem := range failures {
		if err := installCoreAsset(dir, server.URL+path, sha256Hex(files[path]), []string{""}, nil); err == nil || !strings.Contains(err.Error(), problem) {
			t.Errorf("%s 应报错「%s」：%v", path, problem, err)
		}
	}
	if err := installCoreAsset(dir, server.URL+"/core.zip", strings.Repeat("0", 64), []string{""}, nil); err == nil || !strings.Contains(err.Error(), "校验值不一致") {
		t.Errorf("校验值不一致时应报错：%v", err)
	}
	if data, _ := os.ReadFile(binary); string(data) != string(program) {
		t.Error("安装失败时不应动已有的内核")
	}
}

func TestCoreAsset(t *testing.T) {
	original := coreSha256Amd64
	defer func() { coreSha256Amd64 = original }()
	coreSha256Amd64 = ""
	if _, _, ok := coreAsset("windows", "amd64"); ok {
		t.Error("没有内置校验值时不能下载")
	}
	coreSha256Amd64 = "abc"
	if name, sha, ok := coreAsset("windows", "amd64"); !ok || name != "mihomo-windows-amd64-v1-"+coreVersion+".zip" || sha != "abc" {
		t.Errorf("x64 应使用兼容所有处理器的 v1 版本：%q %q %v", name, sha, ok)
	}
	if name, _, _ := coreAsset("windows", "arm64"); name != "mihomo-windows-arm64-"+coreVersion+".zip" {
		t.Errorf("ARM64 的文件名不对：%q", name)
	}
	if _, _, ok := coreAsset("linux", "amd64"); ok {
		t.Error("只有 Windows 版在程序里下载内核")
	}
}

func TestDownloadGeoData(t *testing.T) {
	mmdb := append(bytes.Repeat([]byte{1}, 70<<10), []byte("\xab\xcd\xefMaxMind.com")...)
	geosite := append([]byte{0x0a}, bytes.Repeat([]byte{2}, 70<<10)...)
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		switch request.URL.Path {
		case "/blocked/country.mmdb":
			_, _ = writer.Write([]byte("<html>" + strings.Repeat("x", 70<<10) + "</html>"))
		case "/mirror/country.mmdb":
			_, _ = writer.Write(mmdb)
		case "/mirror/geosite.dat":
			_, _ = writer.Write(geosite)
		default:
			writer.WriteHeader(http.StatusNotFound)
		}
	}))
	defer server.Close()
	original := coreGeoFiles
	defer func() { coreGeoFiles = original }()
	coreGeoFiles = []struct {
		Name string
		Urls []string
	}{
		{"Country.mmdb", []string{server.URL + "/blocked/country.mmdb", server.URL + "/mirror/country.mmdb"}},
		{"GeoSite.dat", []string{server.URL + "/mirror/geosite.dat"}},
	}
	dir := t.TempDir()
	if err := downloadGeoData(dir, []string{""}); err != nil {
		t.Fatal(err)
	}
	if data, _ := os.ReadFile(filepath.Join(dir, "Country.mmdb")); !bytes.Equal(data, mmdb) {
		t.Error("第一个地址返回的不是有效数据时应换下一个地址")
	}
	if data, _ := os.ReadFile(filepath.Join(dir, "GeoSite.dat")); !bytes.Equal(data, geosite) {
		t.Error("GeoSite.dat 内容不对")
	}

	coreGeoFiles[1].Urls = []string{server.URL + "/missing/geosite.dat"}
	_ = os.Remove(filepath.Join(dir, "GeoSite.dat"))
	if err := downloadGeoData(dir, []string{""}); err == nil || !strings.Contains(err.Error(), "GeoSite.dat") {
		t.Errorf("下载不到时应说明是哪个文件：%v", err)
	}

	for name, data := range map[string][]byte{
		"Country.mmdb": bytes.Repeat([]byte{1}, 70<<10),
		"GeoSite.dat":  append([]byte{'<'}, bytes.Repeat([]byte{2}, 70<<10)...),
	} {
		if validateGeoData(name, data) == nil {
			t.Errorf("%s 的无效内容应被发现", name)
		}
	}
	if validateGeoData("GeoSite.dat", []byte{0x0a}) == nil {
		t.Error("太小的文件应被发现")
	}
}
