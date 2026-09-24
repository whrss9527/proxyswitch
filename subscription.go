package main

import (
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"mime"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"
)

// 订阅：从机场给的地址下载节点列表（Clash 配置，或 base64 编码的 ss:// vmess:// 等节点链接），
// 保存到内核的工作目录，由内核解析。响应头 subscription-userinfo 里的已用流量、总流量和到期时间一起记下来。

const (
	maxSubscriptionSize  = 16 << 20
	subscriptionTimeout  = 30 * time.Second
	subscriptionInterval = 24 * time.Hour
	subscriptionRetry    = time.Hour
)

// subscriptionUserAgent 与 mihomo 自己下载订阅时的 User-Agent 相同，机场据此返回 Clash 格式。
var subscriptionUserAgent = "clash.meta/" + coreVersion

// SubscriptionInfo 是一个订阅的下载记录和机场提供的流量信息，保存在 state.json。
// Source 是订阅地址的摘要，地址改了就要重新下载；Updated 是上次下载成功的时间（也用作内核重新读取的依据），
// Attempted 是上次尝试的时间；Nodes 是下载时估计的节点数。
type SubscriptionInfo struct {
	Source    string `json:"source,omitempty"`
	Updated   string `json:"updated,omitempty"`
	Attempted string `json:"attempted,omitempty"`
	Error     string `json:"error,omitempty"`
	Nodes     int    `json:"nodes,omitempty"`
	Upload    int64  `json:"upload,omitempty"`
	Download  int64  `json:"download,omitempty"`
	Total     int64  `json:"total,omitempty"`
	Expire    int64  `json:"expire,omitempty"`
}

// subscriptionDownload 是一次下载的结果。Name 是机场在 Content-Disposition 里给的名字，Nodes 是估计的节点数。
type subscriptionDownload struct {
	Content []byte
	Info    SubscriptionInfo
	Name    string
	Nodes   int
	Format  string
}

// httpStatusError 表示服务器返回了错误状态，换一条网络路径重试也没用。
type httpStatusError struct {
	status int
}

func (err httpStatusError) Error() string {
	switch err.status {
	case http.StatusUnauthorized, http.StatusForbidden, http.StatusNotFound, http.StatusGone:
		return fmt.Sprintf("服务器返回 HTTP %d，订阅地址可能填错了或已经失效", err.status)
	}
	return fmt.Sprintf("服务器返回 HTTP %d", err.status)
}

// fetchSubscription 下载订阅，proxies 是依次尝试的网络路径：空字符串表示直连，其余是代理地址。
// 连不上时换下一条路径；服务器返回了错误状态或内容不对时不再重试。
func fetchSubscription(address string, proxies []string) (subscriptionDownload, error) {
	if err := validateSubscriptionUrl(address); err != nil {
		return subscriptionDownload{}, err
	}
	var firstErr error
	for _, proxyUrl := range proxies {
		result, err := fetchSubscriptionVia(address, proxyUrl)
		if err == nil {
			return result, nil
		}
		if firstErr == nil {
			firstErr = err
		}
		var statusErr httpStatusError
		if errors.As(err, &statusErr) || errors.Is(err, errBadSubscription) {
			return subscriptionDownload{}, err
		}
	}
	if firstErr == nil {
		firstErr = errors.New("没有可用的网络路径")
	}
	return subscriptionDownload{}, firstErr
}

var errBadSubscription = errors.New("订阅内容不对")

func fetchSubscriptionVia(address, proxyUrl string) (subscriptionDownload, error) {
	client := updateClient(proxyUrl, subscriptionTimeout)
	request, err := http.NewRequest(http.MethodGet, address, nil)
	if err != nil {
		return subscriptionDownload{}, errors.New("订阅地址格式不对")
	}
	request.Header.Set("User-Agent", subscriptionUserAgent)
	response, err := client.Do(request)
	if err != nil {
		return subscriptionDownload{}, errors.New(friendlyDownloadError(err, subscriptionTimeout))
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return subscriptionDownload{}, httpStatusError{response.StatusCode}
	}
	content, err := io.ReadAll(io.LimitReader(response.Body, maxSubscriptionSize+1))
	if err != nil {
		return subscriptionDownload{}, errors.New("下载中断：" + friendlyDownloadError(err, subscriptionTimeout))
	}
	if len(content) > maxSubscriptionSize {
		return subscriptionDownload{}, fmt.Errorf("%w：超过 %d MB", errBadSubscription, maxSubscriptionSize>>20)
	}
	format, nodes, err := inspectSubscription(content)
	if err != nil {
		return subscriptionDownload{}, err
	}
	return subscriptionDownload{
		Content: content,
		Info:    parseSubscriptionUserinfo(response.Header.Get("subscription-userinfo")),
		Name:    subscriptionNameFromHeader(response.Header.Get("Content-Disposition")),
		Nodes:   nodes,
		Format:  format,
	}, nil
}

// friendlyDownloadError 与测速用的错误说明相同，只是把“测速地址”换成下载的地址。
func friendlyDownloadError(err error, timeout time.Duration) string {
	return strings.ReplaceAll(friendlyRequestError(err, timeout), "测速地址", "下载地址")
}

func validateSubscriptionUrl(address string) error {
	parsed, err := url.Parse(strings.TrimSpace(address))
	if err != nil || (parsed.Scheme != "http" && parsed.Scheme != "https") || parsed.Host == "" {
		return errors.New("订阅地址应以 http:// 或 https:// 开头")
	}
	return nil
}

// parseSubscriptionUserinfo 解析 subscription-userinfo：upload=字节; download=字节; total=字节; expire=Unix 秒。
func parseSubscriptionUserinfo(header string) SubscriptionInfo {
	var info SubscriptionInfo
	for _, field := range strings.Split(strings.ToLower(strings.ReplaceAll(header, " ", "")), ";") {
		name, value, found := strings.Cut(field, "=")
		if !found {
			continue
		}
		number, err := strconv.ParseInt(value, 10, 64)
		if err != nil {
			floatValue, floatErr := strconv.ParseFloat(value, 64)
			if floatErr != nil {
				continue
			}
			number = int64(floatValue)
		}
		switch name {
		case "upload":
			info.Upload = number
		case "download":
			info.Download = number
		case "total":
			info.Total = number
		case "expire":
			info.Expire = number
		}
	}
	return info
}

// subscriptionNameFromHeader 取出机场在 Content-Disposition 里给的文件名（一般是机场的名字），去掉扩展名。
func subscriptionNameFromHeader(header string) string {
	if header == "" {
		return ""
	}
	_, params, err := mime.ParseMediaType(header)
	if err != nil {
		return ""
	}
	name := strings.TrimSpace(params["filename"])
	for _, extension := range []string{".yaml", ".yml", ".txt", ".conf"} {
		if strings.HasSuffix(strings.ToLower(name), extension) {
			name = strings.TrimSpace(name[:len(name)-len(extension)])
		}
	}
	if !utf8.ValidString(name) || strings.ContainsAny(name, "\r\n") {
		return ""
	}
	if runes := []rune(name); len(runes) > maxProfileNameLength {
		name = string(runes[:maxProfileNameLength])
	}
	return name
}

// 订阅里的节点链接支持的协议，与 mihomo 能转换的一致。
var nodeLinkSchemes = []string{"ss", "ssr", "vmess", "vless", "trojan", "hysteria", "hysteria2", "hy2", "tuic", "anytls", "socks", "socks5", "http", "https", "mierus"}

// inspectSubscription 判断订阅内容的格式（clash / links）并估计节点数。不做完整的 YAML 解析，实际的节点由内核解析。
func inspectSubscription(content []byte) (format string, nodes int, err error) {
	text := strings.TrimSpace(strings.TrimPrefix(string(content), "\xef\xbb\xbf"))
	if text == "" {
		return "", 0, fmt.Errorf("%w：内容是空的", errBadSubscription)
	}
	if strings.HasPrefix(text, "<") {
		return "", 0, fmt.Errorf("%w：返回的是网页，订阅地址可能填错了", errBadSubscription)
	}
	if decoded, ok := decodeBase64Text(text); ok {
		text = decoded
	}
	if count, found := countYamlProxies(text); found {
		if count == 0 {
			return "", 0, fmt.Errorf("%w：订阅里没有节点，可能已经过期或流量用完了", errBadSubscription)
		}
		return "clash", count, nil
	}
	if count := countNodeLinks(text); count > 0 {
		return "links", count, nil
	}
	return "", 0, fmt.Errorf("%w：既不是 Clash 配置，也不是节点链接", errBadSubscription)
}

// decodeBase64Text 尝试把整段内容当作 base64 解码（允许换行、URL 安全字符和省略填充），解出含节点链接的文本才算成功。
func decodeBase64Text(text string) (string, bool) {
	compact := strings.Join(strings.Fields(text), "")
	for _, encoding := range []*base64.Encoding{base64.StdEncoding, base64.RawStdEncoding, base64.URLEncoding, base64.RawURLEncoding} {
		data, err := encoding.DecodeString(compact)
		if err != nil {
			continue
		}
		decoded := string(data)
		if utf8.ValidString(decoded) && countNodeLinks(decoded) > 0 {
			return decoded, true
		}
	}
	return "", false
}

func countNodeLinks(text string) int {
	count := 0
	for _, line := range strings.Split(text, "\n") {
		scheme, _, found := strings.Cut(strings.TrimSpace(line), "://")
		if found && containsString(nodeLinkSchemes, strings.ToLower(scheme)) {
			count++
		}
	}
	return count
}

// countYamlProxies 数 Clash 配置顶层 proxies 列表的项数：只看 “proxies:” 键下面与第一项缩进相同的 “- ” 行。
func countYamlProxies(text string) (count int, found bool) {
	listIndent := -1
	for _, line := range strings.Split(text, "\n") {
		line = strings.TrimRight(line, "\r")
		trimmed := strings.TrimLeft(line, " ")
		if trimmed == "" || strings.HasPrefix(trimmed, "#") {
			continue
		}
		indent := len(line) - len(trimmed)
		if !found {
			if indent == 0 && strings.HasPrefix(line, "proxies:") {
				found = true
				rest := strings.TrimSpace(strings.TrimPrefix(line, "proxies:"))
				if strings.HasPrefix(rest, "[") {
					// 写成一行的列表：[] 表示没有节点，其余按 name 的个数算。
					return strings.Count(rest, "name:"), true
				}
			}
			continue
		}
		isItem := trimmed == "-" || strings.HasPrefix(trimmed, "- ") || strings.HasPrefix(trimmed, "-{")
		if listIndent < 0 {
			if !isItem {
				return 0, true
			}
			listIndent = indent
		}
		if indent < listIndent || (indent == listIndent && !isItem) {
			break
		}
		if indent == listIndent {
			count++
		}
	}
	return count, found
}

// subscriptionSource 是订阅地址的摘要，存在状态里判断下载的文件是不是当前地址的。
func subscriptionSource(address string) string {
	digest := sha256.Sum256([]byte(strings.TrimSpace(address)))
	return hex.EncodeToString(digest[:8])
}

// subscriptionFile 是订阅保存在内核工作目录里的相对路径。
func subscriptionFile(profileId string) string {
	return "subscriptions/" + profileId + ".yaml"
}

// writeSubscriptionFile 先写临时文件再改名，内核读到的一定是完整的内容。
func writeSubscriptionFile(coreDir, profileId string, content []byte) error {
	path := filepath.Join(coreDir, filepath.FromSlash(subscriptionFile(profileId)))
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	temporary := path + ".tmp"
	if err := os.WriteFile(temporary, content, 0o644); err != nil {
		return err
	}
	if err := os.Rename(temporary, path); err != nil {
		_ = os.Remove(temporary)
		return err
	}
	return nil
}
