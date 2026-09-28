package main

import (
	"reflect"
	"testing"
)

func TestParseYamlLite(t *testing.T) {
	text := "\xef\xbb\xbf# Clash 配置\n" + `mixed-port: 7890
proxy-groups:
  - name: "🚀 节点选择"
    type: select
    proxies:
      - ♻️ 自动选择
      - DIRECT
  - {name: 🎯 全球直连, type: select, proxies: [DIRECT, "🚀 节点选择"]}
  -   name: 🛑 全球拦截
      type: select
      proxies: [REJECT,
        DIRECT]
rule-providers:
  reject:
    type: http
    behavior: domain
    url: "https://example.com/reject.txt"   # 注释
    path: ./ruleset/reject.yaml
  cncidr: {type: http, behavior: ipcidr, url: https://example.com/cncidr.txt?a=1#frag, format: text}
  'quoted': &anchor
    type: inline
    payload:
    - '+.example.org'
    - 1.2.3.0/24
script:
  code: |
    def main():
      return "DIRECT"
rules:
- RULE-SET,reject,🛑 全球拦截
- 'DOMAIN-SUFFIX,google.com,🚀 节点选择'
- "IP-CIDR6,2001:db8::/32,DIRECT,no-resolve"   # 行尾注释
- MATCH,🚀 节点选择
`
	root := yamlMap(parseYamlLite(text))
	if root["mixed-port"] != "7890" {
		t.Errorf("顶层的值：%v", root["mixed-port"])
	}
	groups := yamlList(root["proxy-groups"])
	if len(groups) != 3 {
		t.Fatalf("应有 3 个策略组：%#v", root["proxy-groups"])
	}
	want := []map[string]any{
		{"name": "🚀 节点选择", "type": "select", "proxies": []any{"♻️ 自动选择", "DIRECT"}},
		{"name": "🎯 全球直连", "type": "select", "proxies": []any{"DIRECT", "🚀 节点选择"}},
		{"name": "🛑 全球拦截", "type": "select", "proxies": []any{"REJECT", "DIRECT"}},
	}
	for index, group := range groups {
		if !reflect.DeepEqual(group, map[string]any(want[index])) {
			t.Errorf("第 %d 个策略组：%#v", index+1, group)
		}
	}
	providers := yamlMap(root["rule-providers"])
	if reject := yamlMap(providers["reject"]); reject["url"] != "https://example.com/reject.txt" || reject["behavior"] != "domain" || reject["path"] != "./ruleset/reject.yaml" {
		t.Errorf("块状的规则集：%#v", reject)
	}
	if cncidr := yamlMap(providers["cncidr"]); cncidr["url"] != "https://example.com/cncidr.txt?a=1#frag" || cncidr["format"] != "text" {
		t.Errorf("行内的规则集：%#v", cncidr)
	}
	if quoted := yamlMap(providers["quoted"]); !reflect.DeepEqual(yamlStrings(quoted["payload"]), []string{"+.example.org", "1.2.3.0/24"}) || quoted["type"] != "inline" {
		t.Errorf("带引号的键、锚点和和键对齐的列表：%#v", quoted)
	}
	if code := yamlMap(root["script"])["code"]; code != "" {
		t.Errorf("多行字符串跳过：%#v", code)
	}
	if rules := yamlStrings(root["rules"]); !reflect.DeepEqual(rules, []string{"RULE-SET,reject,🛑 全球拦截", "DOMAIN-SUFFIX,google.com,🚀 节点选择", "IP-CIDR6,2001:db8::/32,DIRECT,no-resolve", "MATCH,🚀 节点选择"}) {
		t.Errorf("规则列表：%#v", rules)
	}
	if parseYamlLite("# 只有注释\n") != nil {
		t.Error("没有内容时是 nil")
	}
}
