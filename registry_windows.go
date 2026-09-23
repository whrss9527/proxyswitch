//go:build windows

package main

import (
	"errors"
	"fmt"
	"syscall"
	"unsafe"
)

// 极简注册表封装，只用到读写字符串和 DWORD。

type registryKey uintptr

var errValueNotFound = errors.New("注册表值不存在")

func openRegistryKey(root uintptr, path string, access uint32) (registryKey, error) {
	var handle uintptr
	result, _, _ := procRegOpenKeyExW.Call(root, uintptr(unsafe.Pointer(utf16Pointer(path))), 0, uintptr(access), uintptr(unsafe.Pointer(&handle)))
	if result != errorSuccess {
		return 0, fmt.Errorf("打开注册表 %s 失败：%v", path, syscall.Errno(result))
	}
	return registryKey(handle), nil
}

// createRegistryKey 打开（不存在则创建）一个可读写的键。
func createRegistryKey(root uintptr, path string) (registryKey, error) {
	var handle uintptr
	var disposition uint32
	result, _, _ := procRegCreateKeyExW.Call(root, uintptr(unsafe.Pointer(utf16Pointer(path))), 0, 0, 0,
		uintptr(keyRead|keyWrite), 0, uintptr(unsafe.Pointer(&handle)), uintptr(unsafe.Pointer(&disposition)))
	if result != errorSuccess {
		return 0, fmt.Errorf("打开注册表 %s 失败：%v", path, syscall.Errno(result))
	}
	return registryKey(handle), nil
}

func (key registryKey) Close() {
	procRegCloseKey.Call(uintptr(key))
}

func (key registryKey) query(name string) (valueType uint32, data []byte, err error) {
	namePointer := utf16Pointer(name)
	buffer := make([]byte, 1024)
	for {
		size := uint32(len(buffer))
		result, _, _ := procRegQueryValueExW.Call(uintptr(key), uintptr(unsafe.Pointer(namePointer)), 0,
			uintptr(unsafe.Pointer(&valueType)), uintptr(unsafe.Pointer(&buffer[0])), uintptr(unsafe.Pointer(&size)))
		switch result {
		case errorSuccess:
			return valueType, buffer[:size], nil
		case errorMoreData:
			buffer = make([]byte, size+2)
		case errorFileNotFound:
			return 0, nil, errValueNotFound
		default:
			return 0, nil, fmt.Errorf("读取注册表值 %s 失败：%v", name, syscall.Errno(result))
		}
	}
}

func (key registryKey) String(name string) (string, error) {
	valueType, data, err := key.query(name)
	if err != nil {
		return "", err
	}
	if valueType != regSz && valueType != regExpandSz {
		return "", fmt.Errorf("注册表值 %s 不是字符串", name)
	}
	encoded := make([]uint16, len(data)/2)
	for index := range encoded {
		encoded[index] = uint16(data[2*index]) | uint16(data[2*index+1])<<8
	}
	return syscall.UTF16ToString(encoded), nil
}

func (key registryKey) Dword(name string) (uint32, error) {
	valueType, data, err := key.query(name)
	if err != nil {
		return 0, err
	}
	if valueType != regDword || len(data) < 4 {
		return 0, fmt.Errorf("注册表值 %s 不是 DWORD", name)
	}
	return uint32(data[0]) | uint32(data[1])<<8 | uint32(data[2])<<16 | uint32(data[3])<<24, nil
}

func (key registryKey) SetString(name, value string) error {
	encoded := syscall.StringToUTF16(replaceNul(value))
	result, _, _ := procRegSetValueExW.Call(uintptr(key), uintptr(unsafe.Pointer(utf16Pointer(name))), 0, regSz,
		uintptr(unsafe.Pointer(&encoded[0])), uintptr(len(encoded)*2))
	if result != errorSuccess {
		return fmt.Errorf("写入注册表值 %s 失败：%v", name, syscall.Errno(result))
	}
	return nil
}

func (key registryKey) SetDword(name string, value uint32) error {
	result, _, _ := procRegSetValueExW.Call(uintptr(key), uintptr(unsafe.Pointer(utf16Pointer(name))), 0, regDword,
		uintptr(unsafe.Pointer(&value)), 4)
	if result != errorSuccess {
		return fmt.Errorf("写入注册表值 %s 失败：%v", name, syscall.Errno(result))
	}
	return nil
}

// DeleteValue 删除一个值，值本来就不存在不算错误。
func (key registryKey) DeleteValue(name string) error {
	result, _, _ := procRegDeleteValueW.Call(uintptr(key), uintptr(unsafe.Pointer(utf16Pointer(name))))
	if result != errorSuccess && result != errorFileNotFound {
		return fmt.Errorf("删除注册表值 %s 失败：%v", name, syscall.Errno(result))
	}
	return nil
}

// SubkeyNames 列出直接子键的名字。
func (key registryKey) SubkeyNames() []string {
	var names []string
	for index := uint32(0); ; index++ {
		buffer := make([]uint16, 256)
		length := uint32(len(buffer))
		result, _, _ := procRegEnumKeyExW.Call(uintptr(key), uintptr(index), uintptr(unsafe.Pointer(&buffer[0])), uintptr(unsafe.Pointer(&length)), 0, 0, 0, 0)
		if result != errorSuccess {
			return names
		}
		names = append(names, syscall.UTF16ToString(buffer[:length]))
	}
}

// Binary 读取二进制值。
func (key registryKey) Binary(name string) ([]byte, error) {
	_, data, err := key.query(name)
	return data, err
}

// readRegistryString 读取单个字符串值，出错时返回空串。
func readRegistryString(root uintptr, path, name string) string {
	key, err := openRegistryKey(root, path, keyRead)
	if err != nil {
		return ""
	}
	defer key.Close()
	value, _ := key.String(name)
	return value
}

// readRegistryDword 读取单个 DWORD 值。
func readRegistryDword(root uintptr, path, name string) (uint32, bool) {
	key, err := openRegistryKey(root, path, keyRead)
	if err != nil {
		return 0, false
	}
	defer key.Close()
	value, err := key.Dword(name)
	return value, err == nil
}
