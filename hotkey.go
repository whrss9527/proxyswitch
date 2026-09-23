package main

import (
	"errors"
	"fmt"
	"strings"
)

// Hotkey 是解析后的全局快捷键。
type Hotkey struct {
	Modifiers uint32
	KeyCode   uint32
	Text      string
}

// RegisterHotKey 的修饰键位
const (
	modifierAlt     = 0x0001
	modifierControl = 0x0002
	modifierShift   = 0x0004
	modifierWin     = 0x0008
)

var namedKeys = map[string]uint32{
	"space":       0x20,
	"tab":         0x09,
	"enter":       0x0D,
	"return":      0x0D,
	"esc":         0x1B,
	"escape":      0x1B,
	"backspace":   0x08,
	"insert":      0x2D,
	"ins":         0x2D,
	"delete":      0x2E,
	"del":         0x2E,
	"home":        0x24,
	"end":         0x23,
	"pageup":      0x21,
	"pgup":        0x21,
	"pagedown":    0x22,
	"pgdn":        0x22,
	"left":        0x25,
	"up":          0x26,
	"right":       0x27,
	"down":        0x28,
	"pause":       0x13,
	"capslock":    0x14,
	"numlock":     0x90,
	"scrolllock":  0x91,
	"printscreen": 0x2C,
	"`":           0xC0,
	"-":           0xBD,
	"=":           0xBB,
	"[":           0xDB,
	"]":           0xDD,
	"\\":          0xDC,
	";":           0xBA,
	"'":           0xDE,
	",":           0xBC,
	".":           0xBE,
	"/":           0xBF,
}

var keyDisplay = map[uint32]string{
	0x20: "Space", 0x09: "Tab", 0x0D: "Enter", 0x1B: "Esc", 0x08: "Backspace",
	0x2D: "Insert", 0x2E: "Delete", 0x24: "Home", 0x23: "End", 0x21: "PageUp", 0x22: "PageDown",
	0x25: "Left", 0x26: "Up", 0x27: "Right", 0x28: "Down", 0x13: "Pause", 0x14: "CapsLock",
	0x90: "NumLock", 0x91: "ScrollLock", 0x2C: "PrintScreen",
	0xC0: "`", 0xBD: "-", 0xBB: "=", 0xDB: "[", 0xDD: "]", 0xDC: "\\", 0xBA: ";", 0xDE: "'",
	0xBC: ",", 0xBE: ".", 0xBF: "/",
}

// parseHotkey 解析形如 "Ctrl+Alt+P"、"Win+Shift+F9" 的快捷键描述，要求至少一个修饰键和一个主键。
func parseHotkey(text string) (Hotkey, error) {
	var hotkey Hotkey
	tokens := hotkeyTokens(text)
	if len(tokens) == 0 {
		return hotkey, errors.New("快捷键为空")
	}
	var names []string
	keySet := false
	for _, token := range tokens {
		lowered := strings.ToLower(token)
		if modifier, name, ok := modifierOf(lowered); ok {
			hotkey.Modifiers |= modifier
			names = append(names, name)
			continue
		}
		if keySet {
			return hotkey, fmt.Errorf("只能有一个主键，多余的：%q", token)
		}
		keyCode, display, err := parseKey(lowered)
		if err != nil {
			return hotkey, err
		}
		hotkey.KeyCode = keyCode
		keySet = true
		names = append(names, display)
	}
	if !keySet {
		return hotkey, errors.New("缺少主键（例如 Ctrl+Alt+P 里的 P）")
	}
	if hotkey.Modifiers == 0 {
		return hotkey, errors.New("至少需要一个修饰键（Ctrl / Alt / Shift / Win），否则会拦截正常输入")
	}
	hotkey.Text = strings.Join(names, "+")
	return hotkey, nil
}

// parseModifiers 解析只含修饰键的组合，例如 "Ctrl+Alt"，用于按数字切换配置。
func parseModifiers(text string) (Hotkey, error) {
	var hotkey Hotkey
	tokens := hotkeyTokens(text)
	if len(tokens) == 0 {
		return hotkey, errors.New("修饰键为空")
	}
	var names []string
	for _, token := range tokens {
		modifier, name, ok := modifierOf(strings.ToLower(token))
		if !ok {
			return hotkey, fmt.Errorf("%q 不是修饰键，只能用 Ctrl / Alt / Shift / Win", token)
		}
		if hotkey.Modifiers&modifier != 0 {
			continue
		}
		hotkey.Modifiers |= modifier
		names = append(names, name)
	}
	hotkey.Text = strings.Join(names, "+")
	return hotkey, nil
}

// hotkeyTokens 按 + 拆分，允许空格；末尾的 "++" 表示主键就是 + 本身。
func hotkeyTokens(text string) []string {
	parts := strings.Split(strings.TrimSpace(text), "+")
	var tokens []string
	for index, part := range parts {
		part = strings.TrimSpace(part)
		if part == "" {
			if index == len(parts)-1 && len(tokens) > 0 {
				tokens = append(tokens, "plus")
			}
			continue
		}
		tokens = append(tokens, part)
	}
	return tokens
}

func modifierOf(lowered string) (uint32, string, bool) {
	switch lowered {
	case "ctrl", "control", "ctl":
		return modifierControl, "Ctrl", true
	case "alt", "option":
		return modifierAlt, "Alt", true
	case "shift":
		return modifierShift, "Shift", true
	case "win", "windows", "super", "meta", "cmd":
		return modifierWin, "Win", true
	}
	return 0, "", false
}

func parseKey(lowered string) (uint32, string, error) {
	if lowered == "plus" {
		return 0xBB, "+", nil
	}
	if len(lowered) == 1 {
		char := lowered[0]
		switch {
		case char >= 'a' && char <= 'z':
			return uint32(char - 'a' + 'A'), strings.ToUpper(lowered), nil
		case char >= '0' && char <= '9':
			return uint32(char), lowered, nil
		}
		if keyCode, ok := namedKeys[lowered]; ok {
			return keyCode, keyDisplay[keyCode], nil
		}
		return 0, "", fmt.Errorf("不支持的按键 %q", lowered)
	}
	if strings.HasPrefix(lowered, "f") {
		number := 0
		valid := len(lowered) > 1
		for _, digit := range lowered[1:] {
			if digit < '0' || digit > '9' {
				valid = false
				break
			}
			number = number*10 + int(digit-'0')
		}
		if valid && number >= 1 && number <= 24 {
			return uint32(0x70 + number - 1), fmt.Sprintf("F%d", number), nil
		}
	}
	if strings.HasPrefix(lowered, "numpad") && len(lowered) == 7 && lowered[6] >= '0' && lowered[6] <= '9' {
		return uint32(0x60 + lowered[6] - '0'), "Numpad" + lowered[6:], nil
	}
	if keyCode, ok := namedKeys[lowered]; ok {
		return keyCode, keyDisplay[keyCode], nil
	}
	return 0, "", fmt.Errorf("不支持的按键 %q", lowered)
}
