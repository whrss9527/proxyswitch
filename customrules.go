package main

import (
	"errors"
	"fmt"
	"net/netip"
	"strings"
)

// 自定义规则：某个域名（包括它的子域名）或 IP / 网段固定走节点、直连或被拦截。排在订阅配置的分流规则前面，
// 全局代理时也生效；只作用于经过内置内核的流量（订阅配置）。

// CustomRule 是一条自定义规则：Value 是域名或 IP / 网段，Policy 是 proxy（走节点）/ direct（直连）/ reject（拦截），
// Disabled 表示暂时停用。
type CustomRule struct {
	Value    string `json:"value"`
	Policy   string `json:"policy"`
	Disabled bool   `json:"disabled,omitempty"`
}

var customRulePolicies = []string{rulePolicyProxy, rulePolicyDirect, rulePolicyReject}

// normalizeRuleTarget 把「https://www.YouTube.com/watch」「*.youtube.com」「youtube.com:443」这样的输入整理成
// youtube.com 的形式，IP 和网段整理成标准写法。
func normalizeRuleTarget(text string) string {
	value := strings.ToLower(strings.TrimSpace(text))
	if _, rest, found := strings.Cut(value, "://"); found {
		value = rest
	}
	if _, err := netip.ParsePrefix(value); err != nil {
		if index := strings.IndexAny(value, "/?#"); index >= 0 {
			value = value[:index]
		}
	}
	// 去掉端口：[IPv6]:端口，或者只有一个冒号的 主机:端口。
	if strings.HasPrefix(value, "[") {
		if end := strings.Index(value, "]"); end > 0 {
			value = value[1:end]
		}
	} else if host, port, found := strings.Cut(value, ":"); found && port != "" && strings.Trim(port, "0123456789") == "" {
		value = host
	}
	value = strings.Trim(strings.TrimPrefix(value, "*."), ".")
	if prefix, err := netip.ParsePrefix(value); err == nil {
		return prefix.Masked().String()
	}
	if address, err := netip.ParseAddr(value); err == nil {
		return address.String()
	}
	return value
}

// isRuleDomain 表示看起来是一个域名（至少两段）。
func isRuleDomain(value string) bool {
	return strings.Contains(value, ".") && ruleDomainPattern.MatchString(value)
}

// validateCustomRule 检查一条自定义规则，Value 应已整理过。
func validateCustomRule(rule CustomRule) error {
	if rule.Value == "" {
		return errors.New("请填写域名或 IP")
	}
	if _, ok := normalizeRulePrefix(rule.Value); !ok && !isRuleDomain(rule.Value) {
		return fmt.Errorf("认不出「%s」：填域名（例如 youtube.com）或 IP / 网段（例如 8.8.8.8、10.0.0.0/8）", rule.Value)
	}
	if !containsString(customRulePolicies, rule.Policy) {
		return fmt.Errorf("「%s」的去向 %q 不认识，可用：%s", rule.Value, rule.Policy, strings.Join(customRulePolicies, " / "))
	}
	return nil
}

// customRuleLine 是自定义规则在内核里的写法：域名包括子域名，IP 段不解析域名。停用的或认不出来的返回空。
func customRuleLine(rule CustomRule) string {
	if rule.Disabled {
		return ""
	}
	target := corePolicyName(rule.Policy)
	value := normalizeRuleTarget(rule.Value)
	if prefix, ok := normalizeRulePrefix(value); ok {
		kind := "IP-CIDR"
		if prefix.Addr().Is6() {
			kind = "IP-CIDR6"
		}
		return kind + "," + prefix.String() + "," + target + ",no-resolve"
	}
	if isRuleDomain(value) {
		return "DOMAIN-SUFFIX," + value + "," + target
	}
	return ""
}

// customRuleLines 是启用的自定义规则在内核里的写法，按配置里的顺序。
func customRuleLines(rules []CustomRule) []string {
	var lines []string
	for _, rule := range rules {
		if line := customRuleLine(rule); line != "" {
			lines = append(lines, line)
		}
	}
	return lines
}
