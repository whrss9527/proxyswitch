package main

import (
	"errors"
	"fmt"
	"net/netip"
	"strings"
)

// 自定义规则：某个域名（包括它的子域名）或 IP / 网段、或者某个程序的连接固定走节点、直连或被拦截。排在订阅配置的
// 分流规则前面，全局代理时也生效；只作用于经过内置内核的流量（订阅配置）。

// CustomRule 是一条自定义规则：Type 为空时 Value 是域名或 IP / 网段，为 program 时 Value 是程序名（WeChat.exe）或
// 程序的完整路径；Policy 是 proxy（走节点）/ direct（直连）/ reject（拦截）/ group:策略组名，Disabled 表示暂时停用。
type CustomRule struct {
	Type     string `json:"type,omitempty"`
	Value    string `json:"value"`
	Policy   string `json:"policy"`
	Disabled bool   `json:"disabled,omitempty"`
}

const (
	customRuleProgram = "program"
	maxProgramLength  = 260
)

var customRulePolicies = []string{rulePolicyProxy, rulePolicyDirect, rulePolicyReject}

// normalizeCustomRule 整理一条自定义规则：以 .exe 结尾的当作程序（手写配置时可以不写 type），程序按
// normalizeProgramTarget、网站按 normalizeRuleTarget 整理。windows 表示在 Windows 上运行，程序名补上 .exe。
func normalizeCustomRule(rule *CustomRule, windows bool) {
	rule.Type = strings.ToLower(strings.TrimSpace(rule.Type))
	rule.Policy = normalizeRulePolicy(rule.Policy)
	if rule.Type == "" && strings.HasSuffix(strings.ToLower(strings.Trim(strings.TrimSpace(rule.Value), `"'`)), ".exe") {
		rule.Type = customRuleProgram
	}
	if rule.Type == customRuleProgram {
		rule.Value = normalizeProgramTarget(rule.Value, windows)
	} else {
		rule.Value = normalizeRuleTarget(rule.Value)
	}
}

// normalizeProgramTarget 去掉首尾的空白和引号。Windows 上的程序名没写扩展名时补上 .exe（内核按带扩展名的进程名
// 匹配），路径里的 / 换成 \。
func normalizeProgramTarget(text string, windows bool) string {
	value := strings.TrimSpace(strings.Trim(strings.TrimSpace(text), `"'`))
	if !windows || value == "" {
		return value
	}
	if isProgramPath(value) {
		return strings.ReplaceAll(value, "/", `\`)
	}
	if !strings.HasSuffix(strings.ToLower(value), ".exe") {
		value += ".exe"
	}
	return value
}

// isProgramPath 表示程序规则写的是完整路径，不只是程序名。
func isProgramPath(value string) bool {
	return strings.ContainsAny(value, `\/`)
}

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

// validateCustomRule 检查一条自定义规则，Value 应已整理过；groups 是现有的策略组。
func validateCustomRule(rule CustomRule, groups []string) error {
	switch rule.Type {
	case customRuleProgram:
		if rule.Value == "" {
			return errors.New("请填写程序名，例如 WeChat.exe")
		}
		if len(rule.Value) > maxProgramLength || strings.ContainsAny(rule.Value, ",\x00\r\n\t") {
			return fmt.Errorf("程序「%s」写得不对：填程序名（例如 WeChat.exe）或完整路径", rule.Value)
		}
	case "":
		if rule.Value == "" {
			return errors.New("请填写域名或 IP")
		}
		if _, ok := normalizeRulePrefix(rule.Value); !ok && !isRuleDomain(rule.Value) {
			return fmt.Errorf("认不出「%s」：填域名（例如 youtube.com）或 IP / 网段（例如 8.8.8.8、10.0.0.0/8）", rule.Value)
		}
	default:
		return fmt.Errorf("「%s」的类型 %q 不认识，可用：program，或者不写（域名和 IP）", rule.Value, rule.Type)
	}
	if err := validateRulePolicy(rule.Policy, groups); err != nil {
		return fmt.Errorf("「%s」的%v", rule.Value, err)
	}
	return nil
}

// customRuleLine 是自定义规则在内核里的写法：域名包括子域名，IP 段不解析域名，程序按进程名或路径匹配。
// 停用的或认不出来的返回空。groups 是现有的策略组，去向指向已删除的组时走节点。
func customRuleLine(rule CustomRule, groups []string) string {
	if rule.Disabled {
		return ""
	}
	target := corePolicyTarget(rule.Policy, groups)
	if rule.Type == customRuleProgram {
		if rule.Value == "" || strings.ContainsAny(rule.Value, ",\r\n") {
			return ""
		}
		if isProgramPath(rule.Value) {
			return "PROCESS-PATH," + rule.Value + "," + target
		}
		return "PROCESS-NAME," + rule.Value + "," + target
	}
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
func customRuleLines(rules []CustomRule, groups []string) []string {
	var lines []string
	for _, rule := range rules {
		if line := customRuleLine(rule, groups); line != "" {
			lines = append(lines, line)
		}
	}
	return lines
}
