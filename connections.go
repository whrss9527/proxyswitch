package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
)

// 连接页：经内核的连接（正在进行的和最近的）、按出口累计的流量和出口 IP。内核只列出还开着的连接，短连接一闪就没了，
// 所以内核运行时后台每两秒读一次：没见过的连接记进「最近的连接」，每条连接新增的流量按出口（节点、直连、上游代理）累计。

const (
	connectionPollInterval = 2 * time.Second
	connectionHistoryLimit = 200
	// connectionListLimit 是设置页一次最多拿到的正在进行的连接，页面上再按筛选显示一部分。
	connectionListLimit = 500
	trafficSaveInterval = 30 * time.Second
	trafficFileName     = "traffic.json"
	exitCheckTimeout    = 8 * time.Second
)

// ConnectionRecord 是连接页里的一条连接。Client 是来源 IP（本机是 127.0.0.1，共享的设备是它的局域网地址），Process 是
// 发起连接的程序（本机的连接才有）；Chains 是内核给的出口链：第一个是实际的出口（节点、DIRECT、上游代理），最后一个是
// 规则指到的策略（ProxySwitch 或者某个策略组）；Share 表示经局域网共享的入口。
type ConnectionRecord struct {
	Id       string   `json:"id"`
	Client   string   `json:"client"`
	Process  string   `json:"process,omitempty"`
	Host     string   `json:"host"`
	Port     string   `json:"port"`
	Network  string   `json:"network"`
	Chains   []string `json:"chains"`
	Rule     string   `json:"rule"`
	Start    string   `json:"start"`
	Share    bool     `json:"share,omitempty"`
	Upload   int64    `json:"upload"`
	Download int64    `json:"download"`
}

func connectionRecord(connection CoreConnection) ConnectionRecord {
	chains := connection.Chains
	if chains == nil {
		chains = []string{}
	}
	return ConnectionRecord{
		Id:       connection.Id,
		Client:   connection.Metadata.SourceIp,
		Process:  connection.Metadata.Process,
		Host:     connection.Target(),
		Port:     connection.Metadata.DestinationPort,
		Network:  strings.ToUpper(connection.Metadata.Network),
		Chains:   chains,
		Rule:     strings.TrimSpace(connection.Rule + " " + connection.RulePayload),
		Start:    connection.Start,
		Share:    connection.Metadata.InboundName == coreShareListener,
		Upload:   connection.Upload,
		Download: connection.Download,
	}
}

// outbound 是连接实际走的出口，内核没给时算直连。
func (record ConnectionRecord) outbound() string {
	if len(record.Chains) == 0 || record.Chains[0] == "" {
		return "DIRECT"
	}
	return record.Chains[0]
}

// TrafficTotal 是上行、下行的字节数。
type TrafficTotal struct {
	Upload   int64 `json:"upload"`
	Download int64 `json:"download"`
}

// TrafficStats 是按出口累计的流量，保存在 traffic.json：内核重启后接着算，可以清零。Since 是开始统计的时间。
type TrafficStats struct {
	Since     string                  `json:"since"`
	Outbounds map[string]TrafficTotal `json:"outbounds"`
}

// OutboundTraffic 是一个出口和它累计的流量。
type OutboundTraffic struct {
	Name string `json:"name"`
	TrafficTotal
}

// TrafficView 是给设置页的流量统计：出口按总量从大到小排。
type TrafficView struct {
	Since     string            `json:"since"`
	Outbounds []OutboundTraffic `json:"outbounds"`
}

// ConnectionsView 是连接页的内容。Session 是内核这次运行以来经过它的总流量。
type ConnectionsView struct {
	Running bool               `json:"running"`
	Active  []ConnectionRecord `json:"active"`
	Recent  []ConnectionRecord `json:"recent"`
	Session TrafficTotal       `json:"session"`
	Traffic TrafficView        `json:"traffic"`
	Exit    ExitView           `json:"exit"`
}

// connectionMonitor 记着内核的连接：正在进行的、最近的，以及按出口累计的流量。
type connectionMonitor struct {
	mutex   sync.Mutex
	path    string
	now     func() time.Time
	running bool
	active  []ConnectionRecord
	recent  []ConnectionRecord
	// seen 是上次读到的每条连接的累计流量，下次只算差。
	seen    map[string]TrafficTotal
	session TrafficTotal
	stats   TrafficStats
	dirty   bool
	saved   time.Time
}

// newConnectionMonitor 读出 path 里保存的流量统计，没有时从现在开始统计。
func newConnectionMonitor(path string, now func() time.Time) *connectionMonitor {
	monitor := &connectionMonitor{path: path, now: now, seen: map[string]TrafficTotal{}, saved: now()}
	if data, err := os.ReadFile(path); err == nil {
		if err := json.Unmarshal(data, &monitor.stats); err != nil {
			slog.Warn("流量统计读不了，重新开始统计", "err", err)
			monitor.stats = TrafficStats{}
		}
	}
	if monitor.stats.Outbounds == nil {
		monitor.stats.Outbounds = map[string]TrafficTotal{}
	}
	if monitor.stats.Since == "" {
		monitor.stats.Since = now().Format(time.RFC3339)
	}
	return monitor
}

// ingest 记下一次读到的连接：内核没在运行时 running 为 false，只清掉正在进行的连接，最近的连接留着。
func (monitor *connectionMonitor) ingest(snapshot CoreConnections, running bool) {
	monitor.mutex.Lock()
	defer monitor.mutex.Unlock()
	monitor.running = running
	if !running {
		monitor.active, monitor.seen, monitor.session = nil, map[string]TrafficTotal{}, TrafficTotal{}
		monitor.saveIfDue()
		return
	}
	monitor.session = TrafficTotal{Upload: snapshot.UploadTotal, Download: snapshot.DownloadTotal}
	positions := make(map[string]int, len(monitor.recent))
	for position, record := range monitor.recent {
		positions[record.Id] = position
	}
	active := make([]ConnectionRecord, 0, len(snapshot.Connections))
	seen := make(map[string]TrafficTotal, len(snapshot.Connections))
	var fresh []ConnectionRecord
	for _, connection := range snapshot.Connections {
		record := connectionRecord(connection)
		active = append(active, record)
		previous := monitor.seen[record.Id]
		seen[record.Id] = TrafficTotal{Upload: record.Upload, Download: record.Download}
		if up, down := max(0, record.Upload-previous.Upload), max(0, record.Download-previous.Download); up > 0 || down > 0 {
			total := monitor.stats.Outbounds[record.outbound()]
			total.Upload += up
			total.Download += down
			monitor.stats.Outbounds[record.outbound()] = total
			monitor.dirty = true
		}
		if position, found := positions[record.Id]; found {
			monitor.recent[position] = record
		} else {
			fresh = append(fresh, record)
		}
	}
	newestFirst := func(records []ConnectionRecord) {
		sort.SliceStable(records, func(i, j int) bool { return records[i].Start > records[j].Start })
	}
	newestFirst(active)
	newestFirst(fresh)
	monitor.active, monitor.seen = active, seen
	monitor.recent = append(fresh, monitor.recent...)
	if len(monitor.recent) > connectionHistoryLimit {
		monitor.recent = monitor.recent[:connectionHistoryLimit]
	}
	monitor.saveIfDue()
}

// saveIfDue 在流量有变化、离上次保存超过 trafficSaveInterval 时写入文件。调用时持有锁。
func (monitor *connectionMonitor) saveIfDue() {
	if monitor.dirty && monitor.now().Sub(monitor.saved) >= trafficSaveInterval {
		monitor.saveLocked()
	}
}

func (monitor *connectionMonitor) saveLocked() {
	monitor.dirty, monitor.saved = false, monitor.now()
	data, _ := json.MarshalIndent(monitor.stats, "", "  ")
	if err := writeFileAtomically(monitor.path, data); err != nil {
		slog.Warn("保存流量统计失败", "err", err)
	}
}

// save 立即保存有变化的流量统计（程序退出时）。
func (monitor *connectionMonitor) save() {
	monitor.mutex.Lock()
	defer monitor.mutex.Unlock()
	if monitor.dirty {
		monitor.saveLocked()
	}
}

// resetTraffic 清零按出口累计的流量，从现在重新开始统计。
func (monitor *connectionMonitor) resetTraffic() {
	monitor.mutex.Lock()
	defer monitor.mutex.Unlock()
	monitor.stats = TrafficStats{Since: monitor.now().Format(time.RFC3339), Outbounds: map[string]TrafficTotal{}}
	monitor.saveLocked()
}

// clearRecent 清空最近的连接。
func (monitor *connectionMonitor) clearRecent() {
	monitor.mutex.Lock()
	defer monitor.mutex.Unlock()
	monitor.recent = nil
}

// forget 从正在进行的连接里去掉已经断开的（不等下次读取）。id 为空时去掉全部。
func (monitor *connectionMonitor) forget(id string) {
	monitor.mutex.Lock()
	defer monitor.mutex.Unlock()
	kept := monitor.active[:0]
	for _, record := range monitor.active {
		if id != "" && record.Id != id {
			kept = append(kept, record)
		}
	}
	monitor.active = kept
}

// view 是给设置页的内容（出口 IP 由调用方补上）。
func (monitor *connectionMonitor) view() ConnectionsView {
	monitor.mutex.Lock()
	defer monitor.mutex.Unlock()
	view := ConnectionsView{
		Running: monitor.running,
		Active:  append([]ConnectionRecord{}, monitor.active[:min(len(monitor.active), connectionListLimit)]...),
		Recent:  append([]ConnectionRecord{}, monitor.recent...),
		Session: monitor.session,
		Traffic: TrafficView{Since: monitor.stats.Since, Outbounds: []OutboundTraffic{}},
	}
	for name, total := range monitor.stats.Outbounds {
		view.Traffic.Outbounds = append(view.Traffic.Outbounds, OutboundTraffic{Name: name, TrafficTotal: total})
	}
	sort.Slice(view.Traffic.Outbounds, func(i, j int) bool {
		a, b := view.Traffic.Outbounds[i], view.Traffic.Outbounds[j]
		if a.Upload+a.Download != b.Upload+b.Download {
			return a.Upload+a.Download > b.Upload+b.Download
		}
		return a.Name < b.Name
	})
	return view
}

// ---------- 出口 IP ----------

// errCoreNotRunning 表示需要内核的操作时内核没有运行。
var errCoreNotRunning = errors.New("代理内核没有运行")

// exitIpEndpoints 是查出口 IP 的公开接口，依次尝试；开发模式可以换成本机模拟的接口。
var exitIpEndpoints = []string{"https://api.ip.sb/geoip", "https://ipinfo.io/json", "https://ipapi.co/json/"}

// ExitInfo 是出口 IP 和它的归属。CountryCode 是两个字母的国家代码，Country、City 是接口给的英文名；
// Checked 是查询的时间，Endpoint 是查到结果的接口。经内核查的还有 Node（查的时候正在使用的订阅选中的节点，
// 切换后要重查）和 Outbound（这次查询实际走的出口：规则可能让查询接口直连）。
type ExitInfo struct {
	Ip           string `json:"ip"`
	CountryCode  string `json:"country_code,omitempty"`
	Country      string `json:"country,omitempty"`
	City         string `json:"city,omitempty"`
	Organization string `json:"organization,omitempty"`
	Checked      string `json:"checked"`
	Endpoint     string `json:"endpoint"`
	Node         string `json:"node,omitempty"`
	Outbound     string `json:"outbound,omitempty"`
}

// ExitState 是一种出口 IP 最近一次查询的结果：查到的信息，或者没查到的原因；Checking 表示正在查。
type ExitState struct {
	Info     *ExitInfo `json:"info,omitempty"`
	Error    string    `json:"error,omitempty"`
	Checking bool      `json:"checking,omitempty"`
}

// ExitView 是经节点（经内核）和直连两种出口。
type ExitView struct {
	Proxy  ExitState `json:"proxy"`
	Direct ExitState `json:"direct"`
}

// parseExitInfo 兼容几个常见接口的字段：api.ip.sb（ip / country_code / country / city / isp / organization）、
// ipinfo.io（ip / country 是两位代码 / city / org）、ipapi.co（ip / country_code / country_name / city / org）、
// ip-api.com（query / countryCode / country / city / isp）。
func parseExitInfo(data []byte) (ExitInfo, bool) {
	var fields map[string]any
	if json.Unmarshal(data, &fields) != nil {
		return ExitInfo{}, false
	}
	text := func(keys ...string) string {
		for _, key := range keys {
			if value, ok := fields[key].(string); ok && strings.TrimSpace(value) != "" {
				return strings.TrimSpace(value)
			}
		}
		return ""
	}
	info := ExitInfo{Ip: text("ip", "query"), CountryCode: text("country_code", "countryCode"), Country: text("country_name", "country"), City: text("city"), Organization: text("isp", "organization", "org", "asn_organization")}
	if info.Ip == "" {
		return ExitInfo{}, false
	}
	// ipinfo.io 的 country 就是两位代码。
	if info.CountryCode == "" && len(info.Country) == 2 {
		info.CountryCode, info.Country = info.Country, ""
	}
	if strings.EqualFold(info.Country, info.CountryCode) {
		info.Country = ""
	}
	info.CountryCode = strings.ToUpper(info.CountryCode)
	return info, true
}

// fetchExitInfo 经 proxyUrl（空表示直连）访问一个查询接口。
func fetchExitInfo(ctx context.Context, endpoint, proxyUrl string) (ExitInfo, error) {
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return ExitInfo{}, err
	}
	request.Header.Set("User-Agent", "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/128.0 Safari/537.36")
	request.Header.Set("Accept", "application/json")
	response, err := updateClient(proxyUrl, exitCheckTimeout).Do(request)
	if err != nil {
		return ExitInfo{}, errors.New(friendlyDownloadError(err, exitCheckTimeout))
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return ExitInfo{}, fmt.Errorf("查询接口返回 HTTP %d", response.StatusCode)
	}
	data, err := io.ReadAll(io.LimitReader(response.Body, 64<<10))
	if err != nil {
		return ExitInfo{}, err
	}
	info, ok := parseExitInfo(data)
	if !ok {
		return ExitInfo{}, errors.New("读不懂查询接口返回的内容")
	}
	return info, nil
}

// exitEndpointHost 是查询接口的主机和端口，从内核的日志里找这次查询走的出口用。
func exitEndpointHost(endpoint string) (string, int) {
	parsed, err := url.Parse(endpoint)
	if err != nil {
		return "", 0
	}
	port := 443
	if parsed.Scheme == "http" {
		port = 80
	}
	if number, err := strconv.Atoi(parsed.Port()); err == nil {
		port = number
	}
	return parsed.Hostname(), port
}

// exitChecker 记着两种出口最近一次查询的结果。
type exitChecker struct {
	mutex sync.Mutex
	view  ExitView
}

func (checker *exitChecker) state(direct bool) *ExitState {
	if direct {
		return &checker.view.Direct
	}
	return &checker.view.Proxy
}

// begin 标记开始查询，已经在查时返回 false。
func (checker *exitChecker) begin(direct bool) bool {
	checker.mutex.Lock()
	defer checker.mutex.Unlock()
	state := checker.state(direct)
	if state.Checking {
		return false
	}
	state.Checking = true
	return true
}

func (checker *exitChecker) finish(direct bool, info ExitInfo, err error) {
	checker.mutex.Lock()
	defer checker.mutex.Unlock()
	state := checker.state(direct)
	if err != nil {
		*state = ExitState{Error: err.Error()}
		return
	}
	*state = ExitState{Info: &info}
}

func (checker *exitChecker) snapshot() ExitView {
	checker.mutex.Lock()
	defer checker.mutex.Unlock()
	view := checker.view
	if view.Proxy.Info != nil {
		info := *view.Proxy.Info
		view.Proxy.Info = &info
	}
	if view.Direct.Info != nil {
		info := *view.Direct.Info
		view.Direct.Info = &info
	}
	return view
}

// clearProxy 忘掉经节点的出口（内核停了）。
func (checker *exitChecker) clearProxy() {
	checker.mutex.Lock()
	defer checker.mutex.Unlock()
	if !checker.view.Proxy.Checking {
		checker.view.Proxy = ExitState{}
	}
}

// ---------- 设置页上的操作 ----------

// watchConnections 在内核运行时每两秒读一次它的连接：记下最近的连接、按出口累计流量；局域网共享期间同时记下经共享入口的
// 连接（共享页和网址诊断用）。
func (service *subscriptionService) watchConnections() {
	for range time.Tick(connectionPollInterval) {
		service.pollConnections()
	}
}

func (service *subscriptionService) pollConnections() {
	status := service.core.Status()
	if !status.Running {
		service.connections.ingest(CoreConnections{}, false)
		service.exits.clearProxy()
		return
	}
	snapshot, err := service.core.Snapshot()
	if err != nil {
		slog.Debug("读取内核的连接失败", "err", err)
		return
	}
	service.connections.ingest(snapshot, true)
	if status.Share.Listening {
		service.shares.record(snapshot.Connections)
	}
}

// Connections 是连接页的内容：最近一次读到的连接、流量统计和出口 IP。
func (service *subscriptionService) Connections() ConnectionsView {
	view := service.connections.view()
	view.Exit = service.exits.snapshot()
	return view
}

// CloseConnection 断开一条连接，id 为空时断开全部。
func (service *subscriptionService) CloseConnection(id string) error {
	if !service.core.Status().Running {
		return errCoreNotRunning
	}
	var err error
	if id == "" {
		err = service.core.CloseAllConnections()
	} else {
		err = service.core.CloseConnection(id)
	}
	if err != nil {
		return fmt.Errorf("没有断开：%v", err)
	}
	service.connections.forget(id)
	return nil
}

// ClearConnections 清空连接页的「最近的连接」。
func (service *subscriptionService) ClearConnections() {
	service.connections.clearRecent()
}

// ResetTraffic 清零按出口累计的流量。
func (service *subscriptionService) ResetTraffic() {
	service.connections.resetTraffic()
}

// SaveTraffic 保存流量统计，程序退出时调用。
func (service *subscriptionService) SaveTraffic() {
	service.connections.save()
}

// CheckExit 查出口 IP：direct 时直连（这台电脑自己的公网地址），否则经内核的代理端口（网站看到的地址），并从内核的日志里
// 找出这次查询走的出口。几个公开接口依次试；已经在查时直接返回。
func (service *subscriptionService) CheckExit(direct bool) error {
	proxyUrl, profileId := "", ""
	if !direct {
		status := service.core.Status()
		if !status.Running || status.Port == 0 {
			return errCoreNotRunning
		}
		proxyUrl = fmt.Sprintf("http://127.0.0.1:%d", status.Port)
		_ = service.onEngine(func() {
			if status := service.engine.Status(); status.State == statusOn && status.Profile != nil && status.Profile.IsSubscription() {
				profileId = status.Profile.Id
			}
		})
	}
	if !service.exits.begin(direct) {
		return nil
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Duration(len(exitIpEndpoints))*(exitCheckTimeout+diagnoseTraceWait+time.Second))
	defer cancel()
	var info ExitInfo
	var err error
	for _, endpoint := range exitIpEndpoints {
		if direct {
			info, err = fetchExitInfo(ctx, endpoint, "")
		} else {
			host, port := exitEndpointHost(endpoint)
			var trace *RouteTrace
			_, trace = service.core.TraceConnection(ctx, host, port, func() DiagnoseProbe {
				info, err = fetchExitInfo(ctx, endpoint, proxyUrl)
				return DiagnoseProbe{}
			})
			if err == nil && trace != nil {
				info.Outbound = trace.Outbound()
			}
		}
		if err == nil {
			info.Endpoint = endpoint
			break
		}
	}
	if err == nil {
		info.Checked = time.Now().Format(time.RFC3339)
		if profileId != "" {
			info.Node = service.core.CurrentNode(profileId)
		}
		slog.Info("查到出口 IP", "direct", direct, "ip", info.Ip, "country", info.CountryCode, "outbound", info.Outbound)
	}
	service.exits.finish(direct, info, err)
	return err
}

// trafficPath 是流量统计的文件。
func trafficPath(paths Paths) string {
	return filepath.Join(paths.Dir, trafficFileName)
}
