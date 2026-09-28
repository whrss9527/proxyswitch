//go:build windows

package main

import "testing"

// TestFlushDnsCache 清除 DNS 缓存（和 ipconfig /flushdns 一样，不需要管理员权限，也不改设置）。
func TestFlushDnsCache(t *testing.T) {
	if err := flushDnsCache(); err != nil {
		t.Fatal(err)
	}
}
