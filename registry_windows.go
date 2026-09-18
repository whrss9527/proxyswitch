//go:build windows

package main

import (
	"fmt"
	"syscall"
	"unsafe"
)

// 极简注册表封装（只用到 HKCU 下的几个值）。

type regKey uintptr

func regOpen(root uintptr, path string, access uint32) (regKey, error) {
	var h uintptr
	r, _, _ := procRegOpenKeyExW.Call(root,
		uintptr(unsafe.Pointer(utf16Ptr(path))), 0, uintptr(access),
		uintptr(unsafe.Pointer(&h)))
	if r != errorSuccess {
		return 0, fmt.Errorf("RegOpenKeyEx(%s): %v", path, syscall.Errno(r))
	}
	return regKey(h), nil
}

// regCreate 打开（不存在则创建）一个键，带读写权限。
func regCreate(root uintptr, path string) (regKey, error) {
	var h uintptr
	var disp uint32
	r, _, _ := procRegCreateKeyExW.Call(root,
		uintptr(unsafe.Pointer(utf16Ptr(path))), 0, 0, 0,
		uintptr(keyRead|keyWrite), 0,
		uintptr(unsafe.Pointer(&h)), uintptr(unsafe.Pointer(&disp)))
	if r != errorSuccess {
		return 0, fmt.Errorf("RegCreateKeyEx(%s): %v", path, syscall.Errno(r))
	}
	return regKey(h), nil
}

func (k regKey) close() {
	procRegCloseKey.Call(uintptr(k))
}

// query 读取原始数据；值不存在时返回 errNotFound。
func (k regKey) query(name string) (typ uint32, data []byte, err error) {
	namep := utf16Ptr(name)
	buf := make([]byte, 1024)
	for {
		size := uint32(len(buf))
		r, _, _ := procRegQueryValueExW.Call(uintptr(k),
			uintptr(unsafe.Pointer(namep)), 0,
			uintptr(unsafe.Pointer(&typ)),
			uintptr(unsafe.Pointer(&buf[0])),
			uintptr(unsafe.Pointer(&size)))
		switch r {
		case errorSuccess:
			return typ, buf[:size], nil
		case errorMoreData:
			buf = make([]byte, size+2)
			continue
		case errorFileNotFound:
			return 0, nil, errNotFound
		default:
			return 0, nil, fmt.Errorf("RegQueryValueEx(%s): %v", name, syscall.Errno(r))
		}
	}
}

func (k regKey) getString(name string) (string, error) {
	typ, data, err := k.query(name)
	if err != nil {
		return "", err
	}
	if typ != regSz && typ != regExpandSz {
		return "", fmt.Errorf("值 %s 不是字符串类型(%d)", name, typ)
	}
	n := len(data) / 2
	u := make([]uint16, n)
	for i := 0; i < n; i++ {
		u[i] = uint16(data[2*i]) | uint16(data[2*i+1])<<8
	}
	return syscall.UTF16ToString(u), nil
}

func (k regKey) getDWORD(name string) (uint32, error) {
	typ, data, err := k.query(name)
	if err != nil {
		return 0, err
	}
	if typ != regDword || len(data) < 4 {
		return 0, fmt.Errorf("值 %s 不是 DWORD 类型(%d)", name, typ)
	}
	return uint32(data[0]) | uint32(data[1])<<8 | uint32(data[2])<<16 | uint32(data[3])<<24, nil
}

func (k regKey) setString(name, value string) error {
	u := syscall.StringToUTF16(replaceNUL(value)) // 含结尾 NUL
	r, _, _ := procRegSetValueExW.Call(uintptr(k),
		uintptr(unsafe.Pointer(utf16Ptr(name))), 0, regSz,
		uintptr(unsafe.Pointer(&u[0])), uintptr(len(u)*2))
	if r != errorSuccess {
		return fmt.Errorf("RegSetValueEx(%s): %v", name, syscall.Errno(r))
	}
	return nil
}

func (k regKey) setDWORD(name string, value uint32) error {
	r, _, _ := procRegSetValueExW.Call(uintptr(k),
		uintptr(unsafe.Pointer(utf16Ptr(name))), 0, regDword,
		uintptr(unsafe.Pointer(&value)), 4)
	if r != errorSuccess {
		return fmt.Errorf("RegSetValueEx(%s): %v", name, syscall.Errno(r))
	}
	return nil
}

// deleteValue 删除一个值；值本来就不存在不算错误。
func (k regKey) deleteValue(name string) error {
	r, _, _ := procRegDeleteValueW.Call(uintptr(k), uintptr(unsafe.Pointer(utf16Ptr(name))))
	if r != errorSuccess && r != errorFileNotFound {
		return fmt.Errorf("RegDeleteValue(%s): %v", name, syscall.Errno(r))
	}
	return nil
}
