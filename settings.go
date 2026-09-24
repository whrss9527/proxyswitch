package main

import (
	"crypto/rand"
	"crypto/subtle"
	"embed"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"log/slog"
	"net"
	"net/http"
	"net/url"
	"path"
	"strconv"
	"strings"
	"sync"
	"time"
)

// 设置界面：程序内起一个只监听 127.0.0.1 随机端口的 HTTP 服务，页面在独立窗口或浏览器里打开。
// 数据接口都要求请求头 X-Token（每次启动随机生成），并校验 Host 与 Sec-Fetch-Site，防止其他网页跨站调用。

// 页面是 web 目录下的 settings.html，样式和脚本经 /assets/ 提供。
//
//go:embed web
var webFiles embed.FS

// SettingsBackend 是设置界面用到的程序能力，Windows 上由 App 实现，开发和测试时用 devBackend。
type SettingsBackend interface {
	State() SettingsState
	SaveConfig(config *Config) error
	TurnOn() error
	TurnOff() error
	UseProfile(name string) error
	ApplyAutoSwitch() error
	SetAutostart(enabled bool) error
	Listeners() []Listener
	Diagnostics() Diagnostics
	LogTail(maxLines int) string
	ClearAllProxies() error
	OpenConfigDir() error
	OpenConfigFile() error
	OpenLogFile() error
	OpenUrl(address string) error
	ActiveProxyUrl() string
}

type SettingsState struct {
	Version     string           `json:"version"`
	Platform    string           `json:"platform"`
	Paths       PathsInfo        `json:"paths"`
	Config      *Config          `json:"config"`
	ConfigError string           `json:"config_error,omitempty"`
	Status      StatusInfo       `json:"status"`
	Autostart   bool             `json:"autostart"`
	Hotkeys     HotkeyStatus     `json:"hotkeys"`
	Network     NetworkInfo      `json:"network"`
	AutoSwitch  AutoSwitchStatus `json:"auto_switch"`
	Accent      string           `json:"accent"`
	Defaults    DefaultsInfo     `json:"defaults"`
	Targets     []TargetInfo     `json:"targets"`
	Palette     []string         `json:"palette"`
	Navigate    NavigateInfo     `json:"navigate"`
}

// NavigateInfo 是让已打开的设置页切换页面的请求，Serial 每次加一，页面发现变化时切到 Page。
type NavigateInfo struct {
	Page   string `json:"page"`
	Serial int    `json:"serial"`
}

type PathsInfo struct {
	Dir      string `json:"dir"`
	Config   string `json:"config"`
	Log      string `json:"log"`
	Portable bool   `json:"portable"`
}

// StatusInfo.State：off 已关闭 / on 由本程序开启 / external 系统代理被其他程序开启。
// Terminal 是开启时在当前终端使用代理的命令；ExternalProfile 是 external 时由系统代理转成的配置，页面可以一键保存。
type StatusInfo struct {
	State           string            `json:"state"`
	Profile         string            `json:"profile"`
	External        string            `json:"external,omitempty"`
	Applied         []string          `json:"applied"`
	Health          string            `json:"health"`
	HealthMessage   string            `json:"health_message,omitempty"`
	Terminal        []TerminalCommand `json:"terminal,omitempty"`
	ExternalProfile *Profile          `json:"external_profile,omitempty"`
}

type HotkeyStatus struct {
	Toggle        string `json:"toggle"`
	ToggleError   string `json:"toggle_error,omitempty"`
	ProfilesError string `json:"profiles_error,omitempty"`
}

// AutoSwitchStatus：Network 当前网络的描述，MatchIndex 当前网络命中的规则（-1 表示走默认动作），Result / Time 最近一次自动切换的结果。
type AutoSwitchStatus struct {
	Network    string `json:"network"`
	MatchIndex int    `json:"match_index"`
	Result     string `json:"result,omitempty"`
	Time       string `json:"time,omitempty"`
}

type DefaultsInfo struct {
	Bypass  string `json:"bypass"`
	NoProxy string `json:"no_proxy"`
	Hotkey  string `json:"hotkey"`
	TestUrl string `json:"test_url"`
}

type TargetInfo struct {
	Id          string `json:"id"`
	Label       string `json:"label"`
	Description string `json:"description"`
	Available   bool   `json:"available"`
	Note        string `json:"note,omitempty"`
}

type GitStatus struct {
	Available  bool   `json:"available"`
	Path       string `json:"path,omitempty"`
	HttpProxy  string `json:"http_proxy"`
	HttpsProxy string `json:"https_proxy"`
}

type Diagnostics struct {
	System        SystemProxyState  `json:"system"`
	SystemSource  string            `json:"system_source"`
	SystemError   string            `json:"system_error,omitempty"`
	Connections   []string          `json:"connections"`
	MachinePolicy bool              `json:"machine_policy"`
	Env           map[string]string `json:"env"`
	Git           GitStatus         `json:"git"`
	Npm           map[string]string `json:"npm"`
	NpmrcPath     string            `json:"npmrc_path"`
}

func targetInfos(gitAvailable bool) []TargetInfo {
	gitNote := ""
	if !gitAvailable {
		gitNote = "没有找到 git（不在 PATH 里），开启时会跳过"
	}
	return []TargetInfo{
		{targetSystem, "系统代理", "浏览器和大多数软件都走它", true, ""},
		{targetEnv, "环境变量", "curl、go、pip 等命令行工具，新开的终端生效", true, ""},
		{targetGit, "git", "git clone、pull 等（全局 http.proxy）", gitAvailable, gitNote},
		{targetNpm, "npm / pnpm", "写入用户目录的 .npmrc", true, ""},
	}
}

func defaultsInfo() DefaultsInfo {
	return DefaultsInfo{Bypass: defaultBypass, NoProxy: defaultNoProxy, Hotkey: "Ctrl+Alt+P", TestUrl: defaultTestUrl}
}

type SettingsServer struct {
	backend SettingsBackend
	extra   func(mux *http.ServeMux)
	// 页面文件，默认是编译进程序的 web 目录；开发时可以换成磁盘上的目录，改完刷新即可看到。
	files fs.FS

	mutex    sync.Mutex
	token    string
	listener net.Listener
	server   *http.Server
	address  string
	navigate NavigateInfo
}

func newSettingsServer(backend SettingsBackend) *SettingsServer {
	files, _ := fs.Sub(webFiles, "web")
	return &SettingsServer{backend: backend, files: files}
}

// Start 启动服务（已启动则直接返回），返回带 token 的页面地址。
func (settings *SettingsServer) Start() (string, error) {
	settings.mutex.Lock()
	defer settings.mutex.Unlock()
	if settings.listener != nil {
		return settings.address, nil
	}
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return "", fmt.Errorf("无法监听本地端口：%v", err)
	}
	buffer := make([]byte, 16)
	if _, err := rand.Read(buffer); err != nil {
		listener.Close()
		return "", err
	}
	settings.token = hex.EncodeToString(buffer)
	settings.listener = listener
	port := listener.Addr().(*net.TCPAddr).Port
	settings.address = fmt.Sprintf("http://127.0.0.1:%d/?token=%s", port, settings.token)

	mux := http.NewServeMux()
	mux.HandleFunc("GET /{$}", settings.handlePage)
	mux.HandleFunc("GET /assets/{name}", settings.handleAsset)
	mux.HandleFunc("GET /api/state", settings.handleState)
	mux.HandleFunc("PUT /api/config", settings.handleConfig)
	mux.HandleFunc("POST /api/validate", settings.handleValidate)
	mux.HandleFunc("POST /api/preview", settings.handlePreview)
	mux.HandleFunc("POST /api/on", settings.handleTurnOn)
	mux.HandleFunc("POST /api/off", settings.handleTurnOff)
	mux.HandleFunc("POST /api/use", settings.handleUse)
	mux.HandleFunc("POST /api/auto-switch/apply", settings.handleApplyAutoSwitch)
	mux.HandleFunc("POST /api/autostart", settings.handleAutostart)
	mux.HandleFunc("POST /api/test", settings.handleTest)
	mux.HandleFunc("GET /api/detect", settings.handleDetect)
	mux.HandleFunc("GET /api/diagnostics", settings.handleDiagnostics)
	mux.HandleFunc("GET /api/log", settings.handleLog)
	mux.HandleFunc("POST /api/clear-all", settings.handleClearAll)
	mux.HandleFunc("POST /api/open/config-dir", settings.handleOpenConfigDir)
	mux.HandleFunc("POST /api/open/config-file", settings.handleOpenConfigFile)
	mux.HandleFunc("POST /api/open/log", settings.handleOpenLog)
	mux.HandleFunc("POST /api/open-url", settings.handleOpenUrl)
	mux.HandleFunc("GET /api/update", settings.handleUpdate)
	if settings.extra != nil {
		settings.extra(mux)
	}
	settings.server = &http.Server{Handler: settings.guard(mux), ReadHeaderTimeout: 10 * time.Second}
	go func() {
		if err := settings.server.Serve(listener); err != nil && !errors.Is(err, http.ErrServerClosed) {
			slog.Error("设置服务退出", "err", err)
		}
	}()
	slog.Info("设置服务已启动", "port", port)
	return settings.address, nil
}

// ShowPage 请求已经打开的设置页切到 page，页面下次同步状态时切换。
func (settings *SettingsServer) ShowPage(page string) {
	settings.mutex.Lock()
	defer settings.mutex.Unlock()
	settings.navigate = NavigateInfo{Page: page, Serial: settings.navigate.Serial + 1}
}

func (settings *SettingsServer) state() SettingsState {
	state := settings.backend.State()
	settings.mutex.Lock()
	state.Navigate = settings.navigate
	settings.mutex.Unlock()
	return state
}

func (settings *SettingsServer) Stop() {
	settings.mutex.Lock()
	defer settings.mutex.Unlock()
	if settings.server != nil {
		_ = settings.server.Close()
		settings.server = nil
		settings.listener = nil
		settings.address = ""
	}
}

func (settings *SettingsServer) guard(next http.Handler) http.Handler {
	return http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		host := request.Host
		if hostname, _, err := net.SplitHostPort(host); err == nil {
			host = hostname
		}
		if host != "127.0.0.1" && host != "localhost" {
			http.Error(writer, "forbidden", http.StatusForbidden)
			return
		}
		if site := request.Header.Get("Sec-Fetch-Site"); site != "" && site != "same-origin" && site != "none" {
			http.Error(writer, "forbidden", http.StatusForbidden)
			return
		}
		writer.Header().Set("Cache-Control", "no-store")
		writer.Header().Set("X-Content-Type-Options", "nosniff")
		writer.Header().Set("Referrer-Policy", "no-referrer")
		// 页面和样式脚本本身不含数据，不要求 token（刷新页面时地址里已经没有 token）；数据接口都要求。
		if request.URL.Path == "/" || strings.HasPrefix(request.URL.Path, "/assets/") {
			next.ServeHTTP(writer, request)
			return
		}
		token := request.Header.Get("X-Token")
		settings.mutex.Lock()
		expected := settings.token
		settings.mutex.Unlock()
		if token == "" || subtle.ConstantTimeCompare([]byte(token), []byte(expected)) != 1 {
			http.Error(writer, "forbidden", http.StatusForbidden)
			return
		}
		next.ServeHTTP(writer, request)
	})
}

func (settings *SettingsServer) handlePage(writer http.ResponseWriter, request *http.Request) {
	page, err := fs.ReadFile(settings.files, "settings.html")
	if err != nil {
		http.Error(writer, err.Error(), http.StatusInternalServerError)
		return
	}
	writer.Header().Set("Content-Type", "text/html; charset=utf-8")
	writer.Header().Set("Content-Security-Policy", "default-src 'none'; script-src 'self'; style-src 'self' 'unsafe-inline'; img-src 'self' data:; connect-src 'self'; form-action 'none'; base-uri 'none'; frame-ancestors 'none'")
	_, _ = writer.Write(page)
}

var assetTypes = map[string]string{
	".css": "text/css; charset=utf-8",
	".js":  "text/javascript; charset=utf-8",
	".svg": "image/svg+xml",
}

// handleAsset 提供页面的样式和脚本。它们不含任何数据，所以不要求 token。
func (settings *SettingsServer) handleAsset(writer http.ResponseWriter, request *http.Request) {
	name := request.PathValue("name")
	contentType, allowed := assetTypes[path.Ext(name)]
	data, err := fs.ReadFile(settings.files, name)
	if !allowed || err != nil {
		http.NotFound(writer, request)
		return
	}
	writer.Header().Set("Content-Type", contentType)
	_, _ = writer.Write(data)
}

func writeJson(writer http.ResponseWriter, status int, value any) {
	writer.Header().Set("Content-Type", "application/json; charset=utf-8")
	writer.WriteHeader(status)
	_ = json.NewEncoder(writer).Encode(value)
}

func writeError(writer http.ResponseWriter, status int, message string) {
	writeJson(writer, status, map[string]string{"error": message})
}

func decodeJsonBody(writer http.ResponseWriter, request *http.Request, target any) bool {
	if err := json.NewDecoder(io.LimitReader(request.Body, 1<<20)).Decode(target); err != nil {
		writeError(writer, http.StatusBadRequest, "请求格式错误")
		return false
	}
	return true
}

// respondState 在操作成功后返回最新状态，失败时返回错误和最新状态，页面据此刷新。
func (settings *SettingsServer) respondState(writer http.ResponseWriter, request *http.Request, err error) {
	if err != nil {
		slog.WarnContext(request.Context(), "设置页操作失败", "path", request.URL.Path, "err", err)
		writeJson(writer, http.StatusConflict, map[string]any{"error": err.Error(), "state": settings.state()})
		return
	}
	writeJson(writer, http.StatusOK, settings.state())
}

func (settings *SettingsServer) handleState(writer http.ResponseWriter, request *http.Request) {
	writeJson(writer, http.StatusOK, settings.state())
}

func (settings *SettingsServer) handleConfig(writer http.ResponseWriter, request *http.Request) {
	body, err := io.ReadAll(io.LimitReader(request.Body, 1<<20))
	if err != nil {
		writeError(writer, http.StatusBadRequest, "读取请求失败")
		return
	}
	// 与手写配置文件走同一套解析和校验，保证页面保存的内容和文件一致。
	config, err := parseConfig(string(body))
	if err != nil {
		writeError(writer, http.StatusBadRequest, err.Error())
		return
	}
	settings.respondState(writer, request, settings.backend.SaveConfig(config))
}

func (settings *SettingsServer) handleValidate(writer http.ResponseWriter, request *http.Request) {
	body, err := io.ReadAll(io.LimitReader(request.Body, 1<<20))
	if err != nil {
		writeError(writer, http.StatusBadRequest, "读取请求失败")
		return
	}
	config, err := parseConfig(string(body))
	if err != nil {
		writeError(writer, http.StatusBadRequest, err.Error())
		return
	}
	writeJson(writer, http.StatusOK, map[string]any{"config": config})
}

func (settings *SettingsServer) handlePreview(writer http.ResponseWriter, request *http.Request) {
	var profile Profile
	if !decodeJsonBody(writer, request, &profile) {
		return
	}
	writeJson(writer, http.StatusOK, map[string]any{"lines": previewChanges(profile)})
}

func (settings *SettingsServer) handleTurnOn(writer http.ResponseWriter, request *http.Request) {
	settings.respondState(writer, request, settings.backend.TurnOn())
}

func (settings *SettingsServer) handleTurnOff(writer http.ResponseWriter, request *http.Request) {
	settings.respondState(writer, request, settings.backend.TurnOff())
}

func (settings *SettingsServer) handleUse(writer http.ResponseWriter, request *http.Request) {
	var body struct {
		Name string `json:"name"`
	}
	if !decodeJsonBody(writer, request, &body) {
		return
	}
	settings.respondState(writer, request, settings.backend.UseProfile(body.Name))
}

func (settings *SettingsServer) handleApplyAutoSwitch(writer http.ResponseWriter, request *http.Request) {
	settings.respondState(writer, request, settings.backend.ApplyAutoSwitch())
}

func (settings *SettingsServer) handleAutostart(writer http.ResponseWriter, request *http.Request) {
	var body struct {
		Enabled bool `json:"enabled"`
	}
	if !decodeJsonBody(writer, request, &body) {
		return
	}
	settings.respondState(writer, request, settings.backend.SetAutostart(body.Enabled))
}

const (
	proxyTestTimeout = 8 * time.Second
	detectTimeout    = 1500 * time.Millisecond
)

// handleTest 测试编辑中的地址（server / pac），或按名字测试已保存的配置。
func (settings *SettingsServer) handleTest(writer http.ResponseWriter, request *http.Request) {
	var body struct {
		Server  string `json:"server"`
		Pac     string `json:"pac"`
		Profile string `json:"profile"`
	}
	if !decodeJsonBody(writer, request, &body) {
		return
	}
	state := settings.backend.State()
	testUrl := defaultTestUrl
	if state.Config != nil {
		testUrl = state.Config.TestUrl
		if profile := state.Config.FindProfile(body.Profile); profile != nil {
			body.Server, body.Pac = profile.Server, profile.Pac
		}
	}
	server, pac := strings.TrimSpace(body.Server), strings.TrimSpace(body.Pac)
	if server == "" && pac == "" {
		writeError(writer, http.StatusBadRequest, "没有可测试的地址")
		return
	}
	if server != "" {
		if err := validateServer(server); err != nil {
			writeJson(writer, http.StatusOK, TestResult{Message: "地址格式不对：" + err.Error()})
			return
		}
	}
	writeJson(writer, http.StatusOK, testProfileConnection(server, pac, testUrl, proxyTestTimeout))
}

func (settings *SettingsServer) handleDetect(writer http.ResponseWriter, request *http.Request) {
	started := time.Now()
	proxies := detectProxies(settings.backend.Listeners(), detectTimeout)
	slog.InfoContext(request.Context(), "检测本机代理", "found", len(proxies), "elapsed_ms", time.Since(started).Milliseconds())
	writeJson(writer, http.StatusOK, map[string]any{"proxies": proxies})
}

func (settings *SettingsServer) handleDiagnostics(writer http.ResponseWriter, request *http.Request) {
	writeJson(writer, http.StatusOK, settings.backend.Diagnostics())
}

func (settings *SettingsServer) handleLog(writer http.ResponseWriter, request *http.Request) {
	lines, err := strconv.Atoi(request.URL.Query().Get("lines"))
	if err != nil || lines <= 0 || lines > 2000 {
		lines = 300
	}
	writeJson(writer, http.StatusOK, map[string]string{"text": settings.backend.LogTail(lines)})
}

func (settings *SettingsServer) handleClearAll(writer http.ResponseWriter, request *http.Request) {
	settings.respondState(writer, request, settings.backend.ClearAllProxies())
}

func (settings *SettingsServer) handleOpenConfigDir(writer http.ResponseWriter, request *http.Request) {
	settings.respondOpened(writer, settings.backend.OpenConfigDir())
}

func (settings *SettingsServer) handleOpenConfigFile(writer http.ResponseWriter, request *http.Request) {
	settings.respondOpened(writer, settings.backend.OpenConfigFile())
}

func (settings *SettingsServer) handleOpenLog(writer http.ResponseWriter, request *http.Request) {
	settings.respondOpened(writer, settings.backend.OpenLogFile())
}

func (settings *SettingsServer) respondOpened(writer http.ResponseWriter, err error) {
	if err != nil {
		writeError(writer, http.StatusInternalServerError, err.Error())
		return
	}
	writeJson(writer, http.StatusOK, map[string]bool{"ok": true})
}

// 除了本项目的页面，设置页还会打开 Windows 的“位置”隐私设置（读取 Wi-Fi 名称需要）。
const locationSettingsUrl = "ms-settings:privacy-location"

// handleOpenUrl 只允许打开固定的几个地址，避免被利用来启动任意程序。
func (settings *SettingsServer) handleOpenUrl(writer http.ResponseWriter, request *http.Request) {
	var body struct {
		Url string `json:"url"`
	}
	if !decodeJsonBody(writer, request, &body) {
		return
	}
	if body.Url != repositoryUrl && !strings.HasPrefix(body.Url, repositoryUrl+"/") && body.Url != locationSettingsUrl {
		writeError(writer, http.StatusBadRequest, "不允许打开这个地址")
		return
	}
	settings.respondOpened(writer, settings.backend.OpenUrl(body.Url))
}

type UpdateInfo struct {
	Current   string `json:"current"`
	Latest    string `json:"latest"`
	Newer     bool   `json:"newer"`
	Url       string `json:"url"`
	Published string `json:"published"`
	Notes     string `json:"notes"`
}

func (settings *SettingsServer) handleUpdate(writer http.ResponseWriter, request *http.Request) {
	info, err := checkLatestRelease(settings.backend.ActiveProxyUrl())
	if err != nil {
		slog.WarnContext(request.Context(), "检查更新失败", "err", err)
		writeError(writer, http.StatusBadGateway, "检查更新失败："+err.Error())
		return
	}
	writeJson(writer, http.StatusOK, info)
}

const releaseApiUrl = "https://api.github.com/repos/whrss9527/proxyswitch/releases/latest"

func checkLatestRelease(proxyUrl string) (UpdateInfo, error) {
	transport := &http.Transport{Proxy: nil, DisableKeepAlives: true}
	if proxyUrl != "" {
		if parsed, err := url.Parse(proxyUrl); err == nil {
			transport.Proxy = http.ProxyURL(parsed)
		}
	}
	client := &http.Client{Transport: transport, Timeout: 10 * time.Second}
	request, err := http.NewRequest(http.MethodGet, releaseApiUrl, nil)
	if err != nil {
		return UpdateInfo{}, err
	}
	request.Header.Set("Accept", "application/vnd.github+json")
	request.Header.Set("User-Agent", appName+"/"+appVersion)
	response, err := client.Do(request)
	if err != nil {
		return UpdateInfo{}, errors.New(friendlyRequestError(err, 10*time.Second))
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return UpdateInfo{}, fmt.Errorf("GitHub 返回 HTTP %d", response.StatusCode)
	}
	var release struct {
		TagName     string `json:"tag_name"`
		HtmlUrl     string `json:"html_url"`
		PublishedAt string `json:"published_at"`
		Body        string `json:"body"`
	}
	if err := json.NewDecoder(io.LimitReader(response.Body, 1<<20)).Decode(&release); err != nil {
		return UpdateInfo{}, err
	}
	latest := strings.TrimPrefix(release.TagName, "v")
	notes := release.Body
	if runes := []rune(notes); len(runes) > 1200 {
		notes = string(runes[:1200]) + "…"
	}
	return UpdateInfo{
		Current:   appVersion,
		Latest:    latest,
		Newer:     compareVersions(latest, appVersion) > 0,
		Url:       release.HtmlUrl,
		Published: release.PublishedAt,
		Notes:     notes,
	}, nil
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

// previewChanges 描述开启这套配置后会改动哪些设置，给编辑页的“将会进行的更改”用。
func previewChanges(profile Profile) []string {
	profile.Server = strings.TrimSpace(profile.Server)
	profile.Pac = strings.TrimSpace(profile.Pac)
	if profile.NoProxy == "" {
		profile.NoProxy = defaultNoProxy
	}
	if profile.Bypass == "" {
		profile.Bypass = defaultBypass
	}
	proxyUrl := serverToUrl(profile.Server)
	var lines []string
	for _, target := range profile.ApplyTo {
		switch target {
		case targetSystem:
			switch {
			case profile.Pac != "" && profile.Server != "":
				lines = append(lines, fmt.Sprintf("系统代理：使用 PAC 脚本 %s，同时设置代理服务器 %s", profile.Pac, serverToWinInet(profile.Server)))
			case profile.Pac != "":
				lines = append(lines, "系统代理：使用 PAC 脚本 "+profile.Pac)
			case profile.Server != "":
				lines = append(lines, fmt.Sprintf("系统代理：代理服务器 %s，不走代理的地址 %s", serverToWinInet(profile.Server), describeBypass(profile.Bypass)))
			}
		case targetEnv:
			if proxyUrl != "" {
				lines = append(lines, fmt.Sprintf("环境变量：HTTP_PROXY、HTTPS_PROXY = %s，NO_PROXY = %s", proxyUrl, profile.NoProxy))
			}
		case targetGit:
			if proxyUrl != "" {
				lines = append(lines, "git：全局 http.proxy、https.proxy = "+proxyUrl)
			}
		case targetNpm:
			if proxyUrl != "" {
				lines = append(lines, "npm：.npmrc 的 proxy、https-proxy = "+proxyUrl)
			}
		}
	}
	return lines
}

func describeBypass(bypass string) string {
	var labels []string
	count := 0
	hasLan := false
	for _, entry := range strings.Split(bypass, ";") {
		entry = strings.TrimSpace(entry)
		switch {
		case entry == "":
			continue
		case entry == "<local>":
			labels = append(labels, "本地名称")
		case entry == "localhost" || entry == "127.*":
			if !containsString(labels, "本机") {
				labels = append(labels, "本机")
			}
		case entry == "10.*" || entry == "192.168.*" || strings.HasPrefix(entry, "172.1") || strings.HasPrefix(entry, "172.2") || strings.HasPrefix(entry, "172.3"):
			if !hasLan {
				hasLan = true
				labels = append(labels, "局域网")
			}
		default:
			count++
		}
	}
	if count > 0 {
		labels = append(labels, fmt.Sprintf("另外 %d 项", count))
	}
	if len(labels) == 0 {
		return "无"
	}
	return strings.Join(labels, "、")
}
