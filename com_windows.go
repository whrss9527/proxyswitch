//go:build windows

package main

import (
	"fmt"
	"syscall"
	"unsafe"
)

// 调用 COM / WinRT 接口的小工具：接口指针的第一个字是虚函数表，方法按表里的位置调用。
// 0~2 是 IUnknown 的方法（QueryInterface、AddRef、Release），WinRT 接口的 3~5 是 IInspectable 的。

const methodRelease = 2

type guid struct {
	data1 uint32
	data2 uint16
	data3 uint16
	data4 [8]byte
}

// comCall 调用接口 object 的虚函数表里第 index 个方法，返回的 HRESULT 为负时是错误。
//
//go:uintptrescapes
func comCall(object unsafe.Pointer, index int, args ...uintptr) error {
	vtable := *(*unsafe.Pointer)(object)
	method := *(*uintptr)(unsafe.Add(vtable, index*int(unsafe.Sizeof(uintptr(0)))))
	result, _, _ := syscall.SyscallN(method, append([]uintptr{uintptr(object)}, args...)...)
	return hresultError(result)
}

func comRelease(object unsafe.Pointer) {
	if object != nil {
		_ = comCall(object, methodRelease)
	}
}

// comQuery 从 object 取得 iid 接口（QueryInterface）。
func comQuery(object unsafe.Pointer, iid *guid) (unsafe.Pointer, error) {
	var result unsafe.Pointer
	if err := comCall(object, 0, uintptr(unsafe.Pointer(iid)), uintptr(unsafe.Pointer(&result))); err != nil {
		return nil, err
	}
	return result, nil
}

func hresultError(result uintptr) error {
	if int32(result) >= 0 {
		return nil
	}
	return fmt.Errorf("%v（0x%08X）", syscall.Errno(uint32(result)), uint32(result))
}
