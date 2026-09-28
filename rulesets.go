package main

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"net/url"
	"path"
	"strings"
	"unicode/utf8"
)

// 分流规则集：一列远程（或本机、内置）的规则，按顺序匹配，每条有自己的去向和开关。
// 自定义规则排在所有规则集前面，都没命中的流量按「其余流量」（final_policy）走。所有订阅配置共用这一份。
//
// 三种加载方式：内置的（大陆直连）直接写成规则；纯规则列表（.list、.txt、.yaml、.mrs）下载后交给内核的 rule-provider；
// 小火箭 / Surge / Clash 的完整配置（.conf 等，或者下载后认出来是完整配置的）由 ProxySwitch 转换后并入。

// RuleSet 是一个规则集。Url 是 http(s) 地址、本机文件（file:///…）或内置的 builtin://china-direct；Policy 是去向：
// 空表示按规则文件里写的策略（完整配置；纯列表空着时走节点，内置的直连）；Behavior 是纯列表的类型
// （classical 完整规则 / domain 域名 / ipcidr IP 段），空着时从内容判断；Disabled 表示暂时停用。
type RuleSet struct {
	Name     string `json:"name"`
	Url      string `json:"url"`
	Policy   string `json:"policy,omitempty"`
	Behavior string `json:"behavior,omitempty"`
	Disabled bool   `json:"disabled,omitempty"`
}

const (
	builtinChinaDirect = "builtin://china-direct"
	builtinScheme      = "builtin://"

	ruleSetBuiltin  = "builtin"
	ruleSetProvider = "provider"
	ruleSetConvert  = "convert"

	behaviorClassical = "classical"
	behaviorDomain    = "domain"
	behaviorIpcidr    = "ipcidr"

	maxRuleSetNameLength = 40
)

var ruleSetBehaviors = []string{behaviorClassical, behaviorDomain, behaviorIpcidr}

// ruleListExtensions 是纯规则列表的扩展名：交给内核的 rule-provider 加载。
var ruleListExtensions = []string{"list", "txt", "text", "yaml", "yml", "mrs"}

// Id 是规则集在内核里的名字（rule-provider）和在内核目录里的文件夹名，由地址算出来：地址改了就是另一个规则集。
func (set RuleSet) Id() string {
	return ruleSetId(set.Url)
}

func ruleSetId(address string) string {
	sum := sha256.Sum256([]byte(address))
	return "rs-" + hex.EncodeToString(sum[:4])
}

func (set RuleSet) IsBuiltin() bool {
	return strings.HasPrefix(strings.ToLower(set.Url), builtinScheme)
}

// extension 是地址里文件的扩展名（不含查询参数），小写。
func (set RuleSet) extension() string {
	address := set.Url
	if parsed, err := url.Parse(address); err == nil {
		address = parsed.Path
	}
	return strings.TrimPrefix(strings.ToLower(path.Ext(address)), ".")
}

// guessKind 是没下载前按地址猜的加载方式：内置、纯列表（按扩展名）或完整配置。下载后以认出来的为准。
func (set RuleSet) guessKind() string {
	switch {
	case set.IsBuiltin():
		return ruleSetBuiltin
	case containsString(ruleListExtensions, set.extension()):
		return ruleSetProvider
	}
	return ruleSetConvert
}

// format 是纯列表在内核里的格式。
func (set RuleSet) format() string {
	switch set.extension() {
	case "yaml", "yml":
		return "yaml"
	case "mrs":
		return "mrs"
	}
	return "text"
}

// storedExtension 是纯列表保存在内核目录里时的扩展名。
func (set RuleSet) storedExtension() string {
	switch set.format() {
	case "yaml":
		return "yaml"
	case "mrs":
		return "mrs"
	}
	return "txt"
}

// guessBehavior 按地址猜纯列表的类型：带 geoip、ip 字样的是 IP 段；.mrs 只能是域名或 IP 段；geosite、domain 字样的是域名；
// 其余当完整规则（和 macOS 版一样）。文本和 YAML 的列表下载后按内容判断。
func (set RuleSet) guessBehavior() string {
	lowered := strings.ToLower(set.Url)
	for _, hint := range []string{"geoip", "ipcidr", "ip-cidr", "_ip.", "/ip.", "-ip.", "cnip", "chinaip", "china_ip", "china-ip"} {
		if strings.Contains(lowered, hint) {
			return behaviorIpcidr
		}
	}
	if set.format() == "mrs" {
		return behaviorDomain
	}
	for _, hint := range []string{"geosite", "_domain", "-domain", "/domain."} {
		if strings.Contains(lowered, hint) {
			return behaviorDomain
		}
	}
	return behaviorClassical
}

// chinaDirectRuleSet 是内置的大陆直连：国内的域名（GEOSITE cn）和 IP（GEOIP CN）直连，需要下载一次地理数据。
func chinaDirectRuleSet() RuleSet {
	return RuleSet{Name: "国内直连", Url: builtinChinaDirect, Policy: rulePolicyDirect}
}

// builtinRules 是内置规则集展开成的规则，target 是去向在内核里的名字。
func builtinRules(address, target string) []string {
	if address == builtinChinaDirect {
		return []string{"GEOSITE,cn," + target, "GEOIP,CN," + target}
	}
	return nil
}

// ruleSetDefaultName 是没填名字时的名字：规则库里的名字，或者地址里的文件名，没有文件名时用主机名。
func ruleSetDefaultName(address string) string {
	if entry := ruleLibraryEntry(address); entry != nil {
		return entry.Name
	}
	parsed, err := url.Parse(address)
	if err != nil {
		return "规则"
	}
	if name := strings.TrimSuffix(path.Base(parsed.Path), path.Ext(parsed.Path)); name != "" && name != "." && name != "/" {
		return name
	}
	if parsed.Hostname() != "" {
		return parsed.Hostname()
	}
	return "规则"
}

func normalizeRuleSet(set *RuleSet) {
	set.Url = strings.TrimSpace(set.Url)
	if !set.IsBuiltin() {
		set.Url = normalizeSubscriptionAddress(set.Url)
	}
	set.Url = migratedRuleUrl(set.Url)
	set.Name = strings.TrimSpace(set.Name)
	if set.Name == "" {
		set.Name = ruleSetDefaultName(set.Url)
	}
	if set.Policy != "" {
		set.Policy = normalizeRulePolicy(set.Policy)
	}
	set.Behavior = strings.ToLower(strings.TrimSpace(set.Behavior))
	if set.IsBuiltin() {
		set.Behavior = ""
	}
}

// validateRuleSetUrl 检查规则集的地址：http(s)、本机文件或内置的。
func validateRuleSetUrl(address string) error {
	if strings.HasPrefix(strings.ToLower(address), builtinScheme) {
		if address != builtinChinaDirect {
			return fmt.Errorf("没有内置的规则 %q，可用：%s", address, builtinChinaDirect)
		}
		return nil
	}
	if _, isFile := localFilePath(address); isFile {
		return nil
	}
	return validateRulesUrl(address)
}

// validateRuleSets 检查规则集和「其余流量」的去向：地址不能重复，去向是固定的三种或现有的策略组。
func validateRuleSets(config *Config) error {
	groups := config.GroupNames()
	seen := map[string]bool{}
	for index, set := range config.RuleSets {
		if set.Url == "" {
			return fmt.Errorf("第 %d 个规则集没有填地址", index+1)
		}
		if utf8.RuneCountInString(set.Name) > maxRuleSetNameLength {
			return fmt.Errorf("规则集「%s」的名字太长，最多 %d 个字", set.Name, maxRuleSetNameLength)
		}
		if err := validateRuleSetUrl(set.Url); err != nil {
			return fmt.Errorf("规则集「%s」的%v", set.Name, err)
		}
		if seen[set.Url] {
			return fmt.Errorf("规则集「%s」的地址和前面的重复了", set.Name)
		}
		seen[set.Url] = true
		if set.Policy != "" {
			if err := validateRulePolicy(set.Policy, groups); err != nil {
				return fmt.Errorf("规则集「%s」的%v", set.Name, err)
			}
		}
		if set.Behavior != "" {
			if !containsString(ruleSetBehaviors, set.Behavior) {
				return fmt.Errorf("规则集「%s」的类型 %q 不认识，可用：%s", set.Name, set.Behavior, strings.Join(ruleSetBehaviors, " / "))
			}
			if set.format() == "mrs" && set.Behavior == behaviorClassical {
				return fmt.Errorf("规则集「%s」是 mrs 格式，类型只能是 domain 或 ipcidr", set.Name)
			}
		}
	}
	if config.FinalPolicy != "" {
		if err := validateRulePolicy(config.FinalPolicy, groups); err != nil {
			return fmt.Errorf("其余流量（final_policy）的%v", err)
		}
	}
	return nil
}

// migrateRuleSets 把旧版每个订阅配置自己的规则地址（profiles 里的 rules）换成全局的规则集列表：第一个订阅配置用的规则启用，
// 其余的列出来但先停用；都没填（用的是内置的大陆直连）时是一条内置的国内直连。配置里已经有 rule_sets 时不动。
func migrateRuleSets(config *Config) {
	defer func() {
		for index := range config.Profiles {
			config.Profiles[index].Rules = ""
		}
	}()
	if config.RuleSets != nil {
		return
	}
	config.RuleSets = []RuleSet{}
	builtin := false
	for _, profile := range config.Profiles {
		if !profile.IsSubscription() {
			continue
		}
		address := migratedRuleUrl(strings.TrimSpace(profile.Rules))
		if address == "" {
			builtin = true
			continue
		}
		duplicate := false
		for _, set := range config.RuleSets {
			duplicate = duplicate || set.Url == address
		}
		if !duplicate {
			config.RuleSets = append(config.RuleSets, RuleSet{Url: address, Disabled: len(config.RuleSets) > 0})
		}
	}
	if len(config.RuleSets) == 0 {
		config.RuleSets = []RuleSet{chinaDirectRuleSet()}
	} else if builtin {
		china := chinaDirectRuleSet()
		china.Disabled = true
		config.RuleSets = append(config.RuleSets, china)
	}
}

// 旧版的小火箭规则预设放在 GitHub Pages 上，换成 GitHub 的原始地址（同一份文件）：国内连不上时可以换 jsDelivr 镜像。
const (
	oldRulePresetBase = "https://johnshall.github.io/Shadowrocket-ADBlock-Rules-Forever/"
	rulePresetBase    = "https://raw.githubusercontent.com/johnshall/Shadowrocket-ADBlock-Rules-Forever/release/"
)

func migratedRuleUrl(address string) string {
	if file, found := strings.CutPrefix(address, oldRulePresetBase); found && file != "" && !strings.Contains(file, "/") {
		return rulePresetBase + file
	}
	return address
}

// RetargetGroup 在策略组被删掉或改名后，把指向它的自定义规则、规则集和「其余流量」改到新的去向 target。
func (config *Config) RetargetGroup(name, target string) {
	old := ruleTargetGroupPrefix + name
	for index := range config.CustomRules {
		if config.CustomRules[index].Policy == old {
			config.CustomRules[index].Policy = target
		}
	}
	for index := range config.RuleSets {
		if config.RuleSets[index].Policy == old {
			config.RuleSets[index].Policy = target
		}
	}
	if config.FinalPolicy == old {
		config.FinalPolicy = target
	}
}

// rulesSummary 是分流规则的简短说明：只启用了一个规则集时是它的名字，否则是启用的个数。
func rulesSummary(config *Config) string {
	var names []string
	for _, set := range config.RuleSets {
		if !set.Disabled {
			names = append(names, set.Name)
		}
	}
	switch len(names) {
	case 0:
		return "没有启用规则集"
	case 1:
		return names[0]
	}
	return fmt.Sprintf("%d 个规则集", len(names))
}

// hasDownloadedRuleSets 表示有启用的、要下载的规则集（不是内置的）。
func (config *Config) hasDownloadedRuleSets() bool {
	for _, set := range config.RuleSets {
		if !set.Disabled && !set.IsBuiltin() {
			return true
		}
	}
	return false
}

// FindRuleSet 按地址查找规则集，找不到返回 nil。
func (config *Config) FindRuleSet(address string) *RuleSet {
	for index := range config.RuleSets {
		if config.RuleSets[index].Url == address {
			return &config.RuleSets[index]
		}
	}
	return nil
}

// ---------- 规则库 ----------

// RuleLibraryEntry 是规则库里的一条：常用的公开规则，一键加进规则集。Policy 是默认去向，空表示按文件里写的策略。
type RuleLibraryEntry struct {
	Name     string `json:"name"`
	Detail   string `json:"detail"`
	Url      string `json:"url"`
	Policy   string `json:"policy,omitempty"`
	Behavior string `json:"behavior,omitempty"`
	Category string `json:"category"`
}

// 规则库收录 blackmatrix7、MetaCubeX、ACL4SSR 和 johnshall 维护的规则，和 macOS 版相同。地址用 GitHub 的原始地址，
// 下载时连不上会换 jsDelivr 镜像。
const (
	libraryBlackmatrix = "https://raw.githubusercontent.com/blackmatrix7/ios_rule_script/master/rule/Clash/"
	libraryMetaGeo     = "https://raw.githubusercontent.com/MetaCubeX/meta-rules-dat/meta/geo/"
	libraryAcl4ssr     = "https://raw.githubusercontent.com/ACL4SSR/ACL4SSR/master/Clash/"
)

var ruleLibrary = func() []RuleLibraryEntry {
	const (
		basics       = "基础"
		ads          = "去广告"
		services     = "境外服务"
		ai           = "AI"
		streaming    = "流媒体"
		social       = "社交与游戏"
		shadowrocket = "小火箭完整配置"
	)
	entries := []RuleLibraryEntry{
		{"国内直连", ".cn 和国内的域名、国内 IP 直连，内置，不用下载规则（第一次用时下载一次地理数据）", builtinChinaDirect, rulePolicyDirect, "", basics},
		{"国内域名", "MetaCubeX 整理的国内域名（geosite:cn），比按 .cn 后缀判断全得多", libraryMetaGeo + "geosite/cn.mrs", rulePolicyDirect, behaviorDomain, basics},
		{"被墙网站", "已知被屏蔽的网站（geosite:gfw）", libraryMetaGeo + "geosite/gfw.mrs", rulePolicyProxy, behaviorDomain, basics},
		{"境外常用网站", "geosite:geolocation-!cn，国外常用网站都走节点", libraryMetaGeo + "geosite/geolocation-!cn.mrs", rulePolicyProxy, behaviorDomain, basics},
		{"广告与跟踪", "geosite:category-ads-all，体积小、够用", libraryMetaGeo + "geosite/category-ads-all.mrs", rulePolicyReject, behaviorDomain, ads},
		{"广告（ACL4SSR）", "常见广告域名", libraryAcl4ssr + "BanAD.list", rulePolicyReject, behaviorClassical, ads},
		{"应用内广告（ACL4SSR）", "程序和应用里的广告、统计上报", libraryAcl4ssr + "BanProgramAD.list", rulePolicyReject, behaviorClassical, ads},
		{"Apple", "苹果的服务，一般直连更快", libraryBlackmatrix + "Apple/Apple.list", rulePolicyDirect, behaviorClassical, services},
		{"Microsoft", "微软的服务", libraryBlackmatrix + "Microsoft/Microsoft.list", rulePolicyDirect, behaviorClassical, services},
		{"Google", "Google 全家", libraryBlackmatrix + "Google/Google.list", rulePolicyProxy, behaviorClassical, services},
		{"GitHub", "GitHub 及其静态资源", libraryBlackmatrix + "GitHub/GitHub.list", rulePolicyProxy, behaviorClassical, services},
		{"PayPal", "", libraryBlackmatrix + "PayPal/PayPal.list", rulePolicyProxy, behaviorClassical, services},
		{"OpenAI / ChatGPT", "", libraryBlackmatrix + "OpenAI/OpenAI.list", rulePolicyProxy, behaviorClassical, ai},
		{"Claude", "", libraryBlackmatrix + "Claude/Claude.list", rulePolicyProxy, behaviorClassical, ai},
		{"Gemini", "", libraryBlackmatrix + "Gemini/Gemini.list", rulePolicyProxy, behaviorClassical, ai},
		{"YouTube", "", libraryBlackmatrix + "YouTube/YouTube.list", rulePolicyProxy, behaviorClassical, streaming},
		{"Netflix", "", libraryBlackmatrix + "Netflix/Netflix.list", rulePolicyProxy, behaviorClassical, streaming},
		{"Disney+", "", libraryBlackmatrix + "Disney/Disney.list", rulePolicyProxy, behaviorClassical, streaming},
		{"Spotify", "", libraryBlackmatrix + "Spotify/Spotify.list", rulePolicyProxy, behaviorClassical, streaming},
		{"TikTok", "", libraryBlackmatrix + "TikTok/TikTok.list", rulePolicyProxy, behaviorClassical, streaming},
		{"哔哩哔哩", "国内直连", libraryBlackmatrix + "BiliBili/BiliBili.list", rulePolicyDirect, behaviorClassical, streaming},
		{"Telegram", "Telegram 的域名和 IP 段", libraryBlackmatrix + "Telegram/Telegram.list", rulePolicyProxy, behaviorClassical, social},
		{"Twitter / X", "", libraryBlackmatrix + "Twitter/Twitter.list", rulePolicyProxy, behaviorClassical, social},
		{"Steam", "商店和下载，一般直连", libraryBlackmatrix + "Steam/Steam.list", rulePolicyDirect, behaviorClassical, social},
	}
	// johnshall/Shadowrocket-ADBlock-Rules-Forever 每天自动生成的小火箭完整配置，带自己的 FINAL。
	presets := []struct{ name, file, detail string }{
		{"黑名单", "sr_top500_banlist.conf", "被墙的常用网站走节点，其余直连"},
		{"黑名单 + 去广告", "sr_top500_banlist_ad.conf", "黑名单，外加拦截广告和跟踪"},
		{"白名单", "sr_top500_whitelist.conf", "国内常用网站和国内 IP 直连，其余走节点"},
		{"白名单 + 去广告", "sr_top500_whitelist_ad.conf", "白名单，外加拦截广告和跟踪"},
		{"国内 IP 直连", "sr_cnip.conf", "只按 IP 归属分流：国内直连，国外走节点"},
		{"国内 IP 直连 + 去广告", "sr_cnip_ad.conf", "按 IP 归属分流，外加拦截广告"},
		{"全部直连 + 去广告", "sr_direct_banad.conf", "不走节点，只拦广告"},
		{"全部走节点 + 去广告", "sr_proxy_banad.conf", "全部走节点，外加拦截广告"},
		{"懒人配置", "lazy.conf", "按常用的网站和 App 分流，国内直连，国外走节点"},
	}
	for _, preset := range presets {
		entries = append(entries, RuleLibraryEntry{preset.name, preset.detail + "。完整配置，规则的去向和 FINAL 都按文件里写的", rulePresetBase + preset.file, "", "", shadowrocket})
	}
	return entries
}()

// ruleLibraryEntry 按地址查找规则库里的一条，找不到返回 nil。
func ruleLibraryEntry(address string) *RuleLibraryEntry {
	for index := range ruleLibrary {
		if ruleLibrary[index].Url == address {
			return &ruleLibrary[index]
		}
	}
	return nil
}

// ---------- 去向 ----------

// ruleSetTarget 是规则集的规则在内核里的去向：规则集设了去向就用它；否则按规则文件里写的：策略名和某个策略组同名
// （不区分大小写）时指到那个组，其余按归类后的走节点、直连、拦截。
func ruleSetTarget(forced, kind, name string, groups []string) string {
	if forced != "" {
		return corePolicyTarget(forced, groups)
	}
	if name != "" {
		for _, group := range groups {
			if strings.EqualFold(group, name) {
				return group
			}
		}
	}
	return corePolicyName(kind)
}

// RuleSetInfo 是一个规则集的下载记录，按规则集的 id 保存在 state.json。Updated 是上次下载成功的时间，Attempted 是上次尝试的
// 时间，Error 是上次失败的原因（失败时继续用上次下载的）；其余字段见 ruleSetDownload。
type RuleSetInfo struct {
	Updated    string `json:"updated,omitempty"`
	Attempted  string `json:"attempted,omitempty"`
	Error      string `json:"error,omitempty"`
	Kind       string `json:"kind,omitempty"`
	Revision   string `json:"revision,omitempty"`
	Format     string `json:"format,omitempty"`
	Behavior   string `json:"behavior,omitempty"`
	Rules      int    `json:"rules,omitempty"`
	Sets       int    `json:"sets,omitempty"`
	FailedSets int    `json:"failed_sets,omitempty"`
	Skipped    int    `json:"skipped,omitempty"`
	Final      string `json:"final,omitempty"`
	FinalName  string `json:"final_name,omitempty"`
	Geo        bool   `json:"geo,omitempty"`
}
