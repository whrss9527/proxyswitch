package main

import (
	"encoding/json"
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
)

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
	Subscriptions []CoreSubscription
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
	Url       string   `json:"url,omitempty"`
	Interval  int      `json:"interval,omitempty"`
	Tolerance int      `json:"tolerance,omitempty"`
	Lazy      bool     `json:"lazy,omitempty"`
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
	ProxyProviders     map[string]coreProvider     `json:"proxy-providers"`
	ProxyGroups        []coreGroup                 `json:"proxy-groups"`
	RuleProviders      map[string]coreRuleProvider `json:"rule-providers,omitempty"`
	Rules              []string                    `json:"rules"`
}

// coreConfigText 生成内核的配置。controller 和 secret 是 REST API 的地址和密码，只在本机监听。
func coreConfigText(settings CoreSettings, controller, secret string) []byte {
	testUrl := settings.TestUrl
	if testUrl == "" {
		testUrl = defaultTestUrl
	}
	config := coreConfigFile{
		MixedPort:          settings.Port,
		BindAddress:        "127.0.0.1",
		Mode:               "rule",
		LogLevel:           "warning",
		UnifiedDelay:       true,
		TcpConcurrent:      true,
		FindProcessMode:    "off",
		ExternalController: controller,
		Secret:             secret,
		// 选中的节点由 ProxySwitch 记在配置文件里，每次启动后重新设置，不用内核自己记。
		Profile:        map[string]bool{"store-selected": false, "store-fake-ip": false},
		GeoxUrl:        map[string]string{},
		ProxyProviders: map[string]coreProvider{},
		ProxyGroups:    []coreGroup{},
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
	config.ProxyGroups = append(config.ProxyGroups, coreGroup{Name: coreTopGroup, Type: "select", Proxies: subscriptionGroups})
	config.Rules = append(config.Rules, corePrivateRules...)
	switch {
	case settings.Mode == "global":
	case len(settings.Rules) > 0:
		// 地理数据还没下载时先跳过 GEOIP 规则：内核缺少数据会拒绝整个配置。
		for _, rule := range settings.Rules {
			if settings.GeoReady || !strings.HasPrefix(rule, "GEOIP,") {
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
	data, _ := json.MarshalIndent(config, "", "  ")
	return append(data, '\n')
}
