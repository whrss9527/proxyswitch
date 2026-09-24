package main

import (
	"bytes"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"time"
)

// Core 管理 mihomo 内核进程。引擎用 Sync 给出期望的状态（CoreSettings），由一个后台 goroutine 串行地
// 启动、重新加载或停止内核，不阻塞 UI 线程；需要结果的调用方用 Wait 等待。内核只在本机监听，
// REST API 用每次启动随机生成的地址和密码。节点列表、切换、测速直接调用 REST API。

const (
	coreStartTimeout  = 15 * time.Second
	coreApiTimeout    = 10 * time.Second
	coreDelayTimeout  = 5 * time.Second
	coreCrashWindow   = time.Minute
	coreMaxCrashes    = 3
	coreRestartDelay  = 2 * time.Second
	coreLogName       = "core.log"
	coreConfigName    = "config.yaml"
	coreLogTailLength = 600
)

var errCoreMissing = errors.New("还没有下载代理内核")

// coreBinaryName 是内核程序的文件名。
func coreBinaryName() string {
	if runtime.GOOS == "windows" {
		return "mihomo.exe"
	}
	return "mihomo"
}

// CoreStatus 是内核的运行状态，给设置页显示。
type CoreStatus struct {
	Running bool   `json:"running"`
	Port    int    `json:"port,omitempty"`
	Error   string `json:"error,omitempty"`
}

// CoreNode 是订阅里的一个节点。Tested 表示测过延迟，Alive 是最近一次测试是否成功（本机测得的延迟可能是 0）。
type CoreNode struct {
	Name   string `json:"name"`
	Type   string `json:"type"`
	Delay  int    `json:"delay"`
	Tested bool   `json:"tested"`
	Alive  bool   `json:"alive"`
}

// CoreNodes 是一个订阅的节点列表。Selected 是选中的节点，空表示自动选择；Current 是实际在用的节点。
type CoreNodes struct {
	Nodes    []CoreNode `json:"nodes"`
	Selected string     `json:"selected"`
	Current  string     `json:"current"`
}

type Core struct {
	mutex      sync.Mutex
	applied    *sync.Cond
	wake       chan struct{}
	wanted     CoreSettings
	generation int
	done       int
	doneErr    error
	onError    func(message string)
	client     *http.Client

	// 以下只由后台 goroutine 修改，读取时持有 mutex。
	process    *exec.Cmd
	exited     chan struct{}
	controller string
	secret     string
	current    CoreSettings
	configText []byte
	lastError  string
	crashes    []time.Time
}

// newCore 创建内核管理器并启动后台 goroutine。onError 在内核启动失败或意外退出时调用（在后台 goroutine 里）。
func newCore(onError func(message string)) *Core {
	core := &Core{wake: make(chan struct{}, 1), onError: onError, client: &http.Client{Transport: &http.Transport{Proxy: nil}}}
	core.applied = sync.NewCond(&core.mutex)
	go core.run()
	return core
}

// Sync 设定内核应该处于的状态，立即返回。返回值交给 Wait 等待这次设定生效。
func (core *Core) Sync(settings CoreSettings) int {
	// 复制订阅列表：调用方之后修改自己的切片不能影响内核记住的状态。
	settings.Subscriptions = append([]CoreSubscription(nil), settings.Subscriptions...)
	core.mutex.Lock()
	core.wanted = settings
	core.generation++
	generation := core.generation
	core.mutex.Unlock()
	select {
	case core.wake <- struct{}{}:
	default:
	}
	return generation
}

// Wait 等待第 generation 次 Sync（或更新的设定）生效，返回生效时的错误。
func (core *Core) Wait(generation int, timeout time.Duration) error {
	deadline := time.Now().Add(timeout)
	timer := time.AfterFunc(timeout, func() {
		core.mutex.Lock()
		core.applied.Broadcast()
		core.mutex.Unlock()
	})
	defer timer.Stop()
	core.mutex.Lock()
	defer core.mutex.Unlock()
	for core.done < generation {
		if !time.Now().Before(deadline) {
			return errors.New("代理内核没有及时响应")
		}
		core.applied.Wait()
	}
	return core.doneErr
}

// Stop 停止内核并等待它退出。
func (core *Core) Stop() {
	_ = core.Wait(core.Sync(CoreSettings{}), coreStartTimeout)
}

// Kill 立即结束内核进程，不经过后台 goroutine，程序退出时调用：这时后台 goroutine 可能正等着 UI 线程。
func (core *Core) Kill() {
	core.mutex.Lock()
	command := core.process
	core.process = nil
	core.mutex.Unlock()
	if command != nil {
		_ = command.Process.Kill()
	}
}

func (core *Core) Status() CoreStatus {
	core.mutex.Lock()
	defer core.mutex.Unlock()
	status := CoreStatus{Running: core.process != nil, Error: core.lastError}
	if status.Running {
		status.Port = core.current.Port
	}
	return status
}

func (core *Core) run() {
	for range core.wake {
		core.mutex.Lock()
		settings, generation := core.wanted, core.generation
		core.mutex.Unlock()
		err := core.apply(settings)
		message := ""
		if err != nil {
			message = err.Error()
			slog.Warn("代理内核出错", "err", err)
		}
		core.mutex.Lock()
		reported := core.lastError
		core.lastError = message
		core.mutex.Unlock()
		// 先通知再标记完成：等待这次设定的调用方返回时，通知已经发出。同样的错误只通知一次。
		if message != "" && message != reported && core.onError != nil {
			core.onError(message)
		}
		core.mutex.Lock()
		core.done, core.doneErr = generation, err
		core.applied.Broadcast()
		core.mutex.Unlock()
	}
}

// apply 让内核进入 settings 描述的状态：没有订阅时停止；程序、目录、端口变了就重启；配置变了就重新加载；
// 订阅文件更新了就让内核重新读取；最后设置每个订阅选中的节点和正在使用的订阅。
func (core *Core) apply(settings CoreSettings) error {
	if len(settings.Subscriptions) == 0 {
		core.stopProcess()
		return nil
	}
	if !core.running() || core.current.Binary != settings.Binary || core.current.BinaryStamp != settings.BinaryStamp || core.current.Dir != settings.Dir || core.current.Port != settings.Port {
		core.stopProcess()
		if err := core.checkCrashes(); err != nil {
			return err
		}
		if err := core.start(settings); err != nil {
			return err
		}
	} else if text := coreConfigText(settings, core.controller, core.secret); !bytes.Equal(text, core.configText) {
		if err := core.reload(text, settings); err != nil {
			return err
		}
	} else {
		for _, subscription := range settings.Subscriptions {
			if subscription.Revision != core.revisionOf(subscription.Id) {
				if err := core.request(http.MethodPut, "/providers/proxies/"+url.PathEscape(coreProviderName(subscription.Id)), nil, nil, coreApiTimeout); err != nil {
					return fmt.Errorf("重新读取订阅失败：%v", err)
				}
			}
		}
	}
	core.mutex.Lock()
	core.current = settings
	core.mutex.Unlock()
	return core.applySelections(settings)
}

func (core *Core) running() bool {
	core.mutex.Lock()
	defer core.mutex.Unlock()
	return core.process != nil
}

func (core *Core) revisionOf(profileId string) string {
	for _, subscription := range core.current.Subscriptions {
		if subscription.Id == profileId {
			return subscription.Revision
		}
	}
	return ""
}

// checkCrashes 在内核短时间内反复退出时不再自动重启，免得一直循环。
func (core *Core) checkCrashes() error {
	now := time.Now()
	core.mutex.Lock()
	recent := core.crashes[:0]
	for _, when := range core.crashes {
		if now.Sub(when) < coreCrashWindow {
			recent = append(recent, when)
		}
	}
	core.crashes = recent
	count := len(recent)
	core.mutex.Unlock()
	if count >= coreMaxCrashes {
		return fmt.Errorf("代理内核一分钟内退出了 %d 次，已停止自动重启：%s", count, core.logTail())
	}
	return nil
}

func (core *Core) start(settings CoreSettings) error {
	if !fileExists(settings.Binary) {
		return errCoreMissing
	}
	if err := os.MkdirAll(settings.Dir, 0o755); err != nil {
		return fmt.Errorf("无法创建内核的工作目录：%v", err)
	}
	if filepath.Dir(settings.Binary) == filepath.Clean(settings.Dir) {
		removeOldCorePrograms(settings.Binary)
	}
	if err := checkPortFree(settings.Port); err != nil {
		return err
	}
	apiPort, err := freeLocalPort()
	if err != nil {
		return err
	}
	secret := make([]byte, 16)
	if _, err := rand.Read(secret); err != nil {
		return err
	}
	controller := "127.0.0.1:" + strconv.Itoa(apiPort)
	secretText := hex.EncodeToString(secret)
	text := coreConfigText(settings, controller, secretText)
	configPath := filepath.Join(settings.Dir, coreConfigName)
	if err := os.WriteFile(configPath, text, 0o600); err != nil {
		return fmt.Errorf("无法写入内核配置：%v", err)
	}
	logFile, err := os.Create(filepath.Join(settings.Dir, coreLogName))
	if err != nil {
		return fmt.Errorf("无法创建内核日志：%v", err)
	}
	command := exec.Command(settings.Binary, "-d", settings.Dir, "-f", configPath)
	command.Dir = settings.Dir
	command.Stdout, command.Stderr = logFile, logFile
	prepareCoreCommand(command)
	if err := command.Start(); err != nil {
		logFile.Close()
		return fmt.Errorf("无法启动代理内核：%v", err)
	}
	attachCoreProcess(command.Process)
	exited := make(chan struct{})
	core.mutex.Lock()
	core.process, core.exited, core.controller, core.secret = command, exited, controller, secretText
	core.configText = nil
	core.mutex.Unlock()
	go core.watch(command, exited, logFile)
	slog.Info("代理内核已启动", "pid", command.Process.Pid, "port", settings.Port)

	if err := core.waitReady(exited, settings); err != nil {
		core.stopProcess()
		return err
	}
	core.mutex.Lock()
	core.configText = text
	core.mutex.Unlock()
	return nil
}

// watch 等待内核退出；不是 ProxySwitch 让它退出的，就记一次崩溃并让后台 goroutine 重新启动它。
func (core *Core) watch(command *exec.Cmd, exited chan struct{}, logFile *os.File) {
	err := command.Wait()
	logFile.Close()
	close(exited)
	core.mutex.Lock()
	unexpected := core.process == command
	if unexpected {
		core.process = nil
		core.crashes = append(core.crashes, time.Now())
	}
	core.mutex.Unlock()
	if unexpected {
		slog.Warn("代理内核意外退出", "err", err, "log", core.logTail())
		time.AfterFunc(coreRestartDelay, func() {
			select {
			case core.wake <- struct{}{}:
			default:
			}
		})
	}
}

// waitReady 等 REST API 可用、所有订阅都已加载、代理端口已在监听。
func (core *Core) waitReady(exited chan struct{}, settings CoreSettings) error {
	deadline := time.Now().Add(coreStartTimeout)
	for {
		if core.request(http.MethodGet, "/version", nil, nil, time.Second) == nil {
			break
		}
		select {
		case <-exited:
			return fmt.Errorf("代理内核启动后立即退出了：%s", core.logTail())
		case <-time.After(50 * time.Millisecond):
		}
		if time.Now().After(deadline) {
			return fmt.Errorf("代理内核启动超时：%s", core.logTail())
		}
	}
	if err := core.waitProviders(settings, deadline); err != nil {
		return err
	}
	connection, err := net.DialTimeout("tcp", "127.0.0.1:"+strconv.Itoa(settings.Port), 2*time.Second)
	if err != nil {
		return fmt.Errorf("代理内核没能监听端口 %d：%s", settings.Port, core.logTail())
	}
	connection.Close()
	return nil
}

// waitProviders 等内核加载完所有订阅：REST API 比订阅先就绪，这时列出的节点还不完整。
func (core *Core) waitProviders(settings CoreSettings, deadline time.Time) error {
	for {
		var result struct {
			Providers map[string]json.RawMessage `json:"providers"`
		}
		err := core.request(http.MethodGet, "/providers/proxies", nil, &result, coreApiTimeout)
		if err == nil {
			missing := false
			for _, subscription := range settings.Subscriptions {
				if _, found := result.Providers[coreProviderName(subscription.Id)]; !found {
					missing = true
				}
			}
			if !missing {
				return nil
			}
		}
		if time.Now().After(deadline) {
			return fmt.Errorf("代理内核没能加载订阅：%s", core.logTail())
		}
		time.Sleep(50 * time.Millisecond)
	}
}

func (core *Core) reload(text []byte, settings CoreSettings) error {
	configPath := filepath.Join(settings.Dir, coreConfigName)
	if err := os.WriteFile(configPath, text, 0o600); err != nil {
		return fmt.Errorf("无法写入内核配置：%v", err)
	}
	// 不加 force：端口没变时内核保留原来的监听，不打断正在进行的连接（端口变了会重启内核）。
	if err := core.request(http.MethodPut, "/configs", map[string]string{"path": configPath}, nil, coreStartTimeout); err != nil {
		return fmt.Errorf("代理内核没能加载新配置：%v", err)
	}
	if err := core.waitProviders(settings, time.Now().Add(coreStartTimeout)); err != nil {
		return err
	}
	core.mutex.Lock()
	core.configText = text
	core.mutex.Unlock()
	return nil
}

// applySelections 设置每个订阅选中的节点（选中的节点已不在订阅里时改用自动选择）和正在使用的订阅。
func (core *Core) applySelections(settings CoreSettings) error {
	for _, subscription := range settings.Subscriptions {
		target := subscription.Node
		if target == "" {
			target = coreAutoGroup(subscription.Id)
		}
		if err := core.selectIn(subscription.Id, target); err != nil && target != coreAutoGroup(subscription.Id) {
			slog.Warn("选中的节点不在订阅里，改用自动选择", "profile", subscription.Id, "node", target)
			_ = core.selectIn(subscription.Id, coreAutoGroup(subscription.Id))
		}
	}
	if settings.Active != "" {
		if err := core.selectIn(coreTopGroup, settings.Active); err != nil {
			return fmt.Errorf("代理内核没能切换订阅：%v", err)
		}
	}
	return nil
}

// selectIn 在选择组 group 里选中 name，已经选中时不再调用。
func (core *Core) selectIn(group, name string) error {
	var current struct {
		Now string `json:"now"`
	}
	if err := core.request(http.MethodGet, "/proxies/"+url.PathEscape(group), nil, &current, coreApiTimeout); err == nil && current.Now == name {
		return nil
	}
	return core.request(http.MethodPut, "/proxies/"+url.PathEscape(group), map[string]string{"name": name}, nil, coreApiTimeout)
}

func (core *Core) stopProcess() {
	core.mutex.Lock()
	command, exited := core.process, core.exited
	core.process, core.configText = nil, nil
	core.mutex.Unlock()
	if command == nil {
		return
	}
	_ = command.Process.Kill()
	select {
	case <-exited:
	case <-time.After(5 * time.Second):
		slog.Warn("代理内核没有及时退出")
	}
	slog.Info("代理内核已停止")
}

// Nodes 列出订阅里的节点和最近一次测得的延迟。
func (core *Core) Nodes(profileId string) (CoreNodes, error) {
	var provider struct {
		Proxies []struct {
			Name    string `json:"name"`
			Type    string `json:"type"`
			Alive   bool   `json:"alive"`
			History []struct {
				Delay int `json:"delay"`
			} `json:"history"`
		} `json:"proxies"`
	}
	if err := core.request(http.MethodGet, "/providers/proxies/"+url.PathEscape(coreProviderName(profileId)), nil, &provider, coreApiTimeout); err != nil {
		return CoreNodes{}, err
	}
	result := CoreNodes{Nodes: []CoreNode{}}
	for _, proxy := range provider.Proxies {
		node := CoreNode{Name: proxy.Name, Type: proxy.Type, Alive: proxy.Alive}
		if count := len(proxy.History); count > 0 {
			node.Tested, node.Delay = true, proxy.History[count-1].Delay
		}
		result.Nodes = append(result.Nodes, node)
	}
	var group struct {
		Now string `json:"now"`
	}
	if err := core.request(http.MethodGet, "/proxies/"+url.PathEscape(profileId), nil, &group, coreApiTimeout); err != nil {
		return CoreNodes{}, err
	}
	result.Selected, result.Current = group.Now, group.Now
	if group.Now == coreAutoGroup(profileId) {
		result.Selected = ""
		if err := core.request(http.MethodGet, "/proxies/"+url.PathEscape(coreAutoGroup(profileId)), nil, &group, coreApiTimeout); err == nil {
			result.Current = group.Now
		}
	}
	return result, nil
}

// Select 立即在内核里选中节点（空表示自动选择）。配置文件里的记录由引擎负责。
func (core *Core) Select(profileId, node string) error {
	target := node
	if target == "" {
		target = coreAutoGroup(profileId)
	}
	return core.selectIn(profileId, target)
}

// TestDelays 测试订阅里所有节点的延迟，返回节点名到毫秒数，测不通的节点不在结果里。
// 测的是自动选择组，测完它会改选延迟最低的节点。
func (core *Core) TestDelays(profileId, testUrl string) (map[string]int, error) {
	query := url.Values{"url": {testUrl}, "timeout": {strconv.Itoa(int(coreDelayTimeout / time.Millisecond))}}
	result := map[string]int{}
	err := core.request(http.MethodGet, "/group/"+url.PathEscape(coreAutoGroup(profileId))+"/delay?"+query.Encode(), nil, &result, coreDelayTimeout+5*time.Second)
	return result, err
}

// request 调用内核的 REST API。body 不为空时以 JSON 发送，result 不为空时把响应解析进去。
func (core *Core) request(method, path string, body, result any, timeout time.Duration) error {
	core.mutex.Lock()
	controller, secret, running := core.controller, core.secret, core.process != nil
	core.mutex.Unlock()
	if !running || controller == "" {
		return errors.New("代理内核没有运行")
	}
	var reader io.Reader
	if body != nil {
		data, _ := json.Marshal(body)
		reader = bytes.NewReader(data)
	}
	request, err := http.NewRequest(method, "http://"+controller+path, reader)
	if err != nil {
		return err
	}
	request.Header.Set("Authorization", "Bearer "+secret)
	if body != nil {
		request.Header.Set("Content-Type", "application/json")
	}
	client := *core.client
	client.Timeout = timeout
	response, err := client.Do(request)
	if err != nil {
		return err
	}
	defer response.Body.Close()
	data, err := io.ReadAll(io.LimitReader(response.Body, 32<<20))
	if err != nil {
		return err
	}
	if response.StatusCode >= 300 {
		var failure struct {
			Message string `json:"message"`
		}
		if json.Unmarshal(data, &failure) == nil && failure.Message != "" {
			return errors.New(failure.Message)
		}
		return fmt.Errorf("HTTP %d", response.StatusCode)
	}
	if result != nil && len(data) > 0 {
		return json.Unmarshal(data, result)
	}
	return nil
}

// logTail 返回内核日志的最后几行，用来说明启动失败的原因。
func (core *Core) logTail() string {
	core.mutex.Lock()
	dir := core.wanted.Dir
	if core.current.Dir != "" {
		dir = core.current.Dir
	}
	core.mutex.Unlock()
	data, err := os.ReadFile(filepath.Join(dir, coreLogName))
	text := strings.TrimSpace(string(data))
	if err != nil || text == "" {
		return "没有输出"
	}
	if len(text) > coreLogTailLength {
		text = "…" + text[len(text)-coreLogTailLength:]
	}
	return text
}

func freeLocalPort() (int, error) {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return 0, err
	}
	defer listener.Close()
	return listener.Addr().(*net.TCPAddr).Port, nil
}

// checkPortFree 在启动内核前确认代理端口没有被其他程序占用，给出比内核日志更清楚的提示。
func checkPortFree(port int) error {
	listener, err := net.Listen("tcp", "127.0.0.1:"+strconv.Itoa(port))
	if err != nil {
		return fmt.Errorf("端口 %d 已被其他程序占用，请在「常规」里给代理内核换一个端口", port)
	}
	listener.Close()
	return nil
}
