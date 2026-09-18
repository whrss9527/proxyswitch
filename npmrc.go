package main

import (
	"os"
	"path/filepath"
	"strings"
)

// npm / pnpm 代理：直接编辑用户目录下的 .npmrc（ini 格式），只动 proxy / https-proxy / noproxy 三行，
// 其余内容原样保留。

var npmrcKeys = []string{"proxy", "https-proxy", "noproxy"}

// updateNpmrc 返回修改后的 .npmrc 内容。proxyURL 为空表示移除代理配置。
func updateNpmrc(content, proxyURL, noProxy string) string {
	want := map[string]string{}
	if proxyURL != "" {
		want["proxy"] = proxyURL
		want["https-proxy"] = proxyURL
		if strings.TrimSpace(noProxy) != "" {
			want["noproxy"] = noProxy
		}
	}

	nl := "\n"
	if strings.Contains(content, "\r\n") {
		nl = "\r\n"
	}
	lines := strings.Split(strings.ReplaceAll(content, "\r\n", "\n"), "\n")
	// 去掉末尾因结尾换行产生的空串
	if len(lines) > 0 && lines[len(lines)-1] == "" {
		lines = lines[:len(lines)-1]
	}

	seen := map[string]bool{}
	var out []string
	for _, line := range lines {
		key := npmrcLineKey(line)
		if key == "" {
			out = append(out, line)
			continue
		}
		v, ok := want[key]
		if !ok {
			continue // 需要删除的行
		}
		if seen[key] {
			continue // 重复的行只保留一条
		}
		seen[key] = true
		out = append(out, key+"="+v)
	}
	for _, key := range npmrcKeys {
		if v, ok := want[key]; ok && !seen[key] {
			out = append(out, key+"="+v)
		}
	}
	if len(out) == 0 {
		return ""
	}
	return strings.Join(out, nl) + nl
}

// npmrcLineKey 判断一行是否是我们管理的键，是则返回规范化的键名。
func npmrcLineKey(line string) string {
	t := strings.TrimSpace(line)
	if t == "" || strings.HasPrefix(t, "#") || strings.HasPrefix(t, ";") {
		return ""
	}
	i := strings.IndexAny(t, "=:")
	if i < 0 {
		return ""
	}
	key := strings.ToLower(strings.TrimSpace(t[:i]))
	key = strings.ReplaceAll(key, "_", "-")
	if key == "https_proxy" {
		key = "https-proxy"
	}
	for _, k := range npmrcKeys {
		if key == k {
			return k
		}
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

// setNpmProxy 写入（proxyURL 非空）或移除（proxyURL 为空）npm 代理。
func setNpmProxy(proxyURL, noProxy string) error {
	path, err := npmrcPath()
	if err != nil {
		return err
	}
	old, err := os.ReadFile(path)
	if err != nil && !os.IsNotExist(err) {
		return err
	}
	updated := updateNpmrc(string(old), proxyURL, noProxy)
	if updated == string(old) {
		return nil
	}
	if updated == "" && os.IsNotExist(err) {
		return nil
	}
	return os.WriteFile(path, []byte(updated), 0o644)
}

// readNpmProxy 返回 .npmrc 中当前的 https-proxy / proxy 值。
func readNpmProxy() string {
	path, err := npmrcPath()
	if err != nil {
		return ""
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return ""
	}
	var httpProxy string
	for _, line := range strings.Split(strings.ReplaceAll(string(data), "\r\n", "\n"), "\n") {
		key := npmrcLineKey(line)
		if key == "" {
			continue
		}
		i := strings.IndexAny(line, "=:")
		v := strings.TrimSpace(line[i+1:])
		if key == "https-proxy" && v != "" {
			return v
		}
		if key == "proxy" {
			httpProxy = v
		}
	}
	return httpProxy
}
