package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/netip"
	"net/url"
	"strconv"
	"strings"
	"sync"
)

// 网址诊断在后台逐项检查，设置页轮询进度。同一时间只有一次诊断，新的诊断会取消还没完成的那次。

// diagnoseSnapshot 是诊断开始时本机的代理状态，在引擎线程上取，之后在后台使用。
type diagnoseSnapshot struct {
	state    string
	profile  *Profile
	system   SystemProxyState
	active   string
	corePort int
	share    ShareConfig
	upstream ShareUpstream
	testUrl  string
	turnOn   string
}

func (engine *Engine) diagnoseSnapshot() diagnoseSnapshot {
	status := engine.Status()
	snapshot := diagnoseSnapshot{state: status.State, system: status.System, testUrl: defaultTestUrl, upstream: ShareUpstream{Kind: shareUpstreamDirect}}
	if status.Profile != nil {
		profile := *status.Profile
		snapshot.profile = &profile
	}
	if engine.config == nil {
		return snapshot
	}
	snapshot.active = engine.activeSubscriptionId()
	snapshot.corePort = engine.config.Core.Port
	snapshot.share = engine.config.Share
	snapshot.testUrl = engine.config.TestUrl
	if snapshot.share.Enabled {
		snapshot.upstream = engine.shareUpstream(status, snapshot.active)
	}
	snapshot.turnOn = engine.subscriptionToTurnOn()
	return snapshot
}

// subscriptionToTurnOn 是可以一键开启的订阅配置：最近使用的配置是已经下载好的订阅时用它，否则用第一个下载好的订阅。
func (engine *Engine) subscriptionToTurnOn() string {
	if engine.core == nil || !fileExists(engine.coreBinary()) {
		return ""
	}
	if selected := engine.selectedProfile(); selected != nil && selected.IsSubscription() && engine.subscriptionLoaded(selected) {
		return selected.Name
	}
	for index := range engine.config.Profiles {
		if profile := &engine.config.Profiles[index]; profile.IsSubscription() && engine.subscriptionLoaded(profile) {
			return profile.Name
		}
	}
	return ""
}

// diagnoseRunner 保存最近一次诊断的进度。
type diagnoseRunner struct {
	mutex  sync.Mutex
	job    DiagnoseJob
	cancel context.CancelFunc
}

func diagnoseRows(perspective string) []DiagnoseRow {
	status := "本机代理"
	if perspective == diagnoseDevice {
		status = "局域网共享"
	}
	rows := []DiagnoseRow{
		{Id: "status", Title: status, Outcome: "pending"},
		{Id: "dns", Title: "域名解析", Outcome: "pending"},
		{Id: "direct", Title: "直接访问", Outcome: "pending"},
		{Id: "proxied", Title: "经代理访问", Outcome: "pending"},
		{Id: "node", Title: "节点", Outcome: "pending"},
	}
	if perspective == diagnoseDevice {
		rows = append(rows, DiagnoseRow{Id: "device", Title: "设备的连接", Outcome: "pending"})
	}
	return rows
}

// StartDiagnose 开始诊断一个网址，perspective 是 pc（这台电脑）或 device（经局域网共享上网的设备）。立即返回，进度见 Diagnose。
func (service *subscriptionService) StartDiagnose(address, perspective string) (DiagnoseJob, error) {
	target, err := normalizeDiagnoseUrl(address)
	if err != nil {
		return DiagnoseJob{}, err
	}
	if perspective != diagnoseDevice {
		perspective = diagnosePc
	}
	var snapshot diagnoseSnapshot
	if err := service.onEngine(func() { snapshot = service.engine.diagnoseSnapshot() }); err != nil {
		return DiagnoseJob{}, err
	}
	ctx, cancel := context.WithCancel(context.Background())
	runner := &service.diagnose
	runner.mutex.Lock()
	if runner.cancel != nil {
		runner.cancel()
	}
	runner.cancel = cancel
	serial := runner.job.Serial + 1
	runner.job = DiagnoseJob{Serial: serial, Url: target.String(), Perspective: perspective, Running: true, Rows: diagnoseRows(perspective)}
	runner.mutex.Unlock()
	go service.runDiagnose(ctx, serial, target, perspective, snapshot)
	return service.Diagnose(), nil
}

// Diagnose 返回最近一次诊断的进度和结果。
func (service *subscriptionService) Diagnose() DiagnoseJob {
	runner := &service.diagnose
	runner.mutex.Lock()
	defer runner.mutex.Unlock()
	job := runner.job
	job.Rows = append([]DiagnoseRow{}, job.Rows...)
	return job
}

// CancelDiagnose 停止正在进行的诊断。
func (service *subscriptionService) CancelDiagnose() {
	runner := &service.diagnose
	runner.mutex.Lock()
	defer runner.mutex.Unlock()
	if runner.cancel != nil {
		runner.cancel()
		runner.cancel = nil
	}
	if runner.job.Running {
		runner.job.Running = false
		for index := range runner.job.Rows {
			if row := &runner.job.Rows[index]; row.Outcome == "pending" || row.Outcome == "running" {
				row.Outcome, row.Summary = "skipped", "已停止"
			}
		}
	}
}

// updateDiagnose 修改第 serial 次诊断的进度；已经有更新的诊断或者被停止时不改。
func (service *subscriptionService) updateDiagnose(ctx context.Context, serial int, change func(job *DiagnoseJob)) {
	runner := &service.diagnose
	runner.mutex.Lock()
	defer runner.mutex.Unlock()
	if runner.job.Serial == serial && ctx.Err() == nil {
		change(&runner.job)
	}
}

func (service *subscriptionService) runDiagnose(ctx context.Context, serial int, target *url.URL, perspective string, snapshot diagnoseSnapshot) {
	set := func(id, outcome, summary, detail string) {
		service.updateDiagnose(ctx, serial, func(job *DiagnoseJob) {
			for index := range job.Rows {
				if job.Rows[index].Id == id {
					job.Rows[index].Outcome, job.Rows[index].Summary, job.Rows[index].Detail = outcome, summary, detail
				}
			}
		})
	}
	facts := service.collectDiagnose(ctx, target, perspective, snapshot, set)
	if ctx.Err() != nil {
		return
	}
	verdict := diagnoseVerdict(facts)
	service.updateDiagnose(ctx, serial, func(job *DiagnoseJob) {
		job.Running = false
		job.Verdict = &verdict
		job.Subscription = facts.Subscription
		job.Report = diagnoseReport(*job)
	})
	slog.Info("网址诊断", "url", target.String(), "perspective", perspective, "verdict", verdict.Headline)
}

// collectDiagnose 把链路走一遍，逐项记下结果，返回给结论用的事实。
func (service *subscriptionService) collectDiagnose(ctx context.Context, target *url.URL, perspective string, snapshot diagnoseSnapshot, set func(id, outcome, summary, detail string)) DiagnoseFacts {
	host, port := target.Hostname(), diagnosePort(target)
	facts := DiagnoseFacts{Perspective: perspective, Host: host, TurnOn: snapshot.turnOn, ShareUpstream: snapshot.upstream}
	core := service.core.Status()

	// 1. 本机或局域网共享的状态。
	set("status", "running", "正在看…", "")
	facts.Route, facts.RouteText = diagnoseRoute(snapshot)
	facts.ShareListening = snapshot.share.Enabled && core.Share.Listening
	if perspective == diagnoseDevice {
		facts.UsesCore = facts.ShareListening && snapshot.upstream.Kind == shareUpstreamCore
		switch {
		case facts.ShareListening:
			set("status", "pass", fmt.Sprintf("共享入口在监听端口 %d，%s", snapshot.share.Port, shareUpstreamSummary(snapshot.upstream)), snapshot.upstream.Reason)
		case !snapshot.share.Enabled:
			set("status", "fail", "局域网共享没有开启", "")
		default:
			set("status", "fail", "共享入口没在监听", core.Share.Error)
		}
	} else {
		facts.UsesCore = facts.Route == "subscription"
		switch facts.Route {
		case "off":
			set("status", "warn", "本机没开代理，浏览器直接连接", "")
		case "subscription":
			summary := "用的是订阅配置" + facts.RouteText
			if node := service.core.CurrentNode(snapshot.active); node != "" {
				summary += "，当前节点 " + node
			}
			set("status", "pass", summary, "")
		case "profile":
			set("status", "pass", "用的是"+facts.RouteText, "")
		default:
			set("status", "warn", "系统代理由其他程序设置："+facts.RouteText, "")
		}
	}
	if facts.UsesCore {
		facts.Subscription = snapshot.active
	}
	if ctx.Err() != nil {
		return facts
	}

	// 2. 域名解析：系统的解析，以及经内核到境外的解析作对照。
	set("dns", "running", "正在解析…", "")
	if _, err := netip.ParseAddr(host); err == nil {
		set("dns", "skipped", "填的是 IP，不用解析", "")
	} else {
		system := lookupSystem(ctx, host)
		var remote []string
		if core.Running && core.Port != 0 {
			remote = lookupRemote(ctx, host, &url.URL{Scheme: "http", Host: "127.0.0.1:" + strconv.Itoa(core.Port)})
		}
		remoteText := ""
		if remote != nil {
			remoteText = "境外解析：" + strings.Join(remote, "、")
			if len(remote) == 0 {
				remoteText = "境外也解析不到"
			}
		}
		switch {
		case ctx.Err() != nil:
		case len(system) == 0:
			facts.DnsFailed = true
			set("dns", "fail", "本机解析不到这个域名", remoteText)
		default:
			detail := remoteText
			if len(remote) > 0 && !overlaps(system, remote) {
				detail += "。两边结果不同：CDN 常按地区给不同的地址，不一定是污染，直连不通时才要怀疑"
			}
			set("dns", "pass", "本机解析："+strings.Join(firstStrings(system, 3), "、"), detail)
		}
	}
	if ctx.Err() != nil {
		return facts
	}

	// 3. 直接访问。
	set("direct", "running", "正在访问…", "")
	direct := probeUrl(ctx, target, nil, diagnoseDirectTimeout)
	facts.Direct = &direct
	if ctx.Err() != nil {
		return facts
	}
	if direct.Ok {
		set("direct", "pass", "可以访问："+direct.Summary(), "")
	} else {
		set("direct", "fail", "不通："+direct.Summary(), "")
	}

	// 4. 经代理访问，经内核时顺便从日志里找出这次连接的判定。
	set("proxied", "running", "正在访问…", "")
	service.probeProxied(ctx, target, host, port, perspective, snapshot, core, &facts, set)
	if ctx.Err() != nil {
		return facts
	}

	// 5. 节点：经内核的节点时看当前节点通不通。
	set("node", "running", "正在测…", "")
	switch {
	case facts.UsesCore && core.Running && snapshot.active != "":
		node := service.core.CurrentNode(snapshot.active)
		if node == "" {
			set("node", "warn", "读不到当前节点", "")
			break
		}
		delay := service.core.NodeDelay(snapshot.active, node, snapshot.testUrl)
		facts.Node, facts.NodeDelay = node, &delay
		if delay > 0 {
			set("node", "pass", fmt.Sprintf("%s：%d ms", node, delay), "")
		} else {
			set("node", "fail", node+" 连不上", "")
		}
	case facts.UsesCore:
		set("node", "warn", "内核没有运行", "")
	default:
		set("node", "skipped", "这条链路不经节点", "")
	}
	if ctx.Err() != nil {
		return facts
	}

	// 6. 设备视角：最近有没有设备访问这个网站。
	if perspective == diagnoseDevice {
		service.ShareActivity()
		count := service.shares.recentFor(host)
		facts.DeviceConnections = &count
		if count > 0 {
			set("device", "pass", fmt.Sprintf("最近有 %d 条设备对它的连接", count), "")
		} else {
			set("device", "warn", "最近没有设备对这个网站的连接", "在设备上打开它，再到「局域网共享」页看「最近的连接」")
		}
	}
	return facts
}

// diagnoseRoute 说明本机在用的代理：off / subscription / profile / external，以及给人看的说法。
func diagnoseRoute(snapshot diagnoseSnapshot) (string, string) {
	switch {
	case snapshot.state == statusOn && snapshot.profile != nil:
		profile := snapshot.profile
		if profile.IsSubscription() && profile.Id == snapshot.active {
			return "subscription", "「" + profile.Name + "」（" + map[string]string{"rule": "按规则分流", "global": "全局代理"}[profile.Mode] + "）"
		}
		return "profile", "「" + profile.Name + "」（" + profile.Summary() + "）"
	case snapshot.state == statusExternal:
		return "external", snapshot.system.Describe()
	}
	return "off", ""
}

// probeProxied 按本机或共享的链路经代理访问一次，结果记进 facts。
func (service *subscriptionService) probeProxied(ctx context.Context, target *url.URL, host string, port int, perspective string, snapshot diagnoseSnapshot, core CoreStatus, facts *DiagnoseFacts, set func(id, outcome, summary, detail string)) {
	var result DiagnoseProbe
	switch {
	case perspective == diagnoseDevice && !facts.ShareListening:
		set("proxied", "skipped", "共享入口没在监听，没法测", "")
		return
	case perspective == diagnoseDevice:
		facts.ProxiedVia = "共享入口"
		result, facts.Trace = service.traceThrough(ctx, target, host, port, snapshot.share.Port)
	case facts.Route == "subscription":
		facts.ProxiedVia = "订阅配置" + facts.RouteText
		if core.Port == 0 {
			set("proxied", "fail", "内核没有在本机提供代理", core.Error)
			failed := DiagnoseProbe{Failure: "内核没有运行"}
			facts.Proxied = &failed
			return
		}
		result, facts.Trace = service.traceThrough(ctx, target, host, port, core.Port)
	case facts.Route == "off":
		set("proxied", "skipped", "本机没开代理", "")
		return
	default:
		server, pac := snapshot.system.Server, ""
		if facts.Route == "profile" {
			server, pac = snapshot.profile.Server, snapshot.profile.Pac
			facts.ProxiedVia = facts.RouteText
		} else {
			facts.ProxiedVia = "系统代理（" + facts.RouteText + "）"
			if snapshot.system.PacEnabled {
				server, pac = "", snapshot.system.Pac
			}
		}
		var problem string
		result, problem = probeThroughServer(ctx, target, server, pac, &facts.ProxiedVia)
		if problem != "" {
			set("proxied", "skipped", problem, "")
			return
		}
	}
	facts.Proxied = &result
	summary := "不通：" + result.Summary()
	if result.Ok {
		summary = "可以访问：" + result.Summary()
	}
	detail := ""
	if trace := facts.Trace; trace != nil {
		summary = "走 " + trace.Chain + "：" + result.Summary()
		if trace.Rule != "" {
			summary = "命中 " + trace.Rule + "，" + summary
		}
		if trace.Error != "" {
			detail = "内核：" + trace.Error
		}
	} else if facts.UsesCore || perspective == diagnoseDevice {
		detail = "内核没有记录到这次连接的判定"
	}
	outcome := "fail"
	if result.Ok {
		outcome = "pass"
	}
	set("proxied", outcome, summary, detail)
}

// traceThrough 经本机 port 上的内核入口访问一次，同时抓内核的判定。
func (service *subscriptionService) traceThrough(ctx context.Context, target *url.URL, host string, port, proxyPort int) (DiagnoseProbe, *RouteTrace) {
	proxy := &url.URL{Scheme: "http", Host: "127.0.0.1:" + strconv.Itoa(proxyPort)}
	return service.core.TraceConnection(ctx, host, port, func() DiagnoseProbe {
		return probeUrl(ctx, target, proxy, diagnoseProxiedTimeout)
	})
}

// probeThroughServer 经代理访问一次：有 PAC 时和浏览器一样按 PAC 为这个网址选出的代理（或直连）访问，via 补上
// PAC 的选择；否则经代理服务器。没法测时返回原因。
func probeThroughServer(ctx context.Context, target *url.URL, server, pac string, via *string) (DiagnoseProbe, string) {
	if pac != "" {
		route, err := pacProxyForUrl(pac, target.String(), diagnoseDnsTimeout)
		switch {
		case errors.Is(err, errPacUnsupported) && server == "":
			return DiagnoseProbe{}, "没法在这里执行 PAC 脚本"
		case errors.Is(err, errPacUnsupported):
		case err != nil:
			return DiagnoseProbe{Failure: "PAC 脚本执行失败：" + err.Error()}, ""
		case route == "":
			*via += "，PAC 选择直连"
			return probeUrl(ctx, target, nil, diagnoseProxiedTimeout), ""
		default:
			*via += "，PAC 选择 " + route
			server = route
		}
	}
	proxy, err := proxyUrlForTarget(server, target.Scheme)
	if err != nil {
		return DiagnoseProbe{}, err.Error()
	}
	return probeUrl(ctx, target, proxy, diagnoseProxiedTimeout), ""
}

func overlaps(first, second []string) bool {
	for _, item := range first {
		if containsString(second, item) {
			return true
		}
	}
	return false
}

// shareUpstreamSummary 是共享的流量往哪走的一句话。
func shareUpstreamSummary(upstream ShareUpstream) string {
	switch upstream.Kind {
	case shareUpstreamCore:
		return "设备和本机一样走节点和分流规则"
	case shareUpstreamProxy:
		return "设备的流量转发到 " + strings.TrimPrefix(upstream.Proxy, "http://")
	case shareUpstreamUnsupported:
		return "本机用的是 PAC，设备暂时直连"
	}
	return "设备经这台电脑直接上网"
}
