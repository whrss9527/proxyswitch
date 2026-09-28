package main

import (
	"encoding/json"
	"net/url"
	"strconv"
	"strings"
)

// 订阅配置由 mihomo 内核代理。这里按订阅生成内核的配置：每个订阅是一个读本地文件的 proxy-provider，
// 配一个手动选择的组（默认选“自动选择”，即延迟最低的节点）；顶层的 ProxySwitch 组选正在使用的订阅，
// 规则把流量交给它。YAML 兼容 JSON，所以直接用 encoding/json 生成，不用处理 YAML 的引号和转义。

// coreVersion 是使用的 mihomo 版本。Windows 上下载这个版本的官方发布，并按发布构建时写入的 SHA-256 校验。
const coreVersion = "v1.19.31"

const (
	defaultCorePort = 17890
	// coreTopGroup 是规则最终把流量交给的组，它的选项是各个订阅的组。
	coreTopGroup = "ProxySwitch"
	// coreHealthInterval 是内核在后台测试节点延迟的间隔（秒），lazy 表示只在组被使用时才测。
	coreHealthInterval = 1800
	// coreShareListener 是局域网共享的入口，也是它专用的分流规则（sub-rules）的名字。
	coreShareListener = "lan-share"
	// coreUpstreamProxy 是本机用其他代理时，共享的流量转发过去的那个代理在内核里的名字。
	coreUpstreamProxy = "上游代理"
)

// 共享出去的流量往哪走，跟着本机的代理状态。
const (
	shareUpstreamDirect      = "direct"
	shareUpstreamCore        = "core"
	shareUpstreamProxy       = "proxy"
	shareUpstreamUnsupported = "unsupported"
)

// ShareUpstream 是共享出去的流量往哪走：Kind 是 direct（本机没开代理，设备经这台电脑直连）、core（本机用的是订阅，
// 设备走同样的节点和分流规则）、proxy（本机用的是其他代理，转发给 Proxy）或 unsupported（PAC 没法转发，设备暂时直连，
// Reason 说明原因）。
type ShareUpstream struct {
	Kind   string `json:"kind"`
	Proxy  string `json:"proxy,omitempty"`
	Reason string `json:"reason,omitempty"`
}

// CoreShare 是局域网共享：共享入口的端口、允许连进来的来源（IP 段，含本机回环）、共享流量的去向。
type CoreShare struct {
	Port     int
	Allowed  []string
	Upstream ShareUpstream
}

// CoreSettings 是内核应该处于的状态，由引擎按配置和已下载的订阅算出来。BinaryStamp 是内核程序文件的修改时间，
// 程序被替换（更新内核）后改变，内核随之重启。Active 是正在使用的订阅配置的 id（可以为空）；Mode 是它的模式；
// GeoReady 表示地理数据已下载，大陆直连规则和 GEOIP 规则才能生效。Rules 是它按规则分流时用的规则（见 rules.go，
// 最后一条是 MATCH），RuleProviders 是其中的规则集文件；Rules 为空时用内置的大陆直连。
type CoreSettings struct {
	Binary        string
	BinaryStamp   string
	Dir           string
	Port          int
	TestUrl       string
	Active        string
	Mode          string
	GeoReady      bool
	Rules         []string
	RuleProviders map[string]CoreRuleProvider
	// CustomRules 是自定义规则在内核里的写法，排在分流规则前面，全局代理时也生效。
	CustomRules []string
	// PolicyGroups 是策略组，节点来自订阅 GroupSource（正在使用的订阅，没有时是第一个订阅）。
	PolicyGroups  []PolicyGroup
	GroupSource   string
	Subscriptions []CoreSubscription
	// Share 是局域网共享，nil 表示没开。没有订阅时内核只为共享运行，本机的代理端口不监听。
	Share *CoreShare
	// Tun 表示开启 TUN 模式：虚拟网卡接管整台电脑的流量，内核要以管理员权限运行（见 core_tun_windows.go）。
	Tun bool
}

// coreMixedPort 是内核在本机提供代理的端口：只为局域网共享运行时为 0（不监听）。
func coreMixedPort(settings CoreSettings) int {
	if len(settings.Subscriptions) == 0 {
		return 0
	}
	return settings.Port
}

// CoreSubscription 是一个已下载的订阅。Node 为空表示自动选择；Revision 在订阅文件更新后改变，内核据此重新读取。
type CoreSubscription struct {
	Id       string
	Node     string
	Revision string
}

// coreServer 是订阅配置的代理地址：内核在本机监听的端口。
func coreServer(port int) string {
	return "127.0.0.1:" + strconv.Itoa(port)
}

func coreProviderName(profileId string) string {
	return profileId + "-nodes"
}

func coreAutoGroup(profileId string) string {
	return profileId + "-auto"
}

// 大陆直连规则用到的地理数据：mihomo 在工作目录里按这些文件名查找。依次尝试的下载地址里，jsDelivr 在国内一般能直接访问。
var coreGeoFiles = []struct {
	Name string
	Urls []string
}{
	{"Country.mmdb", []string{
		"https://testingcf.jsdelivr.net/gh/MetaCubeX/meta-rules-dat@release/country.mmdb",
		"https://fastly.jsdelivr.net/gh/MetaCubeX/meta-rules-dat@release/country.mmdb",
		"https://github.com/MetaCubeX/meta-rules-dat/releases/download/latest/country.mmdb",
	}},
	{"GeoSite.dat", []string{
		"https://testingcf.jsdelivr.net/gh/MetaCubeX/meta-rules-dat@release/geosite.dat",
		"https://fastly.jsdelivr.net/gh/MetaCubeX/meta-rules-dat@release/geosite.dat",
		"https://github.com/MetaCubeX/meta-rules-dat/releases/download/latest/geosite.dat",
	}},
}

// 本机和局域网地址无论什么模式都直连。
var corePrivateRules = []string{
	"DOMAIN,localhost,DIRECT",
	"DOMAIN-SUFFIX,local,DIRECT",
	"IP-CIDR,127.0.0.0/8,DIRECT,no-resolve",
	"IP-CIDR,10.0.0.0/8,DIRECT,no-resolve",
	"IP-CIDR,172.16.0.0/12,DIRECT,no-resolve",
	"IP-CIDR,192.168.0.0/16,DIRECT,no-resolve",
	"IP-CIDR,169.254.0.0/16,DIRECT,no-resolve",
	"IP-CIDR,100.64.0.0/10,DIRECT,no-resolve",
	"IP-CIDR6,::1/128,DIRECT,no-resolve",
	"IP-CIDR6,fc00::/7,DIRECT,no-resolve",
	"IP-CIDR6,fe80::/10,DIRECT,no-resolve",
}

// 大陆直连：国内的域名和 IP 直连，其余走节点。
var coreMainlandRules = []string{
	"GEOSITE,cn,DIRECT",
	"GEOIP,CN,DIRECT",
}

type coreHealthCheck struct {
	Enable   bool   `json:"enable"`
	Url      string `json:"url"`
	Interval int    `json:"interval"`
	Lazy     bool   `json:"lazy"`
}

type coreProvider struct {
	Type        string          `json:"type"`
	Path        string          `json:"path"`
	HealthCheck coreHealthCheck `json:"health-check"`
}

type coreGroup struct {
	Name      string   `json:"name"`
	Type      string   `json:"type"`
	Proxies   []string `json:"proxies,omitempty"`
	Use       []string `json:"use,omitempty"`
	Filter    string   `json:"filter,omitempty"`
	Url       string   `json:"url,omitempty"`
	Interval  int      `json:"interval,omitempty"`
	Tolerance int      `json:"tolerance,omitempty"`
	Strategy  string   `json:"strategy,omitempty"`
	Lazy      bool     `json:"lazy,omitempty"`
}

type coreListener struct {
	Name   string `json:"name"`
	Type   string `json:"type"`
	Listen string `json:"listen"`
	Port   int    `json:"port"`
	Rule   string `json:"rule"`
}

type coreProxy struct {
	Name     string `json:"name"`
	Type     string `json:"type"`
	Server   string `json:"server"`
	Port     int    `json:"port"`
	Username string `json:"username,omitempty"`
	Password string `json:"password,omitempty"`
	Tls      bool   `json:"tls,omitempty"`
}

type coreSniffer struct {
	Enable              bool                      `json:"enable"`
	ParsePureIp         bool                      `json:"parse-pure-ip"`
	OverrideDestination bool                      `json:"override-destination"`
	ForceDnsMapping     bool                      `json:"force-dns-mapping"`
	Sniff               map[string]map[string]any `json:"sniff"`
}

// coreTun 是 TUN 模式的虚拟网卡：自动设置路由接管整台电脑的流量，DNS 查询交给内核。用 gVisor 协议栈，
// 不需要 Windows 防火墙放行。
type coreTun struct {
	Enable              bool     `json:"enable"`
	Device              string   `json:"device"`
	Stack               string   `json:"stack"`
	AutoRoute           bool     `json:"auto-route"`
	AutoDetectInterface bool     `json:"auto-detect-interface"`
	DnsHijack           []string `json:"dns-hijack"`
}

// coreDns 是 TUN 模式下内核的 DNS：fake-ip 让分流规则拿到域名，走节点的域名由节点解析，直连的用国内的 DoH 解析。
// 局域网、系统联网检测、游戏机和时间同步的域名返回真实的 IP。
type coreDns struct {
	Enable            bool     `json:"enable"`
	Ipv6              bool     `json:"ipv6"`
	EnhancedMode      string   `json:"enhanced-mode"`
	FakeIpRange       string   `json:"fake-ip-range"`
	FakeIpFilter      []string `json:"fake-ip-filter"`
	DefaultNameserver []string `json:"default-nameserver"`
	Nameserver        []string `json:"nameserver"`
}

// coreTunDevice 是虚拟网卡的名字。测试时换成建不出来的名字（太长），内核只记一条日志，不会真的改路由。
var coreTunDevice = "ProxySwitch"

func coreTunConfig() (*coreTun, *coreDns) {
	return &coreTun{
			Enable: true, Device: coreTunDevice, Stack: "gvisor", AutoRoute: true, AutoDetectInterface: true,
			DnsHijack: []string{"any:53", "tcp://any:53"},
		}, &coreDns{
			Enable: true, EnhancedMode: "fake-ip", FakeIpRange: "198.18.0.1/16",
			FakeIpFilter: []string{
				"*.lan", "*.local", "*.localdomain", "*.home.arpa", "localhost.ptlogin2.qq.com",
				"+.msftconnecttest.com", "+.msftncsi.com", "time.windows.com", "+.pool.ntp.org", "time.*.com",
				"+.srv.nintendo.net", "+.stun.playstation.net", "xbox.*.microsoft.com", "+.xboxlive.com", "stun.*.*",
			},
			DefaultNameserver: []string{"223.5.5.5", "119.29.29.29"},
			Nameserver:        []string{"https://doh.pub/dns-query", "https://dns.alidns.com/dns-query"},
		}
}

type coreRuleProvider struct {
	Type     string `json:"type"`
	Behavior string `json:"behavior"`
	Format   string `json:"format"`
	Path     string `json:"path"`
}

type coreConfigFile struct {
	MixedPort          int                         `json:"mixed-port"`
	AllowLan           bool                        `json:"allow-lan"`
	BindAddress        string                      `json:"bind-address"`
	Mode               string                      `json:"mode"`
	LogLevel           string                      `json:"log-level"`
	UnifiedDelay       bool                        `json:"unified-delay"`
	TcpConcurrent      bool                        `json:"tcp-concurrent"`
	FindProcessMode    string                      `json:"find-process-mode"`
	ExternalController string                      `json:"external-controller"`
	Secret             string                      `json:"secret"`
	Profile            map[string]bool             `json:"profile"`
	GeoAutoUpdate      bool                        `json:"geo-auto-update"`
	GeodataMode        bool                        `json:"geodata-mode"`
	GeoxUrl            map[string]string           `json:"geox-url"`
	LanAllowedIps      []string                    `json:"lan-allowed-ips,omitempty"`
	Sniffer            coreSniffer                 `json:"sniffer"`
	Tun                *coreTun                    `json:"tun,omitempty"`
	Dns                *coreDns                    `json:"dns,omitempty"`
	Listeners          []coreListener              `json:"listeners,omitempty"`
	Proxies            []coreProxy                 `json:"proxies,omitempty"`
	ProxyProviders     map[string]coreProvider     `json:"proxy-providers"`
	ProxyGroups        []coreGroup                 `json:"proxy-groups"`
	RuleProviders      map[string]coreRuleProvider `json:"rule-providers,omitempty"`
	Rules              []string                    `json:"rules"`
	SubRules           map[string][]string         `json:"sub-rules,omitempty"`
}

// coreConfigText 生成内核的配置。controller 和 secret 是 REST API 的地址和密码，只在本机监听。
func coreConfigText(settings CoreSettings, controller, secret string) []byte {
	testUrl := settings.TestUrl
	if testUrl == "" {
		testUrl = defaultTestUrl
	}
	config := coreConfigFile{
		MixedPort:     coreMixedPort(settings),
		BindAddress:   "127.0.0.1",
		Mode:          "rule",
		LogLevel:      "warning",
		UnifiedDelay:  true,
		TcpConcurrent: true,
		// 只在有按程序分流的规则时查找连接来自哪个程序。
		FindProcessMode:    "strict",
		ExternalController: controller,
		Secret:             secret,
		// 选中的节点由 ProxySwitch 记在配置文件里，每次启动后重新设置，不用内核自己记。
		Profile:        map[string]bool{"store-selected": false, "store-fake-ip": false},
		GeoxUrl:        map[string]string{},
		ProxyProviders: map[string]coreProvider{},
		ProxyGroups:    []coreGroup{},
		// 域名嗅探：自己解析 DNS 被污染的设备（PS5 等）和程序会按假 IP 来连，从 TLS / HTTP 握手里取回域名，
		// 按域名分流，并把域名交给节点去解析。
		Sniffer: coreSniffer{Enable: true, ParsePureIp: true, OverrideDestination: true, ForceDnsMapping: true, Sniff: map[string]map[string]any{
			"HTTP": {"ports": []any{80, "8080-8880"}},
			"TLS":  {"ports": []any{443, 8443}},
		}},
	}
	for _, geo := range coreGeoFiles {
		key := map[string]string{"Country.mmdb": "mmdb", "GeoSite.dat": "geosite"}[geo.Name]
		config.GeoxUrl[key] = geo.Urls[0]
	}
	var subscriptionGroups []string
	for _, subscription := range settings.Subscriptions {
		provider := coreProviderName(subscription.Id)
		config.ProxyProviders[provider] = coreProvider{
			Type:        "file",
			Path:        subscriptionFile(subscription.Id),
			HealthCheck: coreHealthCheck{Enable: true, Url: testUrl, Interval: coreHealthInterval, Lazy: true},
		}
		config.ProxyGroups = append(config.ProxyGroups,
			coreGroup{Name: coreAutoGroup(subscription.Id), Type: "url-test", Use: []string{provider}, Url: testUrl, Interval: coreHealthInterval, Tolerance: 50, Lazy: true},
			coreGroup{Name: subscription.Id, Type: "select", Proxies: []string{coreAutoGroup(subscription.Id)}, Use: []string{provider}},
		)
		subscriptionGroups = append(subscriptionGroups, subscription.Id)
	}
	if len(subscriptionGroups) == 0 {
		// 只为局域网共享运行：本机不经内核，没有节点。
		config.Rules = []string{"MATCH,DIRECT"}
		addShare(&config, settings.Share)
		data, _ := json.MarshalIndent(config, "", "  ")
		return append(data, '\n')
	}
	config.ProxyGroups = append(config.ProxyGroups, coreGroup{Name: coreTopGroup, Type: "select", Proxies: subscriptionGroups})
	if containsString(subscriptionGroups, settings.GroupSource) {
		for _, group := range settings.PolicyGroups {
			config.ProxyGroups = append(config.ProxyGroups, corePolicyGroup(group, settings.GroupSource, testUrl))
		}
	}
	config.Rules = append(config.Rules, corePrivateRules...)
	config.Rules = append(config.Rules, settings.CustomRules...)
	switch {
	case settings.Mode == "global":
	case len(settings.Rules) > 0:
		// 地理数据还没下载时先跳过 GEOIP 和 GEOSITE 规则：内核缺少数据会拒绝整个配置。
		for _, rule := range settings.Rules {
			if settings.GeoReady || !strings.HasPrefix(rule, "GEOIP,") && !strings.HasPrefix(rule, "GEOSITE,") {
				config.Rules = append(config.Rules, rule)
			}
		}
		config.RuleProviders = map[string]coreRuleProvider{}
		for name, provider := range settings.RuleProviders {
			config.RuleProviders[name] = coreRuleProvider{Type: "file", Behavior: provider.Behavior, Format: "text", Path: provider.Path}
		}
	case settings.GeoReady:
		config.Rules = append(config.Rules, coreMainlandRules...)
	}
	if !strings.HasPrefix(config.Rules[len(config.Rules)-1], "MATCH,") {
		config.Rules = append(config.Rules, "MATCH,"+coreTopGroup)
	}
	if settings.Tun {
		config.Tun, config.Dns = coreTunConfig()
	}
	addShare(&config, settings.Share)
	data, _ := json.MarshalIndent(config, "", "  ")
	return append(data, '\n')
}

// addShare 加上局域网共享：共享入口监听所有网卡，只放行允许的来源；它的流量按同名的 sub-rules 分流，
// 切换去向时只改规则，入口不动，已有的连接不断。
func addShare(config *coreConfigFile, share *CoreShare) {
	if share == nil {
		return
	}
	config.LanAllowedIps = share.Allowed
	config.Listeners = []coreListener{{Name: coreShareListener, Type: "mixed", Listen: "0.0.0.0", Port: share.Port, Rule: coreShareListener}}
	upstream := share.Upstream
	if upstream.Kind == shareUpstreamProxy {
		if proxy, ok := coreUpstream(upstream.Proxy); ok {
			config.Proxies = []coreProxy{proxy}
		} else {
			upstream = ShareUpstream{Kind: shareUpstreamDirect}
		}
	}
	config.SubRules = map[string][]string{coreShareListener: shareRules(upstream, config.Rules)}
}

// shareRules 是共享入口的分流：本机用订阅时和本机完全一样；本机用其他代理时局域网直连、其余转发给它；否则全部直连。
func shareRules(upstream ShareUpstream, mainRules []string) []string {
	switch upstream.Kind {
	case shareUpstreamCore:
		return mainRules
	case shareUpstreamProxy:
		return append(append([]string{}, corePrivateRules...), "MATCH,"+coreUpstreamProxy)
	}
	return []string{"MATCH,DIRECT"}
}

// coreUpstream 把 http://主机:端口、https://主机:端口 或 socks5://主机:端口（可以带用户名和密码）写成内核里的代理。
func coreUpstream(proxyUrl string) (coreProxy, bool) {
	parsed, err := url.Parse(proxyUrl)
	if err != nil || parsed.Hostname() == "" {
		return coreProxy{}, false
	}
	port, err := strconv.Atoi(parsed.Port())
	if err != nil || port < 1 || port > 65535 {
		return coreProxy{}, false
	}
	kind := map[string]string{"http": "http", "https": "http", "socks5": "socks5", "socks5h": "socks5", "socks": "socks5"}[parsed.Scheme]
	if kind == "" {
		return coreProxy{}, false
	}
	proxy := coreProxy{Name: coreUpstreamProxy, Type: kind, Server: parsed.Hostname(), Port: port, Tls: parsed.Scheme == "https"}
	if parsed.User != nil {
		proxy.Username = parsed.User.Username()
		proxy.Password, _ = parsed.User.Password()
	}
	return proxy, true
}
