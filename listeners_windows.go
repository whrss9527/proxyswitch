//go:build windows

package main

import (
	"encoding/binary"
	"net"
	"path/filepath"
	"syscall"
	"unsafe"
)

// 列出本机正在监听的 TCP 端口和对应进程，供“检测本机代理”使用。

const (
	tcpTableOwnerPidListener = 3
	tcpRowV4Size             = 24
	tcpRowV6Size             = 56
)

func listTcpListeners() []Listener {
	names := map[uint32]string{}
	var listeners []Listener
	for _, family := range []uint32{afInet, afInet6} {
		for _, listener := range tcpListenersOf(family) {
			if _, found := names[listener.Pid]; !found {
				names[listener.Pid] = processName(listener.Pid)
			}
			listener.Process = names[listener.Pid]
			listeners = append(listeners, listener)
		}
	}
	return listeners
}

func tcpListenersOf(family uint32) []Listener {
	var size uint32
	procGetExtendedTcpTable.Call(0, uintptr(unsafe.Pointer(&size)), 0, uintptr(family), tcpTableOwnerPidListener, 0)
	if size == 0 {
		return nil
	}
	var buffer []byte
	for attempt := 0; attempt < 3; attempt++ {
		buffer = make([]byte, size+1024)
		size = uint32(len(buffer))
		result, _, _ := procGetExtendedTcpTable.Call(uintptr(unsafe.Pointer(&buffer[0])), uintptr(unsafe.Pointer(&size)), 0, uintptr(family), tcpTableOwnerPidListener, 0)
		if result == errorSuccess {
			break
		}
		if result != errorInsufficientBuffer {
			return nil
		}
	}
	count := int(binary.LittleEndian.Uint32(buffer[0:4]))
	var listeners []Listener
	if family == afInet {
		// MIB_TCPROW_OWNER_PID：state、localAddr、localPort、remoteAddr、remotePort、owningPid。
		for index := 0; index < count; index++ {
			row := buffer[4+index*tcpRowV4Size:]
			if len(row) < tcpRowV4Size {
				break
			}
			listeners = append(listeners, Listener{
				Address: net.IP(row[4:8]).String(),
				Port:    int(binary.BigEndian.Uint16(row[8:10])),
				Pid:     binary.LittleEndian.Uint32(row[20:24]),
			})
		}
		return listeners
	}
	// MIB_TCP6ROW_OWNER_PID：localAddr[16]、localScopeId、localPort、remoteAddr[16]、remoteScopeId、remotePort、state、owningPid。
	for index := 0; index < count; index++ {
		row := buffer[4+index*tcpRowV6Size:]
		if len(row) < tcpRowV6Size {
			break
		}
		address := net.IP(append([]byte(nil), row[0:16]...))
		listeners = append(listeners, Listener{
			Address: address.String(),
			Port:    int(binary.BigEndian.Uint16(row[20:22])),
			Pid:     binary.LittleEndian.Uint32(row[52:56]),
		})
	}
	return listeners
}

// processName 返回进程的可执行文件名；权限不够时返回空串。
func processName(pid uint32) string {
	switch pid {
	case 0:
		return "Idle"
	case 4:
		return "System"
	}
	handle, _, _ := procOpenProcess.Call(processQueryLimitedInformation, 0, uintptr(pid))
	if handle == 0 {
		return ""
	}
	defer procCloseHandle.Call(handle)
	buffer := make([]uint16, 1024)
	length := uint32(len(buffer))
	if result, _, _ := procQueryFullProcessImageNameW.Call(handle, 0, uintptr(unsafe.Pointer(&buffer[0])), uintptr(unsafe.Pointer(&length))); result == 0 {
		return ""
	}
	return filepath.Base(syscall.UTF16ToString(buffer[:length]))
}
