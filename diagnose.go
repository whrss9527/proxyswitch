package main

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"syscall"
	"time"
)

// 网址诊断：某个网站打不开时，把链路走一遍——本机或局域网共享的状态、域名解析、直连、经代理（经内核时从它的日志里
// 找出这次连接命中的规则和出口）、节点、设备最近的连接——逐项给出结果，最后给一句结论和可以直接点的修复操作。

const (
	diagnosePc     = "pc"
	diagnoseDevice = "device"

	diagnoseDnsTimeout     = 5 * time.Second
	diagnoseDirectTimeout  = 8 * time.Second
	diagnoseProxiedTimeout = 10 * time.Second
	// 内核在连接建立或拨号失败时写日志，访问结束后最多再等这么久。
	diagnoseTraceWait = 1500 * time.Millisecond
)

// normalizeDiagnoseUrl 把「youtube.com」补成 https://youtube.com，带 http / https 的原样；认不出来时报错。
func normalizeDiagnoseUrl(text string) (*url.URL, error) {
	value := strings.TrimSpace(text)
	if value == "" || strings.ContainsAny(value, " \t\n") {
		return nil, errors.New("认不出这个网址：填 youtube.com 或者 https://… 这样的地址")
	}
	if !strings.Contains(value, "://") {
		value = "https://" + value
	}
	parsed, err := url.Parse(value)
	if err != nil || (parsed.Scheme != "http" && parsed.Scheme != "https") || parsed.Hostname() == "" {
		return nil, errors.New("认不出这个网址：填 youtube.com 或者 https://… 这样的地址")
	}
	parsed.Host = strings.ToLower(parsed.Host)
	if parsed.Path == "" {
		parsed.Path = "/"
	}
	return parsed, nil
}

// diagnosePort 是访问网址时连接的端口。
func diagnosePort(target *url.URL) int {
	if port, err := strconv.Atoi(target.Port()); err == nil {
		return port
	}
	if target.Scheme == "http" {
		return 80
	}
	return 443
}

// ---------- 访问一次 ----------

// DiagnoseProbe 是访问一次网址的结果：通了有 HTTP 状态码和耗时，没通有原因。
type DiagnoseProbe struct {
	Ok      bool   `json:"ok"`
	Status  int    `json:"status,omitempty"`
	Millis  int64  `json:"millis,omitempty"`
	Failure string `json:"failure,omitempty"`
}

func (probe DiagnoseProbe) Summary() string {
	if probe.Ok {
		return fmt.Sprintf("HTTP %d，%d ms", probe.Status, probe.Millis)
	}
	if probe.Failure == "" {
		return "失败"
	}
	return probe.Failure
}

// probeUrl 访问一次网址：proxy 为 nil 时直接访问。网站返回任何状态码都算通；经代理时 502 / 503 / 504
// 多半是代理连不上网站，算不通。
func probeUrl(ctx context.Context, target *url.URL, proxy *url.URL, timeout time.Duration) DiagnoseProbe {
	transport := &http.Transport{
		DialContext:           (&net.Dialer{Timeout: timeout}).DialContext,
		TLSHandshakeTimeout:   timeout,
		ResponseHeaderTimeout: timeout,
		DisableKeepAlives:     true,
	}
	if proxy != nil {
		transport.Proxy = http.ProxyURL(proxy)
	}
	defer transport.CloseIdleConnections()
	client := &http.Client{Transport: transport, Timeout: timeout, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, target.String(), nil)
	if err != nil {
		return DiagnoseProbe{Failure: "网址格式不对"}
	}
	request.Header.Set("User-Agent", appName+"/"+appVersion)
	started := time.Now()
	response, err := client.Do(request)
	if err != nil {
		return DiagnoseProbe{Millis: time.Since(started).Milliseconds(), Failure: diagnoseFailure(err, proxy != nil)}
	}
	_, _ = io.Copy(io.Discard, io.LimitReader(response.Body, 64<<10))
	response.Body.Close()
	probe := DiagnoseProbe{Ok: true, Status: response.StatusCode, Millis: time.Since(started).Milliseconds()}
	switch {
	case response.StatusCode == http.StatusProxyAuthRequired:
		return DiagnoseProbe{Status: response.StatusCode, Failure: "代理要求用户名和密码（HTTP 407）"}
	case proxy != nil && (response.StatusCode == http.StatusBadGateway || response.StatusCode == http.StatusServiceUnavailable || response.StatusCode == http.StatusGatewayTimeout):
		return DiagnoseProbe{Status: response.StatusCode, Failure: fmt.Sprintf("代理连不上这个网站（HTTP %d）", response.StatusCode)}
	}
	return probe
}

// Windows 上的套接字错误码。
const (
	windowsConnectionAborted = 10053
	windowsConnectionReset   = 10054
	windowsNetUnreachable    = 10051
	windowsHostUnreachable   = 10065
)

// diagnoseFailure 把访问失败的错误归成几类说明：超时、被拒绝、被中断、域名解析失败、TLS 握手失败、没有网络。
func diagnoseFailure(err error, proxied bool) string {
	message := err.Error()
	var netError net.Error
	var dnsError *net.DNSError
	var errno syscall.Errno
	var certificateError *tls.CertificateVerificationError
	var unknownAuthority x509.UnknownAuthorityError
	var hostnameError x509.HostnameError
	var recordError tls.RecordHeaderError
	switch {
	case strings.Contains(message, "Proxy Authentication Required"):
		return "代理要求用户名和密码（HTTP 407）"
	case strings.Contains(message, "proxyconnect"):
		return "连不上代理：" + lastErrorPart(message)
	case errors.As(err, &dnsError):
		return "域名解析失败"
	case errors.As(err, &netError) && netError.Timeout(), errors.Is(err, context.DeadlineExceeded):
		return "超时，没有响应"
	case isConnectionRefused(err):
		return "连接被拒绝"
	case errors.As(err, &errno) && (errno == syscall.ENETUNREACH || errno == syscall.EHOSTUNREACH || errno == windowsNetUnreachable || errno == windowsHostUnreachable):
		return "没有网络连接"
	case errors.As(err, &certificateError), errors.As(err, &unknownAuthority), errors.As(err, &hostnameError), strings.Contains(message, "certificate"):
		return "证书校验失败（可能被劫持，或者代理在拦截 HTTPS）"
	case strings.Contains(message, "HTTP response to HTTPS client"):
		return "TLS 握手失败（对方没有用 HTTPS）"
	case errors.As(err, &recordError), strings.Contains(message, "tls: "):
		return "TLS 握手失败（可能被劫持或屏蔽）"
	case errors.As(err, &errno) && (errno == syscall.ECONNRESET || errno == syscall.ECONNABORTED || errno == windowsConnectionReset || errno == windowsConnectionAborted),
		errors.Is(err, io.EOF), errors.Is(err, io.ErrUnexpectedEOF), strings.Contains(message, "connection reset"), strings.HasSuffix(message, "EOF"):
		if proxied {
			return "连接被中断（代理没能连上网站，或者被屏蔽）"
		}
		return "连接被中断（常见于被屏蔽）"
	case strings.Contains(message, "Bad Gateway"), strings.Contains(message, "Service Unavailable"), strings.Contains(message, "Gateway Timeout"):
		return "代理连不上这个网站（" + lastErrorPart(message) + "）"
	}
	return lastErrorPart(message)
}

// ---------- 域名解析 ----------

// lookupSystem 用系统的解析（含 VPN 的分域 DNS）解析域名，最多 5 个地址。
func lookupSystem(ctx context.Context, host string) []string {
	ctx, cancel := context.WithTimeout(ctx, diagnoseDnsTimeout)
	defer cancel()
	addresses, err := net.DefaultResolver.LookupHost(ctx, host)
	if err != nil {
		return []string{}
	}
	return firstStrings(addresses, 5)
}

// lookupRemote 经内核的代理端口用 Cloudflare 的 DoH 解析（内核按规则把它送到节点，得到的是境外看到的结果）。
// 失败时返回 nil。
func lookupRemote(ctx context.Context, host string, proxy *url.URL) []string {
	query := url.Values{"name": {host}, "type": {"A"}}
	ctx, cancel := context.WithTimeout(ctx, diagnoseDnsTimeout)
	defer cancel()
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, "https://cloudflare-dns.com/dns-query?"+query.Encode(), nil)
	if err != nil {
		return nil
	}
	request.Header.Set("Accept", "application/dns-json")
	transport := &http.Transport{Proxy: http.ProxyURL(proxy), DisableKeepAlives: true}
	defer transport.CloseIdleConnections()
	response, err := (&http.Client{Transport: transport, Timeout: diagnoseDnsTimeout}).Do(request)
	if err != nil {
		return nil
	}
	defer response.Body.Close()
	var result struct {
		Answer []struct {
			Type int    `json:"type"`
			Data string `json:"data"`
		} `json:"Answer"`
	}
	if response.StatusCode != http.StatusOK || json.NewDecoder(io.LimitReader(response.Body, 1<<20)).Decode(&result) != nil {
		return nil
	}
	addresses := []string{}
	for _, answer := range result.Answer {
		if answer.Type == 1 && !containsString(addresses, answer.Data) {
			addresses = append(addresses, answer.Data)
		}
	}
	return firstStrings(addresses, 5)
}

func firstStrings(list []string, limit int) []string {
	if len(list) > limit {
		return list[:limit]
	}
	return list
}

// ---------- 内核的判定 ----------

// RouteTrace 是内核对一次连接的判定，从它的日志行里解析出来：
//
//	[TCP] 127.0.0.1:50000 --> www.youtube.com:443 match RuleSet(proxy-1) using ProxySwitch[香港 01]
//	[TCP] 127.0.0.1:50000 --> example.com:443 doesn't match any rule using DIRECT
//	[TCP] dial ProxySwitch (match Match/) 127.0.0.1:50000 --> www.google.com:443 error: …
type RouteTrace struct {
	Host string `json:"host"`
	Port int    `json:"port"`
	// Rule 是命中的规则，例如 RuleSet(proxy-1)、Match；模式直接决定时为空。
	Rule string `json:"rule"`
	// Chain 是内核给的出口：ProxySwitch[香港 01]、DIRECT、上游代理。
	Chain string `json:"chain"`
	Error string `json:"error,omitempty"`
}

// Outbound 是实际的出口：节点名（去掉组名）、DIRECT、REJECT 或上游代理。
func (trace RouteTrace) Outbound() string {
	if open := strings.Index(trace.Chain, "["); open >= 0 && strings.HasSuffix(trace.Chain, "]") {
		return trace.Chain[open+1 : len(trace.Chain)-1]
	}
	return trace.Chain
}

func (trace RouteTrace) IsDirect() bool {
	return trace.Outbound() == "DIRECT"
}

// RuleText 是给人看的规则：没有命中规则时说明。
func (trace RouteTrace) RuleText() string {
	if trace.Rule == "" {
		return "（由模式决定）"
	}
	return trace.Rule
}

func parseRouteTrace(line string) *RouteTrace {
	body, found := strings.CutPrefix(line, "[TCP] ")
	if !found {
		return nil
	}
	arrow := strings.Index(body, " --> ")
	if arrow < 0 {
		return nil
	}
	if head, dial := strings.CutPrefix(body[:arrow], "dial "); dial {
		// head 是「出口 (match 类型/内容) 来源」或「出口 来源」。来源是 IP:端口，按程序分流时后面跟着
		// 「(程序名)」，程序名里可能有空格。
		rest := body[arrow+len(" --> "):]
		target, reason, found := strings.Cut(rest, " error: ")
		if !found {
			return nil
		}
		source := traceSourcePattern.FindStringSubmatch(head)
		if source == nil {
			return nil
		}
		proxy, rule := source[1], ""
		if start := strings.Index(proxy, " (match "); start >= 0 && strings.HasSuffix(proxy, ")") {
			kind, payload, _ := strings.Cut(proxy[start+len(" (match "):len(proxy)-1], "/")
			rule, proxy = kind, proxy[:start]
			if payload != "" {
				rule += "(" + payload + ")"
			}
		}
		host, port, ok := splitTraceTarget(target)
		if !ok {
			return nil
		}
		return &RouteTrace{Host: host, Port: port, Rule: rule, Chain: proxy, Error: reason}
	}
	rest := body[arrow+len(" --> "):]
	// 出口里有节点名，节点名什么字都可能有，所以找第一个「 using 」。
	using := strings.Index(rest, " using ")
	if using < 0 {
		return nil
	}
	middle, rule := rest[:using], ""
	// 「doesn't match any rule」里也有「 match 」，先认它。
	if target, found := strings.CutSuffix(middle, " doesn't match any rule"); found {
		middle, rule = target, "没有命中任何规则"
	} else if target, matched, found := strings.Cut(middle, " match "); found {
		middle, rule = target, matched
	}
	host, port, ok := splitTraceTarget(middle)
	if !ok {
		return nil
	}
	return &RouteTrace{Host: host, Port: port, Rule: rule, Chain: strings.TrimSpace(rest[using+len(" using "):])}
}

// traceSourcePattern 从内核日志「出口 来源」里分出出口：来源是最后的 IP:端口，可能带着 (程序名)。
var traceSourcePattern = regexp.MustCompile(`^(.*) \S+:\d+(?:\(.*\))?$`)

func splitTraceTarget(text string) (string, int, bool) {
	colon := strings.LastIndex(text, ":")
	if colon < 0 {
		return "", 0, false
	}
	port, err := strconv.Atoi(text[colon+1:])
	if err != nil {
		return "", 0, false
	}
	host := strings.TrimSuffix(strings.TrimPrefix(text[:colon], "["), "]")
	return strings.ToLower(host), port, host != ""
}

// ---------- 结论 ----------

// DiagnoseFacts 是检查收集到的事实，结论只看它。
type DiagnoseFacts struct {
	Perspective string
	Host        string
	// Route 是本机在用的代理：off 没开 / subscription 订阅配置 / profile 其他配置 / external 其他程序设置的系统代理。
	Route string
	// RouteText 是本机在用的代理的说法，例如「公司代理（10.0.0.1:8080）」。
	RouteText string
	// UsesCore 表示要诊断的链路经过内核的节点（本机用订阅，或共享的设备和本机一样走节点）。
	UsesCore bool
	// Subscription 是正在使用的订阅配置的 id；TurnOn 是可以一键开启的订阅配置的名字（没有时为空）。
	Subscription   string
	TurnOn         string
	ShareListening bool
	ShareUpstream  ShareUpstream
	Direct         *DiagnoseProbe
	Proxied        *DiagnoseProbe
	// ProxiedVia 是经代理访问时用的代理的说法。
	ProxiedVia string
	Trace      *RouteTrace
	Node       string
	// NodeDelay 为 nil 表示没测，0 表示连不上。
	NodeDelay *int
	// DeviceConnections 是最近设备对这个网站的连接数，只在设备视角时有。
	DeviceConnections *int
}

// DiagnoseAction 是结论里可以直接点的操作。Kind：turn_on 开启订阅配置（Profile 是名字）、pin_to_proxy 让 Host 走节点、
// auto_select 自动选择节点、test_nodes 全部测速、open_nodes 查看节点、open_share 去共享页、open_proxies 去代理页、
// copy_report 复制诊断报告。
type DiagnoseAction struct {
	Kind    string `json:"kind"`
	Label   string `json:"label"`
	Host    string `json:"host,omitempty"`
	Profile string `json:"profile,omitempty"`
}

// DiagnoseVerdict 是结论：一句话、一段解释、可以直接点的操作。
type DiagnoseVerdict struct {
	Headline    string           `json:"headline"`
	Explanation string           `json:"explanation"`
	Actions     []DiagnoseAction `json:"actions"`
}

var copyReportAction = DiagnoseAction{Kind: "copy_report", Label: "复制诊断报告"}

func turnOnAction(profile string) DiagnoseAction {
	return DiagnoseAction{Kind: "turn_on", Label: "开启「" + profile + "」", Profile: profile}
}

func diagnoseVerdict(facts DiagnoseFacts) DiagnoseVerdict {
	verdict := func(headline, explanation string, actions ...DiagnoseAction) DiagnoseVerdict {
		return DiagnoseVerdict{Headline: headline, Explanation: explanation, Actions: actions}
	}
	openShare := DiagnoseAction{Kind: "open_share", Label: "去局域网共享页"}
	withTurnOn := func(actions ...DiagnoseAction) []DiagnoseAction {
		if facts.TurnOn != "" {
			actions = append([]DiagnoseAction{turnOnAction(facts.TurnOn)}, actions...)
		}
		return actions
	}
	if facts.Perspective == diagnoseDevice && !facts.ShareListening {
		return verdict("共享入口没在监听", "设备是经这台电脑的共享入口上网的，入口没起来设备就连不上。到「局域网共享」页看状态和报错（常见是没开共享、端口被占用，或者还没下载内核）。", openShare)
	}
	proxied := facts.Proxied
	if proxied == nil {
		// 本机没开代理，或者没法经它访问。
		if facts.Direct != nil && facts.Direct.Ok {
			return verdict("直连正常，本机没开代理", "这个网站直连就能打开（"+facts.Direct.Summary()+"）。如果浏览器里仍然打不开，多半是网站本身或浏览器的问题，和代理无关。", copyReportAction)
		}
		reason := "没有测"
		if facts.Direct != nil {
			reason = facts.Direct.Summary()
		}
		if facts.TurnOn != "" {
			return verdict("本机没开代理，直连又打不开", "直连："+reason+"。这个网站直连访问不了，开启订阅配置后再试。", withTurnOn(copyReportAction)...)
		}
		return verdict("本机没开代理，直连又打不开", "直连："+reason+"。还没有能用的订阅，先在「代理」页添加机场订阅，或者开启一个代理配置。", DiagnoseAction{Kind: "open_proxies", Label: "去代理页"}, copyReportAction)
	}
	if proxied.Ok {
		explanation := "经" + facts.ProxiedVia + "访问成功：" + proxied.Summary() + "。"
		if trace := facts.Trace; trace != nil {
			if trace.Rule == "" {
				explanation += "内核让它走了 " + trace.Chain + "。"
			} else {
				explanation += "命中规则 " + trace.Rule + "，走 " + trace.Chain + "。"
			}
		}
		if facts.Perspective == diagnoseDevice && facts.DeviceConnections != nil && *facts.DeviceConnections == 0 {
			return verdict("从这台电脑看链路是通的，但设备最近没有访问它", explanation+"「最近的连接」里没有设备对这个网站的连接，说明设备上的那个应用没走代理——PS5 的代理设置只对系统流量和浏览器生效，不少应用用自己的网络连接。用设备的浏览器打开同一个网站可以对照。", openShare, copyReportAction)
		}
		if facts.Perspective == diagnoseDevice {
			return verdict("链路正常", explanation+"如果设备上仍然打不开，多半是那个应用自己的问题。", copyReportAction)
		}
		return verdict("链路正常", explanation+"如果浏览器里仍然打不开，试试刷新或者清除缓存；少数程序不走系统代理。", copyReportAction)
	}
	// 经代理访问失败。
	if trace := facts.Trace; trace != nil {
		detail := trace.Error
		if detail == "" {
			detail = proxied.Summary()
		}
		switch {
		case trace.IsDirect():
			directText := ""
			if facts.Direct != nil && facts.Direct.Ok {
				directText = "，但直接访问是通的（" + facts.Direct.Summary() + "），可能是内核解析到了不同的地址"
			} else if facts.Direct != nil {
				directText = "，直接访问也不通（" + facts.Direct.Summary() + "）"
			}
			explanation := "命中规则 " + trace.RuleText() + "，内核把它分到了直连" + directText + "。"
			if trace.Error != "" {
				explanation += "内核报错：" + trace.Error + "。"
			}
			if facts.UsesCore {
				return verdict("规则把它分到了直连，但直连不通", explanation+"常见原因是这个网站在国内访问不了，或者 DNS 被污染给了错误的地址；让它固定走节点就好。", DiagnoseAction{Kind: "pin_to_proxy", Label: "让 " + facts.Host + " 走节点", Host: facts.Host}, copyReportAction)
			}
			return verdict("直连不通", explanation+"本机没用订阅配置，共享的设备也跟着直连；开启订阅配置后，设备会和本机一样走节点。", withTurnOn(copyReportAction)...)
		case trace.Outbound() == coreUpstreamProxy:
			return verdict("转发给本机的代理失败", "本机用的是其他代理，共享的流量转发给它时失败："+detail+"。检查那个代理现在能不能用（「代理」页可以测速）。", copyReportAction)
		case trace.Outbound() == "REJECT":
			return verdict("规则拦截了这个网站", "命中规则 "+trace.RuleText()+"，被拦截了。去广告的规则有时会误拦，可以让它固定走节点或者直连。", DiagnoseAction{Kind: "pin_to_proxy", Label: "让 " + facts.Host + " 走节点", Host: facts.Host}, copyReportAction)
		case facts.NodeDelay != nil && *facts.NodeDelay == 0:
			return verdict("当前节点连不上", "它走的是节点 "+trace.Outbound()+"，但这个节点现在测不通："+detail+"。换一个节点，或者让程序自动选延迟最低的。", DiagnoseAction{Kind: "auto_select", Label: "自动选择节点"}, DiagnoseAction{Kind: "test_nodes", Label: "全部测速"}, copyReportAction)
		}
		delay := "未测"
		if facts.NodeDelay != nil {
			delay = fmt.Sprintf("%d ms", *facts.NodeDelay)
		}
		return verdict("节点能通，但这个网站经它打不开", "走的是节点 "+trace.Outbound()+"（延迟 "+delay+"），访问结果："+detail+"。可能是这个节点被目标网站屏蔽了，或者网站本身有问题；换个节点试试。", DiagnoseAction{Kind: "open_nodes", Label: "换个节点"}, copyReportAction)
	}
	if facts.UsesCore || facts.Perspective == diagnoseDevice {
		return verdict("经代理访问失败", "结果："+proxied.Summary()+"。内核没有记录到这次连接的判定，可能是内核这时候正在重新加载；再测一次。", copyReportAction)
	}
	return verdict("经"+facts.ProxiedVia+"访问失败", "结果："+proxied.Summary()+"。检查那个代理现在能不能用（「代理」页可以测速），或者换成订阅配置。", withTurnOn(copyReportAction)...)
}

// ---------- 诊断的进度 ----------

// DiagnoseRow 是诊断页上的一项检查。Outcome：pending 等待 / running 正在检查 / pass 正常 / warn 需要注意 / fail 有问题 / skipped 跳过。
type DiagnoseRow struct {
	Id      string `json:"id"`
	Title   string `json:"title"`
	Outcome string `json:"outcome"`
	Summary string `json:"summary"`
	Detail  string `json:"detail,omitempty"`
}

// DiagnoseJob 是一次诊断的进度和结果，设置页轮询显示。
type DiagnoseJob struct {
	Serial      int              `json:"serial"`
	Url         string           `json:"url"`
	Perspective string           `json:"perspective"`
	Running     bool             `json:"running"`
	Rows        []DiagnoseRow    `json:"rows"`
	Verdict     *DiagnoseVerdict `json:"verdict,omitempty"`
	// Subscription 是诊断时正在使用的订阅配置的 id，结论里「自动选择节点」「全部测速」「换个节点」对它操作。
	Subscription string `json:"subscription,omitempty"`
	Report       string `json:"report,omitempty"`
}

// diagnoseReport 是可以复制给别人看的文字报告。
func diagnoseReport(job DiagnoseJob) string {
	view := map[string]string{diagnosePc: "这台电脑", diagnoseDevice: "局域网设备"}[job.Perspective]
	lines := []string{"ProxySwitch 网址诊断：" + job.Url + "（" + view + "，版本 " + appVersion + "）"}
	marks := map[string]string{"pass": "✓", "warn": "!", "fail": "✗", "skipped": "–"}
	for _, row := range job.Rows {
		mark := marks[row.Outcome]
		if mark == "" {
			mark = "…"
		}
		line := mark + " " + row.Title + "：" + row.Summary
		if row.Detail != "" {
			line += "（" + row.Detail + "）"
		}
		lines = append(lines, line)
	}
	if job.Verdict != nil {
		lines = append(lines, "结论："+job.Verdict.Headline, job.Verdict.Explanation)
	}
	return strings.Join(lines, "\n")
}
