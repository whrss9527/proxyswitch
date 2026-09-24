package main

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/netip"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"
)

// 分流规则：把小火箭（Shadowrocket）或 Surge 格式的规则配置转换成内核（mihomo）的规则。
//
// 规则配置的 [Rule] 段从上到下匹配，第一条匹配的规则决定连接走代理、直连还是被拦截，FINAL 是都不匹配时的去向。
// RULE-SET 和 DOMAIN-SET 引用外部的规则列表（Surge 或 QuantumultX 格式），由 ProxySwitch 一起下载并展开；
// [Proxy Group] 里的策略组按默认选项归为三种去向之一。内核做不到的规则（USER-AGENT、URL-REGEX 这类要解密 HTTPS
// 才能判断的，以及 IP-ASN、PROCESS-NAME 等）跳过并计数。
//
// 带去广告的规则有几万条，逐条写进内核的配置会让每个连接逐条比对。去向相同的连续规则先后顺序不影响结果，
// 所以把它们合并：域名写成 domain 规则集（内核用前缀树匹配），IP 段写成 ipcidr 规则集，其余规则保留原样；
// 同一段里先放按域名判断的规则，再放需要解析出 IP 的规则，免得多做 DNS 查询。

const (
	rulePolicyProxy  = "proxy"
	rulePolicyDirect = "direct"
	rulePolicyReject = "reject"

	maxRuleFileSize     = 32 << 20
	ruleDownloadTimeout = time.Minute
	// 条数不多的域名和 IP 段直接写成规则，不单独建规则集文件。
	ruleSetMinimum = 16
	// ruleSetWorkers 是同时下载的规则列表数。
	ruleSetWorkers = 4
)

var ruleUserAgent = "ProxySwitch/" + appVersion

// RulePreset 是设置页里可以直接选的规则配置。
type RulePreset struct {
	Name        string `json:"name"`
	Url         string `json:"url"`
	Description string `json:"description"`
}

// rulePresets 来自 Shadowrocket-ADBlock-Rules-Forever，每天自动生成更新。
const rulePresetBase = "https://johnshall.github.io/Shadowrocket-ADBlock-Rules-Forever/"

var rulePresets = []RulePreset{
	{"黑名单 + 去广告", rulePresetBase + "sr_top500_banlist_ad.conf", "被墙的网站走节点，其余直连；拦截广告"},
	{"白名单 + 去广告", rulePresetBase + "sr_top500_whitelist_ad.conf", "国内网站和能直连的国外网站直连，其余走节点；拦截广告"},
	{"国内外划分 + 去广告", rulePresetBase + "sr_cnip_ad.conf", "中国的网站和 IP 直连，国外走节点；拦截广告"},
	{"黑名单", rulePresetBase + "sr_top500_banlist.conf", "被墙的网站走节点，其余直连"},
	{"白名单", rulePresetBase + "sr_top500_whitelist.conf", "国内网站和能直连的国外网站直连，其余走节点"},
	{"国内外划分", rulePresetBase + "sr_cnip.conf", "中国的网站和 IP 直连，国外走节点"},
	{"懒人配置", rulePresetBase + "lazy.conf", "按常用的网站和 App 分流，国内直连，国外走节点"},
}

// rulesLabel 是订阅配置的分流规则的简短说明：内置的大陆直连、预设的名字或「自定义规则」。
func rulesLabel(profile *Profile) string {
	if profile.Rules == "" {
		return "大陆直连"
	}
	for _, preset := range rulePresets {
		if preset.Url == profile.Rules {
			return preset.Name
		}
	}
	return "自定义规则"
}

// ruleEntry 是一条换成内核写法的规则，不含去向。
type ruleEntry struct {
	Kind      string
	Value     string
	NoResolve bool
}

// ruleLine 是规则配置里的一条规则：普通规则（Entry），或者引用外部列表的 RULE-SET / DOMAIN-SET（Set）。
type ruleLine struct {
	Entry     ruleEntry
	Set       string
	DomainSet bool
	Policy    string
}

// ruleConfig 是解析后的规则配置。Final 是 FINAL 的去向，没有写时为空；Skipped 是内核做不到而跳过的规则数。
type ruleConfig struct {
	Lines   []ruleLine
	Final   string
	Skipped int
}

// ruleSetSource 是一个要下载的规则列表。
type ruleSetSource struct {
	Url       string
	DomainSet bool
}

// Sets 返回引用的外部规则列表，去掉重复。
func (config ruleConfig) Sets() []ruleSetSource {
	var sets []ruleSetSource
	seen := map[ruleSetSource]bool{}
	for _, line := range config.Lines {
		source := ruleSetSource{line.Set, line.DomainSet}
		if line.Set != "" && !seen[source] {
			seen[source] = true
			sets = append(sets, source)
		}
	}
	return sets
}

// ---------- 解析 ----------

var (
	ruleDomainPattern   = regexp.MustCompile(`^[a-z0-9_-]+(\.[a-z0-9_-]+)*$`)
	ruleKeywordPattern  = regexp.MustCompile(`^[a-z0-9_.-]+$`)
	ruleWildcardPattern = regexp.MustCompile(`^[a-z0-9_.*?-]+$`)
	ruleCountryPattern  = regexp.MustCompile(`^[A-Z]{2}$`)
	rulePortPattern     = regexp.MustCompile(`^[0-9]{1,5}(-[0-9]{1,5})?(/[0-9]{1,5}(-[0-9]{1,5})?)*$`)
)

// parseRuleConfig 解析规则配置。没有分段的文件整个当作规则。
func parseRuleConfig(content []byte) (ruleConfig, error) {
	text := strings.TrimPrefix(string(content), "\xef\xbb\xbf")
	if strings.HasPrefix(strings.TrimSpace(text), "<") {
		return ruleConfig{}, errors.New("返回的是网页，规则地址可能填错了")
	}
	sections := map[string][]string{}
	current := ""
	for _, raw := range strings.Split(text, "\n") {
		line := strings.TrimSpace(raw)
		if line == "" || isRuleComment(line) {
			continue
		}
		if strings.HasPrefix(line, "[") && strings.HasSuffix(line, "]") {
			current = strings.ToLower(strings.TrimSpace(line[1 : len(line)-1]))
			continue
		}
		sections[current] = append(sections[current], line)
	}
	rules, found := sections["rule"]
	if !found {
		rules = sections[""]
	}
	groups := parseProxyGroups(sections["proxy group"])
	var config ruleConfig
	for _, line := range rules {
		if !config.add(line, groups) {
			break
		}
	}
	if len(config.Lines) == 0 && config.Final == "" {
		if strings.Contains(text, "\nrules:") || strings.HasPrefix(text, "rules:") {
			return config, errors.New("这是 Clash 的配置，现在只支持小火箭（Shadowrocket）和 Surge 格式的规则配置")
		}
		return config, errors.New("没有找到分流规则，请确认是小火箭（Shadowrocket）或 Surge 格式的规则配置")
	}
	return config, nil
}

func isRuleComment(line string) bool {
	return strings.HasPrefix(line, "#") || strings.HasPrefix(line, ";") || strings.HasPrefix(line, "//")
}

// stripRuleComment 去掉行尾的注释，例如 “DOMAIN-SUFFIX,x.com,PROXY # 说明”。
func stripRuleComment(line string) string {
	for _, marker := range []string{" #", "\t#", " //", "\t//"} {
		if index := strings.Index(line, marker); index >= 0 {
			line = line[:index]
		}
	}
	return strings.TrimSpace(line)
}

func splitRuleFields(line string) []string {
	fields := strings.Split(line, ",")
	for index := range fields {
		fields[index] = strings.TrimSpace(fields[index])
	}
	return fields
}

// add 加入一条规则，遇到 FINAL 时返回 false：之后的规则不会被用到。
func (config *ruleConfig) add(line string, groups map[string]proxyGroup) bool {
	fields := splitRuleFields(stripRuleComment(line))
	kind := strings.ToUpper(fields[0])
	switch kind {
	case "FINAL", "MATCH":
		policy := ""
		if len(fields) > 1 {
			policy = fields[1]
		}
		config.Final = resolvePolicy(policy, groups)
		return false
	case "RULE-SET", "DOMAIN-SET":
		if len(fields) < 3 {
			config.Skipped++
			return true
		}
		if !strings.HasPrefix(strings.ToLower(fields[1]), "http://") && !strings.HasPrefix(strings.ToLower(fields[1]), "https://") {
			// Surge 内置的规则集：LAN 是局域网地址，本来就直连；SYSTEM 等跳过。
			if !strings.EqualFold(fields[1], "LAN") {
				config.Skipped++
			}
			return true
		}
		config.Lines = append(config.Lines, ruleLine{Set: fields[1], DomainSet: kind == "DOMAIN-SET", Policy: resolvePolicy(fields[2], groups)})
		return true
	}
	if len(fields) < 3 {
		config.Skipped++
		return true
	}
	entry, ok := convertRuleEntry(kind, fields[1], fields[3:])
	if !ok {
		config.Skipped++
		return true
	}
	config.Lines = append(config.Lines, ruleLine{Entry: entry, Policy: resolvePolicy(fields[2], groups)})
	return true
}

// convertRuleEntry 把一条 Surge / QuantumultX 写法的规则换成内核的写法，内核做不到或写得不对时返回 false。
func convertRuleEntry(kind, value string, options []string) (ruleEntry, bool) {
	noResolve := false
	for _, option := range options {
		if strings.EqualFold(option, "no-resolve") {
			noResolve = true
		}
	}
	switch kind {
	case "DOMAIN", "HOST":
		domain, ok := normalizeRuleDomain(value)
		return ruleEntry{Kind: "DOMAIN", Value: domain}, ok
	case "DOMAIN-SUFFIX", "HOST-SUFFIX":
		domain, ok := normalizeRuleDomain(strings.TrimPrefix(value, "."))
		return ruleEntry{Kind: "DOMAIN-SUFFIX", Value: domain}, ok
	case "DOMAIN-KEYWORD", "HOST-KEYWORD":
		keyword := strings.ToLower(value)
		return ruleEntry{Kind: "DOMAIN-KEYWORD", Value: keyword}, ruleKeywordPattern.MatchString(keyword)
	case "DOMAIN-WILDCARD", "HOST-WILDCARD":
		pattern := strings.ToLower(value)
		return ruleEntry{Kind: "DOMAIN-WILDCARD", Value: pattern}, ruleWildcardPattern.MatchString(pattern)
	case "DOMAIN-REGEX":
		_, err := regexp.Compile(value)
		return ruleEntry{Kind: "DOMAIN-REGEX", Value: value}, value != "" && err == nil
	case "IP-CIDR", "IP-CIDR6", "IP6-CIDR":
		prefix, ok := normalizeRulePrefix(value)
		if !ok {
			return ruleEntry{}, false
		}
		kind = "IP-CIDR"
		if prefix.Addr().Is6() {
			kind = "IP-CIDR6"
		}
		return ruleEntry{Kind: kind, Value: prefix.String(), NoResolve: noResolve}, true
	case "GEOIP":
		country := strings.ToUpper(value)
		return ruleEntry{Kind: "GEOIP", Value: country, NoResolve: noResolve}, ruleCountryPattern.MatchString(country)
	case "DST-PORT", "DEST-PORT":
		return ruleEntry{Kind: "DST-PORT", Value: value}, rulePortPattern.MatchString(value)
	}
	return ruleEntry{}, false
}

func normalizeRuleDomain(value string) (string, bool) {
	domain := strings.TrimSuffix(strings.ToLower(value), ".")
	return domain, ruleDomainPattern.MatchString(domain)
}

// normalizeRulePrefix 解析 IP 段，单个 IP 当作只含它自己的段。
func normalizeRulePrefix(value string) (netip.Prefix, bool) {
	if prefix, err := netip.ParsePrefix(value); err == nil {
		return prefix.Masked(), true
	}
	if address, err := netip.ParseAddr(value); err == nil {
		return netip.PrefixFrom(address, address.BitLen()), true
	}
	return netip.Prefix{}, false
}

// proxyGroup 是规则配置里的策略组。Default 是 policy-select-name 指定的默认选项。
type proxyGroup struct {
	Kind    string
	Members []string
	Default string
}

// parseProxyGroups 解析 [Proxy Group]：每行 “名字 = 类型,选项,选项,…,参数=值”，名字不区分大小写。
func parseProxyGroups(lines []string) map[string]proxyGroup {
	groups := map[string]proxyGroup{}
	for _, line := range lines {
		name, definition, found := strings.Cut(stripRuleComment(line), "=")
		if !found {
			continue
		}
		fields := splitRuleFields(definition)
		group := proxyGroup{Kind: strings.ToLower(fields[0])}
		for _, field := range fields[1:] {
			if key, value, isParameter := strings.Cut(field, "="); isParameter {
				if strings.EqualFold(strings.TrimSpace(key), "policy-select-name") {
					group.Default = strings.TrimSpace(value)
				}
				continue
			}
			if field != "" {
				group.Members = append(group.Members, field)
			}
		}
		groups[strings.ToLower(strings.TrimSpace(name))] = group
	}
	return groups
}

// resolvePolicy 把规则的去向归为代理、直连、拦截三种。手动选择的策略组看默认选项（policy-select-name，
// 没有写时是第一个选项），自动测速等其他类型的组、节点和不认识的名字都算代理。
func resolvePolicy(name string, groups map[string]proxyGroup) string {
	for depth := 0; depth < 8; depth++ {
		key := strings.ToLower(strings.TrimSpace(name))
		switch {
		case key == "direct":
			return rulePolicyDirect
		case key == "reject" || strings.HasPrefix(key, "reject-"):
			return rulePolicyReject
		}
		group, found := groups[key]
		if !found || group.Kind != "select" {
			return rulePolicyProxy
		}
		name = group.Default
		if name == "" && len(group.Members) > 0 {
			name = group.Members[0]
		}
	}
	return rulePolicyProxy
}

// parseRuleList 解析 RULE-SET 引用的规则列表：每行一条 Surge 或 QuantumultX 写法的规则，行里写的去向忽略，
// 由引用它的规则决定；也接受 Clash 规则集的 payload 写法和只写域名或 IP 段的行。
// domainSet 为真时是 DOMAIN-SET：每行一个域名，以 . 开头表示包括它的子域名。
func parseRuleList(content []byte, domainSet bool) (entries []ruleEntry, skipped int, err error) {
	text := strings.TrimPrefix(string(content), "\xef\xbb\xbf")
	if strings.HasPrefix(strings.TrimSpace(text), "<") {
		return nil, 0, errors.New("返回的是网页，不是规则列表")
	}
	for _, raw := range strings.Split(text, "\n") {
		line := strings.TrimSpace(raw)
		if line == "" || isRuleComment(line) || line == "payload:" {
			continue
		}
		line = strings.TrimSpace(strings.TrimPrefix(line, "- "))
		line = strings.Trim(stripRuleComment(line), `'"`)
		var entry ruleEntry
		ok := false
		if fields := splitRuleFields(line); len(fields) >= 2 && !domainSet {
			entry, ok = convertRuleEntry(strings.ToUpper(fields[0]), fields[1], fields[2:])
		} else if len(fields) == 1 {
			entry, ok = bareRuleEntry(line, domainSet)
		}
		if !ok {
			skipped++
			continue
		}
		entries = append(entries, entry)
	}
	return entries, skipped, nil
}

// bareRuleEntry 解析只写了域名或 IP 段的一行：以 . 或 +. 开头的域名包括子域名，带 * 的是通配。
func bareRuleEntry(line string, domainSet bool) (ruleEntry, bool) {
	if !domainSet {
		if prefix, ok := normalizeRulePrefix(line); ok {
			return convertRuleEntry("IP-CIDR", prefix.String(), nil)
		}
	}
	switch {
	case strings.HasPrefix(line, "+."):
		return convertRuleEntry("DOMAIN-SUFFIX", line[2:], nil)
	case strings.HasPrefix(line, "."):
		return convertRuleEntry("DOMAIN-SUFFIX", line[1:], nil)
	case strings.ContainsAny(line, "*?"):
		return convertRuleEntry("DOMAIN-WILDCARD", line, nil)
	}
	return convertRuleEntry("DOMAIN", line, nil)
}

// ---------- 转换 ----------

// convertedRules 是转换后交给内核的规则：Rules 是规则行，其中的规则集是 Providers 里的文件。
// Count 是展开规则列表后的规则数；Final 是其余网站的去向；Geo 表示用到了 GEOIP，需要地理数据。
type convertedRules struct {
	Rules     []string
	Providers []convertedProvider
	Count     int
	Final     string
	Geo       bool
}

type convertedProvider struct {
	Name     string
	Behavior string
	Lines    []string
}

type policyRule struct {
	entry  ruleEntry
	policy string
}

// corePolicyName 是去向在内核配置里的名字：代理交给 ProxySwitch 组，也就是正在使用的订阅选中的节点。
func corePolicyName(policy string) string {
	switch policy {
	case rulePolicyDirect:
		return "DIRECT"
	case rulePolicyReject:
		return "REJECT"
	}
	return coreTopGroup
}

// convertRules 展开规则列表，把去向相同的连续规则合并成规则集。sets 是按地址下载好的规则列表，
// 没下载到的列表按空处理。prefix 用来给规则集起名字。
func convertRules(config ruleConfig, sets map[ruleSetSource][]ruleEntry, prefix string) convertedRules {
	var rules []policyRule
	for _, line := range config.Lines {
		if line.Set == "" {
			rules = append(rules, policyRule{line.Entry, line.Policy})
			continue
		}
		for _, entry := range sets[ruleSetSource{line.Set, line.DomainSet}] {
			rules = append(rules, policyRule{entry, line.Policy})
		}
	}
	result := convertedRules{Count: len(rules), Final: config.Final}
	for start := 0; start < len(rules); {
		end := start
		for end < len(rules) && rules[end].policy == rules[start].policy {
			end++
		}
		result.addRun(rules[start:end], prefix)
		start = end
	}
	if result.Final == "" {
		// 没有 FINAL 时其余网站走代理：开启的是代理配置，不认识的网站能访问比被直连挡住更合理。
		result.Final = rulePolicyProxy
	}
	result.Rules = append(result.Rules, "MATCH,"+corePolicyName(result.Final))
	return result
}

// addRun 转换一段去向相同的规则：域名规则集、其他按域名判断的规则、不解析域名的 IP 段、要解析域名的 IP 段、GEOIP。
func (result *convertedRules) addRun(rules []policyRule, prefix string) {
	target := corePolicyName(rules[0].policy)
	seen := map[string]bool{}
	var domains, inline, quietIps, ips, needsIp []string
	for _, rule := range rules {
		entry := rule.entry
		key := entry.Kind + "," + entry.Value + "," + strconv.FormatBool(entry.NoResolve)
		if seen[key] {
			continue
		}
		seen[key] = true
		switch entry.Kind {
		case "DOMAIN":
			domains = append(domains, entry.Value)
		case "DOMAIN-SUFFIX":
			domains = append(domains, "+."+entry.Value)
		case "IP-CIDR", "IP-CIDR6":
			if entry.NoResolve {
				quietIps = append(quietIps, entry.Value)
			} else {
				ips = append(ips, entry.Value)
			}
		case "GEOIP":
			result.Geo = true
			rule := "GEOIP," + entry.Value + "," + target
			if entry.NoResolve {
				rule += ",no-resolve"
			}
			needsIp = append(needsIp, rule)
		default:
			inline = append(inline, entry.Kind+","+entry.Value+","+target)
		}
	}
	if len(domains) >= ruleSetMinimum {
		result.addProvider("domain", domains, target, "", prefix)
	} else {
		for _, domain := range domains {
			if suffix, found := strings.CutPrefix(domain, "+."); found {
				result.Rules = append(result.Rules, "DOMAIN-SUFFIX,"+suffix+","+target)
			} else {
				result.Rules = append(result.Rules, "DOMAIN,"+domain+","+target)
			}
		}
	}
	result.Rules = append(result.Rules, inline...)
	result.addIps(quietIps, target, ",no-resolve", prefix)
	result.addIps(ips, target, "", prefix)
	result.Rules = append(result.Rules, needsIp...)
}

func (result *convertedRules) addIps(ips []string, target, option, prefix string) {
	if len(ips) >= ruleSetMinimum {
		result.addProvider("ipcidr", ips, target, option, prefix)
		return
	}
	for _, ip := range ips {
		kind := "IP-CIDR"
		if strings.Contains(ip, ":") {
			kind = "IP-CIDR6"
		}
		result.Rules = append(result.Rules, kind+","+ip+","+target+option)
	}
}

func (result *convertedRules) addProvider(behavior string, lines []string, target, option, prefix string) {
	name := fmt.Sprintf("%s-%d", prefix, len(result.Providers)+1)
	result.Providers = append(result.Providers, convertedProvider{Name: name, Behavior: behavior, Lines: lines})
	result.Rules = append(result.Rules, "RULE-SET,"+name+","+target+option)
}

// ---------- 保存 ----------

// ruleManifest 是转换好的规则，保存在规则目录的 rules.json，生成内核配置时读取。
type ruleManifest struct {
	Rules     []string                    `json:"rules"`
	Providers map[string]CoreRuleProvider `json:"providers"`
}

// CoreRuleProvider 是一个规则集文件，Path 相对于内核的工作目录。
type CoreRuleProvider struct {
	Behavior string `json:"behavior"`
	Path     string `json:"path"`
}

const ruleManifestName = "rules.json"

// ruleDir 是配置 profileId 的规则在内核工作目录里的位置（用 / 分隔，内核配置里也用这个写法）。
func ruleDir(profileId string) string {
	return "rules/" + profileId
}

// writeConvertedRules 把转换好的规则写到 rules/<配置 id>/<版本>/，版本是内容的摘要：内容没变时不重复写，
// 内核的配置也不变；内容变了路径跟着变，内核重新加载时读到的是新文件。
func writeConvertedRules(coreDir, profileId string, converted convertedRules) (string, error) {
	hash := sha256.New()
	for _, rule := range converted.Rules {
		fmt.Fprintln(hash, rule)
	}
	for _, provider := range converted.Providers {
		fmt.Fprintln(hash, provider.Name, provider.Behavior, len(provider.Lines))
		for _, line := range provider.Lines {
			fmt.Fprintln(hash, line)
		}
	}
	revision := hex.EncodeToString(hash.Sum(nil))[:12]
	relative := ruleDir(profileId) + "/" + revision
	target := filepath.Join(coreDir, filepath.FromSlash(relative))
	if fileExists(filepath.Join(target, ruleManifestName)) {
		return revision, nil
	}
	temporary := target + ".download"
	_ = os.RemoveAll(temporary)
	if err := os.MkdirAll(temporary, 0o755); err != nil {
		return "", fmt.Errorf("无法保存规则：%v", err)
	}
	manifest := ruleManifest{Rules: converted.Rules, Providers: map[string]CoreRuleProvider{}}
	for index, provider := range converted.Providers {
		name := strconv.Itoa(index+1) + ".txt"
		if err := os.WriteFile(filepath.Join(temporary, name), []byte(strings.Join(provider.Lines, "\n")+"\n"), 0o644); err != nil {
			_ = os.RemoveAll(temporary)
			return "", fmt.Errorf("无法保存规则：%v", err)
		}
		manifest.Providers[provider.Name] = CoreRuleProvider{Behavior: provider.Behavior, Path: relative + "/" + name}
	}
	data, _ := json.Marshal(manifest)
	if err := os.WriteFile(filepath.Join(temporary, ruleManifestName), data, 0o644); err != nil {
		_ = os.RemoveAll(temporary)
		return "", fmt.Errorf("无法保存规则：%v", err)
	}
	_ = os.RemoveAll(target)
	if err := os.Rename(temporary, target); err != nil {
		_ = os.RemoveAll(temporary)
		return "", fmt.Errorf("无法保存规则：%v", err)
	}
	return revision, nil
}

// readRuleManifest 读取保存好的规则。
func readRuleManifest(coreDir, profileId, revision string) (*ruleManifest, error) {
	data, err := os.ReadFile(filepath.Join(coreDir, filepath.FromSlash(ruleDir(profileId)), revision, ruleManifestName))
	if err != nil {
		return nil, err
	}
	var manifest ruleManifest
	if err := json.Unmarshal(data, &manifest); err != nil {
		return nil, err
	}
	return &manifest, nil
}

// removeRuleRevisions 删除配置 profileId 除 keep 以外的规则版本；keep 为空时删除这个配置的全部规则。
func removeRuleRevisions(coreDir, profileId, keep string) {
	dir := filepath.Join(coreDir, filepath.FromSlash(ruleDir(profileId)))
	if keep == "" {
		_ = os.RemoveAll(dir)
		return
	}
	entries, _ := os.ReadDir(dir)
	for _, entry := range entries {
		if entry.Name() != keep && entry.Name() != ruleSetCacheDir {
			_ = os.RemoveAll(filepath.Join(dir, entry.Name()))
		}
	}
}

// ---------- 下载 ----------

// ruleSetCacheDir 保存上次下载成功的规则列表：下次下载失败时仍然可以用。
const ruleSetCacheDir = "sets"

// rulesDownload 是一次下载并转换好的规则。Revision 是保存的版本；Sets 是引用的规则列表数，
// FailedSets 是其中没下载到、也没有上次的副本而没有用上的；Skipped 是内核做不到而跳过的规则数。
type rulesDownload struct {
	Revision   string
	Count      int
	Sets       int
	FailedSets int
	Skipped    int
	Final      string
	Geo        bool
}

// RulesInfo 是一个订阅配置的分流规则的下载记录，保存在 state.json。Source 是规则地址的摘要，地址改了要重新下载；
// Updated 是上次下载成功的时间，Attempted 是上次尝试的时间；Revision 是在用的规则版本；其余字段见 rulesDownload。
type RulesInfo struct {
	Source     string `json:"source,omitempty"`
	Updated    string `json:"updated,omitempty"`
	Attempted  string `json:"attempted,omitempty"`
	Error      string `json:"error,omitempty"`
	Revision   string `json:"revision,omitempty"`
	Rules      int    `json:"rules,omitempty"`
	Sets       int    `json:"sets,omitempty"`
	FailedSets int    `json:"failed_sets,omitempty"`
	Skipped    int    `json:"skipped,omitempty"`
	Final      string `json:"final,omitempty"`
	Geo        bool   `json:"geo,omitempty"`
}

// RulesCheck 是在编辑配置时检查规则地址的结果：规则数（不含规则列表里的）、引用的规则列表数、跳过的规则数、其余网站的去向。
type RulesCheck struct {
	Rules   int    `json:"rules"`
	Sets    int    `json:"sets"`
	Skipped int    `json:"skipped"`
	Final   string `json:"final"`
}

// checkRuleConfig 下载规则配置检查地址是否可用，不下载引用的规则列表，不保存。
func checkRuleConfig(address string, paths []string) (RulesCheck, error) {
	if err := validateRulesUrl(address); err != nil {
		return RulesCheck{}, err
	}
	content, err := fetchRuleFile(address, paths)
	if err != nil {
		return RulesCheck{}, err
	}
	config, err := parseRuleConfig(content)
	if err != nil {
		return RulesCheck{}, err
	}
	check := RulesCheck{Sets: len(config.Sets()), Skipped: config.Skipped, Final: config.Final}
	for _, line := range config.Lines {
		if line.Set == "" {
			check.Rules++
		}
	}
	if check.Final == "" {
		check.Final = rulePolicyProxy
	}
	return check, nil
}

// fetchRules 下载规则配置和它引用的规则列表，转换后保存到内核工作目录的 rules/<配置 id>/ 下。
// 规则列表下载失败时用上次下载的副本，没有副本的跳过并计数。
func fetchRules(coreDir, profileId, address string, paths []string) (rulesDownload, error) {
	if err := validateRulesUrl(address); err != nil {
		return rulesDownload{}, err
	}
	content, err := fetchRuleFile(address, paths)
	if err != nil {
		return rulesDownload{}, err
	}
	config, err := parseRuleConfig(content)
	if err != nil {
		return rulesDownload{}, err
	}
	sources := config.Sets()
	result := rulesDownload{Sets: len(sources), Skipped: config.Skipped}
	cacheDir := filepath.Join(coreDir, filepath.FromSlash(ruleDir(profileId)), ruleSetCacheDir)
	type setResult struct {
		entries []ruleEntry
		skipped int
		err     error
	}
	results := make([]setResult, len(sources))
	var group sync.WaitGroup
	slots := make(chan struct{}, ruleSetWorkers)
	for index, source := range sources {
		group.Add(1)
		go func() {
			defer group.Done()
			slots <- struct{}{}
			defer func() { <-slots }()
			data, err := fetchRuleFile(source.Url, paths)
			cache := filepath.Join(cacheDir, ruleSetCacheName(source))
			if err == nil {
				_ = os.MkdirAll(cacheDir, 0o755)
				_ = writeFileAtomically(cache, data)
			} else if cached, readErr := os.ReadFile(cache); readErr == nil {
				slog.Warn("下载规则列表失败，使用上次下载的", "url", source.Url, "err", err)
				data, err = cached, nil
			}
			if err != nil {
				results[index] = setResult{err: err}
				return
			}
			entries, skipped, err := parseRuleList(data, source.DomainSet)
			results[index] = setResult{entries, skipped, err}
		}()
	}
	group.Wait()
	sets := map[ruleSetSource][]ruleEntry{}
	keep := map[string]bool{}
	for index, source := range sources {
		keep[ruleSetCacheName(source)] = true
		if results[index].err != nil {
			slog.Warn("规则列表没有用上", "url", source.Url, "err", results[index].err)
			result.FailedSets++
			continue
		}
		sets[source] = results[index].entries
		result.Skipped += results[index].skipped
	}
	if cached, err := os.ReadDir(cacheDir); err == nil {
		for _, entry := range cached {
			if !keep[entry.Name()] {
				_ = os.Remove(filepath.Join(cacheDir, entry.Name()))
			}
		}
	}
	converted := convertRules(config, sets, profileId+"-rules")
	revision, err := writeConvertedRules(coreDir, profileId, converted)
	if err != nil {
		return rulesDownload{}, err
	}
	result.Revision, result.Count, result.Final, result.Geo = revision, converted.Count, converted.Final, converted.Geo
	return result, nil
}

func ruleSetCacheName(source ruleSetSource) string {
	sum := sha256.Sum256([]byte(fmt.Sprintf("%v %s", source.DomainSet, source.Url)))
	return hex.EncodeToString(sum[:])[:16] + ".txt"
}

func validateRulesUrl(address string) error {
	parsed, err := url.Parse(strings.TrimSpace(address))
	if err != nil || (parsed.Scheme != "http" && parsed.Scheme != "https") || parsed.Host == "" {
		return errors.New("规则地址应以 http:// 或 https:// 开头")
	}
	return nil
}

// fetchRuleFile 下载规则配置或规则列表，依次尝试各条网络路径，都失败时返回第一条路径的错误。
func fetchRuleFile(address string, paths []string) ([]byte, error) {
	var firstErr error
	for _, proxyUrl := range paths {
		data, err := fetchRuleFileVia(address, proxyUrl)
		if err == nil {
			return data, nil
		}
		if firstErr == nil {
			firstErr = err
		}
	}
	if firstErr == nil {
		firstErr = errors.New("没有可用的网络路径")
	}
	return nil, firstErr
}

func fetchRuleFileVia(address, proxyUrl string) ([]byte, error) {
	request, err := http.NewRequest(http.MethodGet, address, nil)
	if err != nil {
		return nil, errors.New("规则地址格式不对")
	}
	request.Header.Set("User-Agent", ruleUserAgent)
	response, err := updateClient(proxyUrl, ruleDownloadTimeout).Do(request)
	if err != nil {
		return nil, errors.New(friendlyDownloadError(err, ruleDownloadTimeout))
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		if response.StatusCode == http.StatusNotFound || response.StatusCode == http.StatusGone {
			return nil, fmt.Errorf("服务器返回 HTTP %d，地址可能填错了或已经失效", response.StatusCode)
		}
		return nil, fmt.Errorf("服务器返回 HTTP %d", response.StatusCode)
	}
	data, err := io.ReadAll(io.LimitReader(response.Body, maxRuleFileSize+1))
	if err != nil {
		return nil, errors.New("下载中断：" + friendlyDownloadError(err, ruleDownloadTimeout))
	}
	if len(data) > maxRuleFileSize {
		return nil, fmt.Errorf("文件超过 %d MB", maxRuleFileSize>>20)
	}
	return data, nil
}
