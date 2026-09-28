package main

import (
	"strconv"
	"strings"
)

// 只读的 YAML 子集，够解析 Clash 配置里的 proxy-groups、rule-providers 和 rules：按缩进的映射和列表、
// 行内的 [a, b] 和 {k: v}、带引号或不带引号的字符串、注释。结果是 map[string]any、[]any 和 string。
// 锚点和引用（&a、*a）当作普通字符串，多行字符串（| 和 >）跳过，内容为空。

type yamlLine struct {
	indent int
	text   string
}

// parseYamlLite 解析 YAML 文本，认不出结构时返回 nil。
func parseYamlLite(text string) any {
	var lines []yamlLine
	for _, raw := range strings.Split(strings.TrimPrefix(text, "\xef\xbb\xbf"), "\n") {
		raw = strings.TrimRight(raw, "\r")
		content := strings.TrimLeft(raw, " ")
		trimmed := strings.TrimSpace(stripYamlComment(content))
		if trimmed == "" || trimmed == "---" || trimmed == "..." {
			continue
		}
		lines = append(lines, yamlLine{indent: len(raw) - len(content), text: trimmed})
	}
	if len(lines) == 0 {
		return nil
	}
	position := 0
	return parseYamlBlock(lines, &position, lines[0].indent)
}

// stripYamlComment 去掉不在引号里的「 #」开始的注释。
func stripYamlComment(text string) string {
	quote := rune(0)
	for index, char := range text {
		switch {
		case quote != 0:
			if char == quote {
				quote = 0
			}
		case char == '"' || char == '\'':
			quote = char
		case char == '#' && (index == 0 || text[index-1] == ' ' || text[index-1] == '\t'):
			return text[:index]
		}
	}
	return text
}

func isYamlListItem(text string) bool {
	return text == "-" || strings.HasPrefix(text, "- ")
}

// parseYamlBlock 解析从 position 开始、缩进为 indent 的一块：列表或映射。
func parseYamlBlock(lines []yamlLine, position *int, indent int) any {
	if *position >= len(lines) || lines[*position].indent != indent {
		return nil
	}
	if isYamlListItem(lines[*position].text) {
		return parseYamlList(lines, position, indent)
	}
	result := map[string]any{}
	parseYamlMapping(result, lines, position, indent)
	return result
}

func parseYamlList(lines []yamlLine, position *int, indent int) []any {
	items := []any{}
	for *position < len(lines) && lines[*position].indent == indent && isYamlListItem(lines[*position].text) {
		line := lines[*position]
		rest := strings.TrimLeft(strings.TrimPrefix(line.text, "-"), " ")
		*position++
		switch key, value, isEntry := splitYamlEntry(rest); {
		case rest == "":
			items = append(items, parseYamlNested(lines, position, indent, true))
		case isEntry:
			// 「- name: x」开始一个映射，后面的键和 name 对齐。
			childIndent := line.indent + len(line.text) - len(rest)
			item := map[string]any{key: parseYamlValue(value, lines, position, childIndent)}
			parseYamlMapping(item, lines, position, childIndent)
			items = append(items, item)
		default:
			items = append(items, parseYamlValue(rest, lines, position, indent))
		}
	}
	return items
}

func parseYamlMapping(result map[string]any, lines []yamlLine, position *int, indent int) {
	for *position < len(lines) && lines[*position].indent == indent && !isYamlListItem(lines[*position].text) {
		key, value, isEntry := splitYamlEntry(lines[*position].text)
		if !isEntry {
			return
		}
		*position++
		result[key] = parseYamlValue(value, lines, position, indent)
	}
}

// parseYamlNested 解析写在下一行、缩进更深的值；映射里的列表也可以和键对齐（sameIndentList）。
func parseYamlNested(lines []yamlLine, position *int, indent int, inList bool) any {
	if *position >= len(lines) {
		return nil
	}
	next := lines[*position]
	switch {
	case next.indent > indent:
		return parseYamlBlock(lines, position, next.indent)
	case next.indent == indent && !inList && isYamlListItem(next.text):
		return parseYamlList(lines, position, indent)
	}
	return nil
}

// parseYamlValue 解析冒号后面（或 - 后面）的值：空的时候看下一行，行内的列表和映射可以跨行。
func parseYamlValue(value string, lines []yamlLine, position *int, indent int) any {
	if strings.HasPrefix(value, "&") {
		// 锚点：去掉名字，只要值。
		_, value, _ = strings.Cut(value, " ")
		value = strings.TrimSpace(value)
	}
	switch {
	case value == "":
		return parseYamlNested(lines, position, indent, false)
	case strings.HasPrefix(value, "|") || strings.HasPrefix(value, ">"):
		for *position < len(lines) && lines[*position].indent > indent {
			*position++
		}
		return ""
	case strings.HasPrefix(value, "[") || strings.HasPrefix(value, "{"):
		for !yamlFlowClosed(value) && *position < len(lines) && lines[*position].indent > indent {
			value += " " + lines[*position].text
			*position++
		}
		scanner := &yamlFlowScanner{text: value}
		return scanner.value()
	}
	return yamlScalar(value)
}

// splitYamlEntry 把「键: 值」拆开，键可以带引号；冒号后面要有空格或者在行尾，免得把网址当成键。
func splitYamlEntry(text string) (key, value string, ok bool) {
	if strings.HasPrefix(text, "\"") || strings.HasPrefix(text, "'") {
		end := strings.IndexByte(text[1:], text[0])
		if end < 0 {
			return "", "", false
		}
		key, rest := text[1:end+1], strings.TrimLeft(text[end+2:], " ")
		if rest == ":" || strings.HasPrefix(rest, ": ") {
			return key, strings.TrimSpace(strings.TrimPrefix(rest, ":")), true
		}
		return "", "", false
	}
	if strings.HasPrefix(text, "[") || strings.HasPrefix(text, "{") {
		return "", "", false
	}
	for index := 0; index < len(text); index++ {
		if text[index] == ':' && (index == len(text)-1 || text[index+1] == ' ' || text[index+1] == '\t') {
			return strings.TrimSpace(text[:index]), strings.TrimSpace(text[index+1:]), true
		}
	}
	return "", "", false
}

// yamlScalar 去掉引号；双引号里的转义按 Go 的写法处理。
func yamlScalar(text string) string {
	text = strings.TrimSpace(text)
	if len(text) >= 2 {
		switch {
		case text[0] == '"' && text[len(text)-1] == '"':
			if unquoted, err := strconv.Unquote(text); err == nil {
				return unquoted
			}
			return text[1 : len(text)-1]
		case text[0] == '\'' && text[len(text)-1] == '\'':
			return strings.ReplaceAll(text[1:len(text)-1], "''", "'")
		}
	}
	return text
}

// yamlFlowClosed 表示行内的列表或映射的括号已经配平（引号里的不算）。
func yamlFlowClosed(text string) bool {
	depth, quote := 0, byte(0)
	for index := 0; index < len(text); index++ {
		char := text[index]
		switch {
		case quote != 0:
			if char == quote {
				quote = 0
			}
		case char == '"' || char == '\'':
			quote = char
		case char == '[' || char == '{':
			depth++
		case char == ']' || char == '}':
			depth--
		}
	}
	return depth <= 0
}

// yamlFlowScanner 解析行内的 [a, b] 和 {k: v}。
type yamlFlowScanner struct {
	text     string
	position int
}

func (scanner *yamlFlowScanner) skipSpaces() {
	for scanner.position < len(scanner.text) && (scanner.text[scanner.position] == ' ' || scanner.text[scanner.position] == '\t') {
		scanner.position++
	}
}

func (scanner *yamlFlowScanner) peek() byte {
	scanner.skipSpaces()
	if scanner.position < len(scanner.text) {
		return scanner.text[scanner.position]
	}
	return 0
}

func (scanner *yamlFlowScanner) value() any {
	switch scanner.peek() {
	case '[':
		scanner.position++
		items := []any{}
		for {
			switch scanner.peek() {
			case ']':
				scanner.position++
				return items
			case 0:
				return items
			case ',':
				scanner.position++
				continue
			}
			items = append(items, scanner.value())
		}
	case '{':
		scanner.position++
		result := map[string]any{}
		for {
			switch scanner.peek() {
			case '}':
				scanner.position++
				return result
			case 0:
				return result
			case ',':
				scanner.position++
				continue
			}
			key := scanner.scalar(true)
			if scanner.peek() == ':' {
				scanner.position++
				result[key] = scanner.value()
			} else {
				result[key] = ""
			}
		}
	}
	return scanner.scalar(false)
}

// scalar 读一个字符串：带引号的读到配对的引号，不带的读到逗号、括号（键还读到冒号）为止。
func (scanner *yamlFlowScanner) scalar(isKey bool) string {
	scanner.skipSpaces()
	text := scanner.text
	start := scanner.position
	if start < len(text) && (text[start] == '"' || text[start] == '\'') {
		quote := text[start]
		for end := start + 1; end < len(text); end++ {
			if text[end] == '\\' && quote == '"' {
				end++
				continue
			}
			if text[end] == quote {
				scanner.position = end + 1
				return yamlScalar(text[start : end+1])
			}
		}
		scanner.position = len(text)
		return yamlScalar(text[start:])
	}
	end := start
	for end < len(text) {
		char := text[end]
		if char == ',' || char == ']' || char == '}' || (isKey && char == ':' && (end+1 == len(text) || text[end+1] == ' ' || text[end+1] == ',' || text[end+1] == '}')) {
			break
		}
		end++
	}
	scanner.position = end
	return strings.TrimSpace(text[start:end])
}

// ---------- 取值 ----------

func yamlMap(value any) map[string]any {
	result, _ := value.(map[string]any)
	return result
}

func yamlList(value any) []any {
	result, _ := value.([]any)
	return result
}

func yamlString(value any) string {
	result, _ := value.(string)
	return result
}

// yamlStrings 取列表里的字符串，其他类型的项跳过。
func yamlStrings(value any) []string {
	var result []string
	for _, item := range yamlList(value) {
		if text, ok := item.(string); ok {
			result = append(result, text)
		}
	}
	return result
}
