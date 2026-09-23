package main

import "testing"

func TestParseHotkey(t *testing.T) {
	cases := []struct {
		text      string
		modifiers uint32
		keyCode   uint32
		display   string
	}{
		{"Ctrl+Alt+P", modifierControl | modifierAlt, 'P', "Ctrl+Alt+P"},
		{" control + shift + f9 ", modifierControl | modifierShift, 0x78, "Ctrl+Shift+F9"},
		{"Win+Space", modifierWin, 0x20, "Win+Space"},
		{"Alt+1", modifierAlt, '1', "Alt+1"},
		{"Ctrl+Alt++", modifierControl | modifierAlt, 0xBB, "Ctrl+Alt++"},
		{"Ctrl+Numpad5", modifierControl, 0x65, "Ctrl+Numpad5"},
		{"Shift+Alt+PgDn", modifierShift | modifierAlt, 0x22, "Shift+Alt+PageDown"},
		{"ctrl+/", modifierControl, 0xBF, "Ctrl+/"},
	}
	for _, item := range cases {
		hotkey, err := parseHotkey(item.text)
		if err != nil {
			t.Errorf("%q 解析失败：%v", item.text, err)
			continue
		}
		if hotkey.Modifiers != item.modifiers || hotkey.KeyCode != item.keyCode || hotkey.Text != item.display {
			t.Errorf("%q 解析为 %+v，应为 %x %x %s", item.text, hotkey, item.modifiers, item.keyCode, item.display)
		}
	}
	for _, invalid := range []string{"", "P", "Ctrl+Alt", "Ctrl+P+Q", "Ctrl+F25", "Ctrl+Hyper", "Alt+中"} {
		if _, err := parseHotkey(invalid); err == nil {
			t.Errorf("%q 应当解析失败", invalid)
		}
	}
}

func TestParseModifiers(t *testing.T) {
	hotkey, err := parseModifiers("ctrl + alt + ctrl")
	if err != nil || hotkey.Modifiers != modifierControl|modifierAlt || hotkey.Text != "Ctrl+Alt" {
		t.Errorf("解析结果不对：%+v %v", hotkey, err)
	}
	for _, invalid := range []string{"", "Ctrl+1", "P"} {
		if _, err := parseModifiers(invalid); err == nil {
			t.Errorf("%q 应当解析失败", invalid)
		}
	}
}
