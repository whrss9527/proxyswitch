package main

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	_ "embed"
)

// 图形化设置页面：程序内起一个只监听 127.0.0.1 的小 HTTP 服务，用系统默认浏览器打开一个本地网页。
// 页面通过下面的 JSON 接口读写配置，不需要联网，也没有任何外部依赖。
//
//   GET  /                 设置页面（需要 ?token=，token 随机生成、每次启动不同）
//   GET  /api/state        当前配置 + 运行状态
//   PUT  /api/config       保存配置（校验后写文件并重新加载）
//   POST /api/action       {"action":"toggle"|"on"|"off"|"use","name":"..."}
//   POST /api/autostart    {"enabled":true}
//   POST /api/test         {"server":"127.0.0.1:7890"} 或 {"pac":"http://..."} 测试连通性
//
// 所有接口都要求请求头 X-Token（页面里的 JS 会自动带上），并校验 Host，防止其它网页跨站调用。

//go:embed web/settings.html
var settingsPageHTML string

// SettingsBackend 是设置页面需要用到的应用能力；Windows 端由 App 实现，开发/测试时用假实现。
type SettingsBackend interface {
	SettingsState() SettingsState
	SaveConfig(cfg *Config) error
	DoAction(action, name string) error
	SetAutostart(enabled bool) error
}

// SettingsState 是页面初始化 / 刷新时拿到的全部信息。
type SettingsState struct {
	Version     string       `json:"version"`
	Paths       PathsInfo    `json:"paths"`
	Config      *Config      `json:"config"`
	ConfigError string       `json:"config_error,omitempty"`
	Status      StatusInfo   `json:"status"`
	Autostart   bool         `json:"autostart"`
	HotkeyText  string       `json:"hotkey_text"`
	HotkeyError string       `json:"hotkey_error,omitempty"`
	Defaults    DefaultsInfo `json:"defaults"`
	Targets     []TargetInfo `json:"targets"`
	Platform    string       `json:"platform"`
}

type PathsInfo struct {
	Dir      string `json:"dir"`
	Config   string `json:"config"`
	Log      string `json:"log"`
	Portable bool   `json:"portable"`
}

type StatusInfo struct {
	On       bool   `json:"on"`
	Profile  string `json:"profile"`  // 开启时：生效的配置名；关闭时：当前选中的配置名
	External string `json:"external"` // 开启但不是本程序的配置时：外部代理描述
}

type DefaultsInfo struct {
	Bypass  string `json:"bypass"`
	NoProxy string `json:"no_proxy"`
	Hotkey  string `json:"hotkey"`
}

type TargetInfo struct {
	ID    string `json:"id"`
	Label string `json:"label"`
	Desc  string `json:"desc"`
}

var targetInfos = []TargetInfo{
	{targetSystem, "系统代理", "「设置 → 网络和 Internet → 代理」里的那个，浏览器和大多数软件都走它"},
	{targetEnv, "环境变量", "写入 HTTP_PROXY / HTTPS_PROXY / NO_PROXY，命令行工具（curl、go、pip 等）会用；新开的终端才生效"},
	{targetGit, "git", "设置 git 全局代理（需要电脑上装了 git）"},
	{targetNpm, "npm / pnpm", "写入用户目录下 .npmrc 的代理配置"},
}

type SettingsServer struct {
	backend SettingsBackend
	logf    func(format string, args ...any)

	mu    sync.Mutex
	token string
	ln    net.Listener
	srv   *http.Server
	url   string
}

func newSettingsServer(backend SettingsBackend, logf func(string, ...any)) *SettingsServer {
	if logf == nil {
		logf = func(string, ...any) {}
	}
	return &SettingsServer{backend: backend, logf: logf}
}

// Start 启动服务（已启动则直接返回），返回带 token 的页面地址。
func (s *SettingsServer) Start() (string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.ln != nil {
		return s.url, nil
	}
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return "", fmt.Errorf("无法监听本地端口: %v", err)
	}
	b := make([]byte, 16)
	if _, err := rand.Read(b); err != nil {
		ln.Close()
		return "", err
	}
	s.token = hex.EncodeToString(b)
	s.ln = ln
	port := ln.Addr().(*net.TCPAddr).Port
	s.url = fmt.Sprintf("http://127.0.0.1:%d/?token=%s", port, s.token)

	mux := http.NewServeMux()
	mux.HandleFunc("/", s.handlePage)
	mux.HandleFunc("/api/state", s.handleState)
	mux.HandleFunc("/api/config", s.handleConfig)
	mux.HandleFunc("/api/action", s.handleAction)
	mux.HandleFunc("/api/autostart", s.handleAutostart)
	mux.HandleFunc("/api/test", s.handleTest)
	s.srv = &http.Server{
		Handler:           s.guard(mux),
		ReadHeaderTimeout: 10 * time.Second,
	}
	go func() {
		if err := s.srv.Serve(ln); err != nil && !errors.Is(err, http.ErrServerClosed) {
			s.logf("设置服务退出: %v", err)
		}
	}()
	s.logf("设置页面服务已启动: http://127.0.0.1:%d/", port)
	return s.url, nil
}

// URL 返回页面地址（未启动时为空）。
func (s *SettingsServer) URL() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.url
}

func (s *SettingsServer) Stop() {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.srv != nil {
		_ = s.srv.Close()
		s.srv = nil
		s.ln = nil
		s.url = ""
	}
}

// guard 做统一的安全校验：只接受本机 Host、必须带正确 token、拒绝跨站请求。
func (s *SettingsServer) guard(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		host := r.Host
		if h, _, err := net.SplitHostPort(host); err == nil {
			host = h
		}
		if host != "127.0.0.1" && host != "localhost" && host != "[::1]" && host != "::1" {
			http.Error(w, "forbidden", http.StatusForbidden)
			return
		}
		if site := r.Header.Get("Sec-Fetch-Site"); site != "" && site != "same-origin" && site != "none" {
			http.Error(w, "forbidden", http.StatusForbidden)
			return
		}
		token := r.Header.Get("X-Token")
		if token == "" {
			token = r.URL.Query().Get("token")
		}
		s.mu.Lock()
		ok := token != "" && token == s.token
		s.mu.Unlock()
		if !ok {
			if r.URL.Path == "/" {
				w.Header().Set("Content-Type", "text/html; charset=utf-8")
				w.WriteHeader(http.StatusForbidden)
				io.WriteString(w, "<!doctype html><meta charset=utf-8><title>ProxySwitch</title>"+
					"<body style='font-family:system-ui,\"Microsoft YaHei\",sans-serif;padding:40px;color:#374151'>"+
					"<h2>链接已失效</h2><p>请右键托盘图标 → 「设置…」重新打开设置页面。</p>")
				return
			}
			http.Error(w, "forbidden", http.StatusForbidden)
			return
		}
		w.Header().Set("Cache-Control", "no-store")
		w.Header().Set("X-Content-Type-Options", "nosniff")
		w.Header().Set("Referrer-Policy", "no-referrer")
		next.ServeHTTP(w, r)
	})
}

func (s *SettingsServer) handlePage(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path != "/" {
		http.NotFound(w, r)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Content-Security-Policy", "default-src 'none'; script-src 'unsafe-inline'; style-src 'unsafe-inline'; img-src data:; connect-src 'self'; form-action 'none'; base-uri 'none'")
	io.WriteString(w, settingsPageHTML)
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

func writeErr(w http.ResponseWriter, status int, msg string) {
	writeJSON(w, status, map[string]string{"error": msg})
}

func (s *SettingsServer) handleState(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeErr(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	writeJSON(w, http.StatusOK, s.backend.SettingsState())
}

func (s *SettingsServer) handleConfig(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPut && r.Method != http.MethodPost {
		writeErr(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	body, err := io.ReadAll(io.LimitReader(r.Body, 1<<20))
	if err != nil {
		writeErr(w, http.StatusBadRequest, "读取请求失败")
		return
	}
	// 用与配置文件完全相同的解析/校验逻辑，保证页面保存的内容和手写文件一致
	cfg, err := parseConfig(string(body))
	if err != nil {
		writeErr(w, http.StatusBadRequest, err.Error())
		return
	}
	if err := s.backend.SaveConfig(cfg); err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, s.backend.SettingsState())
}

func (s *SettingsServer) handleAction(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeErr(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	var req struct {
		Action string `json:"action"`
		Name   string `json:"name"`
	}
	if err := json.NewDecoder(io.LimitReader(r.Body, 1<<16)).Decode(&req); err != nil {
		writeErr(w, http.StatusBadRequest, "请求格式错误")
		return
	}
	switch req.Action {
	case "toggle", "on", "off", "use":
	default:
		writeErr(w, http.StatusBadRequest, "未知操作")
		return
	}
	if err := s.backend.DoAction(req.Action, req.Name); err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, s.backend.SettingsState())
}

func (s *SettingsServer) handleAutostart(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeErr(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	var req struct {
		Enabled bool `json:"enabled"`
	}
	if err := json.NewDecoder(io.LimitReader(r.Body, 1<<16)).Decode(&req); err != nil {
		writeErr(w, http.StatusBadRequest, "请求格式错误")
		return
	}
	if err := s.backend.SetAutostart(req.Enabled); err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, s.backend.SettingsState())
}

// handleTest 测试代理服务器端口是否能连上 / PAC 地址是否能下载。
func (s *SettingsServer) handleTest(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeErr(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	var req struct {
		Server string `json:"server"`
		PAC    string `json:"pac"`
	}
	if err := json.NewDecoder(io.LimitReader(r.Body, 1<<16)).Decode(&req); err != nil {
		writeErr(w, http.StatusBadRequest, "请求格式错误")
		return
	}
	type result struct {
		OK      bool   `json:"ok"`
		Message string `json:"message"`
		Millis  int64  `json:"millis"`
	}
	start := time.Now()
	switch {
	case strings.TrimSpace(req.Server) != "":
		hostport := testTarget(req.Server)
		if hostport == "" {
			writeJSON(w, http.StatusOK, result{OK: false, Message: "地址格式不对，应为 主机:端口"})
			return
		}
		conn, err := net.DialTimeout("tcp", hostport, 3*time.Second)
		if err != nil {
			writeJSON(w, http.StatusOK, result{OK: false, Message: "连不上 " + hostport + "：" + shortNetErr(err), Millis: time.Since(start).Milliseconds()})
			return
		}
		conn.Close()
		writeJSON(w, http.StatusOK, result{OK: true, Message: hostport + " 端口可以连通", Millis: time.Since(start).Milliseconds()})
	case strings.TrimSpace(req.PAC) != "":
		client := &http.Client{Timeout: 5 * time.Second, Transport: &http.Transport{Proxy: nil}}
		resp, err := client.Get(strings.TrimSpace(req.PAC))
		if err != nil {
			writeJSON(w, http.StatusOK, result{OK: false, Message: "下载 PAC 失败：" + shortNetErr(err), Millis: time.Since(start).Milliseconds()})
			return
		}
		defer resp.Body.Close()
		data, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
		if resp.StatusCode != http.StatusOK {
			writeJSON(w, http.StatusOK, result{OK: false, Message: fmt.Sprintf("PAC 地址返回 HTTP %d", resp.StatusCode), Millis: time.Since(start).Milliseconds()})
			return
		}
		if !strings.Contains(string(data), "FindProxyForURL") {
			writeJSON(w, http.StatusOK, result{OK: false, Message: "能下载，但内容看起来不是 PAC 脚本（没有 FindProxyForURL）", Millis: time.Since(start).Milliseconds()})
			return
		}
		writeJSON(w, http.StatusOK, result{OK: true, Message: fmt.Sprintf("PAC 脚本可以下载（%d 字节）", len(data)), Millis: time.Since(start).Milliseconds()})
	default:
		writeErr(w, http.StatusBadRequest, "没有可测试的地址")
	}
}

// testTarget 从 server 字段里挑出用于连通性测试的 host:port。
func testTarget(server string) string {
	u := serverToURL(server)
	if u == "" {
		return ""
	}
	if i := strings.Index(u, "://"); i >= 0 {
		u = u[i+3:]
	}
	u = strings.TrimSuffix(u, "/")
	if _, _, err := net.SplitHostPort(u); err != nil {
		return ""
	}
	return u
}

func shortNetErr(err error) string {
	msg := err.Error()
	switch {
	case strings.Contains(msg, "refused"):
		return "连接被拒绝（端口没有程序在监听）"
	case strings.Contains(msg, "timeout") || strings.Contains(msg, "i/o timeout"):
		return "连接超时"
	case strings.Contains(msg, "no such host"):
		return "域名无法解析"
	}
	if i := strings.LastIndex(msg, ": "); i >= 0 {
		return msg[i+2:]
	}
	return msg
}

// ---------- 配置文件写入 ----------

// marshalConfigFile 把配置序列化成配置文件内容（带 BOM 和一行说明注释，parseConfig 都能处理）。
func marshalConfigFile(cfg *Config) ([]byte, error) {
	data, err := json.MarshalIndent(cfg, "", "  ")
	if err != nil {
		return nil, err
	}
	header := "// ProxySwitch 配置文件。推荐用托盘菜单「设置…」图形化修改；也可以手改，允许 // 注释。\n"
	return []byte("\xEF\xBB\xBF" + header + string(data) + "\n"), nil
}

// writeConfigFile 原子地写入配置文件（先写临时文件再改名）。
func writeConfigFile(path string, cfg *Config) error {
	data, err := marshalConfigFile(cfg)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, data, 0o644); err != nil {
		return err
	}
	if err := os.Rename(tmp, path); err != nil {
		_ = os.Remove(tmp)
		// Windows 上目标文件被别的程序打开时 rename 可能失败，退回到直接覆盖
		return os.WriteFile(path, data, 0o644)
	}
	return nil
}
