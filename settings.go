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
	UpdatePaths() []string
	RememberUpdate(info UpdateInfo)
	InstallUpdate(progress func(received, total int64)) error
	SubscriptionNodes(profileId string) (CoreNodes, error)
	SelectNode(profileId, node string) (CoreNodes, error)
	TestNodes(profileId string) (CoreNodes, error)
	UpdateSubscription(profileId string) error
	CheckSubscription(address string) (SubscriptionCheck, error)
	UpdateRuleSet(address string) error
	UpdateAllRuleSets() (RuleSetsUpdate, error)
	SetMode(profileId, mode string) error
	SelectGroupNode(name, node string) error
	InstallCore() error
	SetShare(enabled bool) error
	ShareActivity() ShareActivity
	ClearShareHistory()
	Connections() ConnectionsView
	CloseConnection(id string) error
	ClearConnections()
	ResetTraffic()
	CheckExit(direct bool) error
	AllowShareFirewall() error
	StartDiagnose(address, perspective string) (DiagnoseJob, error)
	Diagnose() DiagnoseJob
	CancelDiagnose()
	TakeOverClashLinks() error
	LoopbackApps() (LoopbackInfo, error)
	SetLoopback(exempt []string) (LoopbackInfo, error)
	RunningPrograms() []string
	FlushDns() error
	WinHttpInfo() WinHttpInfo
	SetWinHttp(useProxy bool) (WinHttpInfo, error)
	WslInfo() WslInfo
	SetupWsl() (WslInfo, error)
	ResetWsl() (WslInfo, error)
	RestartWsl() (WslInfo, error)
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
	Update      *UpdateInfo      `json:"update,omitempty"`
	Installing  *InstallProgress `json:"installing,omitempty"`
	// Subscriptions 按配置 id 给出订阅的下载情况，RuleSets 按地址给出分流规则集的情况，RuleLibrary 是规则库，
	// Core 是订阅使用的代理内核的情况。
	Subscriptions map[string]SubscriptionInfo `json:"subscriptions"`
	RuleSets      map[string]RuleSetState     `json:"rule_sets"`
	RuleLibrary   []RuleLibraryEntry          `json:"rule_library"`
	Core          CoreInfo                    `json:"core"`
	// Share 是局域网共享的去向、这台电脑的局域网地址和防睡眠的状态，共享入口是否在监听见 Core.Share。
	Share ShareInfo `json:"share"`
	// Speed 是实时网速。
	Speed SpeedInfo `json:"speed"`
	// Links 是 clash:// 链接的登记情况（见 links_windows.go）。
	Links LinksInfo `json:"links"`
	// Groups 是策略组的节点来源和它们在内核里的状态。
	Groups GroupsInfo `json:"groups"`
}

// GroupsInfo 是策略组的情况：Source 是节点来源（正在使用的订阅配置的 id，没有已下载的订阅时为空），
// States 是内核里各个策略组的状态，内核没有运行时为空。
type GroupsInfo struct {
	Source string           `json:"source,omitempty"`
	States []CoreGroupState `json:"states"`
}

// LinksInfo 是链接的登记情况：Clash 是现在处理 clash:// 链接（机场网站的「一键导入」）的程序名，没有时为空；
// ClashOurs 表示就是 ProxySwitch。
type LinksInfo struct {
	Clash     string `json:"clash"`
	ClashOurs bool   `json:"clash_ours"`
}

// CoreInfo 是订阅使用的代理内核的情况。Custom 表示使用配置里指定的内核；InstalledVersion 是下载的内核的版本，
// Version 是这个版本的 ProxySwitch 使用的版本；Downloadable 表示可以在程序里下载内核。
type CoreInfo struct {
	Installed        bool             `json:"installed"`
	Path             string           `json:"path"`
	Custom           bool             `json:"custom"`
	Version          string           `json:"version"`
	InstalledVersion string           `json:"installed_version,omitempty"`
	Port             int              `json:"port"`
	Running          bool             `json:"running"`
	Error            string           `json:"error,omitempty"`
	GeoReady         bool             `json:"geo_ready"`
	Downloadable     bool             `json:"downloadable"`
	Installing       *InstallProgress `json:"installing,omitempty"`
	Share            CoreShareStatus  `json:"share"`
	Tun              CoreTunStatus    `json:"tun"`
}

// NavigateInfo 是让已打开的设置页切换页面的请求，Serial 每次加一，页面发现变化时切到 Page；
// Action 不为空时切换后再执行页面上的这个操作，例如托盘菜单的「检查更新」让关于页立即检查，
// 命令行的「diagnose 网址」让网址诊断页诊断 Argument 里的网址。
type NavigateInfo struct {
	Page     string `json:"page"`
	Action   string `json:"action,omitempty"`
	Argument string `json:"argument,omitempty"`
	Serial   int    `json:"serial"`
}

type PathsInfo struct {
	Dir      string `json:"dir"`
	Config   string `json:"config"`
	Log      string `json:"log"`
	Portable bool   `json:"portable"`
}

// StatusInfo.State：off 已关闭 / on 由本程序开启 / external 系统代理被其他程序开启。Node 是正在使用的订阅实际在用的节点。
// Terminal 是开启时在当前终端使用代理的命令；ExternalProfile 是 external 时由系统代理转成的配置，页面可以一键保存。
type StatusInfo struct {
	State           string            `json:"state"`
	Profile         string            `json:"profile"`
	Node            string            `json:"node,omitempty"`
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
	WinHttp       string            `json:"winhttp"`
	LastCrash     string            `json:"last_crash,omitempty"`
	LastCrashTime string            `json:"last_crash_time,omitempty"`
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

	mutex      sync.Mutex
	token      string
	listener   net.Listener
	server     *http.Server
	address    string
	navigate   NavigateInfo
	installing *InstallProgress
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
	mux.HandleFunc("POST /api/update/install", settings.handleInstallUpdate)
	mux.HandleFunc("GET /api/subscriptions/{id}/nodes", settings.handleNodes)
	mux.HandleFunc("POST /api/subscriptions/{id}/select", settings.handleSelectNode)
	mux.HandleFunc("POST /api/subscriptions/{id}/test", settings.handleTestNodes)
	mux.HandleFunc("POST /api/subscriptions/{id}/update", settings.handleUpdateSubscription)
	mux.HandleFunc("POST /api/subscriptions/check", settings.handleCheckSubscription)
	mux.HandleFunc("POST /api/subscriptions/{id}/mode", settings.handleSetMode)
	mux.HandleFunc("POST /api/groups/select", settings.handleSelectGroupNode)
	mux.HandleFunc("POST /api/rulesets/update", settings.handleUpdateRuleSet)
	mux.HandleFunc("POST /api/rulesets/update-all", settings.handleUpdateAllRuleSets)
	mux.HandleFunc("POST /api/core/install", settings.handleInstallCore)
	mux.HandleFunc("POST /api/share", settings.handleShare)
	mux.HandleFunc("GET /api/share/activity", settings.handleShareActivity)
	mux.HandleFunc("POST /api/share/clear", settings.handleShareClear)
	mux.HandleFunc("POST /api/share/firewall", settings.handleShareFirewall)
	mux.HandleFunc("GET /api/connections", settings.handleConnections)
	mux.HandleFunc("POST /api/connections/close", settings.handleCloseConnection)
	mux.HandleFunc("POST /api/connections/clear", settings.handleClearConnections)
	mux.HandleFunc("POST /api/traffic/reset", settings.handleResetTraffic)
	mux.HandleFunc("POST /api/exit/check", settings.handleCheckExit)
	mux.HandleFunc("POST /api/links/clash", settings.handleClashLinks)
	mux.HandleFunc("GET /api/loopback", settings.handleLoopback)
	mux.HandleFunc("GET /api/programs", settings.handlePrograms)
	mux.HandleFunc("POST /api/dns/flush", settings.handleFlushDns)
	mux.HandleFunc("GET /api/winhttp", settings.handleWinHttp)
	mux.HandleFunc("POST /api/winhttp", settings.handleSetWinHttp)
	mux.HandleFunc("GET /api/wsl", settings.handleWsl)
	mux.HandleFunc("POST /api/wsl/setup", settings.handleWslAction(settings.backend.SetupWsl))
	mux.HandleFunc("POST /api/wsl/reset", settings.handleWslAction(settings.backend.ResetWsl))
	mux.HandleFunc("POST /api/wsl/restart", settings.handleWslAction(settings.backend.RestartWsl))
	mux.HandleFunc("POST /api/loopback", settings.handleSetLoopback)
	mux.HandleFunc("GET /api/diagnose", settings.handleDiagnose)
	mux.HandleFunc("POST /api/diagnose", settings.handleStartDiagnose)
	mux.HandleFunc("POST /api/diagnose/cancel", settings.handleCancelDiagnose)
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

// ShowPage 请求已经打开的设置页切到 page 并执行 action（可以为空），页面下次同步状态时切换。
func (settings *SettingsServer) ShowPage(page, action string) {
	settings.ShowPageWith(page, action, "")
}

// ShowPageWith 和 ShowPage 一样，argument 是交给 action 的参数。
func (settings *SettingsServer) ShowPageWith(page, action, argument string) {
	settings.mutex.Lock()
	defer settings.mutex.Unlock()
	settings.navigate = NavigateInfo{Page: page, Action: action, Argument: argument, Serial: settings.navigate.Serial + 1}
}

func (settings *SettingsServer) state() SettingsState {
	state := settings.backend.State()
	settings.mutex.Lock()
	state.Navigate = settings.navigate
	state.Installing = settings.installing
	settings.mutex.Unlock()
	return state
}

func (settings *SettingsServer) setInstalling(progress *InstallProgress) {
	settings.mutex.Lock()
	settings.installing = progress
	settings.mutex.Unlock()
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
	writer.Header().Set("Content-Security-Policy", "default-src 'none'; script-src 'self'; style-src 'self' 'unsafe-inline'; img-src 'self' data:; font-src 'self'; connect-src 'self'; form-action 'none'; base-uri 'none'; frame-ancestors 'none'")
	_, _ = writer.Write(page)
}

var assetTypes = map[string]string{
	".css": "text/css; charset=utf-8",
	".js":  "text/javascript; charset=utf-8",
	".svg": "image/svg+xml",
	".png": "image/png",
	// 设置页的字体：思源黑体的常用字子集（见 tools/subset_font.py）。
	".woff2": "font/woff2",
}

// handleAsset 提供页面的样式、脚本和图片。它们不含任何数据，所以不要求 token。
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

// 除了本项目的页面，设置页还会打开 Windows 的“位置”隐私设置（读取 Wi-Fi 名称需要）和“代理”设置页。
const (
	locationSettingsUrl     = "ms-settings:privacy-location"
	networkProxySettingsUrl = "ms-settings:network-proxy"
)

// handleOpenUrl 只允许打开固定的几个地址，避免被利用来启动任意程序。
func (settings *SettingsServer) handleOpenUrl(writer http.ResponseWriter, request *http.Request) {
	var body struct {
		Url string `json:"url"`
	}
	if !decodeJsonBody(writer, request, &body) {
		return
	}
	if body.Url != repositoryUrl && !strings.HasPrefix(body.Url, repositoryUrl+"/") && body.Url != locationSettingsUrl && body.Url != networkProxySettingsUrl {
		writeError(writer, http.StatusBadRequest, "不允许打开这个地址")
		return
	}
	settings.respondOpened(writer, settings.backend.OpenUrl(body.Url))
}

func (settings *SettingsServer) handleUpdate(writer http.ResponseWriter, request *http.Request) {
	info, err := checkLatestRelease(settings.backend.UpdatePaths())
	if err != nil {
		slog.WarnContext(request.Context(), "检查更新失败", "err", err)
		writeError(writer, http.StatusBadGateway, "检查更新失败："+err.Error())
		return
	}
	settings.backend.RememberUpdate(info)
	writeJson(writer, http.StatusOK, info)
}

// handleInstallUpdate 下载并安装新版本，下载进度经 /api/state 的 installing 提供。
// 成功后程序会在片刻后退出，由新版本重新打开设置页。
func (settings *SettingsServer) handleInstallUpdate(writer http.ResponseWriter, request *http.Request) {
	settings.mutex.Lock()
	busy := settings.installing != nil
	if !busy {
		settings.installing = &InstallProgress{}
	}
	settings.mutex.Unlock()
	if busy {
		writeError(writer, http.StatusConflict, "正在下载更新")
		return
	}
	err := settings.backend.InstallUpdate(func(received, total int64) {
		settings.setInstalling(&InstallProgress{Received: received, Total: total})
	})
	settings.setInstalling(nil)
	if err != nil {
		slog.WarnContext(request.Context(), "安装更新失败", "err", err)
		writeError(writer, http.StatusBadGateway, err.Error())
		return
	}
	writeJson(writer, http.StatusOK, map[string]bool{"restarting": true})
}

// ---------- 订阅 ----------

// respondNodes 返回节点列表；内核没有运行等错误用 409 返回原因，页面直接显示。
func respondNodes(writer http.ResponseWriter, request *http.Request, nodes CoreNodes, err error) {
	if err != nil {
		slog.WarnContext(request.Context(), "订阅操作失败", "path", request.URL.Path, "err", err)
		writeError(writer, http.StatusConflict, err.Error())
		return
	}
	writeJson(writer, http.StatusOK, nodes)
}

func (settings *SettingsServer) handleNodes(writer http.ResponseWriter, request *http.Request) {
	nodes, err := settings.backend.SubscriptionNodes(request.PathValue("id"))
	respondNodes(writer, request, nodes, err)
}

func (settings *SettingsServer) handleSelectNode(writer http.ResponseWriter, request *http.Request) {
	var body struct {
		Node string `json:"node"`
	}
	if !decodeJsonBody(writer, request, &body) {
		return
	}
	nodes, err := settings.backend.SelectNode(request.PathValue("id"), body.Node)
	respondNodes(writer, request, nodes, err)
}

func (settings *SettingsServer) handleTestNodes(writer http.ResponseWriter, request *http.Request) {
	nodes, err := settings.backend.TestNodes(request.PathValue("id"))
	respondNodes(writer, request, nodes, err)
}

func (settings *SettingsServer) handleUpdateSubscription(writer http.ResponseWriter, request *http.Request) {
	settings.respondState(writer, request, settings.backend.UpdateSubscription(request.PathValue("id")))
}

func (settings *SettingsServer) handleCheckSubscription(writer http.ResponseWriter, request *http.Request) {
	var body struct {
		Url string `json:"url"`
	}
	if !decodeJsonBody(writer, request, &body) {
		return
	}
	result, err := settings.backend.CheckSubscription(strings.TrimSpace(body.Url))
	if err != nil {
		writeError(writer, http.StatusBadGateway, err.Error())
		return
	}
	writeJson(writer, http.StatusOK, result)
}

func (settings *SettingsServer) handleUpdateRuleSet(writer http.ResponseWriter, request *http.Request) {
	var body struct {
		Url string `json:"url"`
	}
	if !decodeJsonBody(writer, request, &body) {
		return
	}
	settings.respondState(writer, request, settings.backend.UpdateRuleSet(body.Url))
}

func (settings *SettingsServer) handleUpdateAllRuleSets(writer http.ResponseWriter, request *http.Request) {
	result, err := settings.backend.UpdateAllRuleSets()
	if err != nil {
		settings.respondState(writer, request, err)
		return
	}
	state := settings.state()
	writeJson(writer, http.StatusOK, map[string]any{"result": result, "state": state})
}

func (settings *SettingsServer) handleSetMode(writer http.ResponseWriter, request *http.Request) {
	var body struct {
		Mode string `json:"mode"`
	}
	if !decodeJsonBody(writer, request, &body) {
		return
	}
	settings.respondState(writer, request, settings.backend.SetMode(request.PathValue("id"), body.Mode))
}

func (settings *SettingsServer) handleSelectGroupNode(writer http.ResponseWriter, request *http.Request) {
	var body struct {
		Group string `json:"group"`
		Node  string `json:"node"`
	}
	if !decodeJsonBody(writer, request, &body) {
		return
	}
	settings.respondState(writer, request, settings.backend.SelectGroupNode(body.Group, body.Node))
}

// handleInstallCore 下载内核，进度经 /api/state 的 core.installing 提供。
func (settings *SettingsServer) handleInstallCore(writer http.ResponseWriter, request *http.Request) {
	settings.respondState(writer, request, settings.backend.InstallCore())
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

// ---------- 局域网共享 ----------

func (settings *SettingsServer) handleShare(writer http.ResponseWriter, request *http.Request) {
	var body struct {
		Enabled bool `json:"enabled"`
	}
	if !decodeJsonBody(writer, request, &body) {
		return
	}
	settings.respondState(writer, request, settings.backend.SetShare(body.Enabled))
}

func (settings *SettingsServer) handleShareActivity(writer http.ResponseWriter, request *http.Request) {
	writeJson(writer, http.StatusOK, settings.backend.ShareActivity())
}

func (settings *SettingsServer) handleShareClear(writer http.ResponseWriter, request *http.Request) {
	settings.backend.ClearShareHistory()
	writeJson(writer, http.StatusOK, settings.backend.ShareActivity())
}

// ---------- 连接页 ----------

func (settings *SettingsServer) handleConnections(writer http.ResponseWriter, request *http.Request) {
	writeJson(writer, http.StatusOK, settings.backend.Connections())
}

// handleCloseConnection 断开一条连接，id 为空时断开全部。
func (settings *SettingsServer) handleCloseConnection(writer http.ResponseWriter, request *http.Request) {
	var body struct {
		Id string `json:"id"`
	}
	if !decodeJsonBody(writer, request, &body) {
		return
	}
	if err := settings.backend.CloseConnection(body.Id); err != nil {
		writeError(writer, http.StatusConflict, err.Error())
		return
	}
	writeJson(writer, http.StatusOK, settings.backend.Connections())
}

func (settings *SettingsServer) handleClearConnections(writer http.ResponseWriter, request *http.Request) {
	settings.backend.ClearConnections()
	writeJson(writer, http.StatusOK, settings.backend.Connections())
}

func (settings *SettingsServer) handleResetTraffic(writer http.ResponseWriter, request *http.Request) {
	settings.backend.ResetTraffic()
	writeJson(writer, http.StatusOK, settings.backend.Connections())
}

// handleCheckExit 查出口 IP，查完返回连接页的内容；没查到的原因记在里面。内核没有运行时返回 409。
func (settings *SettingsServer) handleCheckExit(writer http.ResponseWriter, request *http.Request) {
	var body struct {
		Direct bool `json:"direct"`
	}
	if !decodeJsonBody(writer, request, &body) {
		return
	}
	if err := settings.backend.CheckExit(body.Direct); err != nil && errors.Is(err, errCoreNotRunning) {
		writeError(writer, http.StatusConflict, err.Error())
		return
	}
	writeJson(writer, http.StatusOK, settings.backend.Connections())
}

// handleFlushDns 清除这台电脑的 DNS 缓存。
func (settings *SettingsServer) handleFlushDns(writer http.ResponseWriter, request *http.Request) {
	if err := settings.backend.FlushDns(); err != nil {
		writeError(writer, http.StatusConflict, err.Error())
		return
	}
	writeJson(writer, http.StatusOK, map[string]bool{"ok": true})
}

// handleWinHttp 是 WinHTTP 的代理（Windows 更新等系统服务用它）和现在可以设给它的代理。
func (settings *SettingsServer) handleWinHttp(writer http.ResponseWriter, request *http.Request) {
	writeJson(writer, http.StatusOK, settings.backend.WinHttpInfo())
}

// handleSetWinHttp 把 WinHTTP 的代理设为当前代理（proxy 为 true）或改回直连。
func (settings *SettingsServer) handleSetWinHttp(writer http.ResponseWriter, request *http.Request) {
	var body struct {
		Proxy bool `json:"proxy"`
	}
	if !decodeJsonBody(writer, request, &body) {
		return
	}
	info, err := settings.backend.SetWinHttp(body.Proxy)
	if err != nil {
		slog.WarnContext(request.Context(), "修改 WinHTTP 的代理失败", "err", err)
		writeJson(writer, http.StatusConflict, map[string]any{"error": err.Error(), "winhttp": info})
		return
	}
	writeJson(writer, http.StatusOK, info)
}

// handleWsl 是 WSL 的情况：装了没有、支不支持镜像网络、是不是已经设置成使用本机的代理。
func (settings *SettingsServer) handleWsl(writer http.ResponseWriter, request *http.Request) {
	writeJson(writer, http.StatusOK, settings.backend.WslInfo())
}

// handleWslAction 执行 WSL 的设置、撤销或重启，返回之后的情况；出错时一起返回错误。
func (settings *SettingsServer) handleWslAction(action func() (WslInfo, error)) http.HandlerFunc {
	return func(writer http.ResponseWriter, request *http.Request) {
		info, err := action()
		if err != nil {
			slog.WarnContext(request.Context(), "设置 WSL 失败", "err", err)
			writeJson(writer, http.StatusConflict, map[string]any{"error": err.Error(), "wsl": info})
			return
		}
		writeJson(writer, http.StatusOK, info)
	}
}

// handlePrograms 列出正在运行的程序，给按程序分流的自定义规则选程序名。
func (settings *SettingsServer) handlePrograms(writer http.ResponseWriter, request *http.Request) {
	writeJson(writer, http.StatusOK, map[string][]string{"programs": settings.backend.RunningPrograms()})
}

// handleLoopback 列出微软商店应用和它们能不能连接本机的代理。
func (settings *SettingsServer) handleLoopback(writer http.ResponseWriter, request *http.Request) {
	info, err := settings.backend.LoopbackApps()
	if err != nil {
		writeError(writer, http.StatusBadGateway, err.Error())
		return
	}
	writeJson(writer, http.StatusOK, info)
}

// handleSetLoopback 修改哪些商店应用可以连接本机的代理：exempt 是允许的应用，其余已安装的应用不允许。
func (settings *SettingsServer) handleSetLoopback(writer http.ResponseWriter, request *http.Request) {
	var body struct {
		Exempt []string `json:"exempt"`
	}
	if !decodeJsonBody(writer, request, &body) {
		return
	}
	info, err := settings.backend.SetLoopback(body.Exempt)
	if err != nil {
		slog.WarnContext(request.Context(), "修改商店应用的回环豁免失败", "err", err)
		writeJson(writer, http.StatusConflict, map[string]any{"error": err.Error(), "loopback": info})
		return
	}
	writeJson(writer, http.StatusOK, info)
}

// handleClashLinks 让机场网站的「一键导入 Clash」（clash:// 链接）改由 ProxySwitch 处理。
func (settings *SettingsServer) handleClashLinks(writer http.ResponseWriter, request *http.Request) {
	settings.respondState(writer, request, settings.backend.TakeOverClashLinks())
}

func (settings *SettingsServer) handleShareFirewall(writer http.ResponseWriter, request *http.Request) {
	if err := settings.backend.AllowShareFirewall(); err != nil {
		writeError(writer, http.StatusConflict, err.Error())
		return
	}
	writeJson(writer, http.StatusOK, map[string]bool{"ok": true})
}

// ---------- 网址诊断 ----------

func (settings *SettingsServer) handleDiagnose(writer http.ResponseWriter, request *http.Request) {
	writeJson(writer, http.StatusOK, settings.backend.Diagnose())
}

func (settings *SettingsServer) handleStartDiagnose(writer http.ResponseWriter, request *http.Request) {
	var body struct {
		Url         string `json:"url"`
		Perspective string `json:"perspective"`
	}
	if !decodeJsonBody(writer, request, &body) {
		return
	}
	job, err := settings.backend.StartDiagnose(body.Url, body.Perspective)
	if err != nil {
		writeError(writer, http.StatusBadRequest, err.Error())
		return
	}
	writeJson(writer, http.StatusOK, job)
}

func (settings *SettingsServer) handleCancelDiagnose(writer http.ResponseWriter, request *http.Request) {
	settings.backend.CancelDiagnose()
	writeJson(writer, http.StatusOK, settings.backend.Diagnose())
}
