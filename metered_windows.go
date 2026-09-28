//go:build windows

package main

import (
	"runtime"
	"unsafe"
)

// 按流量计费的网络：INetworkCostManager（网络列表管理器）给出这台电脑现在用的网络的费用类型。手机热点、
// 在 Windows 设置里设成「按流量计费的连接」的 Wi-Fi 是固定或可变计费；漫游、超出流量上限也算。

var (
	procCoCreateInstance = ole32.NewProc("CoCreateInstance")
	procCoUninitialize   = ole32.NewProc("CoUninitialize")

	clsidNetworkListManager = guid{0xDCB00C01, 0x570F, 0x4A9B, [8]byte{0x8D, 0x69, 0x19, 0x9F, 0xDB, 0xA5, 0x72, 0x3B}}
	iidNetworkCostManager   = guid{0xDCB00008, 0x570F, 0x4A9B, [8]byte{0x8D, 0x69, 0x19, 0x9F, 0xDB, 0xA5, 0x72, 0x3B}}
)

const (
	coinitMultithreaded = 0x0
	clsctxAll           = 0x17
	methodGetCost       = 3 // INetworkCostManager

	nlmCostFixed         = 0x2
	nlmCostVariable      = 0x4
	nlmCostOverDataLimit = 0x10000
	nlmCostRoaming       = 0x40000
)

// networkMetered 表示现在的网络按流量计费，读不到时当作不计费。
func networkMetered() bool {
	runtime.LockOSThread()
	defer runtime.UnlockOSThread()
	// 这个线程还没初始化 COM 时初始化成多线程套间，用完释放；已经初始化过（返回 RPC_E_CHANGED_MODE）时直接用。
	if result, _, _ := procCoInitializeEx.Call(0, coinitMultithreaded); hresultError(result) == nil {
		defer procCoUninitialize.Call()
	}
	var manager unsafe.Pointer
	result, _, _ := procCoCreateInstance.Call(uintptr(unsafe.Pointer(&clsidNetworkListManager)), 0, clsctxAll, uintptr(unsafe.Pointer(&iidNetworkCostManager)), uintptr(unsafe.Pointer(&manager)))
	if hresultError(result) != nil || manager == nil {
		return false
	}
	defer comRelease(manager)
	var cost uint32
	if err := comCall(manager, methodGetCost, uintptr(unsafe.Pointer(&cost)), 0); err != nil {
		return false
	}
	return cost&(nlmCostFixed|nlmCostVariable|nlmCostOverDataLimit|nlmCostRoaming) != 0
}
