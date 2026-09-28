//go:build windows

package main

import "testing"

// TestNetworkMetered 读网络的费用类型（只读，不改设置）；在读网络信息的后台线程里也能调用。
func TestNetworkMetered(t *testing.T) {
	t.Logf("按流量计费：%v", networkMetered())
	done := make(chan bool)
	go func() {
		done <- readNetworkInfo().Metered == networkMetered() && cachedNetworkMetered("test") == networkMetered()
	}()
	if !<-done {
		t.Error("网络信息里的按流量计费和单独读的不一致")
	}
}
