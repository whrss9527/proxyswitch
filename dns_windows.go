//go:build windows

package main

import (
	"errors"
	"log/slog"
	"syscall"
)

// 清除 DNS 缓存：和 ipconfig /flushdns 一样，让系统重新解析域名，不需要管理员权限。网址诊断的结论里可以点，
// TUN 模式开始接管流量时也清一次：之前缓存的真实地址让连接绕过内核的域名规则，只能按 IP 分流。

var (
	dnsapi                    = syscall.NewLazyDLL(systemDir() + `\dnsapi.dll`)
	procDnsFlushResolverCache = dnsapi.NewProc("DnsFlushResolverCache")
)

func flushDnsCache() error {
	if err := procDnsFlushResolverCache.Find(); err != nil {
		return err
	}
	if result, _, _ := procDnsFlushResolverCache.Call(); result == 0 {
		return errors.New("系统没能清除 DNS 缓存，可以在命令提示符里运行 ipconfig /flushdns")
	}
	slog.Info("已清除 DNS 缓存")
	return nil
}

// FlushDns 清除这台电脑的 DNS 缓存（网址诊断的结论里点）。
func (app *App) FlushDns() error {
	return flushDnsCache()
}
