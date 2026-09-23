//go:build windows

package main

import (
	"encoding/binary"
	"errors"
	"fmt"
	"log/slog"
	"runtime"
	"strings"
	"unicode/utf16"
	"unsafe"
)

// Windows 系统代理（设置 → 网络和 Internet → 代理）的读写。
// 用 WinINET 的按连接设置接口：它会同时更新注册表里的 ProxyEnable 等值和系统设置界面读取的
// DefaultConnectionSettings。拨号和 VPN 连接各有一份代理设置（连着 VPN 时用的是 VPN 自己的那份），这里一并设置。
// 接口调用失败时退回直接写注册表。

const internetSettingsKey = `Software\Microsoft\Windows\CurrentVersion\Internet Settings`

const (
	internetOptionRefresh              = 37
	internetOptionSettingsChanged      = 39
	internetOptionPerConnectionOption  = 75
	internetOptionProxySettingsChanged = 95

	perConnectionFlags         = 1
	perConnectionProxyServer   = 2
	perConnectionProxyBypass   = 3
	perConnectionAutoConfigUrl = 4
	perConnectionFlagsUi       = 10

	proxyTypeDirect       = 0x1
	proxyTypeProxy        = 0x2
	proxyTypeAutoProxyUrl = 0x4
	proxyTypeAutoDetect   = 0x8
)

// perConnectionOption 对应 INTERNET_PER_CONN_OPTIONW，value 是 DWORD 或字符串指针的联合体。
type perConnectionOption struct {
	option  uint32
	padding uint32
	value   [8]byte
}

func (option *perConnectionOption) setDword(value uint32) {
	binary.LittleEndian.PutUint32(option.value[:4], value)
}

func (option *perConnectionOption) dword() uint32 {
	return binary.LittleEndian.Uint32(option.value[:4])
}

func (option *perConnectionOption) setString(pointer *uint16) {
	*(**uint16)(unsafe.Pointer(&option.value[0])) = pointer
}

func (option *perConnectionOption) stringPointer() *uint16 {
	return *(**uint16)(unsafe.Pointer(&option.value[0]))
}

type perConnectionOptionList struct {
	size        uint32
	connection  *uint16
	count       uint32
	errorOption uint32
	options     *perConnectionOption
}

func queryConnectionOptions(connection *uint16, options []perConnectionOption) error {
	list := perConnectionOptionList{connection: connection, count: uint32(len(options)), options: &options[0]}
	list.size = uint32(unsafe.Sizeof(list))
	size := list.size
	result, _, err := procInternetQueryOptionW.Call(0, internetOptionPerConnectionOption, uintptr(unsafe.Pointer(&list)), uintptr(unsafe.Pointer(&size)))
	runtime.KeepAlive(options)
	if result == 0 {
		return fmt.Errorf("InternetQueryOption：%v", err)
	}
	return nil
}

// queryConnectionFlags 读取代理类型标志。优先读 FLAGS_UI（与系统设置界面一致），不支持时退回 FLAGS，
// fromUi 表示读的是 FLAGS_UI。
func queryConnectionFlags(connection *uint16) (flags uint32, fromUi bool, err error) {
	for _, optionId := range []uint32{perConnectionFlagsUi, perConnectionFlags} {
		options := []perConnectionOption{{option: optionId}}
		if err = queryConnectionOptions(connection, options); err != nil {
			continue
		}
		// 正常情况下至少带 PROXY_TYPE_DIRECT，读到 0 说明这个选项不被支持。
		if flags = options[0].dword(); flags != 0 {
			return flags, optionId == perConnectionFlagsUi, nil
		}
	}
	if err == nil {
		err = errors.New("读不到代理类型")
	}
	return 0, false, err
}

func queryConnectionStrings(connection *uint16) (server, bypass, pac string, err error) {
	options := []perConnectionOption{{option: perConnectionProxyServer}, {option: perConnectionProxyBypass}, {option: perConnectionAutoConfigUrl}}
	if err := queryConnectionOptions(connection, options); err != nil {
		return "", "", "", err
	}
	values := make([]string, len(options))
	for index := range options {
		pointer := options[index].stringPointer()
		if pointer == nil {
			continue
		}
		values[index] = utf16PointerToString(pointer)
		procGlobalFree.Call(uintptr(unsafe.Pointer(pointer)))
	}
	return values[0], values[1], values[2], nil
}

// readSystemProxy 读取当前的系统代理设置（局域网连接），source 说明读取方式。
// 不支持 FLAGS_UI 的实现（很老的系统或兼容层）读不到 PAC，用注册表里的值补上。
func readSystemProxy() (state SystemProxyState, source string, err error) {
	flags, fromUi, flagsErr := queryConnectionFlags(nil)
	server, bypass, pac, stringsErr := queryConnectionStrings(nil)
	if flagsErr != nil || stringsErr != nil {
		state, err = readSystemProxyFromRegistry()
		return state, "注册表", err
	}
	server, pac = strings.TrimSpace(server), strings.TrimSpace(pac)
	state = SystemProxyState{
		ProxyEnabled: flags&proxyTypeProxy != 0 && server != "",
		PacEnabled:   flags&proxyTypeAutoProxyUrl != 0 && pac != "",
		AutoDetect:   flags&proxyTypeAutoDetect != 0,
		Server:       server,
		Bypass:       bypass,
		Pac:          pac,
	}
	if fromUi {
		return state, "WinINET", nil
	}
	if registry, err := readSystemProxyFromRegistry(); err == nil {
		state.PacEnabled, state.Pac = registry.PacEnabled, registry.Pac
		if state.Bypass == "" {
			state.Bypass = registry.Bypass
		}
	}
	return state, "WinINET + 注册表", nil
}

func readSystemProxyFromRegistry() (SystemProxyState, error) {
	var state SystemProxyState
	key, err := openRegistryKey(hkeyCurrentUser, internetSettingsKey, keyRead)
	if err != nil {
		return state, err
	}
	defer key.Close()
	enabled, _ := key.Dword("ProxyEnable")
	state.Server, _ = key.String("ProxyServer")
	state.Bypass, _ = key.String("ProxyOverride")
	state.Pac, _ = key.String("AutoConfigURL")
	state.Server, state.Pac = strings.TrimSpace(state.Server), strings.TrimSpace(state.Pac)
	state.ProxyEnabled = enabled != 0 && state.Server != ""
	state.PacEnabled = state.Pac != ""
	if connections, err := openRegistryKey(hkeyCurrentUser, internetSettingsKey+`\Connections`, keyRead); err == nil {
		// DefaultConnectionSettings 第 8~11 字节是代理类型标志。
		if _, data, err := connections.query("DefaultConnectionSettings"); err == nil && len(data) >= 12 {
			state.AutoDetect = binary.LittleEndian.Uint32(data[8:12])&proxyTypeAutoDetect != 0
		}
		connections.Close()
	}
	return state, nil
}

// writeSystemProxy 把代理设置写入局域网连接和所有拨号 / VPN 连接，通知系统刷新，再读回确认已经生效。
func writeSystemProxy(state SystemProxyState) error {
	lanErr := setConnectionProxy(nil, state)
	for _, name := range rasEntryNames() {
		if err := setConnectionProxy(utf16Pointer(name), state); err != nil {
			slog.Warn("设置拨号 / VPN 连接的代理失败", "connection", name, "err", err)
		}
	}
	// 接口失败，或实现不完整（不支持 FLAGS_UI）时，同时直接写注册表。
	if _, fromUi, _ := queryConnectionFlags(nil); lanErr != nil || !fromUi {
		if lanErr != nil {
			slog.Warn("WinINET 设置代理失败，改为直接写注册表", "err", lanErr)
		}
		if err := writeSystemProxyToRegistry(state); err != nil {
			return err
		}
	}
	refreshInternetSettings()
	current, _, err := readSystemProxy()
	if err == nil && !systemProxyMatches(current, state) {
		slog.Warn("系统代理设置没有生效", "wanted", state, "actual", current, "machine_policy", machineWideProxy())
		if machineWideProxy() {
			return errors.New("组策略设置了按计算机统一设置代理，当前用户的代理设置不会生效")
		}
		return errors.New("写入后读回的设置不一致，可能被组策略或安全软件锁定")
	}
	return nil
}

// systemProxyMatches 比较是否生效：开关状态一致，开启的那一项地址一致。
func systemProxyMatches(actual, wanted SystemProxyState) bool {
	if actual.ProxyEnabled != wanted.ProxyEnabled || actual.PacEnabled != wanted.PacEnabled {
		return false
	}
	if wanted.ProxyEnabled && !strings.EqualFold(strings.TrimSpace(actual.Server), strings.TrimSpace(wanted.Server)) {
		return false
	}
	if wanted.PacEnabled && !strings.EqualFold(strings.TrimSpace(actual.Pac), strings.TrimSpace(wanted.Pac)) {
		return false
	}
	return true
}

func setConnectionProxy(connection *uint16, state SystemProxyState) error {
	flags := uint32(proxyTypeDirect)
	if state.ProxyEnabled {
		flags |= proxyTypeProxy
	}
	if state.PacEnabled {
		flags |= proxyTypeAutoProxyUrl
	}
	if state.AutoDetect {
		flags |= proxyTypeAutoDetect
	}
	server := utf16Pointer(state.Server)
	bypass := utf16Pointer(state.Bypass)
	// 不用 PAC 时传 NULL：部分实现把非空的地址当作已启用。
	var pac *uint16
	if state.PacEnabled {
		pac = utf16Pointer(state.Pac)
	}
	options := []perConnectionOption{{option: perConnectionFlags}, {option: perConnectionProxyServer}, {option: perConnectionProxyBypass}, {option: perConnectionAutoConfigUrl}}
	options[0].setDword(flags)
	options[1].setString(server)
	options[2].setString(bypass)
	options[3].setString(pac)
	list := perConnectionOptionList{connection: connection, count: uint32(len(options)), options: &options[0]}
	list.size = uint32(unsafe.Sizeof(list))
	result, _, err := procInternetSetOptionW.Call(0, internetOptionPerConnectionOption, uintptr(unsafe.Pointer(&list)), uintptr(list.size))
	runtime.KeepAlive(server)
	runtime.KeepAlive(bypass)
	runtime.KeepAlive(pac)
	runtime.KeepAlive(options)
	runtime.KeepAlive(connection)
	if result == 0 {
		return fmt.Errorf("InternetSetOption：%v", err)
	}
	return nil
}

func writeSystemProxyToRegistry(state SystemProxyState) error {
	key, err := createRegistryKey(hkeyCurrentUser, internetSettingsKey)
	if err != nil {
		return err
	}
	defer key.Close()
	enabled := uint32(0)
	if state.ProxyEnabled {
		enabled = 1
	}
	var failures []string
	for _, err := range []error{
		key.SetDword("ProxyEnable", enabled),
		key.SetString("ProxyServer", state.Server),
		key.SetString("ProxyOverride", state.Bypass),
	} {
		if err != nil {
			failures = append(failures, err.Error())
		}
	}
	if state.PacEnabled {
		err = key.SetString("AutoConfigURL", state.Pac)
	} else {
		err = key.DeleteValue("AutoConfigURL")
	}
	if err != nil {
		failures = append(failures, err.Error())
	}
	if len(failures) > 0 {
		return errors.New(strings.Join(failures, "；"))
	}
	return nil
}

// refreshInternetSettings 通知系统代理设置已变更，让浏览器等程序立即生效。
func refreshInternetSettings() {
	procInternetSetOptionW.Call(0, internetOptionSettingsChanged, 0, 0)
	procInternetSetOptionW.Call(0, internetOptionProxySettingsChanged, 0, 0)
	procInternetSetOptionW.Call(0, internetOptionRefresh, 0, 0)
}

// rasEntryNameSize 是 RASENTRYNAMEW 的大小：dwSize + szEntryName[257] + dwFlags + szPhonebookPath[261]。
const rasEntryNameSize = 1048

// rasEntryNames 返回本机所有拨号 / VPN 连接的名字。
func rasEntryNames() []string {
	if procRasEnumEntriesW.Find() != nil {
		return nil
	}
	size := uint32(rasEntryNameSize)
	count := uint32(0)
	buffer := make([]byte, rasEntryNameSize)
	binary.LittleEndian.PutUint32(buffer[0:4], rasEntryNameSize)
	result, _, _ := procRasEnumEntriesW.Call(0, 0, uintptr(unsafe.Pointer(&buffer[0])), uintptr(unsafe.Pointer(&size)), uintptr(unsafe.Pointer(&count)))
	if result == errorBufferTooSmall && size > rasEntryNameSize {
		buffer = make([]byte, size)
		binary.LittleEndian.PutUint32(buffer[0:4], rasEntryNameSize)
		result, _, _ = procRasEnumEntriesW.Call(0, 0, uintptr(unsafe.Pointer(&buffer[0])), uintptr(unsafe.Pointer(&size)), uintptr(unsafe.Pointer(&count)))
	}
	if result != errorSuccess {
		return nil
	}
	var names []string
	for index := 0; index < int(count) && (index+1)*rasEntryNameSize <= len(buffer); index++ {
		entry := buffer[index*rasEntryNameSize+4 : index*rasEntryNameSize+4+514]
		name := decodeUtf16Bytes(entry)
		if name != "" {
			names = append(names, name)
		}
	}
	return names
}

// decodeUtf16Bytes 解码小端 UTF-16 字节，遇到 NUL 截止。
func decodeUtf16Bytes(data []byte) string {
	encoded := make([]uint16, 0, len(data)/2)
	for index := 0; index+1 < len(data); index += 2 {
		char := binary.LittleEndian.Uint16(data[index:])
		if char == 0 {
			break
		}
		encoded = append(encoded, char)
	}
	return string(utf16.Decode(encoded))
}

// machineWideProxy 表示组策略设置了“按计算机而不是按用户设置代理”，此时修改当前用户的代理不会生效。
func machineWideProxy() bool {
	value, found := readRegistryDword(hkeyLocalMachine, `SOFTWARE\Policies\Microsoft\Windows\CurrentVersion\Internet Settings`, "ProxySettingsPerUser")
	return found && value == 0
}
