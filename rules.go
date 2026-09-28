package main

import (
	"bytes"
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

// 分流规则：把小火箭（Shadowrocket）、Surge 或 Clash（mihomo）格式的规则配置转换成内核（mihomo）的规则。
//
// 规则配置的 [Rule] 段从上到下匹配，第一条匹配的规则决定连接走代理、直连还是被拦截，FINAL 是都不匹配时的去向。
// RULE-SET 和 DOMAIN-SET 引用外部的规则列表（Surge 或 QuantumultX 格式），由 ProxySwitch 一起下载并展开；
// [Proxy Group] 里的策略组按默认选项归为三种去向之一。内核做不到的规则（USER-AGENT、URL-REGEX 这类要解密 HTTPS
// 才能判断的，以及 IP-ASN 等）跳过并计数；PROCESS-NAME 和 PROCESS-PATH 按连接来自的程序匹配。Clash 配置用顶层的 rules 列表，策略组看 proxy-groups，
// RULE-SET 引用 rule-providers 里的规则集。
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

// ruleEntry 是一条换成内核写法的规则，不含去向。
type ruleEntry struct {
	Kind      string
	Value     string
	NoResolve bool
}

// ruleLine 是规则配置里的一条规则：普通规则（Entry），或者引用外部列表的 RULE-SET / DOMAIN-SET（Set）。
// NoResolve 表示引用的列表里的 IP 段都不解析域名（RULE-SET 后面写了 no-resolve）。Policy 是归类后的去向（走节点、直连、
// 拦截），Name 是文件里写的策略名：和某个策略组同名时指到那个组（见 ruleSetTarget）。
type ruleLine struct {
	Entry     ruleEntry
	Set       string
	DomainSet bool
	NoResolve bool
	Policy    string
	Name      string
}

// ruleConfig 是解析后的规则配置。Final 是 FINAL 归类后的去向，没有写时为空，FinalName 是文件里写的策略名；
// Skipped 是内核做不到而跳过的规则数。
type ruleConfig struct {
	Lines     []ruleLine
	Final     string
	FinalName string
	Skipped   int
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
	ruleSitePattern     = regexp.MustCompile(`^[a-z0-9!@_.-]+$`)
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
	if !found && clashConfigPattern.MatchString(text) {
		return parseClashRuleConfig(text)
	}
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
		return config, errors.New("没有找到分流规则，请确认是小火箭（Shadowrocket）、Surge 或 Clash 格式的规则配置")
	}
	return config, nil
}

// clashConfigPattern 认出 Clash（mihomo）的配置：顶层有 rules 列表。
var clashConfigPattern = regexp.MustCompile(`(?m)^rules:`)

// clashRuleProvider 是 Clash 配置里 rule-providers 的一项：http 类型从 Url 下载，inline 类型的内容写在 Payload 里。
type clashRuleProvider struct {
	Type     string
	Behavior string
	Format   string
	Url      string
	Payload  []string
}

// parseClashRuleConfig 解析 Clash（mihomo）的配置：rules 按顺序转换，策略组按 proxy-groups 里的第一个选项归为走节点、
// 直连或拦截，RULE-SET 引用 rule-providers 里的规则集（http 的下载，inline 的直接用）。
func parseClashRuleConfig(text string) (ruleConfig, error) {
	root := yamlMap(parseYamlLite(text))
	groups := map[string]proxyGroup{}
	for _, item := range yamlList(root["proxy-groups"]) {
		group := yamlMap(item)
		if name := yamlString(group["name"]); name != "" {
			groups[strings.ToLower(name)] = proxyGroup{Kind: strings.ToLower(yamlString(group["type"])), Members: yamlStrings(group["proxies"])}
		}
	}
	providers := map[string]clashRuleProvider{}
	for name, item := range yamlMap(root["rule-providers"]) {
		definition := yamlMap(item)
		providers[name] = clashRuleProvider{
			Type:     strings.ToLower(yamlString(definition["type"])),
			Behavior: strings.ToLower(yamlString(definition["behavior"])),
			Format:   strings.ToLower(yamlString(definition["format"])),
			Url:      yamlString(definition["url"]),
			Payload:  yamlStrings(definition["payload"]),
		}
	}
	var config ruleConfig
	for _, rule := range yamlStrings(root["rules"]) {
		if !config.addClash(rule, groups, providers) {
			break
		}
	}
	if len(config.Lines) == 0 && config.Final == "" {
		return config, errors.New("Clash 配置里没有找到分流规则（rules）")
	}
	return config, nil
}

// addClash 加入 Clash 配置里的一条规则，遇到 MATCH 时返回 false。PASS（跳过这条规则）和内核做不到的规则跳过。
func (config *ruleConfig) addClash(rule string, groups map[string]proxyGroup, providers map[string]clashRuleProvider) bool {
	fields := splitRuleFields(stripRuleComment(rule))
	kind := strings.ToUpper(fields[0])
	if len(fields) >= 3 && strings.EqualFold(fields[2], "PASS") || len(fields) == 2 && kind != "MATCH" && strings.EqualFold(fields[1], "PASS") {
		config.Skipped++
		return true
	}
	if kind != "RULE-SET" {
		return config.add(rule, groups)
	}
	provider, found := providers[fieldAt(fields, 1)]
	if len(fields) < 3 || !found {
		config.Skipped++
		return true
	}
	policy := resolvePolicy(fields[2], groups)
	domainSet := provider.Behavior == "domain"
	switch {
	case provider.Type == "inline":
		for _, item := range provider.Payload {
			if entry, ok := ruleListEntry(item, domainSet); ok {
				entry.NoResolve = entry.NoResolve || isIpRule(entry) && hasNoResolve(fields[3:])
				config.Lines = append(config.Lines, ruleLine{Entry: entry, Policy: policy, Name: fields[2]})
			} else {
				config.Skipped++
			}
		}
	case provider.Format == "mrs" || provider.Behavior != "domain" && provider.Behavior != "ipcidr" && provider.Behavior != "classical":
		// mrs 是二进制格式，读不了。
		config.Skipped++
	case strings.HasPrefix(strings.ToLower(provider.Url), "http://") || strings.HasPrefix(strings.ToLower(provider.Url), "https://"):
		config.Lines = append(config.Lines, ruleLine{Set: provider.Url, DomainSet: domainSet, NoResolve: hasNoResolve(fields[3:]), Policy: policy, Name: fields[2]})
	default:
		config.Skipped++
	}
	return true
}

func fieldAt(fields []string, index int) string {
	if index < len(fields) {
		return fields[index]
	}
	return ""
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
		config.Final, config.FinalName = resolvePolicy(policy, groups), policy
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
		config.Lines = append(config.Lines, ruleLine{Set: fields[1], DomainSet: kind == "DOMAIN-SET", NoResolve: hasNoResolve(fields[3:]), Policy: resolvePolicy(fields[2], groups), Name: fields[2]})
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
	config.Lines = append(config.Lines, ruleLine{Entry: entry, Policy: resolvePolicy(fields[2], groups), Name: fields[2]})
	return true
}

func hasNoResolve(options []string) bool {
	for _, option := range options {
		if strings.EqualFold(option, "no-resolve") {
			return true
		}
	}
	return false
}

// isIpRule 表示规则按 IP 判断（要不要为它解析域名由 no-resolve 决定）。
func isIpRule(entry ruleEntry) bool {
	return entry.Kind == "IP-CIDR" || entry.Kind == "IP-CIDR6" || entry.Kind == "GEOIP"
}

// convertRuleEntry 把一条 Surge / QuantumultX 写法的规则换成内核的写法，内核做不到或写得不对时返回 false。
func convertRuleEntry(kind, value string, options []string) (ruleEntry, bool) {
	noResolve := hasNoResolve(options)
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
	case "GEOSITE":
		site := strings.ToLower(value)
		return ruleEntry{Kind: "GEOSITE", Value: site}, ruleSitePattern.MatchString(site)
	case "DST-PORT", "DEST-PORT":
		return ruleEntry{Kind: "DST-PORT", Value: value}, rulePortPattern.MatchString(value)
	case "PROCESS-NAME", "PROCESS-PATH":
		return ruleEntry{Kind: kind, Value: value}, value != "" && len(value) <= maxProgramLength && !strings.ContainsAny(value, ",\x00\r\n")
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
		entry, ok := ruleListEntry(strings.Trim(stripRuleComment(line), `'"`), domainSet)
		if !ok {
			skipped++
			continue
		}
		entries = append(entries, entry)
	}
	return entries, skipped, nil
}

// ruleListEntry 转换规则列表里的一行：带逗号的是规则（行里写的去向忽略），否则是域名或 IP 段。
func ruleListEntry(line string, domainSet bool) (ruleEntry, bool) {
	fields := splitRuleFields(line)
	if len(fields) >= 2 && !domainSet {
		return convertRuleEntry(strings.ToUpper(fields[0]), fields[1], fields[2:])
	}
	if len(fields) == 1 && fields[0] != "" {
		return bareRuleEntry(fields[0], domainSet)
	}
	return ruleEntry{}, false
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

// ruleTemplate 是转换后的一条规则，还没定去向：Rule 是规则本身（不含去向），Policy 和 Name 见 ruleLine，Option 是去向后面的
// 选项（,no-resolve）。生成内核配置时才按规则集的设置定去向（见 ruleSetTarget）：改了规则集的去向、增删改策略组都不用重新转换。
type ruleTemplate struct {
	Rule   string `json:"r"`
	Policy string `json:"p"`
	Name   string `json:"n,omitempty"`
	Option string `json:"o,omitempty"`
}

// line 是这条规则在内核里的写法，forced 是规则集设的去向（空表示按文件里写的）。
func (rule ruleTemplate) line(forced string, groups []string) string {
	return rule.Rule + "," + ruleSetTarget(forced, rule.Policy, rule.Name, groups) + rule.Option
}

// convertedRules 是转换后交给内核的规则：Rules 是规则，其中的规则集是 Providers 里的文件。Count 是展开规则列表后的规则数；
// Final 和 FinalName 是 FINAL 的去向（没写时为空）；Geo 表示用到了 GEOIP 或 GEOSITE，需要地理数据。
type convertedRules struct {
	Rules     []ruleTemplate
	Providers []convertedProvider
	Count     int
	Final     string
	FinalName string
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
	name   string
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

// convertRules 展开规则列表，把去向相同（归类和文件里写的策略名都相同）的连续规则合并成规则集。sets 是按地址下载好的规则列表，
// 没下载到的列表按空处理。prefix 用来给规则集起名字。
func convertRules(config ruleConfig, sets map[ruleSetSource][]ruleEntry, prefix string) convertedRules {
	var rules []policyRule
	for _, line := range config.Lines {
		name := strings.TrimSpace(line.Name)
		if line.Set == "" {
			rules = append(rules, policyRule{line.Entry, line.Policy, name})
			continue
		}
		for _, entry := range sets[ruleSetSource{line.Set, line.DomainSet}] {
			entry.NoResolve = entry.NoResolve || line.NoResolve && isIpRule(entry)
			rules = append(rules, policyRule{entry, line.Policy, name})
		}
	}
	result := convertedRules{Count: len(rules), Final: config.Final, FinalName: strings.TrimSpace(config.FinalName)}
	for start := 0; start < len(rules); {
		end := start
		for end < len(rules) && rules[end].policy == rules[start].policy && strings.EqualFold(rules[end].name, rules[start].name) {
			end++
		}
		result.addRun(rules[start:end], prefix)
		start = end
	}
	return result
}

// addRun 转换一段去向相同的规则：域名规则集、其他按域名判断的规则、不解析域名的 IP 段、要解析域名的 IP 段、GEOIP。
func (result *convertedRules) addRun(rules []policyRule, prefix string) {
	policy, name := rules[0].policy, rules[0].name
	template := func(rule, option string) ruleTemplate {
		return ruleTemplate{Rule: rule, Policy: policy, Name: name, Option: option}
	}
	seen := map[string]bool{}
	var domains, quietIps, ips []string
	var inline, needsIp []ruleTemplate
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
		case "GEOSITE":
			result.Geo = true
			inline = append(inline, template("GEOSITE,"+entry.Value, ""))
		case "GEOIP":
			result.Geo = true
			option := ""
			if entry.NoResolve {
				option = ",no-resolve"
			}
			needsIp = append(needsIp, template("GEOIP,"+entry.Value, option))
		default:
			inline = append(inline, template(entry.Kind+","+entry.Value, ""))
		}
	}
	if len(domains) >= ruleSetMinimum {
		result.addProvider("domain", domains, template, "", prefix)
	} else {
		for _, domain := range domains {
			if suffix, found := strings.CutPrefix(domain, "+."); found {
				result.Rules = append(result.Rules, template("DOMAIN-SUFFIX,"+suffix, ""))
			} else {
				result.Rules = append(result.Rules, template("DOMAIN,"+domain, ""))
			}
		}
	}
	result.Rules = append(result.Rules, inline...)
	result.addIps(quietIps, template, ",no-resolve", prefix)
	result.addIps(ips, template, "", prefix)
	result.Rules = append(result.Rules, needsIp...)
}

func (result *convertedRules) addIps(ips []string, template func(rule, option string) ruleTemplate, option, prefix string) {
	if len(ips) >= ruleSetMinimum {
		result.addProvider("ipcidr", ips, template, option, prefix)
		return
	}
	for _, ip := range ips {
		kind := "IP-CIDR"
		if strings.Contains(ip, ":") {
			kind = "IP-CIDR6"
		}
		result.Rules = append(result.Rules, template(kind+","+ip, option))
	}
}

func (result *convertedRules) addProvider(behavior string, lines []string, template func(rule, option string) ruleTemplate, option, prefix string) {
	name := fmt.Sprintf("%s-%d", prefix, len(result.Providers)+1)
	result.Providers = append(result.Providers, convertedProvider{Name: name, Behavior: behavior, Lines: lines})
	result.Rules = append(result.Rules, template("RULE-SET,"+name, option))
}

// ---------- 保存 ----------

// ruleManifest 是转换好的规则，保存在规则集文件夹的 rules.json，生成内核配置时读取。
type ruleManifest struct {
	Rules     []ruleTemplate              `json:"rules"`
	Providers map[string]CoreRuleProvider `json:"providers"`
	Final     string                      `json:"final,omitempty"`
	FinalName string                      `json:"final_name,omitempty"`
}

// CoreRuleProvider 是一个规则集文件，Path 相对于内核的工作目录；Format 是 text（默认）、yaml 或 mrs。
// Optional 表示是下载来的列表：内核读不出来（格式或类型不对）时不等它加载，免得拖慢内核启动。
type CoreRuleProvider struct {
	Behavior string `json:"behavior"`
	Format   string `json:"format,omitempty"`
	Path     string `json:"path"`
	Optional bool   `json:"optional,omitempty"`
}

const ruleManifestName = "rules.json"

// ruleDir 是规则集 id 的文件在内核工作目录里的位置（用 / 分隔，内核配置里也用这个写法）。
func ruleDir(id string) string {
	return "rules/" + id
}

// writeConvertedRules 把转换好的规则写到 rules/<规则集 id>/<版本>/，版本是内容的摘要：内容没变时不重复写，
// 内核的配置也不变；内容变了路径跟着变，内核重新加载时读到的是新文件。
func writeConvertedRules(coreDir, id string, converted convertedRules) (string, error) {
	hash := sha256.New()
	for _, rule := range converted.Rules {
		fmt.Fprintln(hash, rule.Rule, rule.Policy, rule.Name, rule.Option)
	}
	fmt.Fprintln(hash, "final", converted.Final, converted.FinalName)
	for _, provider := range converted.Providers {
		fmt.Fprintln(hash, provider.Name, provider.Behavior, len(provider.Lines))
		for _, line := range provider.Lines {
			fmt.Fprintln(hash, line)
		}
	}
	revision := hex.EncodeToString(hash.Sum(nil))[:12]
	relative := ruleDir(id) + "/" + revision
	target := filepath.Join(coreDir, filepath.FromSlash(relative))
	if fileExists(filepath.Join(target, ruleManifestName)) {
		return revision, nil
	}
	temporary := target + ".download"
	_ = os.RemoveAll(temporary)
	if err := os.MkdirAll(temporary, 0o755); err != nil {
		return "", fmt.Errorf("无法保存规则：%v", err)
	}
	manifest := ruleManifest{Rules: converted.Rules, Providers: map[string]CoreRuleProvider{}, Final: converted.Final, FinalName: converted.FinalName}
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
func readRuleManifest(coreDir, id, revision string) (*ruleManifest, error) {
	data, err := os.ReadFile(filepath.Join(coreDir, filepath.FromSlash(ruleDir(id)), revision, ruleManifestName))
	if err != nil {
		return nil, err
	}
	var manifest ruleManifest
	if err := json.Unmarshal(data, &manifest); err != nil {
		return nil, err
	}
	return &manifest, nil
}

// removeRuleRevisions 删除规则集 id 除 keep 以外的版本（转换结果的文件夹，或者下载的列表文件）；keep 为空时删除这个规则集的
// 全部文件。上次下载的引用列表（sets）保留，下次下载失败时还能用。
func removeRuleRevisions(coreDir, id, keep string) {
	dir := filepath.Join(coreDir, filepath.FromSlash(ruleDir(id)))
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

// ruleSetCacheDir 保存完整配置里引用的规则列表上次下载成功的副本：下次下载失败时仍然可以用。
const ruleSetCacheDir = "sets"

// ruleSetDownload 是一次下载好的规则集。Kind 是认出来的加载方式（provider 纯列表 / convert 完整配置）；Revision 是保存的
// 版本：纯列表是 rules/<id>/ 下的文件名，完整配置是转换结果的文件夹名；Behavior 是纯列表的类型（从内容判断，mrs 按地址猜）；
// Count 是规则数（纯列表是行数，完整配置是展开引用的列表后的规则数）。以下是完整配置的：Sets 是引用的规则列表数，FailedSets 是
// 其中没下载到、也没有上次的副本而没有用上的；Skipped 是内核做不到而跳过的规则数；Final / FinalName 是 FINAL；
// Geo 表示用到了 GEOIP 或 GEOSITE，需要地理数据。
type ruleSetDownload struct {
	Kind       string
	Revision   string
	Format     string
	Behavior   string
	Count      int
	Sets       int
	FailedSets int
	Skipped    int
	Final      string
	FinalName  string
	Geo        bool
}

// fetchRuleSet 下载规则集，保存到内核工作目录的 rules/<id>/ 下：纯列表原样保存（交给内核读），完整配置转换后保存。
// 地址是纯列表的扩展名、内容却是完整配置（Clash 的 rules:、小火箭的 [Rule] 段）时也转换。
func fetchRuleSet(coreDir string, set RuleSet, paths []string) (ruleSetDownload, error) {
	content, err := fetchRuleFile(set.Url, paths)
	if err != nil {
		return ruleSetDownload{}, err
	}
	if set.guessKind() == ruleSetProvider && !needsConversion(content) {
		return saveRuleList(coreDir, set, content)
	}
	return convertRuleSet(coreDir, set.Id(), content, paths)
}

// zstdMagic 是 zstd 压缩数据的开头：mrs 格式的规则集是 zstd 压缩的。
var zstdMagic = []byte{0x28, 0xb5, 0x2f, 0xfd}

// saveRuleList 检查并保存纯规则列表。格式看内容：有 payload: 的是 YAML，否则是一行一条的文本（扩展名不一定对）；
// 文件名是内容的摘要：内容变了路径跟着变，内核重新加载时读到的是新文件。
func saveRuleList(coreDir string, set RuleSet, content []byte) (ruleSetDownload, error) {
	result := ruleSetDownload{Kind: ruleSetProvider, Format: set.format()}
	if result.Format != "mrs" {
		result.Format = "text"
		if yamlPayloadPattern.Match(content) {
			result.Format = "yaml"
		}
	}
	if result.Format == "mrs" {
		if !bytes.HasPrefix(content, zstdMagic) {
			return result, errors.New("不是 mrs 格式的规则集，地址可能填错了")
		}
		result.Behavior = set.guessBehavior()
	} else {
		text := strings.TrimPrefix(string(content), "\xef\xbb\xbf")
		if strings.HasPrefix(strings.TrimSpace(text), "<") {
			return result, errors.New("返回的是网页，规则地址可能填错了")
		}
		result.Behavior, result.Count = detectRuleListBehavior(text)
		if result.Count == 0 {
			return result, errors.New("里面没有规则")
		}
	}
	sum := sha256.Sum256(content)
	result.Revision = hex.EncodeToString(sum[:])[:12] + "." + map[string]string{"text": "txt", "yaml": "yaml", "mrs": "mrs"}[result.Format]
	dir := filepath.Join(coreDir, filepath.FromSlash(ruleDir(set.Id())))
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return result, fmt.Errorf("无法保存规则：%v", err)
	}
	if target := filepath.Join(dir, result.Revision); !fileExists(target) {
		if err := writeFileAtomically(target, content); err != nil {
			return result, fmt.Errorf("无法保存规则：%v", err)
		}
	}
	return result, nil
}

// yamlPayloadPattern 认出 Clash 规则集的 YAML 写法：顶格的 payload: 列表。
var yamlPayloadPattern = regexp.MustCompile(`(?m)^payload:`)

// needsConversion 表示下载的内容是完整配置、要由 ProxySwitch 转换：小火箭 / Surge 的 [Rule] 段，或者 Clash 顶格的 rules: 列表
// （纯规则列表用的是 payload:）。
func needsConversion(content []byte) bool {
	for _, raw := range strings.Split(strings.TrimPrefix(string(content), "\xef\xbb\xbf"), "\n") {
		if strings.HasPrefix(raw, " ") || strings.HasPrefix(raw, "\t") {
			continue
		}
		switch strings.ToLower(strings.TrimSpace(raw)) {
		case "[rule]", "rules:":
			return true
		case "payload:":
			return false
		}
	}
	return false
}

// ruleListKinds 是规则列表里一行开头的规则类型：有这些的是完整规则的列表（classical）。
var ruleListKinds = []string{
	"DOMAIN", "DOMAIN-SUFFIX", "DOMAIN-KEYWORD", "DOMAIN-WILDCARD", "DOMAIN-REGEX", "HOST", "HOST-SUFFIX", "HOST-KEYWORD", "HOST-WILDCARD",
	"IP-CIDR", "IP-CIDR6", "IP6-CIDR", "IP-SUFFIX", "IP-ASN", "SRC-IP-CIDR", "GEOIP", "GEOSITE", "DST-PORT", "SRC-PORT", "IN-PORT",
	"PROCESS-NAME", "PROCESS-PATH", "NETWORK", "USER-AGENT", "URL-REGEX", "RULE-SET", "AND", "OR", "NOT",
}

// detectRuleListBehavior 从内容判断纯规则列表的类型：有「类型,内容」这种完整规则的是 classical；全是 IP 段的是 ipcidr；
// 其余当 domain。同时数出规则的行数。
func detectRuleListBehavior(text string) (behavior string, lines int) {
	sampled, cidrs, classical := 0, 0, false
	for _, raw := range strings.Split(text, "\n") {
		line := strings.TrimSpace(raw)
		if line == "" || isRuleComment(line) || line == "payload:" {
			continue
		}
		line = strings.Trim(stripRuleComment(strings.TrimSpace(strings.TrimPrefix(line, "- "))), `'"`)
		if line == "" {
			continue
		}
		lines++
		if classical {
			continue
		}
		if kind, _, found := strings.Cut(line, ","); found && containsString(ruleListKinds, strings.ToUpper(strings.TrimSpace(kind))) {
			classical = true
			continue
		}
		if sampled < 200 {
			sampled++
			if _, ok := normalizeRulePrefix(line); ok {
				cidrs++
			}
		}
	}
	switch {
	case classical:
		return behaviorClassical, lines
	case sampled > 0 && cidrs == sampled:
		return behaviorIpcidr, lines
	}
	return behaviorDomain, lines
}

// convertRuleSet 转换完整配置：下载它引用的规则列表（下载失败时用上次下载的副本，没有副本的跳过并计数），
// 转换后保存到 rules/<id>/<版本>/。
func convertRuleSet(coreDir, id string, content []byte, paths []string) (ruleSetDownload, error) {
	config, err := parseRuleConfig(content)
	if err != nil {
		return ruleSetDownload{}, err
	}
	sources := config.Sets()
	result := ruleSetDownload{Kind: ruleSetConvert, Sets: len(sources), Skipped: config.Skipped}
	cacheDir := filepath.Join(coreDir, filepath.FromSlash(ruleDir(id)), ruleSetCacheDir)
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
	converted := convertRules(config, sets, id)
	revision, err := writeConvertedRules(coreDir, id, converted)
	if err != nil {
		return ruleSetDownload{}, err
	}
	result.Revision, result.Count, result.Final, result.FinalName, result.Geo = revision, converted.Count, converted.Final, converted.FinalName, converted.Geo
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

// ruleMirror 是 GitHub 原始地址（raw.githubusercontent.com）在 jsDelivr 上的镜像，国内一般能直接访问；
// 不是 GitHub 原始地址时返回空。
func ruleMirror(address string) string {
	parsed, err := url.Parse(address)
	if err != nil || !strings.EqualFold(parsed.Hostname(), "raw.githubusercontent.com") {
		return ""
	}
	parts := strings.SplitN(strings.TrimPrefix(parsed.Path, "/"), "/", 4)
	if len(parts) < 4 || parts[3] == "" {
		return ""
	}
	return "https://testingcf.jsdelivr.net/gh/" + parts[0] + "/" + parts[1] + "@" + parts[2] + "/" + parts[3]
}

// ruleFileCandidates 是经某条网络路径下载规则时依次尝试的地址：GitHub 上的规则经代理时先试原地址，直连时先试镜像
// （国内直连 GitHub 常常不通）。
func ruleFileCandidates(address string, direct bool) []string {
	mirror := ruleMirror(address)
	switch {
	case mirror == "":
		return []string{address}
	case direct:
		return []string{mirror, address}
	}
	return []string{address, mirror}
}

// fetchRuleFile 下载规则配置或规则列表，依次尝试各条网络路径（每条路径上 GitHub 的规则也试 jsDelivr 镜像），
// 都失败时返回第一次的错误。本机的文件（file:///…）直接读取。
func fetchRuleFile(address string, paths []string) ([]byte, error) {
	if path, isFile := localFilePath(address); isFile {
		data, err := os.ReadFile(path)
		if err != nil {
			return nil, fmt.Errorf("读不了本机的文件：%v", err)
		}
		if len(data) > maxRuleFileSize {
			return nil, fmt.Errorf("文件超过 %d MB", maxRuleFileSize>>20)
		}
		return data, nil
	}
	var firstErr error
	for _, proxyUrl := range paths {
		for _, candidate := range ruleFileCandidates(address, proxyUrl == "") {
			data, err := fetchRuleFileVia(candidate, proxyUrl)
			if err == nil {
				return data, nil
			}
			if firstErr == nil {
				firstErr = err
			}
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
	if len(data) == 0 {
		return nil, errors.New("文件是空的")
	}
	return data, nil
}
