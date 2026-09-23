package main

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"net/url"
	"strings"
	"testing"
	"time"
)

type settingsFixture struct {
	backend *devBackend
	server  *SettingsServer
	base    string
	token   string
}

func newSettingsFixture(t *testing.T, configText string) *settingsFixture {
	t.Helper()
	paths := pathsIn(t.TempDir(), false)
	if configText != "" {
		config, err := parseConfig(configText)
		if err != nil {
			t.Fatal(err)
		}
		if err := writeConfigFile(paths.Config, config); err != nil {
			t.Fatal(err)
		}
	}
	httpProxy, socks, _ := startTestFakes(t)
	backend := newDevBackend(paths, httpProxy, socks)
	server := newSettingsServer(backend)
	address, err := server.Start()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(server.Stop)
	parsed, _ := url.Parse(address)
	return &settingsFixture{backend: backend, server: server, base: "http://" + parsed.Host, token: parsed.Query().Get("token")}
}

func (fixture *settingsFixture) request(t *testing.T, method, path string, body any, headers map[string]string) (int, []byte) {
	t.Helper()
	var reader io.Reader
	switch value := body.(type) {
	case nil:
	case string:
		reader = strings.NewReader(value)
	default:
		data, _ := json.Marshal(value)
		reader = bytes.NewReader(data)
	}
	request, _ := http.NewRequest(method, fixture.base+path, reader)
	request.Header.Set("X-Token", fixture.token)
	for key, value := range headers {
		if value == "" {
			request.Header.Del(key)
		} else {
			request.Header.Set(key, value)
		}
	}
	response, err := (&http.Client{Timeout: 10 * time.Second}).Do(request)
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	data, _ := io.ReadAll(response.Body)
	return response.StatusCode, data
}

func (fixture *settingsFixture) state(t *testing.T, data []byte) SettingsState {
	t.Helper()
	var state SettingsState
	if err := json.Unmarshal(data, &state); err != nil {
		t.Fatalf("状态不是合法 JSON：%v\n%s", err, data)
	}
	return state
}

func TestSettingsGuard(t *testing.T) {
	fixture := newSettingsFixture(t, "")
	if status, _ := fixture.request(t, "GET", "/api/state", nil, map[string]string{"X-Token": ""}); status != http.StatusForbidden {
		t.Errorf("没有 token 应拒绝，得到 %d", status)
	}
	if status, _ := fixture.request(t, "GET", "/api/state", nil, map[string]string{"X-Token": "wrong"}); status != http.StatusForbidden {
		t.Errorf("token 错误应拒绝，得到 %d", status)
	}
	if status, _ := fixture.request(t, "GET", "/api/state", nil, map[string]string{"Sec-Fetch-Site": "cross-site"}); status != http.StatusForbidden {
		t.Errorf("跨站请求应拒绝，得到 %d", status)
	}
	request, _ := http.NewRequest("GET", fixture.base+"/api/state", nil)
	request.Host = "evil.example.com"
	request.Header.Set("X-Token", fixture.token)
	response, err := http.DefaultClient.Do(request)
	if err != nil {
		t.Fatal(err)
	}
	response.Body.Close()
	if response.StatusCode != http.StatusForbidden {
		t.Errorf("Host 不是本机应拒绝（防 DNS 重绑定），得到 %d", response.StatusCode)
	}
	// 页面和静态文件不含数据，刷新页面时地址里已经没有 token，也要能打开。
	page, err := http.Get(fixture.base + "/")
	if err != nil {
		t.Fatal(err)
	}
	body, _ := io.ReadAll(page.Body)
	page.Body.Close()
	if page.StatusCode != http.StatusOK || !strings.Contains(string(body), "<title>ProxySwitch 设置</title>") {
		t.Errorf("应能打开页面，得到 %d", page.StatusCode)
	}
	if !strings.Contains(page.Header.Get("Content-Security-Policy"), "script-src 'self'") {
		t.Error("页面应带 CSP")
	}
	for _, asset := range []string{"/assets/settings.css", "/assets/core.js", "/assets/app.js"} {
		response, err := http.Get(fixture.base + asset)
		if err != nil {
			t.Fatal(err)
		}
		response.Body.Close()
		if response.StatusCode != http.StatusOK {
			t.Errorf("%s 应能访问，得到 %d", asset, response.StatusCode)
		}
	}
	for _, missing := range []string{"/assets/settings.html", "/assets/nothing.js", "/assets/../settings.go"} {
		response, err := http.Get(fixture.base + missing)
		if err != nil {
			t.Fatal(err)
		}
		response.Body.Close()
		if response.StatusCode == http.StatusOK {
			t.Errorf("%s 不应能访问", missing)
		}
	}
	crossSite, _ := http.NewRequest("GET", fixture.base+"/", nil)
	crossSite.Header.Set("Sec-Fetch-Site", "cross-site")
	response, err = http.DefaultClient.Do(crossSite)
	if err != nil {
		t.Fatal(err)
	}
	response.Body.Close()
	if response.StatusCode != http.StatusForbidden {
		t.Error("其他网站打开页面应拒绝")
	}
}

func TestSettingsConfigFlow(t *testing.T) {
	fixture := newSettingsFixture(t, "")
	status, data := fixture.request(t, "GET", "/api/state", nil, nil)
	state := fixture.state(t, data)
	if status != http.StatusOK || state.Config == nil || len(state.Config.Profiles) != 0 || state.Platform != "dev" {
		t.Fatalf("初始状态不对：%d %s", status, data)
	}

	config := *state.Config
	config.Profiles = []Profile{
		{Name: "本机", Server: "127.0.0.1:7890", ApplyTo: []string{"system", "git"}},
		{Name: "公司", Server: "10.0.0.1:8080"},
	}
	status, data = fixture.request(t, "PUT", "/api/config", config, nil)
	if status != http.StatusOK {
		t.Fatalf("保存失败：%d %s", status, data)
	}
	state = fixture.state(t, data)
	if len(state.Config.Profiles) != 2 || state.Config.Profiles[1].Color != profilePalette[1] || state.Config.Profiles[0].Id == "" {
		t.Errorf("保存后应补全颜色和 id：%+v", state.Config.Profiles)
	}

	status, data = fixture.request(t, "PUT", "/api/config", `{"profiles": [{"name": "坏", "server": "x"}]}`, nil)
	if status != http.StatusBadRequest || !strings.Contains(string(data), "主机:端口") {
		t.Errorf("非法配置应返回 400 和原因：%d %s", status, data)
	}
	status, data = fixture.request(t, "POST", "/api/validate", `{"profiles": [{"name": "好", "server": "a:1"}]}`, nil)
	if status != http.StatusOK || !strings.Contains(string(data), `"name":"好"`) {
		t.Errorf("校验接口应返回规范化后的配置：%d %s", status, data)
	}

	status, data = fixture.request(t, "POST", "/api/use", map[string]string{"name": "本机"}, nil)
	state = fixture.state(t, data)
	if status != http.StatusOK || state.Status.State != statusOn || state.Status.Profile != "本机" {
		t.Errorf("切换配置失败：%d %+v", status, state.Status)
	}
	if fixture.backend.system.Git != "http://127.0.0.1:7890" {
		t.Errorf("git 代理应已设置：%q", fixture.backend.system.Git)
	}
	status, data = fixture.request(t, "POST", "/api/off", nil, nil)
	if state = fixture.state(t, data); status != http.StatusOK || state.Status.State != statusOff {
		t.Errorf("关闭失败：%d %+v", status, state.Status)
	}
	status, data = fixture.request(t, "POST", "/api/use", map[string]string{"name": "没有"}, nil)
	if status != http.StatusConflict || !strings.Contains(string(data), "没有名为") || !strings.Contains(string(data), `"state"`) {
		t.Errorf("操作失败应返回 409、原因和最新状态：%d %s", status, data)
	}
	status, data = fixture.request(t, "POST", "/api/preview", Profile{Server: "socks5://127.0.0.1:1080", ApplyTo: []string{"system", "env"}}, nil)
	if status != http.StatusOK || !strings.Contains(string(data), "socks=127.0.0.1:1080") || !strings.Contains(string(data), "socks5://127.0.0.1:1080") {
		t.Errorf("预览不对：%d %s", status, data)
	}
}

func TestSettingsTestAndDetect(t *testing.T) {
	fixture := newSettingsFixture(t, "")
	httpAddress := fixture.backend.httpProxy.Address()
	status, data := fixture.request(t, "POST", "/api/test", map[string]string{"server": "bad"}, nil)
	if status != http.StatusOK || !strings.Contains(string(data), "地址格式不对") {
		t.Errorf("非法地址应直接提示：%s", data)
	}
	testUrl, stop, _ := startFakeTestServer()
	defer stop()
	config := *fixture.backend.engine.Config()
	config.TestUrl = testUrl
	if status, data := fixture.request(t, "PUT", "/api/config", config, nil); status != http.StatusOK {
		t.Fatalf("保存失败：%s", data)
	}
	status, data = fixture.request(t, "POST", "/api/test", map[string]string{"server": httpAddress}, nil)
	var result TestResult
	_ = json.Unmarshal(data, &result)
	if status != http.StatusOK || !result.Ok {
		t.Errorf("经假代理测速应成功：%s", data)
	}
	status, data = fixture.request(t, "GET", "/api/detect", nil, nil)
	var detected struct {
		Proxies []DetectedProxy `json:"proxies"`
	}
	_ = json.Unmarshal(data, &detected)
	if status != http.StatusOK || len(detected.Proxies) < 2 {
		t.Errorf("应检测到两个假代理：%s", data)
	}
}

func TestSettingsOpenUrlRestricted(t *testing.T) {
	fixture := newSettingsFixture(t, "")
	if status, _ := fixture.request(t, "POST", "/api/open-url", map[string]string{"url": "https://evil.example.com"}, nil); status != http.StatusBadRequest {
		t.Error("只允许打开项目主页")
	}
	if status, _ := fixture.request(t, "POST", "/api/open-url", map[string]string{"url": repositoryUrl + "evil"}, nil); status != http.StatusBadRequest {
		t.Error("前缀相同的其他地址也应拒绝")
	}
	if status, _ := fixture.request(t, "POST", "/api/open-url", map[string]string{"url": repositoryUrl + "/releases"}, nil); status != http.StatusOK {
		t.Error("项目页面应允许打开")
	}
}

func TestCompareVersions(t *testing.T) {
	cases := []struct {
		first, second string
		wanted        int
	}{
		{"2.0.0", "1.9.9", 1},
		{"v1.10.0", "1.9.0", 1},
		{"1.1", "1.1.0", 0},
		{"1.1.1", "1.1.2", -1},
		{"2.0.0-beta", "2.0.0", 0},
	}
	for _, item := range cases {
		if got := compareVersions(item.first, item.second); got != item.wanted {
			t.Errorf("compareVersions(%q, %q) = %d，应为 %d", item.first, item.second, got, item.wanted)
		}
	}
}

func TestDescribeBypass(t *testing.T) {
	if got := describeBypass(defaultBypass); got != "本机、局域网、本地名称" {
		t.Errorf("默认例外描述不对：%s", got)
	}
	if got := describeBypass("*.corp.com;localhost;10.*"); got != "本机、局域网、另外 1 项" {
		t.Errorf("自定义例外描述不对：%s", got)
	}
	if describeBypass("") != "无" {
		t.Error("空例外应描述为无")
	}
}
