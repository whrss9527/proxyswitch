package main

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"os"
	"runtime"
	"strconv"
	"strings"
	"time"
)

// 检查和下载更新：读取 GitHub 上的最新发布，找到本机架构的 exe，用发布里的 SHA256SUMS.txt 校验。
// 替换正在运行的程序并重新启动由 Windows 版的 App 完成。

// 两个地址都是变量，测试版本可以用 -ldflags -X 把它们指到本地的模拟服务。
var (
	releaseApiUrl = "https://api.github.com/repos/whrss9527/proxyswitch/releases/latest"
	// releasePageUrl 是最新发布的网页，GitHub 会把它跳转到 /releases/tag/<标签>。接口拒绝访问时从这里找最新版本：
	// 接口对未登录的访问每个 IP 每小时只允许 60 次，公司网络、代理服务器的出口 IP 很多人共用，很容易超过。
	releasePageUrl = repositoryUrl + "/releases/latest"
)

const (
	checksumsAssetName  = "SHA256SUMS.txt"
	maxUpdateSize       = 64 << 20
	updateCheckInterval = 24 * time.Hour
	updateRetryInterval = 3 * time.Hour
	updateCheckTimeout  = 10 * time.Second
	updateDownloadLimit = 10 * time.Minute
)

// UpdateInfo 是最新发布的信息。CanInstall 表示发布里有本机架构的程序和校验文件，可以在程序里直接更新。
type UpdateInfo struct {
	Current    string `json:"current"`
	Latest     string `json:"latest"`
	Newer      bool   `json:"newer"`
	Url        string `json:"url"`
	Published  string `json:"published"`
	Notes      string `json:"notes"`
	AssetSize  int64  `json:"asset_size,omitempty"`
	CanInstall bool   `json:"can_install"`

	assetName    string
	assetUrl     string
	checksumsUrl string
}

// InstallProgress 是正在下载的更新的进度，Total 为 0 表示大小未知。
type InstallProgress struct {
	Received int64 `json:"received"`
	Total    int64 `json:"total"`
}

// updateAssetName 是发布里给某个架构用的 exe 文件名，与 Makefile 和发布流程一致。
func updateAssetName(arch string) string {
	if arch == "arm64" {
		return "ProxySwitch-arm64.exe"
	}
	return "ProxySwitch.exe"
}

// updateClient 返回访问 GitHub 用的 HTTP 客户端；proxyUrl 不为空时经这个代理访问。
func updateClient(proxyUrl string, timeout time.Duration) *http.Client {
	transport := &http.Transport{Proxy: nil, DisableKeepAlives: true}
	if proxyUrl != "" {
		if parsed, err := url.Parse(proxyUrl); err == nil {
			transport.Proxy = http.ProxyURL(parsed)
		}
	}
	return &http.Client{Transport: transport, Timeout: timeout}
}

// checkLatestRelease 读取最新发布的信息。GitHub 接口拒绝访问（多半是超过了访问次数限制）时改从发布页查找。
func checkLatestRelease(proxyUrl string) (UpdateInfo, error) {
	client := updateClient(proxyUrl, updateCheckTimeout)
	info, rejected, err := latestReleaseFromApi(client)
	if !rejected {
		return info, err
	}
	fallback, pageErr := latestReleaseFromPage(client)
	if pageErr != nil {
		slog.Warn("从发布页查找最新版本失败", "err", pageErr)
		return UpdateInfo{}, err
	}
	return fallback, nil
}

// latestReleaseFromApi 用 GitHub 接口读取最新发布。第二个返回值为 true 表示接口返回了错误状态，可以改从发布页查找。
func latestReleaseFromApi(client *http.Client) (UpdateInfo, bool, error) {
	request, err := http.NewRequest(http.MethodGet, releaseApiUrl, nil)
	if err != nil {
		return UpdateInfo{}, false, err
	}
	request.Header.Set("Accept", "application/vnd.github+json")
	request.Header.Set("User-Agent", appName+"/"+appVersion)
	response, err := client.Do(request)
	if err != nil {
		return UpdateInfo{}, false, errors.New(friendlyRequestError(err, client.Timeout))
	}
	defer response.Body.Close()
	switch response.StatusCode {
	case http.StatusOK:
	case http.StatusForbidden, http.StatusTooManyRequests:
		return UpdateInfo{}, true, errors.New("GitHub 暂时限制了当前网络的访问次数，请过一会儿再试")
	default:
		return UpdateInfo{}, true, fmt.Errorf("GitHub 返回 HTTP %d", response.StatusCode)
	}
	var release struct {
		TagName     string `json:"tag_name"`
		HtmlUrl     string `json:"html_url"`
		PublishedAt string `json:"published_at"`
		Body        string `json:"body"`
		Assets      []struct {
			Name string `json:"name"`
			Url  string `json:"browser_download_url"`
			Size int64  `json:"size"`
		} `json:"assets"`
	}
	if err := json.NewDecoder(io.LimitReader(response.Body, 1<<20)).Decode(&release); err != nil {
		return UpdateInfo{}, false, err
	}
	latest := strings.TrimPrefix(release.TagName, "v")
	notes := release.Body
	if runes := []rune(notes); len(runes) > 1200 {
		notes = string(runes[:1200]) + "…"
	}
	info := UpdateInfo{
		Current:   appVersion,
		Latest:    latest,
		Newer:     compareVersions(latest, appVersion) > 0,
		Url:       release.HtmlUrl,
		Published: release.PublishedAt,
		Notes:     notes,
	}
	wanted := updateAssetName(runtime.GOARCH)
	for _, asset := range release.Assets {
		switch asset.Name {
		case wanted:
			info.assetName, info.assetUrl, info.AssetSize = asset.Name, asset.Url, asset.Size
		case checksumsAssetName:
			info.checksumsUrl = asset.Url
		}
	}
	info.CanInstall = info.Newer && info.assetUrl != "" && info.checksumsUrl != ""
	return info, false, nil
}

// latestReleaseFromPage 从最新发布的网页跳转到的地址（…/releases/tag/<标签>）得到版本号，附件用固定的下载地址。
// 发布流程先上传附件再发布，校验文件里有本机程序的校验值就可以在程序里更新。这样得不到更新说明、发布时间和文件大小。
func latestReleaseFromPage(client *http.Client) (UpdateInfo, error) {
	request, err := http.NewRequest(http.MethodGet, releasePageUrl, nil)
	if err != nil {
		return UpdateInfo{}, err
	}
	request.Header.Set("User-Agent", appName+"/"+appVersion)
	noRedirect := *client
	noRedirect.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	response, err := noRedirect.Do(request)
	if err != nil {
		return UpdateInfo{}, errors.New(friendlyRequestError(err, client.Timeout))
	}
	response.Body.Close()
	location, err := response.Location()
	if err != nil {
		return UpdateInfo{}, fmt.Errorf("发布页没有跳转到最新版本（HTTP %d）", response.StatusCode)
	}
	repository, tag, found := strings.Cut(location.Path, "/releases/tag/")
	if !found || tag == "" || strings.Contains(tag, "/") {
		return UpdateInfo{}, errors.New("发布页跳转到了意外的地址：" + location.String())
	}
	latest := strings.TrimPrefix(tag, "v")
	info := UpdateInfo{Current: appVersion, Latest: latest, Newer: compareVersions(latest, appVersion) > 0, Url: location.String()}
	if !info.Newer {
		return info, nil
	}
	downloads := (&url.URL{Scheme: location.Scheme, Host: location.Host, Path: repository + "/releases/download/" + tag + "/"}).String()
	info.assetName = updateAssetName(runtime.GOARCH)
	info.assetUrl = downloads + info.assetName
	info.checksumsUrl = downloads + checksumsAssetName
	_, err = fetchChecksum(client, info.checksumsUrl, info.assetName)
	info.CanInstall = err == nil
	return info, nil
}

// downloadLatestRelease 检查最新发布，有新版本时把本机架构的程序下载到 destination 并校验。
func downloadLatestRelease(proxyUrl, destination string, progress func(received, total int64)) (UpdateInfo, error) {
	info, err := checkLatestRelease(proxyUrl)
	if err != nil {
		return info, err
	}
	if !info.Newer {
		return info, errors.New("已经是最新版本")
	}
	return info, downloadUpdate(updateClient(proxyUrl, updateDownloadLimit), info, destination, progress)
}

// downloadUpdate 把新版本下载到 destination，并与 SHA256SUMS.txt 里的校验值比对，不一致时删除下载的文件。
// progress 在下载过程中接收已下载和总字节数。
func downloadUpdate(client *http.Client, info UpdateInfo, destination string, progress func(received, total int64)) error {
	if !info.CanInstall {
		return errors.New("这个版本的发布里没有本机可用的程序文件或校验文件，请到发布页手动下载")
	}
	expected, err := fetchChecksum(client, info.checksumsUrl, info.assetName)
	if err != nil {
		return err
	}
	file, err := os.OpenFile(destination, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0o755)
	if err != nil {
		return fmt.Errorf("无法在程序所在的文件夹里写入文件，请到发布页手动下载（%v）", err)
	}
	hash := sha256.New()
	err = copyDownload(client, info.assetUrl, io.MultiWriter(file, hash), info.AssetSize, progress)
	if closeErr := file.Close(); err == nil && closeErr != nil {
		err = fmt.Errorf("写入文件失败：%v", closeErr)
	}
	if err == nil && hex.EncodeToString(hash.Sum(nil)) != expected {
		err = errors.New("下载的文件与发布的校验值不一致，可能没有下载完整，请重试")
	}
	if err != nil {
		_ = os.Remove(destination)
	}
	return err
}

// fetchChecksum 从 SHA256SUMS.txt（sha256sum 的输出格式）里找出 name 的校验值。
func fetchChecksum(client *http.Client, checksumsUrl, name string) (string, error) {
	response, err := client.Get(checksumsUrl)
	if err != nil {
		return "", errors.New("下载校验文件失败：" + friendlyRequestError(err, client.Timeout))
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return "", fmt.Errorf("下载校验文件失败：HTTP %d", response.StatusCode)
	}
	data, err := io.ReadAll(io.LimitReader(response.Body, 64<<10))
	if err != nil {
		return "", errors.New("下载校验文件失败：" + friendlyRequestError(err, client.Timeout))
	}
	for _, line := range strings.Split(string(data), "\n") {
		fields := strings.Fields(line)
		if len(fields) == 2 && strings.TrimPrefix(fields[1], "*") == name && len(fields[0]) == sha256.Size*2 {
			return strings.ToLower(fields[0]), nil
		}
	}
	return "", fmt.Errorf("校验文件里没有 %s 的校验值", name)
}

func copyDownload(client *http.Client, address string, writer io.Writer, expectedSize int64, progress func(received, total int64)) error {
	response, err := client.Get(address)
	if err != nil {
		return errors.New("下载失败：" + friendlyRequestError(err, client.Timeout))
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return fmt.Errorf("下载失败：HTTP %d", response.StatusCode)
	}
	total := response.ContentLength
	if total <= 0 {
		total = expectedSize
	}
	buffer := make([]byte, 64<<10)
	var received int64
	for {
		count, readErr := response.Body.Read(buffer)
		if count > 0 {
			received += int64(count)
			if received > maxUpdateSize {
				return errors.New("下载的文件大小异常")
			}
			if _, err := writer.Write(buffer[:count]); err != nil {
				return fmt.Errorf("写入文件失败：%v", err)
			}
			if progress != nil {
				progress(received, total)
			}
		}
		if errors.Is(readErr, io.EOF) {
			return nil
		}
		if readErr != nil {
			return errors.New("下载中断：" + friendlyRequestError(readErr, client.Timeout))
		}
	}
}

// compareVersions 比较 x.y.z 形式的版本号，非数字部分按 0 处理。
func compareVersions(first, second string) int {
	firstParts := strings.Split(strings.TrimPrefix(first, "v"), ".")
	secondParts := strings.Split(strings.TrimPrefix(second, "v"), ".")
	for index := 0; index < len(firstParts) || index < len(secondParts); index++ {
		firstNumber, secondNumber := versionPart(firstParts, index), versionPart(secondParts, index)
		if firstNumber != secondNumber {
			if firstNumber > secondNumber {
				return 1
			}
			return -1
		}
	}
	return 0
}

func versionPart(parts []string, index int) int {
	if index >= len(parts) {
		return 0
	}
	digits := strings.TrimRightFunc(parts[index], func(char rune) bool { return char < '0' || char > '9' })
	number, _ := strconv.Atoi(digits)
	return number
}
