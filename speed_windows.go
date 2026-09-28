//go:build windows

package main

import "unsafe"

var (
	procGetIfTable2  = iphlpapi.NewProc("GetIfTable2")
	procFreeMibTable = iphlpapi.NewProc("FreeMibTable")
)

// InterfaceAndOperStatusFlags 里的位。
const (
	ifFlagHardwareInterface = 0x01
	ifFlagFilterInterface   = 0x02
)

// mibIfRow2 是 MIB_IF_ROW2，只用到类型、状态和收发字节数。
type mibIfRow2 struct {
	interfaceLuid            uint64
	interfaceIndex           uint32
	interfaceGuid            [16]byte
	alias                    [257]uint16
	description              [257]uint16
	physicalAddressLength    uint32
	physicalAddress          [32]byte
	permanentPhysicalAddress [32]byte
	mtu                      uint32
	ifType                   uint32
	tunnelType               uint32
	mediaType                uint32
	physicalMediumType       uint32
	accessType               uint32
	directionType            uint32
	flags                    uint8
	operStatus               uint32
	adminStatus              uint32
	mediaConnectState        uint32
	networkGuid              [16]byte
	connectionType           uint32
	transmitLinkSpeed        uint64
	receiveLinkSpeed         uint64
	inOctets                 uint64
	inCounters               [8]uint64
	outOctets                uint64
	outCounters              [8]uint64
}

// readInterfaceTotals 返回所有已连接的物理网卡累计收发的字节数。不算虚拟网卡和 VPN 隧道（它们的流量最终也走物理网卡，
// 算上会重复），也不算挂在网卡上的过滤驱动（计数和网卡本身一样）。
func readInterfaceTotals() (received, sent uint64, ok bool) {
	var table unsafe.Pointer
	if result, _, _ := procGetIfTable2.Call(uintptr(unsafe.Pointer(&table))); result != 0 || table == nil {
		return 0, 0, false
	}
	defer procFreeMibTable.Call(uintptr(table))
	count := *(*uint32)(table)
	for _, row := range unsafe.Slice((*mibIfRow2)(unsafe.Add(table, 8)), count) {
		if row.flags&ifFlagHardwareInterface == 0 || row.flags&ifFlagFilterInterface != 0 || row.operStatus != ifOperStatusUp || row.ifType == ifTypeSoftwareLoopback {
			continue
		}
		received += row.inOctets
		sent += row.outOctets
	}
	return received, sent, true
}
