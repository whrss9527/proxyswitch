//go:build windows

package main

import (
	"encoding/binary"
	"fmt"
	"net"
	"strings"
	"sync"
	"time"
	"unsafe"
)

// 读取当前网络的特征：已连接的 Wi-Fi 名称、每块有默认网关的网卡的 DNS 后缀、网关 IP 与 MAC。

// ---------- Wi-Fi ----------

const (
	wlanInterfaceInfoSize       = 532
	wlanInterfaceStateOffset    = 528
	wlanInterfaceStateConnected = 1
	wlanOpcodeCurrentConnection = 7
	wlanSsidLengthOffset        = 520
	wlanSsidOffset              = 524
)

type wlanClient struct {
	mutex  sync.Mutex
	handle uintptr
}

var wifi wlanClient

// currentSsids 返回已连接的 Wi-Fi 名称。Windows 11 24H2 起读取 Wi-Fi 名称需要开启“位置”权限，此时返回说明。
func (client *wlanClient) currentSsids() ([]string, string) {
	client.mutex.Lock()
	defer client.mutex.Unlock()
	if procWlanOpenHandle.Find() != nil {
		return nil, ""
	}
	if client.handle == 0 {
		var negotiated uint32
		result, _, _ := procWlanOpenHandle.Call(2, 0, uintptr(unsafe.Pointer(&negotiated)), uintptr(unsafe.Pointer(&client.handle)))
		if result != errorSuccess {
			client.handle = 0
			// 没有无线网卡或 WLAN 服务没有运行。
			return nil, ""
		}
	}
	var list unsafe.Pointer
	result, _, _ := procWlanEnumInterfaces.Call(client.handle, 0, uintptr(unsafe.Pointer(&list)))
	if result != errorSuccess {
		procWlanCloseHandle.Call(client.handle, 0)
		client.handle = 0
		return nil, ""
	}
	defer procWlanFreeMemory.Call(uintptr(list))
	count := *(*uint32)(list)
	var ssids []string
	hint := ""
	for index := uint32(0); index < count; index++ {
		entry := unsafe.Add(list, 8+uintptr(index)*wlanInterfaceInfoSize)
		if *(*uint32)(unsafe.Add(entry, wlanInterfaceStateOffset)) != wlanInterfaceStateConnected {
			continue
		}
		var size uint32
		var attributes unsafe.Pointer
		result, _, _ := procWlanQueryInterface.Call(client.handle, uintptr(entry), wlanOpcodeCurrentConnection, 0,
			uintptr(unsafe.Pointer(&size)), uintptr(unsafe.Pointer(&attributes)), 0)
		if result == errorAccessDenied {
			hint = "Windows 需要开启「位置」权限才能读取 Wi-Fi 名称：设置 → 隐私和安全性 → 位置，打开「让桌面应用访问你的位置」。也可以改用 DNS 后缀或网关作为条件。"
			continue
		}
		if result != errorSuccess || attributes == nil {
			continue
		}
		length := *(*uint32)(unsafe.Add(attributes, wlanSsidLengthOffset))
		if length > 32 {
			length = 32
		}
		ssid := unsafe.Slice((*byte)(unsafe.Add(attributes, wlanSsidOffset)), length)
		if length > 0 {
			ssids = append(ssids, string(ssid))
		}
		procWlanFreeMemory.Call(uintptr(attributes))
	}
	return ssids, hint
}

// ---------- 网卡 ----------

const (
	afUnspec = 0
	afInet   = 2
	afInet6  = 23

	gaaFlagSkipAnycast     = 0x2
	gaaFlagSkipMulticast   = 0x4
	gaaFlagSkipDnsServer   = 0x8
	gaaFlagIncludeGateways = 0x80
	ifTypeSoftwareLoopback = 24
	ifTypeIeee80211        = 71
	ifOperStatusUp         = 1

	adapterNextOffset         = 8
	adapterDnsSuffixOffset    = 56
	adapterFriendlyNameOffset = 72
	adapterIfTypeOffset       = 100
	adapterOperStatusOffset   = 104
	adapterGatewayOffset      = 208
	gatewayNextOffset         = 8
	gatewaySockaddrOffset     = 16
)

func readAdapters() []NetworkAdapter {
	size := uint32(16 << 10)
	var buffer []byte
	for attempt := 0; attempt < 3; attempt++ {
		buffer = make([]byte, size)
		result, _, _ := procGetAdaptersAddresses.Call(afUnspec,
			gaaFlagSkipAnycast|gaaFlagSkipMulticast|gaaFlagSkipDnsServer|gaaFlagIncludeGateways,
			0, uintptr(unsafe.Pointer(&buffer[0])), uintptr(unsafe.Pointer(&size)))
		if result == errorSuccess {
			break
		}
		if result != errorBufferOverflow {
			return nil
		}
	}
	var adapters []NetworkAdapter
	macs := arpTable()
	for adapter := unsafe.Pointer(&buffer[0]); adapter != nil; adapter = *(*unsafe.Pointer)(unsafe.Add(adapter, adapterNextOffset)) {
		ifType := *(*uint32)(unsafe.Add(adapter, adapterIfTypeOffset))
		if ifType == ifTypeSoftwareLoopback || *(*uint32)(unsafe.Add(adapter, adapterOperStatusOffset)) != ifOperStatusUp {
			continue
		}
		gateway := ""
		for entry := *(*unsafe.Pointer)(unsafe.Add(adapter, adapterGatewayOffset)); entry != nil; entry = *(*unsafe.Pointer)(unsafe.Add(entry, gatewayNextOffset)) {
			address := sockaddrIp(*(*unsafe.Pointer)(unsafe.Add(entry, gatewaySockaddrOffset)))
			if address == nil || address.IsUnspecified() {
				continue
			}
			// 优先用 IPv4 网关：可以查到 MAC，也更常见。
			if gateway == "" || (address.To4() != nil && net.ParseIP(gateway).To4() == nil) {
				gateway = address.String()
			}
		}
		if gateway == "" {
			continue
		}
		networkAdapter := NetworkAdapter{
			Name:      utf16PointerToString(*(**uint16)(unsafe.Add(adapter, adapterFriendlyNameOffset))),
			DnsSuffix: utf16PointerToString(*(**uint16)(unsafe.Add(adapter, adapterDnsSuffixOffset))),
			Gateway:   gateway,
			Wireless:  ifType == ifTypeIeee80211,
		}
		if ip := net.ParseIP(gateway).To4(); ip != nil {
			networkAdapter.GatewayMac = macs[gateway]
			if networkAdapter.GatewayMac == "" {
				networkAdapter.GatewayMac = resolveMac(ip)
			}
		}
		adapters = append(adapters, networkAdapter)
	}
	return adapters
}

const errorBufferOverflow = 111

func sockaddrIp(sockaddr unsafe.Pointer) net.IP {
	if sockaddr == nil {
		return nil
	}
	switch *(*uint16)(sockaddr) {
	case afInet:
		return net.IP(append([]byte(nil), unsafe.Slice((*byte)(unsafe.Add(sockaddr, 4)), 4)...))
	case afInet6:
		return net.IP(append([]byte(nil), unsafe.Slice((*byte)(unsafe.Add(sockaddr, 8)), 16)...))
	}
	return nil
}

// arpTable 返回 IPv4 地址到 MAC 的映射（ARP 缓存）。
func arpTable() map[string]string {
	table := map[string]string{}
	var size uint32
	procGetIpNetTable.Call(0, uintptr(unsafe.Pointer(&size)), 0)
	if size == 0 {
		return table
	}
	buffer := make([]byte, size)
	if result, _, _ := procGetIpNetTable.Call(uintptr(unsafe.Pointer(&buffer[0])), uintptr(unsafe.Pointer(&size)), 0); result != errorSuccess {
		return table
	}
	count := binary.LittleEndian.Uint32(buffer[0:4])
	// MIB_IPNETROW：dwIndex、dwPhysAddrLen、bPhysAddr[8]、dwAddr、dwType，共 24 字节。
	for index := uint32(0); index < count; index++ {
		offset := 4 + int(index)*24
		if offset+24 > len(buffer) {
			break
		}
		row := buffer[offset : offset+24]
		length := binary.LittleEndian.Uint32(row[4:8])
		rowType := binary.LittleEndian.Uint32(row[20:24])
		if length != 6 || rowType == 2 {
			continue
		}
		address := net.IP(row[16:20]).String()
		table[address] = formatMac(row[8:14])
	}
	return table
}

var (
	arpMutex    sync.Mutex
	arpFailures = map[string]time.Time{}
)

// resolveMac 在 ARP 缓存里没有网关时主动查询一次；查不到的网关一分钟内不再重复查询，
// 因为网关不回应时每次查询都要等几秒。
func resolveMac(ip net.IP) string {
	key := ip.String()
	arpMutex.Lock()
	failedAt, failed := arpFailures[key]
	arpMutex.Unlock()
	if failed && time.Since(failedAt) < time.Minute {
		return ""
	}
	var mac [8]byte
	length := uint32(len(mac))
	destination := binary.LittleEndian.Uint32(ip)
	if result, _, _ := procSendARP.Call(uintptr(destination), 0, uintptr(unsafe.Pointer(&mac[0])), uintptr(unsafe.Pointer(&length))); result != errorSuccess || length != 6 {
		arpMutex.Lock()
		arpFailures[key] = time.Now()
		arpMutex.Unlock()
		return ""
	}
	return formatMac(mac[:6])
}

func formatMac(bytes []byte) string {
	parts := make([]string, len(bytes))
	for index, value := range bytes {
		parts[index] = fmt.Sprintf("%02x", value)
	}
	return strings.Join(parts, "-")
}

func readNetworkInfo() NetworkInfo {
	ssids, hint := wifi.currentSsids()
	adapters := readAdapters()
	if len(ssids) == 0 && hint != "" {
		if remembered := ssidsFromNetworkList(adapters); len(remembered) > 0 {
			ssids, hint = remembered, ""
		}
	}
	return NetworkInfo{Ssids: ssids, SsidError: hint, Adapters: adapters}
}

const (
	networkListKey      = `SOFTWARE\Microsoft\Windows NT\CurrentVersion\NetworkList`
	networkNameTypeWifi = 71
	systemTimeLength    = 16
)

// ssidsFromNetworkList 在没有位置权限、读不到 Wi-Fi 名称时，按网关 MAC 从系统记录的网络列表里找出 Wi-Fi 名称：
// Signatures\Unmanaged 下每个网络记有网关 MAC 和名称，同一网关有多个记录时取最近连接过的。
func ssidsFromNetworkList(adapters []NetworkAdapter) []string {
	signatures, err := openRegistryKey(hkeyLocalMachine, networkListKey+`\Signatures\Unmanaged`, keyRead)
	if err != nil {
		return nil
	}
	defer signatures.Close()
	var ssids []string
	for _, adapter := range adapters {
		if !adapter.Wireless || adapter.GatewayMac == "" {
			continue
		}
		bestName, bestTime := "", ""
		for _, name := range signatures.SubkeyNames() {
			signature, err := openRegistryKey(hkeyLocalMachine, networkListKey+`\Signatures\Unmanaged\`+name, keyRead)
			if err != nil {
				continue
			}
			mac, _ := signature.Binary("DefaultGatewayMac")
			ssid, _ := signature.String("FirstNetwork")
			profileGuid, _ := signature.String("ProfileGuid")
			signature.Close()
			if len(mac) < 6 || formatMac(mac[:6]) != strings.ToLower(adapter.GatewayMac) || ssid == "" {
				continue
			}
			profile, err := openRegistryKey(hkeyLocalMachine, networkListKey+`\Profiles\`+profileGuid, keyRead)
			if err != nil {
				continue
			}
			nameType, _ := profile.Dword("NameType")
			lastConnected, _ := profile.Binary("DateLastConnected")
			profile.Close()
			if nameType != networkNameTypeWifi || len(lastConnected) < systemTimeLength {
				continue
			}
			// SYSTEMTIME 依次是年、月、星期、日、时、分、秒、毫秒；去掉星期后按字节比较即可比出先后。
			stamp := string(lastConnected[0:4]) + string(lastConnected[6:16])
			if stamp > bestTime {
				bestName, bestTime = ssid, stamp
			}
		}
		if bestName != "" {
			ssids = append(ssids, bestName)
		}
	}
	return ssids
}
