package main

import (
	"bufio"
	"bytes"
	"context"
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

// CoreStatus 是内核的运行状态，给设置页显示。Share 是局域网共享入口的状态，Tun 是 TUN 模式的状态。
type CoreStatus struct {
	Running bool            `json:"running"`
	Port    int             `json:"port,omitempty"`
	Error   string          `json:"error,omitempty"`
	Share   CoreShareStatus `json:"share"`
	Tun     CoreTunStatus   `json:"tun"`
}

// CoreTunStatus 是 TUN 模式的状态：Active 表示虚拟网卡正在接管流量；要开却没开起来时 Error 说明原因
// （没有获得管理员权限、不是 Windows 等）。
type CoreTunStatus struct {
	Active bool   `json:"active"`
	Error  string `json:"error,omitempty"`
}

// coreProcess 是运行中的内核：ProxySwitch 直接启动的进程，或者 TUN 模式下以管理员身份运行的宿主进程
// （见 core_windows.go），宿主退出时内核随之结束。
type coreProcess interface {
	Pid() int
	// Wait 等它退出，只调用一次。
	Wait() error
	// Kill 立即结束它。
	Kill() error
	// Elevated 表示内核以管理员权限运行，可以开启 TUN 模式。
	Elevated() bool
}

// localCoreProcess 是直接启动的内核进程，输出写在内核的日志里。
type localCoreProcess struct {
	command *exec.Cmd
	logFile *os.File
}

func (process *localCoreProcess) Pid() int {
	return process.command.Process.Pid
}

func (process *localCoreProcess) Wait() error {
	err := process.command.Wait()
	process.logFile.Close()
	return err
}

func (process *localCoreProcess) Kill() error {
	return process.command.Process.Kill()
}

func (process *localCoreProcess) Elevated() bool {
	return false
}

// errTunDeclined 表示启动 TUN 模式的内核时用户没有同意以管理员身份运行。
var errTunDeclined = errors.New("没有获得管理员权限，TUN 模式没有开启")

var (
	// coreTunSupported 表示可以开启 TUN 模式（以管理员身份运行内核），只有 Windows 版可以。
	coreTunSupported = runtime.GOOS == "windows"
	// startElevatedCore 以管理员身份启动内核（见 core_windows.go），测试时可以换掉。
	startElevatedCore = startElevatedCoreProcess
)

// CoreShareStatus 是局域网共享入口的状态：Listening 表示正在 Port 上监听，没监听起来时 Error 说明原因。
type CoreShareStatus struct {
	Listening bool   `json:"listening"`
	Port      int    `json:"port,omitempty"`
	Error     string `json:"error,omitempty"`
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
	process    coreProcess
	exited     chan struct{}
	controller string
	secret     string
	current    CoreSettings
	configText []byte
	lastError  string
	crashes    []time.Time
	share      CoreShareStatus
	tun        CoreTunStatus
	// tunDeclined 表示上次要以管理员身份启动时被拒绝了：TUN 模式关掉再打开（或代理关掉再开）之前不再询问。
	tunDeclined bool
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
	process := core.process
	core.process = nil
	core.mutex.Unlock()
	if process != nil {
		_ = process.Kill()
	}
}

func (core *Core) Status() CoreStatus {
	core.mutex.Lock()
	defer core.mutex.Unlock()
	status := CoreStatus{Running: core.process != nil, Error: core.lastError, Share: core.share, Tun: core.tun}
	if status.Running {
		status.Port = coreMixedPort(core.current)
	} else {
		status.Share.Listening = false
		status.Tun.Active = false
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

// apply 让内核进入 settings 描述的状态：既没有订阅也没开局域网共享时停止；程序、目录、本机端口变了就重启；
// 配置变了就重新加载；订阅文件更新了就让内核重新读取；最后设置每个订阅选中的节点和正在使用的订阅。
// 共享入口的问题（端口被占用等）不算内核出错，记在共享的状态里，其余照常。
func (core *Core) apply(settings CoreSettings) error {
	shareError := ""
	if share := settings.Share; share != nil && !core.shareListening(share.Port) {
		if err := checkPortFree(share.Port); err != nil {
			shareError = fmt.Sprintf("端口 %d 被其他程序占用，换一个端口再试", share.Port)
			settings.Share = nil
		}
	}
	err := core.applyCore(settings)
	status := CoreShareStatus{Error: shareError}
	if share := settings.Share; share != nil && err == nil {
		status.Port = share.Port
		if status.Error = core.verifyShare(share.Port); status.Error == "" {
			status.Listening = true
		}
	} else if share != nil && err != nil {
		status.Error = err.Error()
	}
	if status.Error != "" && status.Error != core.Status().Share.Error {
		slog.Warn("局域网共享出错", "err", status.Error)
	}
	core.mutex.Lock()
	core.share = status
	core.mutex.Unlock()
	return err
}

// shareListening 表示共享入口已经在 port 上监听（端口被内核自己占着，不能再检查是否空闲）。
func (core *Core) shareListening(port int) bool {
	core.mutex.Lock()
	defer core.mutex.Unlock()
	return core.process != nil && core.current.Share != nil && core.current.Share.Port == port && core.share.Listening
}

// verifyShare 确认共享入口在监听：入口起不来时内核只记日志，不会退出。
func (core *Core) verifyShare(port int) string {
	for attempt := 0; attempt < 20; attempt++ {
		if connection, err := net.DialTimeout("tcp", "127.0.0.1:"+strconv.Itoa(port), time.Second); err == nil {
			connection.Close()
			return ""
		}
		time.Sleep(100 * time.Millisecond)
	}
	return fmt.Sprintf("端口 %d 没有监听起来：%s", port, core.logTail())
}

func (core *Core) applyCore(settings CoreSettings) error {
	if len(settings.Subscriptions) == 0 && settings.Share == nil {
		core.stopProcess()
		core.setTun(CoreTunStatus{})
		return nil
	}
	// TUN 模式要内核以管理员权限运行：没有这样运行时重新启动（问一次管理员权限），被拒绝后先不开，
	// 等 TUN 模式或者代理关掉再打开时再问。内核已经以管理员权限运行时，关掉 TUN 只是重新加载配置。
	tunError := ""
	if !settings.Tun {
		core.tunDeclined = false
	} else if !coreTunSupported {
		settings.Tun, tunError = false, "TUN 模式只在 Windows 上可用"
	} else if core.tunDeclined {
		settings.Tun, tunError = false, errTunDeclined.Error()
	}
	elevate := settings.Tun && !core.runningElevated()
	if elevate || !core.running() || core.current.Binary != settings.Binary || core.current.BinaryStamp != settings.BinaryStamp || core.current.Dir != settings.Dir || coreMixedPort(core.current) != coreMixedPort(settings) {
		core.stopProcess()
		if err := core.checkCrashes(); err != nil {
			core.setTun(CoreTunStatus{Error: tunError})
			return err
		}
		err := core.start(settings)
		if err != nil && settings.Tun {
			// 没能以管理员身份启动：这次不开 TUN，内核照常运行。
			slog.Warn("TUN 模式没有开启", "err", err)
			if errors.Is(err, errTunDeclined) {
				core.tunDeclined = true
			}
			settings.Tun, tunError = false, err.Error()
			err = core.start(settings)
		}
		if err != nil {
			core.setTun(CoreTunStatus{Error: tunError})
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
	core.setTun(CoreTunStatus{Active: settings.Tun, Error: tunError})
	return core.applySelections(settings)
}

func (core *Core) setTun(status CoreTunStatus) {
	core.mutex.Lock()
	core.tun = status
	core.mutex.Unlock()
}

func (core *Core) running() bool {
	core.mutex.Lock()
	defer core.mutex.Unlock()
	return core.process != nil
}

// runningElevated 表示内核正以管理员权限运行。
func (core *Core) runningElevated() bool {
	core.mutex.Lock()
	defer core.mutex.Unlock()
	return core.process != nil && core.process.Elevated()
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
	if port := coreMixedPort(settings); port != 0 {
		if err := checkPortFree(port); err != nil {
			return err
		}
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
	logPath := filepath.Join(settings.Dir, coreLogName)
	logFile, err := os.Create(logPath)
	if err != nil {
		return fmt.Errorf("无法创建内核日志：%v", err)
	}
	var process coreProcess
	if settings.Tun {
		// 宿主进程自己打开日志往后写。
		logFile.Close()
		if process, err = startElevatedCore(settings.Binary, settings.Dir, configPath, logPath); err != nil {
			return err
		}
	} else {
		command := exec.Command(settings.Binary, "-d", settings.Dir, "-f", configPath)
		command.Dir = settings.Dir
		command.Stdout, command.Stderr = logFile, logFile
		prepareCoreCommand(command)
		if err := command.Start(); err != nil {
			logFile.Close()
			return fmt.Errorf("无法启动代理内核：%v", err)
		}
		attachCoreProcess(command.Process)
		process = &localCoreProcess{command: command, logFile: logFile}
	}
	exited := make(chan struct{})
	core.mutex.Lock()
	core.process, core.exited, core.controller, core.secret = process, exited, controller, secretText
	core.configText = nil
	core.mutex.Unlock()
	go core.watch(process, exited)
	slog.Info("代理内核已启动", "pid", process.Pid(), "port", settings.Port, "elevated", process.Elevated())

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
func (core *Core) watch(process coreProcess, exited chan struct{}) {
	err := process.Wait()
	close(exited)
	core.mutex.Lock()
	unexpected := core.process == process
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
	port := coreMixedPort(settings)
	if port == 0 {
		return nil
	}
	connection, err := net.DialTimeout("tcp", "127.0.0.1:"+strconv.Itoa(port), 2*time.Second)
	if err != nil {
		return fmt.Errorf("代理内核没能监听端口 %d：%s", port, core.logTail())
	}
	connection.Close()
	return nil
}

// waitProviders 等内核加载完所有订阅和规则集：REST API 比它们先就绪，这时列出的节点还不完整，规则也还没生效。
// 规则集到期限还没加载完只记日志：内核照常工作，只是这些规则暂时不起作用。
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
				break
			}
		}
		if time.Now().After(deadline) {
			return fmt.Errorf("代理内核没能加载订阅：%s", core.logTail())
		}
		time.Sleep(50 * time.Millisecond)
	}
	core.resetAutoGroups(settings)
	if settings.Mode == "global" || len(settings.Rules) == 0 {
		return nil
	}
	for !core.ruleProvidersLoaded(settings) {
		if time.Now().After(deadline) {
			slog.Warn("代理内核没能及时加载分流规则", "log", core.logTail())
			return nil
		}
		time.Sleep(50 * time.Millisecond)
	}
	return nil
}

// ruleProvidersLoaded 表示规则集都已读进内核。规则集文件不会是空的，读到的规则数为 0 就是还没加载完。
// resetAutoGroups 等订阅里的节点读进来，再让各订阅的「自动选择」重新挑节点。自动选择会把挑中的节点缓存十秒，
// 加载配置期间（订阅还没读进来）有人查询它时，挑中的是 COMPATIBLE——相当于直连，接下来十秒走节点的流量都会直连。
// 内核认不出订阅里的节点时列表一直是空的，最多等两秒。
func (core *Core) resetAutoGroups(settings CoreSettings) {
	deadline := time.Now().Add(2 * time.Second)
	for {
		var result struct {
			Providers map[string]struct {
				Proxies []json.RawMessage `json:"proxies"`
			} `json:"providers"`
		}
		loaded := core.request(http.MethodGet, "/providers/proxies", nil, &result, coreApiTimeout) == nil
		for _, subscription := range settings.Subscriptions {
			if len(result.Providers[coreProviderName(subscription.Id)].Proxies) == 0 {
				loaded = false
			}
		}
		if loaded || time.Now().After(deadline) {
			break
		}
		time.Sleep(50 * time.Millisecond)
	}
	for _, subscription := range settings.Subscriptions {
		if err := core.request(http.MethodDelete, "/proxies/"+url.PathEscape(coreAutoGroup(subscription.Id)), nil, nil, coreApiTimeout); err != nil {
			slog.Warn("重置自动选择失败", "subscription", subscription.Id, "err", err)
		}
	}
}

func (core *Core) ruleProvidersLoaded(settings CoreSettings) bool {
	var result struct {
		Providers map[string]struct {
			RuleCount int `json:"ruleCount"`
		} `json:"providers"`
	}
	if err := core.request(http.MethodGet, "/providers/rules", nil, &result, coreApiTimeout); err != nil {
		return false
	}
	for name := range settings.RuleProviders {
		if result.Providers[name].RuleCount == 0 {
			return false
		}
	}
	return true
}

func (core *Core) reload(text []byte, settings CoreSettings) error {
	configPath := filepath.Join(settings.Dir, coreConfigName)
	if err := os.WriteFile(configPath, text, 0o600); err != nil {
		return fmt.Errorf("无法写入内核配置：%v", err)
	}
	// 加 force 内核才会重新应用 lan-allowed-ips 这类入口的设置（局域网共享只放行允许的设备）；端口没变时
	// 它保留原来的监听，不打断正在进行的连接（端口变了会重启内核）。
	if err := core.request(http.MethodPut, "/configs?force=true", map[string]string{"path": configPath}, nil, coreStartTimeout); err != nil {
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
	process, exited := core.process, core.exited
	core.process, core.configText = nil, nil
	core.mutex.Unlock()
	if process == nil {
		return
	}
	_ = process.Kill()
	select {
	case <-exited:
	case <-time.After(5 * time.Second):
		slog.Warn("代理内核没有及时退出")
	}
	slog.Info("代理内核已停止")
}

// CoreConnection 是内核里的一条连接（/connections）。Chains 的第一个是实际走的出口（节点、DIRECT 或上游代理）。
type CoreConnection struct {
	Id       string `json:"id"`
	Metadata struct {
		SourceIp        string `json:"sourceIP"`
		DestinationIp   string `json:"destinationIP"`
		DestinationPort string `json:"destinationPort"`
		Host            string `json:"host"`
		SniffHost       string `json:"sniffHost"`
		InboundName     string `json:"inboundName"`
	} `json:"metadata"`
	Upload      int64    `json:"upload"`
	Download    int64    `json:"download"`
	Start       string   `json:"start"`
	Chains      []string `json:"chains"`
	Rule        string   `json:"rule"`
	RulePayload string   `json:"rulePayload"`
}

// Target 是连接访问的主机：域名，没有时是嗅探到的域名或目标 IP。
func (connection CoreConnection) Target() string {
	for _, host := range []string{connection.Metadata.Host, connection.Metadata.SniffHost, connection.Metadata.DestinationIp} {
		if host != "" {
			return host
		}
	}
	return ""
}

// Outbound 是连接实际走的出口。
func (connection CoreConnection) Outbound() string {
	if len(connection.Chains) == 0 {
		return ""
	}
	return connection.Chains[0]
}

// Connections 列出内核里现在开着的连接。
func (core *Core) Connections() ([]CoreConnection, error) {
	var result struct {
		Connections []CoreConnection `json:"connections"`
	}
	err := core.request(http.MethodGet, "/connections", nil, &result, coreApiTimeout)
	return result.Connections, err
}

// TrafficTotals 是内核这次启动以来经过它的累计收发字节数，内核没有运行时 ok 为 false。
func (core *Core) TrafficTotals() (received, sent uint64, ok bool) {
	var result struct {
		DownloadTotal uint64 `json:"downloadTotal"`
		UploadTotal   uint64 `json:"uploadTotal"`
	}
	if core.request(http.MethodGet, "/connections", nil, &result, time.Second) != nil {
		return 0, 0, false
	}
	return result.DownloadTotal, result.UploadTotal, true
}

// NodeDelay 测订阅里一个节点的延迟（毫秒），连不上时返回 0（内核把测得 0 ms 也当作失败，真实的节点不会这么快）。
func (core *Core) NodeDelay(profileId, node, testUrl string) int {
	query := url.Values{"url": {testUrl}, "timeout": {strconv.Itoa(int(coreDelayTimeout / time.Millisecond))}}
	var result struct {
		Delay int `json:"delay"`
	}
	path := "/providers/proxies/" + url.PathEscape(coreProviderName(profileId)) + "/" + url.PathEscape(node) + "/healthcheck?" + query.Encode()
	if core.request(http.MethodGet, path, nil, &result, coreDelayTimeout+2*time.Second) != nil {
		return 0
	}
	return result.Delay
}

// TraceConnection 经内核访问一次（probe 发起访问），同时从内核的日志里找出这次连接的判定：命中哪条规则、走了哪个出口、
// 有没有出错。内核在连接建立或拨号失败时写这行日志，所以先订阅日志再访问，访问结束后最多再等一会儿。
// 内核没有运行时只访问，判定为 nil。
func (core *Core) TraceConnection(ctx context.Context, host string, port int, probe func() DiagnoseProbe) (DiagnoseProbe, *RouteTrace) {
	core.mutex.Lock()
	controller, secret, running := core.controller, core.secret, core.process != nil
	core.mutex.Unlock()
	if !running || controller == "" {
		return probe(), nil
	}
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	found := make(chan *RouteTrace, 1)
	go func() {
		request, err := http.NewRequestWithContext(ctx, http.MethodGet, "http://"+controller+"/logs?level=info", nil)
		if err != nil {
			return
		}
		request.Header.Set("Authorization", "Bearer "+secret)
		// 日志是一直不结束的流，内核写出第一行时才返回响应头；取消 ctx 时结束。
		response, err := (&http.Client{Transport: &http.Transport{Proxy: nil, DisableKeepAlives: true}}).Do(request)
		if err != nil {
			return
		}
		defer response.Body.Close()
		scanner := bufio.NewScanner(response.Body)
		scanner.Buffer(make([]byte, 64<<10), 1<<20)
		for scanner.Scan() {
			var event struct {
				Payload string `json:"payload"`
			}
			if json.Unmarshal(scanner.Bytes(), &event) != nil {
				continue
			}
			if trace := parseRouteTrace(event.Payload); trace != nil && trace.Host == strings.ToLower(host) && trace.Port == port {
				found <- trace
				return
			}
		}
	}()
	// 等内核开始订阅日志，免得错过这次连接的那一行。
	select {
	case <-time.After(300 * time.Millisecond):
	case <-ctx.Done():
	}
	result := probe()
	select {
	case trace := <-found:
		return result, trace
	case <-time.After(diagnoseTraceWait):
		return result, nil
	case <-ctx.Done():
		return result, nil
	}
}

// Nodes 列出订阅里的节点和最近一次测得的延迟。
func (core *Core) Nodes(profileId string) (CoreNodes, error) {
	return core.NodesWithin(profileId, coreApiTimeout)
}

// NodesWithin 与 Nodes 相同，但每次请求最多等 timeout：托盘菜单在 UI 线程上读取，不能久等。
func (core *Core) NodesWithin(profileId string, timeout time.Duration) (CoreNodes, error) {
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
	if err := core.request(http.MethodGet, "/providers/proxies/"+url.PathEscape(coreProviderName(profileId)), nil, &provider, timeout); err != nil {
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
	if err := core.request(http.MethodGet, "/proxies/"+url.PathEscape(profileId), nil, &group, timeout); err != nil {
		return CoreNodes{}, err
	}
	result.Selected, result.Current = group.Now, group.Now
	if group.Now == coreAutoGroup(profileId) {
		result.Selected = ""
		if err := core.request(http.MethodGet, "/proxies/"+url.PathEscape(coreAutoGroup(profileId)), nil, &group, timeout); err == nil {
			result.Current = group.Now
		}
	}
	return result, nil
}

// CurrentNode 返回订阅实际在用的节点（自动选择时是它选中的节点），内核没有运行时返回空字符串。
func (core *Core) CurrentNode(profileId string) string {
	var group struct {
		Now string `json:"now"`
	}
	if core.request(http.MethodGet, "/proxies/"+url.PathEscape(profileId), nil, &group, time.Second) != nil {
		return ""
	}
	if group.Now == coreAutoGroup(profileId) && core.request(http.MethodGet, "/proxies/"+url.PathEscape(group.Now), nil, &group, time.Second) != nil {
		return ""
	}
	return group.Now
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

// freeLocalPort 找一个 TCP 和 UDP 都空着的本机端口：内核的代理端口两种都要监听，而 Windows 为 Hyper-V、WSL 等
// 保留的端口段对 TCP 和 UDP 不一样，TCP 分到的空闲端口可能正好是保留给 UDP 的。
func freeLocalPort() (int, error) {
	var lastErr error
	for range 20 {
		listener, err := net.Listen("tcp", "127.0.0.1:0")
		if err != nil {
			return 0, err
		}
		port := listener.Addr().(*net.TCPAddr).Port
		packet, err := net.ListenPacket("udp", "127.0.0.1:"+strconv.Itoa(port))
		listener.Close()
		if err == nil {
			packet.Close()
			return port, nil
		}
		lastErr = err
	}
	return 0, lastErr
}

// checkPortFree 在启动内核前确认代理端口没有被其他程序占用（TCP 和 UDP 都要能监听），给出比内核日志更清楚的提示。
func checkPortFree(port int) error {
	address := "127.0.0.1:" + strconv.Itoa(port)
	listener, err := net.Listen("tcp", address)
	if err != nil {
		return fmt.Errorf("端口 %d 已被其他程序占用，请在「常规」里给代理内核换一个端口", port)
	}
	listener.Close()
	packet, err := net.ListenPacket("udp", address)
	if err != nil {
		return fmt.Errorf("端口 %d 用不了（被其他程序占用，或者被 Windows 保留给 Hyper-V、WSL 等），请在「常规」里给代理内核换一个端口", port)
	}
	packet.Close()
	return nil
}
