package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"strings"
)

// stripJsonc 去掉 // 与 /* */ 注释以及对象、数组末尾多余的逗号，得到标准 JSON。
// 注释里的换行原样保留，保证解析报错的行号与原文一致。
func stripJsonc(source string) string {
	var output strings.Builder
	output.Grow(len(source))
	inString := false
	escaped := false
	position := 0
	for position < len(source) {
		char := source[position]
		if inString {
			output.WriteByte(char)
			switch {
			case escaped:
				escaped = false
			case char == '\\':
				escaped = true
			case char == '"':
				inString = false
			}
			position++
			continue
		}
		switch {
		case char == '"':
			inString = true
			output.WriteByte(char)
			position++
		case char == '/' && position+1 < len(source) && source[position+1] == '/':
			for position < len(source) && source[position] != '\n' {
				position++
			}
		case char == '/' && position+1 < len(source) && source[position+1] == '*':
			position += 2
			for position < len(source) && !(source[position] == '*' && position+1 < len(source) && source[position+1] == '/') {
				if source[position] == '\n' {
					output.WriteByte('\n')
				}
				position++
			}
			position += 2
		case char == ',' && closesAfterComma(source, position+1):
			position++
		default:
			output.WriteByte(char)
			position++
		}
	}
	return output.String()
}

// closesAfterComma 判断逗号之后（跳过空白与注释）是否紧跟 } 或 ]。
func closesAfterComma(source string, position int) bool {
	for position < len(source) {
		switch {
		case source[position] == ' ' || source[position] == '\t' || source[position] == '\r' || source[position] == '\n':
			position++
		case source[position] == '/' && position+1 < len(source) && source[position+1] == '/':
			for position < len(source) && source[position] != '\n' {
				position++
			}
		case source[position] == '/' && position+1 < len(source) && source[position+1] == '*':
			position += 2
			for position < len(source) && !(source[position] == '*' && position+1 < len(source) && source[position+1] == '/') {
				position++
			}
			position += 2
		default:
			return source[position] == '}' || source[position] == ']'
		}
	}
	return false
}

// describeJsonError 把 encoding/json 的错误转成带行号的中文说明。
func describeJsonError(stripped string, err error) error {
	var syntaxError *json.SyntaxError
	if errors.As(err, &syntaxError) {
		return fmt.Errorf("第 %d 行附近格式错误：%v", lineOfOffset(stripped, syntaxError.Offset), syntaxError)
	}
	var typeError *json.UnmarshalTypeError
	if errors.As(err, &typeError) {
		field := typeError.Field
		if field == "" {
			field = "（顶层）"
		}
		return fmt.Errorf("第 %d 行附近「%s」的类型不对：需要 %v，写的是 %s", lineOfOffset(stripped, typeError.Offset), field, typeError.Type, typeError.Value)
	}
	return fmt.Errorf("格式错误：%v", err)
}

func lineOfOffset(text string, offset int64) int {
	if offset > int64(len(text)) {
		offset = int64(len(text))
	}
	return strings.Count(text[:offset], "\n") + 1
}
