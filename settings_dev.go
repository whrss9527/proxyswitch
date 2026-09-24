package main

import (
	"encoding/json"
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
type devBackend struct {
	mutex     sync.Mutex
	engine    *Engine
	system    *memorySystem
	autostart bool
	notices   []Notice
	httpProxy *fakeProxy
	socks     *fakeProxy
}

var devDefaultNetwork = NetworkInfo{
	Ssids: []string{"Home-WiFi"},
	Adapters: []NetworkAdapter{
		{Name: "WLAN", DnsSuffix: "lan", Gateway: "192.168.1.1", GatewayMac: "a4-91-b1-0c-22-9e", Wireless: true},
	},
}

func newDevBackend(paths Paths, httpProxy, socks *fakeProxy) *devBackend {
	backend := &devBackend{system: newMemorySystem(), httpProxy: httpProxy, socks: socks}
	backend.engine = newEngine(backend.system, paths, backend.addNotice)
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
	defer backend.mutex.Unlock()
	state := backend.engine.settingsState()
	state.Platform = "dev"
	state.Autostart = backend.autostart
	state.Accent = "#0067c0"
	state.Targets = targetInfos(true)
	if config := backend.engine.Config(); config != nil && config.Hotkey != "" {
		if hotkey, err := parseHotkey(config.Hotkey); err == nil {
			state.Hotkeys.Toggle = hotkey.Text
		}
	}
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
	return Diagnostics{
		System:       system.System,
		SystemSource: "内存（开发模式）",
		Connections:  []string{"局域网"},
		Env:          env,
		Git:          GitStatus{Available: true, Path: "git", HttpProxy: system.Git, HttpsProxy: system.Git},
		Npm:          npm,
		NpmrcPath:    "~/.npmrc",
	}
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

func (backend *devBackend) ActiveProxyUrl() string {
	return ""
}

// InstallUpdate 在开发模式下只下载并校验新版本（保存到配置目录），不替换程序。
func (backend *devBackend) InstallUpdate(progress func(received, total int64)) error {
	_, err := downloadLatestRelease("", filepath.Join(backend.engine.paths.Dir, "update.download"), progress)
	return err
}

// healthLoop 与 Windows 版一样定期检查代理服务器能否连上。
func (backend *devBackend) healthLoop(interval time.Duration) {
	for range time.Tick(interval) {
		backend.mutex.Lock()
		backend.engine.ReloadIfChanged()
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
}

// runDevSettings 在任意平台上启动设置页服务，输出一行 JSON：页面地址、假代理地址、测速地址。
func runDevSettings(args []string) int {
	directory, webDir := "", ""
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
				Page string `json:"page"`
			}
			if decodeJsonBody(writer, request, &body) {
				settings.ShowPage(body.Page)
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
