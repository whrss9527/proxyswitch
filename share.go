package main

import (
	"errors"
	"fmt"
	"net"
	"net/netip"
	"net/url"
	"reflect"
	"sort"
	"strconv"
	"strings"
	"sync"
)

// 局域网共享：让同一局域网里的设备（PS5、Switch、手机……）把这台电脑当代理服务器（这台电脑的 IP:端口），
// 享受和本机一样的网络。共享由内置的内核完成：它多开一个监听所有网卡的入口，只放行局域网里的来源，
// 共享的流量跟着本机的代理状态走——本机用订阅就走同样的节点和分流规则，本机用其他代理就转发给它，
// 本机没开代理就经这台电脑直连。没有订阅也能开共享。

const defaultSharePort = 17892

var (
	// shareLanPrefixes 是默认允许的来源：局域网里常见的私有网段。
	shareLanPrefixes = []string{"10.0.0.0/8", "172.16.0.0/12", "192.168.0.0/16", "169.254.0.0/16"}
	// shareLoopbackPrefixes 总是允许：内核自己的代理端口也受同一份名单限制。
	shareLoopbackPrefixes = []string{"127.0.0.0/8", "::1/128"}
)

// ShareConfig 是局域网共享的设置。Allowed 是允许使用的设备（IP 或网段，逗号或空格分隔），留空表示局域网里的所有设备；
// KeepAwake 表示共享期间不让电脑进入空闲睡眠，KeepAwakeOnBattery 表示用电池时也保持。
type ShareConfig struct {
	Enabled            bool   `json:"enabled"`
	Port               int    `json:"port"`
	Allowed            string `json:"allowed"`
	KeepAwake          bool   `json:"keep_awake"`
	KeepAwakeOnBattery bool   `json:"keep_awake_on_battery"`
}

// parseShareClients 把「192.168.1.20, 192.168.2.0/24」这样的文字拆成网段，认不出来的放在 invalid 里。
func parseShareClients(text string) (prefixes, invalid []string) {
	seen := map[string]bool{}
	for _, item := range strings.FieldsFunc(text, func(r rune) bool { return r == ',' || r == '，' || r == ';' || r == ' ' || r == '\n' || r == '\t' }) {
		prefix, ok := normalizeRulePrefix(strings.TrimSpace(item))
		if !ok {
			invalid = append(invalid, item)
			continue
		}
		if text := prefix.String(); !seen[text] {
			seen[text] = true
			prefixes = append(prefixes, text)
		}
	}
	return prefixes, invalid
}

// allowedPrefixes 是内核 lan-allowed-ips 里的网段：本机回环，加上填的设备或默认的局域网网段。
func (share ShareConfig) allowedPrefixes() []string {
	clients, _ := parseShareClients(share.Allowed)
	if len(clients) == 0 {
		clients = shareLanPrefixes
	}
	return append(append([]string{}, shareLoopbackPrefixes...), clients...)
}

func validateShare(share ShareConfig, corePort int) error {
	if share.Port < 1024 || share.Port > 65535 {
		return errors.New("share.port 需要在 1024~65535 之间")
	}
	if share.Port == corePort {
		return fmt.Errorf("share.port 不能和代理内核的端口 %d 相同", corePort)
	}
	if _, invalid := parseShareClients(share.Allowed); len(invalid) > 0 {
		return fmt.Errorf("share.allowed 里认不出这些地址：%s（填 IP 或网段，例如 192.168.1.20、192.168.1.0/24）", strings.Join(invalid, "、"))
	}
	return nil
}

// shareCommandTarget 把 on / off / toggle 换算成要设成的开关状态。
func shareCommandTarget(argument string, current bool) (bool, error) {
	switch strings.ToLower(strings.TrimSpace(argument)) {
	case "on", "enable", "start":
		return true, nil
	case "off", "disable", "stop":
		return false, nil
	case "", "toggle":
		return !current, nil
	}
	return false, fmt.Errorf("不认识「%s」：写 share on、share off 或 share toggle", argument)
}

// ---------- 共享流量的去向 ----------

// activeSubscriptionId 是正在交给内核使用的订阅配置（最近使用的配置是订阅并且已下载），没有时为空。
func (engine *Engine) activeSubscriptionId() string {
	if selected := engine.selectedProfile(); selected != nil && selected.IsSubscription() && engine.subscriptionLoaded(selected) {
		return selected.Id
	}
	return ""
}

// shareUpstream 算出共享出去的流量往哪走：本机用什么，共享的设备就用什么。
func (engine *Engine) shareUpstream(status Status, active string) ShareUpstream {
	direct := ShareUpstream{Kind: shareUpstreamDirect}
	switch status.State {
	case statusOn:
		profile := status.Profile
		switch {
		case profile.IsSubscription():
			if profile.Id == active {
				return ShareUpstream{Kind: shareUpstreamCore}
			}
		case profile.Pac != "":
			return ShareUpstream{Kind: shareUpstreamUnsupported, Reason: "本机用的是 PAC 脚本，没法转发给其他设备，共享的设备暂时直连"}
		case profile.Server != "":
			return engine.proxyUpstream(serverToUrl(profile.Server))
		}
	case statusExternal:
		if status.System.PacEnabled {
			return ShareUpstream{Kind: shareUpstreamUnsupported, Reason: "系统代理是 PAC 脚本，没法转发给其他设备，共享的设备暂时直连"}
		}
		if status.System.ProxyEnabled {
			return engine.proxyUpstream(serverToUrl(status.System.Server))
		}
	}
	return direct
}

// proxyUpstream 转发给本机在用的代理；指回共享入口自己时直连，免得绕圈。
func (engine *Engine) proxyUpstream(proxyUrl string) ShareUpstream {
	parsed, err := url.Parse(proxyUrl)
	if err != nil || parsed.Hostname() == "" {
		return ShareUpstream{Kind: shareUpstreamDirect}
	}
	if port, _ := strconv.Atoi(parsed.Port()); port == engine.config.Share.Port && isLocalHost(parsed.Hostname()) {
		return ShareUpstream{Kind: shareUpstreamDirect}
	}
	return ShareUpstream{Kind: shareUpstreamProxy, Proxy: proxyUrl}
}

// isLocalHost 表示 host 是这台电脑自己：localhost、回环地址或本机网卡的地址。
func isLocalHost(host string) bool {
	if strings.EqualFold(host, "localhost") {
		return true
	}
	address, err := netip.ParseAddr(host)
	if err != nil {
		return false
	}
	if address.IsLoopback() || address.IsUnspecified() {
		return true
	}
	addresses, _ := net.InterfaceAddrs()
	for _, item := range addresses {
		if network, ok := item.(*net.IPNet); ok {
			if local, ok := netip.AddrFromSlice(network.IP); ok && local.Unmap() == address.Unmap() {
				return true
			}
		}
	}
	return false
}

// coreShare 是交给内核的共享参数，没开共享时为 nil。
func (engine *Engine) coreShare(status Status, active string) *CoreShare {
	share := engine.config.Share
	if !share.Enabled {
		return nil
	}
	return &CoreShare{Port: share.Port, Allowed: share.allowedPrefixes(), Upstream: engine.shareUpstream(status, active)}
}

// RefreshShare 在定时检查时调用：本机的代理被其他程序改了之后，共享的去向跟着变。去向变了才让内核重新加载。
func (engine *Engine) RefreshShare(status Status) {
	if engine.core == nil || engine.config == nil || !engine.config.Share.Enabled {
		return
	}
	if share := engine.coreShare(status, engine.activeSubscriptionId()); !reflect.DeepEqual(share, engine.syncedShare) {
		engine.syncCore()
	}
}

// SetShareEnabled 开关局域网共享，保存到配置文件。
func (engine *Engine) SetShareEnabled(enabled bool) error {
	if engine.config == nil {
		return errNoConfig
	}
	if engine.config.Share.Enabled == enabled {
		return nil
	}
	if enabled && engine.core == nil {
		return errors.New("局域网共享需要 ProxySwitch 保持运行，请先打开 ProxySwitch")
	}
	if enabled && !fileExists(engine.coreBinary()) {
		if engine.config.Core.Path != "" {
			return fmt.Errorf("找不到代理内核：%s", engine.config.Core.Path)
		}
		return errors.New("局域网共享由内置的代理内核完成，请先在设置里下载内核")
	}
	updated := engine.config.Clone()
	updated.Share.Enabled = enabled
	return engine.SaveConfig(updated)
}

// shareAwake 是共享期间防睡眠的状态：holding 该阻止睡眠 / paused 用电池供电，按设置暂停 / off 不需要。
// 电脑一睡，共享的设备就断网了；默认只在接着电源时保持，免得忘了关把电用光。
func shareAwake(share ShareConfig, onBattery bool) string {
	switch {
	case !share.Enabled || !share.KeepAwake:
		return "off"
	case onBattery && !share.KeepAwakeOnBattery:
		return "paused"
	}
	return "holding"
}

// ---------- 设置页 ----------

// ShareInfo 是设置页里局域网共享的情况：共享的流量现在往哪走，这台电脑在局域网里的地址，是否在阻止睡眠
// （holding 正在保持唤醒 / paused 用电池暂停了 / off，由各平台补上）。
type ShareInfo struct {
	Upstream  ShareUpstream  `json:"upstream"`
	Addresses []LocalAddress `json:"addresses"`
	Awake     string         `json:"awake"`
}

func (engine *Engine) shareInfo(status Status) ShareInfo {
	info := ShareInfo{Upstream: ShareUpstream{Kind: shareUpstreamDirect}, Addresses: []LocalAddress{}, Awake: "off"}
	if engine.config != nil && engine.config.Share.Enabled {
		info.Upstream = engine.shareUpstream(status, engine.activeSubscriptionId())
	}
	return info
}

// LocalAddress 是这台电脑在局域网里的一个 IPv4 地址。Primary 表示默认路由所在的网卡（一般就是要填的那个）。
type LocalAddress struct {
	Interface string `json:"interface"`
	Ip        string `json:"ip"`
	Primary   bool   `json:"primary"`
}

// localAddresses 列出这台电脑的局域网 IPv4 地址：默认路由所在的排最前，虚拟网卡（WSL、Hyper-V、虚拟机）排后面。
func localAddresses() []LocalAddress {
	primary := ""
	// 向外“连接”一个 UDP 地址不会发出数据，只让系统选出默认路由对应的本机地址。
	if connection, err := net.Dial("udp4", "223.5.5.5:53"); err == nil {
		if address, ok := connection.LocalAddr().(*net.UDPAddr); ok {
			primary = address.IP.String()
		}
		connection.Close()
	}
	interfaces, _ := net.Interfaces()
	addresses := []LocalAddress{}
	for _, item := range interfaces {
		if item.Flags&net.FlagUp == 0 || item.Flags&net.FlagLoopback != 0 {
			continue
		}
		list, _ := item.Addrs()
		for _, address := range list {
			network, ok := address.(*net.IPNet)
			if !ok || network.IP.To4() == nil || network.IP.IsLinkLocalUnicast() || !network.IP.IsPrivate() {
				continue
			}
			ip := network.IP.String()
			addresses = append(addresses, LocalAddress{Interface: item.Name, Ip: ip, Primary: ip == primary})
		}
	}
	sort.SliceStable(addresses, func(i, j int) bool {
		if addresses[i].Primary != addresses[j].Primary {
			return addresses[i].Primary
		}
		return !isVirtualAdapter(addresses[i].Interface) && isVirtualAdapter(addresses[j].Interface)
	})
	return addresses
}

func isVirtualAdapter(name string) bool {
	lower := strings.ToLower(name)
	for _, marker := range []string{"vethernet", "vmware", "virtualbox", "hyper-v", "wsl", "docker", "vbox", "loopback", "tailscale", "zerotier"} {
		if strings.Contains(lower, marker) {
			return true
		}
	}
	return false
}

// ---------- 正在使用的设备和最近的连接 ----------

const shareHistoryLimit = 60

// ShareClient 是一台正经共享入口上网的设备：按来源 IP 归并的连接数、流量，最近访问的主机和走的出口。
type ShareClient struct {
	Ip           string `json:"ip"`
	Connections  int    `json:"connections"`
	Upload       int64  `json:"upload"`
	Download     int64  `json:"download"`
	LastHost     string `json:"last_host"`
	LastOutbound string `json:"last_outbound"`
}

// ShareConnection 是经共享入口的一条连接：哪台设备访问了什么、走了哪个出口、命中了哪条规则。
type ShareConnection struct {
	Id       string `json:"id"`
	Client   string `json:"client"`
	Host     string `json:"host"`
	Port     string `json:"port"`
	Outbound string `json:"outbound"`
	Rule     string `json:"rule"`
	Start    string `json:"start"`
}

// ShareActivity 是共享页里的「正在使用的设备」和「最近的连接」（新的在前）。
type ShareActivity struct {
	Clients []ShareClient     `json:"clients"`
	Recent  []ShareConnection `json:"recent"`
}

// shareHistory 记下经共享入口的连接：内核只列出还开着的连接，短连接一闪就没了，所以每次读取时把没见过的记下来。
type shareHistory struct {
	mutex  sync.Mutex
	seen   map[string]bool
	recent []ShareConnection
}

// record 从内核的连接列表里挑出共享入口的连接，记下新出现的，返回设备和最近的连接。
func (history *shareHistory) record(connections []CoreConnection) ShareActivity {
	history.mutex.Lock()
	defer history.mutex.Unlock()
	if history.seen == nil {
		history.seen = map[string]bool{}
	}
	var shared []CoreConnection
	for _, connection := range connections {
		if connection.Metadata.InboundName == coreShareListener {
			shared = append(shared, connection)
		}
	}
	sort.SliceStable(shared, func(i, j int) bool { return shared[i].Start < shared[j].Start })
	activity := ShareActivity{Clients: []ShareClient{}}
	index := map[string]int{}
	for _, connection := range shared {
		ip := connection.Metadata.SourceIp
		if position, found := index[ip]; found {
			client := &activity.Clients[position]
			client.Connections++
			client.Upload += connection.Upload
			client.Download += connection.Download
			client.LastHost, client.LastOutbound = connection.Target(), connection.Outbound()
		} else if ip != "" {
			index[ip] = len(activity.Clients)
			activity.Clients = append(activity.Clients, ShareClient{Ip: ip, Connections: 1, Upload: connection.Upload, Download: connection.Download, LastHost: connection.Target(), LastOutbound: connection.Outbound()})
		}
		if history.seen[connection.Id] {
			continue
		}
		history.seen[connection.Id] = true
		rule := strings.TrimSpace(connection.Rule + " " + connection.RulePayload)
		history.recent = append([]ShareConnection{{Id: connection.Id, Client: ip, Host: connection.Target(), Port: connection.Metadata.DestinationPort, Outbound: connection.Outbound(), Rule: rule, Start: connection.Start}}, history.recent...)
	}
	if len(history.recent) > shareHistoryLimit {
		history.recent = history.recent[:shareHistoryLimit]
	}
	// 见过的连接别无限增长：够多了就只留列表里的和现在还开着的。
	if len(history.seen) > 2000 {
		history.seen = map[string]bool{}
		for _, connection := range history.recent {
			history.seen[connection.Id] = true
		}
		for _, connection := range shared {
			history.seen[connection.Id] = true
		}
	}
	activity.Recent = append([]ShareConnection{}, history.recent...)
	return activity
}

// clear 清空最近的连接。见过的连接仍然记着，还开着的连接不会马上又出现在列表里。
func (history *shareHistory) clear() {
	history.mutex.Lock()
	defer history.mutex.Unlock()
	history.recent = nil
}

// recentFor 数最近的连接里设备访问 host（或它的子域名）的次数，网址诊断用。本机经共享入口的测试不算。
func (history *shareHistory) recentFor(host string) int {
	history.mutex.Lock()
	defer history.mutex.Unlock()
	host = strings.ToLower(host)
	count := 0
	for _, connection := range history.recent {
		target := strings.ToLower(connection.Host)
		if (target == host || strings.HasSuffix(target, "."+host)) && !isLocalHost(connection.Client) {
			count++
		}
	}
	return count
}
