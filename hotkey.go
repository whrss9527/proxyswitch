package main

import (
	"fmt"
	"strings"
)

// Hotkey 是解析后的全局快捷键。
type Hotkey struct {
	Mods uint32 // MOD_ALT / MOD_CONTROL / MOD_SHIFT / MOD_WIN 的组合
	VK   uint32 // 虚拟键码
	Text string // 规范化后的显示文本，如 "Ctrl+Alt+P"
}

const (
	hkModAlt     = 0x0001
	hkModControl = 0x0002
	hkModShift   = 0x0004
	hkModWin     = 0x0008
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

// parseHotkey 解析形如 "Ctrl+Alt+P"、"Win+Shift+F9" 的快捷键描述。
func parseHotkey(s string) (Hotkey, error) {
	var hk Hotkey
	s = strings.TrimSpace(s)
	if s == "" {
		return hk, fmt.Errorf("快捷键为空")
	}
	// 允许 "Ctrl + Alt + P" 这种带空格的写法；单独的 "+" 作为主键时用 "plus"
	parts := strings.Split(s, "+")
	var tokens []string
	for i, p := range parts {
		p = strings.TrimSpace(p)
		if p == "" {
			// 连续的 "++"：表示主键是 "+" 本身（例如 "Ctrl++"）
			if i == len(parts)-1 && len(tokens) > 0 {
				tokens = append(tokens, "plus")
			}
			continue
		}
		tokens = append(tokens, p)
	}
	if len(tokens) == 0 {
		return hk, fmt.Errorf("快捷键为空")
	}
	var modNames []string
	keySet := false
	for _, t := range tokens {
		lt := strings.ToLower(t)
		switch lt {
		case "ctrl", "control", "ctl":
			hk.Mods |= hkModControl
			modNames = append(modNames, "Ctrl")
			continue
		case "alt", "option":
			hk.Mods |= hkModAlt
			modNames = append(modNames, "Alt")
			continue
		case "shift":
			hk.Mods |= hkModShift
			modNames = append(modNames, "Shift")
			continue
		case "win", "windows", "super", "meta", "cmd":
			hk.Mods |= hkModWin
			modNames = append(modNames, "Win")
			continue
		}
		if keySet {
			return hk, fmt.Errorf("只能有一个主键，多余的：%q", t)
		}
		vk, display, err := parseKey(lt)
		if err != nil {
			return hk, err
		}
		hk.VK = vk
		keySet = true
		modNames = append(modNames, display)
	}
	if !keySet {
		return hk, fmt.Errorf("缺少主键（例如 Ctrl+Alt+P 里的 P）")
	}
	if hk.Mods == 0 {
		return hk, fmt.Errorf("至少需要一个修饰键（Ctrl / Alt / Shift / Win），否则会拦截正常输入")
	}
	hk.Text = strings.Join(modNames, "+")
	return hk, nil
}

func parseKey(lt string) (uint32, string, error) {
	if lt == "plus" {
		return 0xBB, "+", nil
	}
	if len(lt) == 1 {
		c := lt[0]
		switch {
		case c >= 'a' && c <= 'z':
			return uint32(c - 'a' + 'A'), strings.ToUpper(lt), nil
		case c >= '0' && c <= '9':
			return uint32(c), lt, nil
		}
		if vk, ok := namedKeys[lt]; ok {
			return vk, keyDisplay[vk], nil
		}
		return 0, "", fmt.Errorf("不支持的按键 %q", lt)
	}
	// F1 ~ F24
	if strings.HasPrefix(lt, "f") {
		n := 0
		ok := len(lt) > 1
		for _, r := range lt[1:] {
			if r < '0' || r > '9' {
				ok = false
				break
			}
			n = n*10 + int(r-'0')
		}
		if ok && n >= 1 && n <= 24 {
			return uint32(0x70 + n - 1), fmt.Sprintf("F%d", n), nil
		}
	}
	// 小键盘 numpad0 ~ numpad9
	if strings.HasPrefix(lt, "numpad") && len(lt) == 7 && lt[6] >= '0' && lt[6] <= '9' {
		return uint32(0x60 + lt[6] - '0'), "Numpad" + lt[6:], nil
	}
	if vk, ok := namedKeys[lt]; ok {
		return vk, keyDisplay[vk], nil
	}
	return 0, "", fmt.Errorf("不支持的按键 %q", lt)
}
