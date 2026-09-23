package main

import (
	"os"
	"path/filepath"
	"strings"
)

// npm / pnpm 代理：编辑用户目录下的 .npmrc（ini 格式），只动 proxy / https-proxy / noproxy 三行，其余内容原样保留。

var npmrcKeys = []string{"proxy", "https-proxy", "noproxy"}

// updateNpmrc 返回修改后的 .npmrc 内容；proxyUrl 为空表示移除代理配置。
func updateNpmrc(content, proxyUrl, noProxy string) string {
	wanted := map[string]string{}
	if proxyUrl != "" {
		wanted["proxy"] = proxyUrl
		wanted["https-proxy"] = proxyUrl
		if strings.TrimSpace(noProxy) != "" {
			wanted["noproxy"] = noProxy
		}
	}

	newline := "\n"
	if strings.Contains(content, "\r\n") {
		newline = "\r\n"
	}
	lines := strings.Split(strings.ReplaceAll(content, "\r\n", "\n"), "\n")
	if len(lines) > 0 && lines[len(lines)-1] == "" {
		lines = lines[:len(lines)-1]
	}

	written := map[string]bool{}
	var output []string
	for _, line := range lines {
		key := npmrcLineKey(line)
		if key == "" {
			output = append(output, line)
			continue
		}
		value, keep := wanted[key]
		if !keep || written[key] {
			continue
		}
		written[key] = true
		output = append(output, key+"="+value)
	}
	for _, key := range npmrcKeys {
		if value, keep := wanted[key]; keep && !written[key] {
			output = append(output, key+"="+value)
		}
	}
	if len(output) == 0 {
		return ""
	}
	return strings.Join(output, newline) + newline
}

// npmrcLineKey 判断一行是否是我们管理的键，是则返回规范化的键名，否则返回空串。
func npmrcLineKey(line string) string {
	trimmed := strings.TrimSpace(line)
	if trimmed == "" || strings.HasPrefix(trimmed, "#") || strings.HasPrefix(trimmed, ";") {
		return ""
	}
	separator := strings.IndexAny(trimmed, "=:")
	if separator < 0 {
		return ""
	}
	key := strings.ReplaceAll(strings.ToLower(strings.TrimSpace(trimmed[:separator])), "_", "-")
	if containsString(npmrcKeys, key) {
		return key
	}
	return ""
}

func npmrcPath() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(home, ".npmrc"), nil
}

// setNpmProxy 写入（proxyUrl 非空）或移除（proxyUrl 为空）npm 代理。
func setNpmProxy(proxyUrl, noProxy string) error {
	path, err := npmrcPath()
	if err != nil {
		return err
	}
	original, err := os.ReadFile(path)
	if err != nil && !os.IsNotExist(err) {
		return err
	}
	updated := updateNpmrc(string(original), proxyUrl, noProxy)
	if updated == string(original) || (updated == "" && os.IsNotExist(err)) {
		return nil
	}
	return os.WriteFile(path, []byte(updated), 0o644)
}

// readNpmProxy 返回 .npmrc 中 proxy / https-proxy / noproxy 的当前值。
func readNpmProxy() map[string]string {
	values := map[string]string{}
	path, err := npmrcPath()
	if err != nil {
		return values
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return values
	}
	for _, line := range strings.Split(strings.ReplaceAll(string(data), "\r\n", "\n"), "\n") {
		key := npmrcLineKey(line)
		if key == "" {
			continue
		}
		separator := strings.IndexAny(line, "=:")
		values[key] = strings.TrimSpace(line[separator+1:])
	}
	return values
}
