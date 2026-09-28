package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

// devBackend 是开发模式的后端：核心逻辑与 Windows 版相同（Engine），系统设置换成内存实现，
// 网络环境可以通过 /api/dev/network 模拟。`go run . --dev-settings` 在任何平台上都能预览和测试设置页。
// 订阅由真实的内核代理：用 --core 指定本机的 mihomo 程序。
type devBackend struct {
	*subscriptionService
	mutex     sync.Mutex
	engine    *Engine
	system    *memorySystem
	autostart bool
	notices   []Notice
	httpProxy *fakeProxy
	socks     *fakeProxy
	update    *UpdateInfo
	// onBattery 模拟用电池供电，firewallAllowed 记下添加防火墙例外的次数。
	onBattery       bool
	firewallAllowed int
	// links 模拟 clash:// 链接的登记：开始时由另一个程序（clash-verge）处理。
	links LinksInfo
	// loopback 模拟商店应用和它们的回环豁免；loopbackChanges 记下以管理员身份修改的次数。
	loopback        []LoopbackApp
	loopbackChanges int
	// wslConfig 模拟 .wslconfig 的内容，wslRestart 表示改过设置还没重启 WSL。
	wslConfig  string
	wslRestart bool
	// winHttpProxy 和 winHttpBypass 模拟 WinHTTP 的代理设置；dnsFlushes 记下清除 DNS 缓存的次数。
	winHttpProxy  string
	winHttpBypass string
	dnsFlushes    int
}

var devDefaultNetwork = NetworkInfo{
	Ssids: []string{"Home-WiFi"},
	Adapters: []NetworkAdapter{
		{Name: "WLAN", DnsSuffix: "lan", Gateway: "192.168.1.1", GatewayMac: "a4-91-b1-0c-22-9e", Wireless: true},
	},
}

func newDevBackend(paths Paths, httpProxy, socks *fakeProxy) *devBackend {
	backend := &devBackend{system: newMemorySystem(), httpProxy: httpProxy, socks: socks, links: LinksInfo{Clash: "clash-verge"}, loopback: []LoopbackApp{
		{Sid: "S-1-15-2-1001", Name: "Microsoft Store", Package: "microsoft.windowsstore_8wekyb3d8bbwe"},
		{Sid: "S-1-15-2-1002", Name: "Netflix", Package: "4df9e0f8.netflix_mcm4njqhnhss8"},
		{Sid: "S-1-15-2-1003", Name: "Xbox", Package: "microsoft.gamingapp_8wekyb3d8bbwe", Exempt: true},
		{Sid: "S-1-15-2-1004", Name: "邮件和日历", Package: "microsoft.windowscommunicationsapps_8wekyb3d8bbwe"},
	}}
	backend.engine = newEngine(backend.system, paths, backend.addNotice)
	core := newCore(func(message string) {
		_ = backend.locked(func() error {
			backend.addNotice(Notice{Level: noticeError, Title: "代理内核出错", Text: message})
			return nil
		})
	})
	backend.subscriptionService = newSubscriptionService(backend.engine, core, func(action func()) error {
		return backend.locked(func() error {
			action()
			return nil
		})
	})
	_, _ = backend.engine.LoadConfig()
	backend.engine.UpdateNetwork(devDefaultNetwork)
	backend.engine.UpdateNetwork(devDefaultNetwork)
	return backend
}

func (backend *devBackend) addNotice(notice Notice) {
	slog.Info("通知", "level", notice.Level, "title", notice.Title, "text", strings.ReplaceAll(notice.Text, "\n", " | "))
	backend.notices = append(backend.notices, notice)
	if len(backend.notices) > 50 {
		backend.notices = backend.notices[len(backend.notices)-50:]
	}
}

func (backend *devBackend) locked(action func() error) error {
	backend.mutex.Lock()
	defer backend.mutex.Unlock()
	return action()
}

func (backend *devBackend) State() SettingsState {
	backend.mutex.Lock()
	state := backend.engine.settingsState()
	state.Platform = "dev"
	state.Autostart = backend.autostart
	state.Accent = "#0067c0"
	state.Targets = targetInfos(true)
	state.Update = backend.update
	state.Links = backend.links
	if config := backend.engine.Config(); config != nil && config.Hotkey != "" {
		if hotkey, err := parseHotkey(config.Hotkey); err == nil {
			state.Hotkeys.Toggle = hotkey.Text
		}
	}
	if config := backend.engine.Config(); config != nil {
		state.Share.Awake = shareAwake(config.Share, backend.onBattery)
	}
	backend.mutex.Unlock()
	// 与 Windows 版一样在锁外查询内核。
	backend.fillState(&state)
	return state
}

func (backend *devBackend) SaveConfig(config *Config) error {
	return backend.locked(func() error { return backend.engine.SaveConfig(config) })
}

func (backend *devBackend) TurnOn() error {
	return backend.locked(backend.engine.TurnOn)
}

func (backend *devBackend) TurnOff() error {
	return backend.locked(backend.engine.TurnOff)
}

func (backend *devBackend) UseProfile(name string) error {
	return backend.locked(func() error { return backend.engine.UseProfile(name) })
}

func (backend *devBackend) ApplyAutoSwitch() error {
	return backend.locked(backend.engine.ApplyAutoSwitch)
}

func (backend *devBackend) ClearAllProxies() error {
	return backend.locked(backend.engine.ClearAll)
}

func (backend *devBackend) SetAutostart(enabled bool) error {
	return backend.locked(func() error {
		backend.autostart = enabled
		return nil
	})
}

func (backend *devBackend) Listeners() []Listener {
	listeners := []Listener{
		{Address: "127.0.0.1", Port: backend.httpProxy.Port(), Pid: 4242, Process: "dev-http-proxy"},
		{Address: "0.0.0.0", Port: backend.socks.Port(), Pid: 4243, Process: "dev-socks-proxy"},
	}
	return append(listeners, commonPortListeners()...)
}

func (backend *devBackend) Diagnostics() Diagnostics {
	backend.mutex.Lock()
	defer backend.mutex.Unlock()
	system := backend.system
	env := map[string]string{"HTTP_PROXY": system.Env["HTTP_PROXY"], "HTTPS_PROXY": system.Env["HTTPS_PROXY"], "NO_PROXY": system.Env["NO_PROXY"]}
	npm := map[string]string{}
	for key, value := range system.Npm {
		npm[key] = value
	}
	diagnostics := Diagnostics{
		System:       system.System,
		SystemSource: "内存（开发模式）",
		Connections:  []string{"局域网"},
		Env:          env,
		Git:          GitStatus{Available: true, Path: "git", HttpProxy: system.Git, HttpsProxy: system.Git},
		Npm:          npm,
		NpmrcPath:    "~/.npmrc",
		WinHttp:      backend.winHttpProxy,
	}
	if crash, when := readPreviousCrash(backend.engine.paths.PreviousCrash, time.Now()); crash != "" {
		diagnostics.LastCrash, diagnostics.LastCrashTime = crash, when.Format("2006-01-02 15:04")
	}
	return diagnostics
}

func (backend *devBackend) LogTail(maxLines int) string {
	return readLogTail(backend.engine.paths.Log, maxLines)
}

func (backend *devBackend) OpenConfigDir() error {
	slog.Info("开发模式：打开配置目录", "path", backend.engine.paths.Dir)
	return nil
}

func (backend *devBackend) OpenConfigFile() error {
	slog.Info("开发模式：打开配置文件", "path", backend.engine.paths.Config)
	return nil
}

func (backend *devBackend) OpenLogFile() error {
	slog.Info("开发模式：打开日志", "path", backend.engine.paths.Log)
	return nil
}

func (backend *devBackend) OpenUrl(address string) error {
	slog.Info("开发模式：打开网址", "url", address)
	return nil
}

// UpdatePaths 与 Windows 版一样先经代理和内核、最后直连；模拟的发布在本机，实际只直连。
func (backend *devBackend) UpdatePaths() []string {
	backend.mutex.Lock()
	defer backend.mutex.Unlock()
	return proxiesFirst(backend.engine.DownloadPaths())
}

// RememberUpdate 与 Windows 版一样记下检查发现的新版本，没有新版本时清除。
func (backend *devBackend) RememberUpdate(info UpdateInfo) {
	backend.mutex.Lock()
	defer backend.mutex.Unlock()
	backend.update = nil
	if info.Newer {
		backend.update = &info
	}
}

// Close 停止内核，测试结束时调用。
func (backend *devBackend) Close() {
	backend.core.Stop()
}

// InstallUpdate 在开发模式下只下载并校验新版本（保存到配置目录），不替换程序。
func (backend *devBackend) InstallUpdate(progress func(received, total int64)) error {
	_, err := downloadLatestRelease(backend.UpdatePaths(), filepath.Join(backend.engine.paths.Dir, "update.download"), progress)
	return err
}

// FlushDns 在开发模式下只记一次，不清除系统的缓存。
func (backend *devBackend) FlushDns() error {
	return backend.locked(func() error {
		backend.dnsFlushes++
		return nil
	})
}

// WinHttpInfo 在开发模式下返回模拟的 WinHTTP 代理设置。
func (backend *devBackend) WinHttpInfo() WinHttpInfo {
	backend.mutex.Lock()
	defer backend.mutex.Unlock()
	info := WinHttpInfo{Proxy: backend.winHttpProxy, Bypass: backend.winHttpBypass}
	info.Suggested, info.SuggestedBypass, info.Unsupported = backend.engine.WinHttpSuggestion()
	return info
}

// SetWinHttp 在开发模式下只改模拟的设置。
func (backend *devBackend) SetWinHttp(useProxy bool) (WinHttpInfo, error) {
	info := backend.WinHttpInfo()
	if useProxy && info.Suggested == "" {
		return info, errors.New(info.Unsupported)
	}
	backend.mutex.Lock()
	backend.winHttpProxy, backend.winHttpBypass = "", ""
	if useProxy {
		backend.winHttpProxy, backend.winHttpBypass = info.Suggested, info.SuggestedBypass
	}
	backend.mutex.Unlock()
	return backend.WinHttpInfo(), nil
}

// WslInfo 在开发模式下模拟装了 Ubuntu、支持镜像网络的 WSL。
func (backend *devBackend) WslInfo() WslInfo {
	backend.mutex.Lock()
	defer backend.mutex.Unlock()
	info := WslInfo{Installed: true, Distros: []string{"Ubuntu"}, Supported: true, Restart: backend.wslRestart, Config: `C:\Users\dev\.wslconfig`}
	info.Mirrored, info.AutoProxy = wslConfigValues(backend.wslConfig)
	return info
}

// SetupWsl 在开发模式下只改模拟的 .wslconfig。
func (backend *devBackend) SetupWsl() (WslInfo, error) {
	return backend.editWslConfig(setWslProxy)
}

// ResetWsl 在开发模式下只改模拟的 .wslconfig。
func (backend *devBackend) ResetWsl() (WslInfo, error) {
	return backend.editWslConfig(resetWslProxy)
}

func (backend *devBackend) editWslConfig(edit func(string) string) (WslInfo, error) {
	backend.mutex.Lock()
	if updated := edit(backend.wslConfig); updated != backend.wslConfig {
		backend.wslConfig, backend.wslRestart = updated, true
	}
	backend.mutex.Unlock()
	return backend.WslInfo(), nil
}

// RestartWsl 在开发模式下只记下已经重启。
func (backend *devBackend) RestartWsl() (WslInfo, error) {
	backend.mutex.Lock()
	backend.wslRestart = false
	backend.mutex.Unlock()
	return backend.WslInfo(), nil
}

// RunningPrograms 在开发模式下返回几个常见的程序名。
func (backend *devBackend) RunningPrograms() []string {
	return []string{"chrome.exe", "Code.exe", "steam.exe", "Telegram.exe", "WeChat.exe"}
}

// LoopbackApps 在开发模式下返回模拟的商店应用。
func (backend *devBackend) LoopbackApps() (LoopbackInfo, error) {
	backend.mutex.Lock()
	defer backend.mutex.Unlock()
	return LoopbackInfo{Apps: append([]LoopbackApp{}, backend.loopback...)}, nil
}

// SetLoopback 在开发模式下修改模拟的回环豁免，和 Windows 版一样没有变化时不需要「管理员确认」。
func (backend *devBackend) SetLoopback(exempt []string) (LoopbackInfo, error) {
	backend.mutex.Lock()
	var current []string
	for _, item := range backend.loopback {
		if item.Exempt {
			current = append(current, item.Sid)
		}
	}
	desired := mergeLoopback(current, backend.loopback, exempt)
	if !sameSids(desired, current) {
		backend.loopbackChanges++
		allowed := map[string]bool{}
		for _, sid := range desired {
			allowed[sid] = true
		}
		for index := range backend.loopback {
			backend.loopback[index].Exempt = allowed[backend.loopback[index].Sid]
		}
	}
	backend.mutex.Unlock()
	return backend.LoopbackApps()
}

// TakeOverClashLinks 在开发模式下只改模拟的登记情况。
func (backend *devBackend) TakeOverClashLinks() error {
	return backend.locked(func() error {
		backend.links = LinksInfo{Clash: "ProxySwitch", ClashOurs: true}
		return nil
	})
}

// AllowShareFirewall 在开发模式下只记一次，不改系统设置。
func (backend *devBackend) AllowShareFirewall() error {
	return backend.locked(func() error {
		backend.firewallAllowed++
		return nil
	})
}

// healthLoop 与 Windows 版一样定期检查代理服务器能否连上，本机代理被其他程序改了之后让共享跟着变。
func (backend *devBackend) healthLoop(interval time.Duration) {
	for range time.Tick(interval) {
		backend.mutex.Lock()
		backend.engine.ReloadIfChanged()
		backend.engine.GuardSystemProxy()
		backend.engine.RefreshCore(backend.engine.Status())
		target := backend.engine.HealthTarget()
		backend.mutex.Unlock()
		if target == "" {
			continue
		}
		err := checkProxyReachable(target, 800*time.Millisecond)
		backend.mutex.Lock()
		backend.engine.HealthResult(target, err)
		backend.mutex.Unlock()
	}
}

// devRoutes 是开发模式额外的接口，自动化测试用来模拟网络变化、外部程序改代理、代理软件退出。
func (backend *devBackend) devRoutes(mux *http.ServeMux) {
	mux.HandleFunc("POST /api/dev/network", func(writer http.ResponseWriter, request *http.Request) {
		var info NetworkInfo
		if !decodeJsonBody(writer, request, &info) {
			return
		}
		_ = backend.locked(func() error {
			backend.engine.UpdateNetwork(info)
			backend.engine.UpdateNetwork(info)
			return nil
		})
		writeJson(writer, http.StatusOK, backend.State())
	})
	mux.HandleFunc("POST /api/dev/external", func(writer http.ResponseWriter, request *http.Request) {
		var body struct {
			Server string `json:"server"`
		}
		if !decodeJsonBody(writer, request, &body) {
			return
		}
		_ = backend.locked(func() error {
			backend.system.System = SystemProxyState{ProxyEnabled: body.Server != "", Server: body.Server, Bypass: "<local>"}
			return nil
		})
		writeJson(writer, http.StatusOK, backend.State())
	})
	mux.HandleFunc("POST /api/dev/proxy", func(writer http.ResponseWriter, request *http.Request) {
		var body struct {
			Running bool `json:"running"`
		}
		if !decodeJsonBody(writer, request, &body) {
			return
		}
		if body.Running {
			if err := backend.httpProxy.Start(); err != nil {
				writeError(writer, http.StatusInternalServerError, err.Error())
				return
			}
		} else {
			backend.httpProxy.Stop()
		}
		writeJson(writer, http.StatusOK, map[string]bool{"running": body.Running})
	})
	mux.HandleFunc("GET /api/dev/notices", func(writer http.ResponseWriter, request *http.Request) {
		backend.mutex.Lock()
		defer backend.mutex.Unlock()
		type noticeJson struct {
			Level int    `json:"level"`
			Title string `json:"title"`
			Text  string `json:"text"`
		}
		notices := []noticeJson{}
		for _, notice := range backend.notices {
			notices = append(notices, noticeJson{int(notice.Level), notice.Title, notice.Text})
		}
		writeJson(writer, http.StatusOK, notices)
	})
	mux.HandleFunc("GET /api/dev/system", func(writer http.ResponseWriter, request *http.Request) {
		writeJson(writer, http.StatusOK, backend.Diagnostics())
	})
	mux.HandleFunc("GET /api/dev/dns", func(writer http.ResponseWriter, request *http.Request) {
		backend.mutex.Lock()
		defer backend.mutex.Unlock()
		writeJson(writer, http.StatusOK, map[string]int{"flushes": backend.dnsFlushes})
	})
	mux.HandleFunc("POST /api/dev/battery", func(writer http.ResponseWriter, request *http.Request) {
		var body struct {
			OnBattery bool `json:"on_battery"`
		}
		if !decodeJsonBody(writer, request, &body) {
			return
		}
		_ = backend.locked(func() error {
			backend.onBattery = body.OnBattery
			return nil
		})
		writeJson(writer, http.StatusOK, backend.State())
	})
	mux.HandleFunc("GET /api/dev/firewall", func(writer http.ResponseWriter, request *http.Request) {
		backend.mutex.Lock()
		defer backend.mutex.Unlock()
		writeJson(writer, http.StatusOK, map[string]int{"allowed": backend.firewallAllowed})
	})
}

// linkDevCore 让开发模式使用本机的 mihomo：在内核工作目录放一个指向它的链接（不支持链接时复制一份），
// 引擎像正式版一样在工作目录里找到它。
func linkDevCore(paths Paths, source string) error {
	absolute, err := filepath.Abs(source)
	if err != nil {
		return err
	}
	if !fileExists(absolute) {
		return fmt.Errorf("找不到内核程序：%s", absolute)
	}
	if err := os.MkdirAll(paths.Core, 0o755); err != nil {
		return err
	}
	target := filepath.Join(paths.Core, coreBinaryName())
	_ = os.Remove(target)
	if os.Symlink(absolute, target) == nil {
		return nil
	}
	data, err := os.ReadFile(absolute)
	if err != nil {
		return err
	}
	return os.WriteFile(target, data, 0o755)
}

// runDevSettings 在任意平台上启动设置页服务，输出一行 JSON：页面地址、假代理地址、测速地址。
func runDevSettings(args []string) int {
	directory, webDir, corePath := "", "", ""
	for _, argument := range args {
		if value, found := strings.CutPrefix(argument, "--dir="); found {
			directory = value
		}
		if value, found := strings.CutPrefix(argument, "--web="); found {
			webDir = value
		}
		// 自动化测试用本地模拟的 GitHub 发布接口。
		if value, found := strings.CutPrefix(argument, "--release-api="); found {
			releaseApiUrl = value
		}
		if value, found := strings.CutPrefix(argument, "--core="); found {
			corePath = value
		}
	}
	if directory == "" {
		created, err := os.MkdirTemp("", "proxyswitch-dev-")
		if err != nil {
			fmt.Fprintln(os.Stderr, err)
			return 1
		}
		directory = created
	}
	paths := pathsIn(directory, false)
	setupLogger(paths.Log)
	if corePath != "" {
		if err := linkDevCore(paths, corePath); err != nil {
			fmt.Fprintln(os.Stderr, err)
			return 1
		}
	}

	httpProxy, err := startFakeProxy(serveFakeHttpProxy, 35*time.Millisecond)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	socks, err := startFakeProxy(serveFakeSocksProxy, 90*time.Millisecond)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	testUrl, _, err := startFakeTestServer()
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}

	backend := newDevBackend(paths, httpProxy, socks)
	settings := newSettingsServer(backend)
	settings.extra = func(mux *http.ServeMux) {
		backend.devRoutes(mux)
		mux.HandleFunc("POST /api/dev/navigate", func(writer http.ResponseWriter, request *http.Request) {
			var body struct {
				Page     string `json:"page"`
				Action   string `json:"action"`
				Argument string `json:"argument"`
			}
			if decodeJsonBody(writer, request, &body) {
				settings.ShowPageWith(body.Page, body.Action, body.Argument)
				writeJson(writer, http.StatusOK, map[string]bool{"ok": true})
			}
		})
	}
	if webDir != "" {
		settings.files = os.DirFS(webDir)
	}
	address, err := settings.Start()
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	go backend.healthLoop(time.Second)
	go backend.Run()

	info, _ := json.Marshal(map[string]string{
		"url":         address,
		"dir":         directory,
		"config":      filepath.ToSlash(paths.Config),
		"http_proxy":  httpProxy.Address(),
		"socks_proxy": socks.Address(),
		"test_url":    testUrl,
	})
	fmt.Println(string(info))
	select {}
}
