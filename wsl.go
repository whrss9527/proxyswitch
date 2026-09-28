package main

import "strings"

// WSL 走代理：WSL2 默认的 NAT 网络连不到 Windows 的 127.0.0.1，本机的代理在 WSL 里用不了。Windows 11 22H2 起可以
// 把 WSL 的网络设成镜像模式（networkingMode=mirrored），WSL 和 Windows 共用网卡，WSL 里的 127.0.0.1 就是 Windows
// 本机；再加上 autoProxy=true，WSL 启动时自动使用 Windows 的代理设置。这两项写在用户目录下 .wslconfig 的 [wsl2] 段，
// 重启 WSL（wsl --shutdown）后生效。更早的 Windows 可以经局域网共享的入口使用代理。Windows 上的部分见 wsl_windows.go。

// wslMirroredMinBuild 是支持镜像网络的最低 Windows 版本（Windows 11 22H2）。
const wslMirroredMinBuild = 22621

// WslInfo 是 WSL 的情况：Installed 表示装了 WSL 并且有发行版；Supported 表示这个 Windows 版本支持镜像网络；
// Mirrored 和 AutoProxy 是 .wslconfig（Config 是它的路径）里的设置；Restart 表示改过设置、要重启 WSL 才生效。
type WslInfo struct {
	Installed bool     `json:"installed"`
	Distros   []string `json:"distros"`
	Supported bool     `json:"supported"`
	Mirrored  bool     `json:"mirrored"`
	AutoProxy bool     `json:"auto_proxy"`
	Restart   bool     `json:"restart"`
	Config    string   `json:"config"`
}

// Ready 表示 WSL 已经设置成使用本机的代理。
func (info WslInfo) Ready() bool {
	return info.Mirrored && info.AutoProxy
}

// wslConfigValues 读出 .wslconfig 的 [wsl2] 段里的 networkingMode 和 autoProxy。autoProxy 没写时是开启的。
func wslConfigValues(text string) (mirrored, autoProxy bool) {
	autoProxy = true
	section := ""
	for _, line := range strings.Split(strings.TrimPrefix(text, "\ufeff"), "\n") {
		trimmed := strings.TrimSpace(line)
		if name, ok := iniSection(trimmed); ok {
			section = name
			continue
		}
		key, value, ok := iniEntry(trimmed)
		if section != "wsl2" || !ok {
			continue
		}
		switch key {
		case "networkingmode":
			mirrored = strings.EqualFold(value, "mirrored")
		case "autoproxy":
			autoProxy = strings.EqualFold(value, "true")
		}
	}
	return mirrored, autoProxy
}

// setWslProxy 在 .wslconfig 的内容里设置 networkingMode=mirrored 和 autoProxy=true：已有的改掉，没有的加在 [wsl2]
// 段的开头（没有这一段时加在末尾），其余内容、注释和换行符原样保留。
func setWslProxy(text string) string {
	bom := strings.HasPrefix(text, "\ufeff")
	newline := "\n"
	if strings.Contains(text, "\r\n") {
		newline = "\r\n"
	}
	lines := strings.Split(strings.ReplaceAll(strings.TrimPrefix(text, "\ufeff"), "\r\n", "\n"), "\n")
	wanted := map[string]string{"networkingmode": "networkingMode=mirrored", "autoproxy": "autoProxy=true"}
	written := map[string]bool{}
	section, header := "", -1
	for index, line := range lines {
		trimmed := strings.TrimSpace(line)
		if name, ok := iniSection(trimmed); ok {
			section = name
			if name == "wsl2" && header < 0 {
				header = index
			}
			continue
		}
		if key, _, ok := iniEntry(trimmed); ok && section == "wsl2" && wanted[key] != "" {
			lines[index] = wanted[key]
			written[key] = true
		}
	}
	var missing []string
	for _, key := range []string{"networkingmode", "autoproxy"} {
		if !written[key] {
			missing = append(missing, wanted[key])
		}
	}
	if header < 0 {
		for len(lines) > 0 && strings.TrimSpace(lines[len(lines)-1]) == "" {
			lines = lines[:len(lines)-1]
		}
		if len(lines) > 0 {
			lines = append(lines, "")
		}
		lines = append(append(lines, "[wsl2]"), missing...)
		lines = append(lines, "")
	} else if len(missing) > 0 {
		lines = append(lines[:header+1], append(missing, lines[header+1:]...)...)
	}
	result := strings.Join(lines, newline)
	if bom {
		result = "\ufeff" + result
	}
	return result
}

// resetWslProxy 去掉 .wslconfig 里 [wsl2] 段的 networkingMode 和 autoProxy，恢复成 WSL 默认的 NAT 网络，其余内容原样保留。
func resetWslProxy(text string) string {
	newline := "\n"
	if strings.Contains(text, "\r\n") {
		newline = "\r\n"
	}
	lines := strings.Split(text, newline)
	kept := lines[:0]
	section := ""
	for _, line := range lines {
		trimmed := strings.TrimSpace(strings.TrimPrefix(line, "\ufeff"))
		if name, ok := iniSection(trimmed); ok {
			section = name
		} else if key, _, ok := iniEntry(trimmed); ok && section == "wsl2" && (key == "networkingmode" || key == "autoproxy") {
			continue
		}
		kept = append(kept, line)
	}
	return strings.Join(kept, newline)
}

// iniSection 认出「[名字]」这样的段落标题，名字转成小写。
func iniSection(line string) (string, bool) {
	if strings.HasPrefix(line, "[") && strings.HasSuffix(line, "]") {
		return strings.ToLower(strings.TrimSpace(line[1 : len(line)-1])), true
	}
	return "", false
}

// iniEntry 认出「键=值」，键转成小写；注释（# 或 ; 开头）和空行不算。
func iniEntry(line string) (key, value string, ok bool) {
	if line == "" || strings.HasPrefix(line, "#") || strings.HasPrefix(line, ";") {
		return "", "", false
	}
	key, value, ok = strings.Cut(line, "=")
	if !ok {
		return "", "", false
	}
	value = strings.TrimSpace(value)
	if cut := strings.IndexAny(value, "#;"); cut >= 0 {
		value = strings.TrimSpace(value[:cut])
	}
	return strings.ToLower(strings.TrimSpace(key)), value, true
}
