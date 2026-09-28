package main

import (
	"errors"
	"fmt"
	"regexp"
	"strings"
	"unicode/utf8"
)

// 策略组：在「节点」之外给某类流量（流媒体、Telegram……）单独选节点，相当于 Quantumult X 的 policy。成员是正在使用的
// 订阅里按名字筛出来的节点；手动选择的组还多了「跟随节点」「自动选择」和直连三个候选，默认跟随节点（正在使用的配置选中的
// 节点），所以刚建好时行为不变。自定义规则的去向可以是某个策略组（写成 group:名字）。

// PolicyGroup 是一个策略组。Type 是 select（手动选择）/ url-test（自动选择延迟最低的）/ fallback（故障转移）/
// load-balance（负载均衡）；Filter 是节点名的正则表达式（不区分大小写），空表示全部节点；Node 是手动选择的组选中的
// 成员：空表示跟随节点，也可以是「自动选择」、DIRECT（直连）或节点名。
type PolicyGroup struct {
	Name   string `json:"name"`
	Type   string `json:"type"`
	Filter string `json:"filter,omitempty"`
	Node   string `json:"node,omitempty"`
}

const (
	groupSelect      = "select"
	groupUrlTest     = "url-test"
	groupFallback    = "fallback"
	groupLoadBalance = "load-balance"

	maxGroupNameLength   = 20
	maxGroupFilterLength = 500

	// 手动选择的组里两个特殊的成员：正在使用的订阅的自动选择，和直连。
	groupMemberAuto   = "自动选择"
	groupMemberDirect = "DIRECT"

	// 规则的去向指向策略组时写成 group:名字。
	ruleTargetGroupPrefix = "group:"

	// 策略组的健康检查：比订阅的自动选择勤一些，故障转移才能及时换节点；lazy 表示只在组被用到时才测。
	groupHealthInterval = 600
	groupTolerance      = 80
)

var policyGroupTypes = []string{groupSelect, groupUrlTest, groupFallback, groupLoadBalance}

// groupTypeTitles 和 groupTypeDetails 是各类型的名字和说明（托盘菜单用，设置页有自己的一份）。
var (
	groupTypeTitles  = map[string]string{groupSelect: "手动选择", groupUrlTest: "自动选择", groupFallback: "故障转移", groupLoadBalance: "负载均衡"}
	groupTypeDetails = map[string]string{
		groupSelect:      "自己选，默认跟随节点",
		groupUrlTest:     "定期测延迟，用最低的",
		groupFallback:    "按顺序用第一个能用的，坏了换下一个",
		groupLoadBalance: "节点轮流用，分摊流量",
	}
)

// groupTypeAliases 是类型的其他写法：下划线、Mac 版的写法和 Quantumult X 的叫法。
var groupTypeAliases = map[string]string{
	"url_test": groupUrlTest, "urltest": groupUrlTest, "url-latency-benchmark": groupUrlTest,
	"load_balance": groupLoadBalance, "loadbalance": groupLoadBalance, "round-robin": groupLoadBalance,
	"static": groupSelect, "available": groupFallback,
}

// reservedGroupNames 是内核和 ProxySwitch 自己用的名字，不能拿来当组名。
var reservedGroupNames = []string{"DIRECT", "REJECT", "REJECT-DROP", "PASS", "GLOBAL", "COMPATIBLE", coreTopGroup, coreUpstreamProxy, coreShareListener, groupMemberAuto}

func normalizePolicyGroup(group *PolicyGroup) {
	group.Name = strings.TrimSpace(group.Name)
	group.Type = lowerTrim(group.Type, groupSelect)
	if alias, found := groupTypeAliases[group.Type]; found {
		group.Type = alias
	}
	group.Filter = strings.TrimSpace(group.Filter)
	group.Node = strings.TrimSpace(group.Node)
	if group.Type != groupSelect {
		group.Node = ""
	}
}

// validateGroupName 检查策略组的名字：规则行用逗号分隔字段，名字里有逗号会被拆开。
func validateGroupName(name string) error {
	switch {
	case name == "":
		return errors.New("请填写策略组的名字")
	case utf8.RuneCountInString(name) > maxGroupNameLength:
		return fmt.Errorf("策略组「%s」的名字太长，最多 %d 个字", name, maxGroupNameLength)
	case strings.ContainsAny(name, ",，\"`\r\n\t"):
		return fmt.Errorf("策略组「%s」的名字里不能有逗号、引号或换行", name)
	}
	for _, reserved := range reservedGroupNames {
		if strings.EqualFold(name, reserved) {
			return fmt.Errorf("「%s」是内核保留的名字，策略组换一个名字", name)
		}
	}
	return nil
}

// validateGroupFilter 检查节点名的筛选。内核（mihomo）用 .NET 风格的正则（dlclark/regexp2），写错了会让内核崩溃，
// 所以要先检查：Go 的正则能编译的它都能编译，只有 (?P<名字>…) 和 \Q…\E 例外；它还支持 Go 不支持的环视
// （(?!…) 常用来排除节点），检查时把环视当作普通的分组。反引号在内核里用来分隔多个筛选，不允许。
func validateGroupFilter(filter string) error {
	if filter == "" {
		return nil
	}
	if len(filter) > maxGroupFilterLength {
		return fmt.Errorf("筛选太长，最多 %d 个字符", maxGroupFilterLength)
	}
	if strings.Contains(filter, "`") {
		return errors.New("筛选里不能有反引号（`）")
	}
	syntax, ok := coreRegexpSyntax(filter)
	if ok {
		_, err := regexp.Compile(syntax)
		ok = err == nil
	}
	if !ok {
		return fmt.Errorf("筛选「%s」不是正确的正则表达式，例如：港|HK", filter)
	}
	return nil
}

// coreRegexpSyntax 把内核的正则改写成 Go 能检查语法的样子：环视和固化分组换成普通的非捕获分组。
// 有内核不认的写法时返回 false。
func coreRegexpSyntax(pattern string) (string, bool) {
	var builder strings.Builder
	inClass := false
	classStart := 0
	for index := 0; index < len(pattern); index++ {
		char := pattern[index]
		switch {
		case char == '\\':
			if index+1 < len(pattern) {
				if pattern[index+1] == 'Q' {
					return "", false
				}
				builder.WriteString(pattern[index : index+2])
				index++
				continue
			}
		case inClass:
			// 紧跟在 [ 或 [^ 后面的 ] 是字符本身。
			if char == ']' && index > classStart {
				inClass = false
			}
		case char == '[':
			inClass, classStart = true, index+1
			if strings.HasPrefix(pattern[index+1:], "^") {
				classStart++
			}
		case char == '(' && strings.HasPrefix(pattern[index:], "(?P<"):
			return "", false
		case char == '(':
			if opener := lookaroundOpener(pattern[index:]); opener != "" {
				builder.WriteString("(?:")
				index += len(opener) - 1
				continue
			}
		}
		builder.WriteByte(char)
	}
	return builder.String(), true
}

func lookaroundOpener(text string) string {
	for _, opener := range []string{"(?<=", "(?<!", "(?=", "(?!", "(?>"} {
		if strings.HasPrefix(text, opener) {
			return opener
		}
	}
	return ""
}

// coreGroupFilter 是内核配置里的筛选：默认不区分大小写，自己写了 (?…) 标志的照用。
func coreGroupFilter(filter string) string {
	if filter == "" || strings.HasPrefix(filter, "(?") {
		return filter
	}
	return "(?i)" + filter
}

// validatePolicyGroups 检查所有策略组：名字不能重复，也不能和内核里订阅的组重名（组名是配置的 id）。
func validatePolicyGroups(config *Config) error {
	seen := map[string]bool{}
	for _, group := range config.PolicyGroups {
		if err := validateGroupName(group.Name); err != nil {
			return err
		}
		key := strings.ToLower(group.Name)
		if seen[key] {
			return fmt.Errorf("有两个策略组都叫「%s」，名字不能重复", group.Name)
		}
		seen[key] = true
		for _, profile := range config.Profiles {
			if strings.EqualFold(group.Name, profile.Id) || strings.EqualFold(group.Name, coreAutoGroup(profile.Id)) {
				return fmt.Errorf("策略组「%s」和配置「%s」的 id 重名，换一个名字", group.Name, profile.Name)
			}
		}
		if !containsString(policyGroupTypes, group.Type) {
			return fmt.Errorf("策略组「%s」的类型 %q 不认识，可用：%s", group.Name, group.Type, strings.Join(policyGroupTypes, " / "))
		}
		if err := validateGroupFilter(group.Filter); err != nil {
			return fmt.Errorf("策略组「%s」的%v", group.Name, err)
		}
	}
	return nil
}

// FindGroup 按名字查找策略组，找不到返回 nil。
func (config *Config) FindGroup(name string) *PolicyGroup {
	for index := range config.PolicyGroups {
		if config.PolicyGroups[index].Name == name {
			return &config.PolicyGroups[index]
		}
	}
	return nil
}

// GroupNames 是策略组的名字，按配置里的顺序。
func (config *Config) GroupNames() []string {
	names := make([]string, 0, len(config.PolicyGroups))
	for _, group := range config.PolicyGroups {
		names = append(names, group.Name)
	}
	return names
}

// RetargetGroup 在策略组被删掉或改名后，把指向它的规则改到新的去向 target。
func (config *Config) RetargetGroup(name, target string) {
	old := ruleTargetGroupPrefix + name
	for index := range config.CustomRules {
		if config.CustomRules[index].Policy == old {
			config.CustomRules[index].Policy = target
		}
	}
}

// normalizeRulePolicy 整理规则的去向：proxy / direct / reject 不区分大小写，group:名字 保留名字的大小写。
func normalizeRulePolicy(policy string) string {
	value := strings.TrimSpace(policy)
	if len(value) >= len(ruleTargetGroupPrefix) && strings.EqualFold(value[:len(ruleTargetGroupPrefix)], ruleTargetGroupPrefix) {
		return ruleTargetGroupPrefix + strings.TrimSpace(value[len(ruleTargetGroupPrefix):])
	}
	return lowerTrim(value, rulePolicyProxy)
}

// ruleTargetGroup 返回去向指向的策略组名，不是策略组时 ok 为 false。
func ruleTargetGroup(policy string) (name string, ok bool) {
	name, ok = strings.CutPrefix(policy, ruleTargetGroupPrefix)
	return name, ok && name != ""
}

// validateRulePolicy 检查规则的去向：固定的三种，或者现有的策略组。
func validateRulePolicy(policy string, groups []string) error {
	if name, ok := ruleTargetGroup(policy); ok {
		if !containsString(groups, name) {
			return fmt.Errorf("去向指向的策略组「%s」不存在", name)
		}
		return nil
	}
	if !containsString(customRulePolicies, policy) {
		return fmt.Errorf("去向 %q 不认识，可用：%s，或者 group:策略组名", policy, strings.Join(customRulePolicies, " / "))
	}
	return nil
}

// corePolicyTarget 是去向在内核配置里的名字。指向的策略组已经不存在时退回 ProxySwitch（走节点），
// 免得内核因为找不到策略拒绝整个配置。
func corePolicyTarget(policy string, groups []string) string {
	if name, ok := ruleTargetGroup(policy); ok {
		if containsString(groups, name) {
			return name
		}
		return coreTopGroup
	}
	return corePolicyName(policy)
}

// corePolicyGroup 是一个策略组在内核配置里的写法，节点来自订阅 source。
func corePolicyGroup(group PolicyGroup, source, testUrl string) coreGroup {
	result := coreGroup{Name: group.Name, Type: group.Type, Use: []string{coreProviderName(source)}, Filter: coreGroupFilter(group.Filter)}
	switch group.Type {
	case groupSelect:
		result.Proxies = []string{coreTopGroup, coreAutoGroup(source), groupMemberDirect}
	case groupUrlTest:
		result.Url, result.Interval, result.Tolerance, result.Lazy = testUrl, groupHealthInterval, groupTolerance, true
	case groupFallback:
		result.Url, result.Interval, result.Lazy = testUrl, groupHealthInterval, true
	case groupLoadBalance:
		result.Url, result.Interval, result.Strategy, result.Lazy = testUrl, groupHealthInterval, "round-robin", true
	}
	return result
}

// coreGroupMember 是手动选择的组选中的成员在内核里的名字：空是跟随节点（ProxySwitch 组）。
func coreGroupMember(node, source string) string {
	switch node {
	case "":
		return coreTopGroup
	case groupMemberAuto:
		return coreAutoGroup(source)
	}
	return node
}

// groupNodeOf 与 coreGroupMember 相反：内核里的成员名换成配置里的写法。
func groupNodeOf(member, source string) string {
	switch member {
	case coreTopGroup:
		return ""
	case coreAutoGroup(source):
		return groupMemberAuto
	}
	return member
}
