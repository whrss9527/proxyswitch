package main

import (
	"bufio"
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"sort"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"
)

// Listener 是本机一个正在监听的 TCP 端口。
type Listener struct {
	Address string `json:"address"`
	Port    int    `json:"port"`
	Pid     uint32 `json:"pid"`
	Process string `json:"process"`
}

// DetectedProxy 是确认可以当代理用的本机端口。
type DetectedProxy struct {
	Host      string `json:"host"`
	Port      int    `json:"port"`
	Process   string `json:"process"`
	Pid       uint32 `json:"pid"`
	Http      bool   `json:"http"`
	HttpAuth  bool   `json:"http_auth"`
	Socks     bool   `json:"socks"`
	SocksAuth bool   `json:"socks_auth"`
	Server    string `json:"server"`
}

// 这些进程监听的端口不会是代理，跳过以免对系统服务发探测数据。
var systemProcessNames = map[string]bool{
	"system": true, "idle": true, "svchost.exe": true, "lsass.exe": true, "services.exe": true,
	"wininit.exe": true, "winlogon.exe": true, "csrss.exe": true, "smss.exe": true, "spoolsv.exe": true,
	"msmpeng.exe": true, "nissrv.exe": true, "searchindexer.exe": true, "searchhost.exe": true,
	"mdnsresponder.exe": true, "jhi_service.exe": true, "wudfhost.exe": true,
}

// 常见本地代理端口，拿不到监听端口列表时逐个试探。
var commonProxyPorts = []int{1080, 1081, 3128, 7890, 7891, 7897, 7898, 8080, 8118, 8888, 8889, 9090, 10808, 10809, 10810, 20170, 20171, 20172}

const (
	maxProbeCandidates = 64
	probeConcurrency   = 16
)

func proxyCandidates(listeners []Listener) []Listener {
	byPort := map[int]Listener{}
	for _, listener := range listeners {
		if listener.Port < 1024 || listener.Pid == 4 {
			continue
		}
		if systemProcessNames[strings.ToLower(listener.Process)] {
			continue
		}
		listener.Address = probeHost(listener.Address)
		existing, found := byPort[listener.Port]
		// 同一端口 IPv4 / IPv6 各出现一次时优先用 127.0.0.1。
		if !found || (existing.Address != "127.0.0.1" && listener.Address == "127.0.0.1") {
			byPort[listener.Port] = listener
		}
	}
	candidates := make([]Listener, 0, len(byPort))
	for _, listener := range byPort {
		candidates = append(candidates, listener)
	}
	sort.Slice(candidates, func(first, second int) bool { return candidates[first].Port < candidates[second].Port })
	if len(candidates) > maxProbeCandidates {
		candidates = candidates[:maxProbeCandidates]
	}
	return candidates
}

// probeHost 把监听地址换成探测时要连的地址：通配地址用 127.0.0.1。
func probeHost(address string) string {
	switch address {
	case "", "0.0.0.0", "::", "[::]":
		return "127.0.0.1"
	}
	return strings.Trim(address, "[]")
}

// detectProxies 逐个探测候选端口是否是 HTTP / SOCKS5 代理。
// 验证方式：让候选代理连回本机一个临时端口并转发随机口令，收到口令才算确认，不依赖外网。
func detectProxies(listeners []Listener, timeout time.Duration) []DetectedProxy {
	candidates := proxyCandidates(listeners)
	if len(candidates) == 0 {
		return []DetectedProxy{}
	}
	verifier, err := newTunnelVerifier()
	if err != nil {
		return []DetectedProxy{}
	}
	defer verifier.Close()

	results := make([]DetectedProxy, len(candidates))
	found := make([]bool, len(candidates))
	semaphore := make(chan struct{}, probeConcurrency)
	var waitGroup sync.WaitGroup
	for index, candidate := range candidates {
		waitGroup.Add(1)
		semaphore <- struct{}{}
		go func(index int, candidate Listener) {
			defer waitGroup.Done()
			defer func() { <-semaphore }()
			address := net.JoinHostPort(candidate.Address, strconv.Itoa(candidate.Port))
			var httpOk, httpAuth, socksOk, socksAuth bool
			var inner sync.WaitGroup
			inner.Add(2)
			go func() { defer inner.Done(); httpOk, httpAuth = probeHttpProxy(address, verifier, timeout) }()
			go func() { defer inner.Done(); socksOk, socksAuth = probeSocksProxy(address, verifier, timeout) }()
			inner.Wait()
			if !httpOk && !socksOk {
				return
			}
			detected := DetectedProxy{
				Host: candidate.Address, Port: candidate.Port, Process: candidate.Process, Pid: candidate.Pid,
				Http: httpOk, HttpAuth: httpAuth, Socks: socksOk, SocksAuth: socksAuth,
			}
			hostPort := net.JoinHostPort(candidate.Address, strconv.Itoa(candidate.Port))
			if httpOk {
				detected.Server = hostPort
			} else {
				detected.Server = "socks5://" + hostPort
			}
			results[index] = detected
			found[index] = true
		}(index, candidate)
	}
	waitGroup.Wait()

	detected := []DetectedProxy{}
	for index := range results {
		if found[index] {
			detected = append(detected, results[index])
		}
	}
	// 能当 HTTP 代理用的排在前面：系统代理、git、npm 都能直接用。
	sort.SliceStable(detected, func(first, second int) bool {
		return detected[first].Http && !detected[second].Http
	})
	return detected
}

// tunnelVerifier 是一个临时监听端口，用来确认候选代理确实把连接转发了过来。
type tunnelVerifier struct {
	listener net.Listener
	mutex    sync.Mutex
	pending  map[string]chan struct{}
}

const tunnelTokenLength = 16

func newTunnelVerifier() (*tunnelVerifier, error) {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return nil, err
	}
	verifier := &tunnelVerifier{listener: listener, pending: map[string]chan struct{}{}}
	go verifier.serve()
	return verifier, nil
}

func (verifier *tunnelVerifier) serve() {
	for {
		connection, err := verifier.listener.Accept()
		if err != nil {
			return
		}
		go func() {
			defer connection.Close()
			_ = connection.SetReadDeadline(time.Now().Add(5 * time.Second))
			buffer := make([]byte, tunnelTokenLength)
			if _, err := io.ReadFull(connection, buffer); err != nil {
				return
			}
			verifier.mutex.Lock()
			arrived := verifier.pending[string(buffer)]
			delete(verifier.pending, string(buffer))
			verifier.mutex.Unlock()
			if arrived != nil {
				close(arrived)
			}
		}()
	}
}

func (verifier *tunnelVerifier) port() int {
	return verifier.listener.Addr().(*net.TCPAddr).Port
}

func (verifier *tunnelVerifier) expect() (string, chan struct{}) {
	buffer := make([]byte, tunnelTokenLength/2)
	_, _ = rand.Read(buffer)
	token := hex.EncodeToString(buffer)
	arrived := make(chan struct{})
	verifier.mutex.Lock()
	verifier.pending[token] = arrived
	verifier.mutex.Unlock()
	return token, arrived
}

func (verifier *tunnelVerifier) forget(token string) {
	verifier.mutex.Lock()
	delete(verifier.pending, token)
	verifier.mutex.Unlock()
}

func (verifier *tunnelVerifier) Close() {
	_ = verifier.listener.Close()
}

// waitTunnel 通过已建立的隧道发出口令，等临时端口收到。
func (verifier *tunnelVerifier) waitTunnel(connection net.Conn, deadline time.Time) bool {
	token, arrived := verifier.expect()
	if _, err := connection.Write([]byte(token)); err != nil {
		verifier.forget(token)
		return false
	}
	select {
	case <-arrived:
		return true
	case <-time.After(time.Until(deadline)):
		verifier.forget(token)
		return false
	}
}

func probeHttpProxy(address string, verifier *tunnelVerifier, timeout time.Duration) (isProxy, needsAuth bool) {
	connection, err := net.DialTimeout("tcp", address, timeout)
	if err != nil {
		return false, false
	}
	defer connection.Close()
	deadline := time.Now().Add(timeout)
	_ = connection.SetDeadline(deadline)
	target := net.JoinHostPort("127.0.0.1", strconv.Itoa(verifier.port()))
	if _, err := fmt.Fprintf(connection, "CONNECT %s HTTP/1.1\r\nHost: %s\r\nUser-Agent: ProxySwitch\r\n\r\n", target, target); err != nil {
		return false, false
	}
	response, err := http.ReadResponse(bufio.NewReader(connection), &http.Request{Method: http.MethodConnect})
	if err != nil {
		return false, false
	}
	switch {
	case response.StatusCode == http.StatusOK:
		return verifier.waitTunnel(connection, deadline), false
	case response.StatusCode == http.StatusProxyAuthRequired:
		return true, true
	case response.Header.Get("Proxy-Agent") != "" || response.Header.Get("Via") != "":
		// 代理自己的规则拒绝连回本机时会返回错误状态，但响应头能看出它是代理。
		return true, false
	}
	return false, false
}

func probeSocksProxy(address string, verifier *tunnelVerifier, timeout time.Duration) (isProxy, needsAuth bool) {
	connection, err := net.DialTimeout("tcp", address, timeout)
	if err != nil {
		return false, false
	}
	defer connection.Close()
	deadline := time.Now().Add(timeout)
	_ = connection.SetDeadline(deadline)
	if _, err := connection.Write([]byte{5, 1, 0}); err != nil {
		return false, false
	}
	reply := make([]byte, 2)
	if _, err := io.ReadFull(connection, reply); err != nil || reply[0] != 5 {
		return false, false
	}
	switch reply[1] {
	case 0:
	case 2, 0xFF:
		return true, true
	default:
		return false, false
	}
	port := verifier.port()
	request := []byte{5, 1, 0, 1, 127, 0, 0, 1, byte(port >> 8), byte(port)}
	if _, err := connection.Write(request); err != nil {
		return true, false
	}
	header := make([]byte, 4)
	if _, err := io.ReadFull(connection, header); err != nil || header[1] != 0 {
		// 握手已符合 SOCKS5，只是代理规则不允许连回本机。
		return true, false
	}
	addressLength := 0
	switch header[3] {
	case 1:
		addressLength = 4
	case 4:
		addressLength = 16
	case 3:
		length := make([]byte, 1)
		if _, err := io.ReadFull(connection, length); err != nil {
			return true, false
		}
		addressLength = int(length[0])
	}
	if _, err := io.ReadFull(connection, make([]byte, addressLength+2)); err != nil {
		return true, false
	}
	verifier.waitTunnel(connection, deadline)
	return true, false
}

// commonPortListeners 逐个试探常见代理端口，作为拿不到系统监听列表时的退路。
func commonPortListeners() []Listener {
	var listeners []Listener
	var mutex sync.Mutex
	var waitGroup sync.WaitGroup
	for _, port := range commonProxyPorts {
		waitGroup.Add(1)
		go func(port int) {
			defer waitGroup.Done()
			connection, err := net.DialTimeout("tcp", net.JoinHostPort("127.0.0.1", strconv.Itoa(port)), 300*time.Millisecond)
			if err != nil {
				return
			}
			connection.Close()
			mutex.Lock()
			listeners = append(listeners, Listener{Address: "127.0.0.1", Port: port, Pid: 1})
			mutex.Unlock()
		}(port)
	}
	waitGroup.Wait()
	return listeners
}

// TestResult 是一次连通性测试的结果。Millis 为 0 表示没有测出延迟（例如只检查了 PAC 脚本能否下载）；
// Route 是 PAC 为测速地址选择的代理，pacRouteDirect 表示直连。
type TestResult struct {
	Ok      bool   `json:"ok"`
	Millis  int64  `json:"millis"`
	Status  int    `json:"status,omitempty"`
	Message string `json:"message"`
	Route   string `json:"route,omitempty"`
}

// testProfileConnection 测试一套配置：填了代理服务器时经它测速，只有 PAC 时按 PAC 为测速地址选择的代理测速。
func testProfileConnection(server, pac, testUrl string, timeout time.Duration) TestResult {
	if server != "" {
		return testProxyServer(server, testUrl, timeout)
	}
	return testPacProfile(pac, testUrl, timeout)
}

// testProxyServer 先确认能连上代理端口，再经代理请求测速地址，记录请求耗时。
func testProxyServer(server, testUrl string, timeout time.Duration) TestResult {
	target, err := url.Parse(testUrl)
	if err != nil || target.Host == "" {
		return TestResult{Message: "测速地址格式不对：" + testUrl}
	}
	proxyUrl, err := proxyUrlForTarget(server, target.Scheme)
	if err != nil {
		return TestResult{Message: err.Error()}
	}
	// 能否连上代理端口很快就能知道，不用等满整个测速时间。
	connection, err := net.DialTimeout("tcp", proxyUrl.Host, min(timeout, 3*time.Second))
	if err != nil {
		return TestResult{Message: "连不上代理 " + proxyUrl.Host + "：" + friendlyNetError(err)}
	}
	connection.Close()
	return measureRequest(proxyUrl, testUrl, timeout)
}

// measureRequest 经 proxyUrl 请求测速地址并记录耗时，proxyUrl 为 nil 时直接访问。
func measureRequest(proxyUrl *url.URL, testUrl string, timeout time.Duration) TestResult {
	transport := &http.Transport{
		DialContext:           (&net.Dialer{Timeout: timeout}).DialContext,
		TLSHandshakeTimeout:   timeout,
		ResponseHeaderTimeout: timeout,
		DisableKeepAlives:     true,
	}
	if proxyUrl != nil {
		transport.Proxy = http.ProxyURL(proxyUrl)
	}
	defer transport.CloseIdleConnections()
	client := &http.Client{
		Transport: transport,
		Timeout:   timeout,
		CheckRedirect: func(*http.Request, []*http.Request) error {
			return http.ErrUseLastResponse
		},
	}
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, testUrl, nil)
	if err != nil {
		return TestResult{Message: "测速地址格式不对：" + testUrl}
	}
	request.Header.Set("User-Agent", appName+"/"+appVersion)
	started := time.Now()
	response, err := client.Do(request)
	elapsed := time.Since(started).Milliseconds()
	if err != nil {
		return TestResult{Millis: elapsed, Message: friendlyRequestError(err, timeout)}
	}
	_, _ = io.Copy(io.Discard, io.LimitReader(response.Body, 64<<10))
	response.Body.Close()
	result := TestResult{Millis: elapsed, Status: response.StatusCode}
	switch {
	case response.StatusCode == http.StatusProxyAuthRequired:
		result.Message = "代理要求用户名和密码（HTTP 407）"
	case response.StatusCode < 400 && proxyUrl != nil:
		result.Ok = true
		result.Message = fmt.Sprintf("经代理访问成功，HTTP %d", response.StatusCode)
	case response.StatusCode < 400:
		result.Ok = true
		result.Message = fmt.Sprintf("直接访问成功，HTTP %d", response.StatusCode)
	case proxyUrl != nil:
		result.Message = fmt.Sprintf("代理有响应，但测速地址返回 HTTP %d", response.StatusCode)
	default:
		result.Message = fmt.Sprintf("测速地址返回 HTTP %d", response.StatusCode)
	}
	return result
}

// errPacUnsupported 表示不能执行 PAC 脚本（非 Windows 平台，或 file:// 地址），这时 PAC 测速只检查脚本能否下载。
var errPacUnsupported = errors.New("不能执行 PAC 脚本")

const pacRouteDirect = "DIRECT"

// testPacProfile 先确认 PAC 脚本能下载，再按脚本为测速地址选择的代理（或直连）实际访问一次。
func testPacProfile(pacUrl, testUrl string, timeout time.Duration) TestResult {
	script, download := downloadPacScript(pacUrl, timeout)
	if !download.Ok {
		return download
	}
	route, err := "", errPacUnsupported
	if !strings.HasPrefix(strings.ToLower(pacUrl), "file:") {
		route, err = pacProxyForUrl(pacUrl, testUrl, timeout)
	}
	if errors.Is(err, errPacUnsupported) {
		return download
	}
	if err != nil {
		return TestResult{Message: "PAC 脚本能下载，但执行失败：" + err.Error()}
	}
	// Windows 执行 PAC 时会忽略 SOCKS 项，只剩直连；这种脚本测不出真实的去向，只报告能否下载。
	if route == "" && strings.Contains(strings.ToUpper(script), "SOCKS") {
		download.Message += "。脚本里用了 SOCKS 代理，Windows 执行 PAC 时不支持，测不出延迟"
		return download
	}
	if route == "" {
		result := measureRequest(nil, testUrl, timeout)
		result.Route = pacRouteDirect
		result.Message = "PAC 选择直连：" + result.Message
		return result
	}
	result := testProxyServer(route, testUrl, timeout)
	result.Route = route
	result.Message = "PAC 选择 " + route + "：" + result.Message
	return result
}

// downloadPacScript 下载 PAC 脚本并检查内容，返回脚本和检查结果。
func downloadPacScript(pacUrl string, timeout time.Duration) (string, TestResult) {
	var body []byte
	parsed, err := url.Parse(pacUrl)
	if err != nil {
		return "", TestResult{Message: "PAC 地址格式不对"}
	}
	if parsed.Scheme == "file" {
		path := parsed.Path
		if parsed.Host != "" {
			path = "//" + parsed.Host + parsed.Path
		}
		body, err = os.ReadFile(strings.TrimPrefix(path, "/"))
		if err != nil {
			return "", TestResult{Message: "读取 PAC 文件失败：" + err.Error()}
		}
	} else {
		client := &http.Client{Timeout: timeout, Transport: &http.Transport{Proxy: nil, DisableKeepAlives: true}}
		response, err := client.Get(pacUrl)
		if err != nil {
			return "", TestResult{Message: "下载 PAC 失败：" + friendlyRequestError(err, timeout)}
		}
		body, _ = io.ReadAll(io.LimitReader(response.Body, 2<<20))
		response.Body.Close()
		if response.StatusCode != http.StatusOK {
			return "", TestResult{Status: response.StatusCode, Message: fmt.Sprintf("PAC 地址返回 HTTP %d", response.StatusCode)}
		}
	}
	script := string(body)
	if !strings.Contains(script, "FindProxyForURL") {
		return "", TestResult{Message: "能下载，但内容不像 PAC 脚本（没有 FindProxyForURL）"}
	}
	return script, TestResult{Ok: true, Message: fmt.Sprintf("PAC 脚本可以下载（%.1f KB）", float64(len(body))/1024)}
}

// checkProxyReachable 只检查能否连上代理端口，用于开启后的持续检查。
func checkProxyReachable(endpoint string, timeout time.Duration) error {
	connection, err := net.DialTimeout("tcp", endpoint, timeout)
	if err != nil {
		return err
	}
	return connection.Close()
}

// windowsConnectionRefused 是 Windows 上 WSAECONNREFUSED 的错误码。
const windowsConnectionRefused = 10061

func isConnectionRefused(err error) bool {
	var errno syscall.Errno
	if errors.As(err, &errno) {
		return errno == syscall.ECONNREFUSED || errno == windowsConnectionRefused
	}
	return false
}

func friendlyNetError(err error) string {
	var netError net.Error
	var dnsError *net.DNSError
	switch {
	case isConnectionRefused(err):
		return "连接被拒绝，这个端口没有程序在监听（代理软件可能没启动）"
	case errors.As(err, &dnsError):
		return "域名解析失败"
	case errors.As(err, &netError) && netError.Timeout():
		return "连接超时"
	}
	return err.Error()
}

func friendlyRequestError(err error, timeout time.Duration) string {
	message := err.Error()
	var netError net.Error
	switch {
	case strings.Contains(message, "Proxy Authentication Required"):
		return "代理要求用户名和密码（HTTP 407）"
	case errors.As(err, &netError) && netError.Timeout(), errors.Is(err, context.DeadlineExceeded):
		return fmt.Sprintf("超时：%d 秒内没有完成请求", int(timeout.Seconds()))
	case isConnectionRefused(err):
		return "连接被拒绝"
	case strings.Contains(message, "certificate"):
		return "证书校验失败（代理可能在拦截 HTTPS）"
	case strings.Contains(message, "Bad Gateway"), strings.Contains(message, "Service Unavailable"):
		return "代理无法连到测速地址（" + lastErrorPart(message) + "）"
	case strings.Contains(message, "socks connect"):
		return "SOCKS5 代理无法连到测速地址（" + lastErrorPart(message) + "）"
	}
	var dnsError *net.DNSError
	if errors.As(err, &dnsError) {
		return "域名解析失败：" + dnsError.Name
	}
	return lastErrorPart(message)
}

func lastErrorPart(message string) string {
	if index := strings.LastIndex(message, ": "); index >= 0 {
		return message[index+2:]
	}
	return message
}
