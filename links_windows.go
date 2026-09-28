//go:build windows

package main

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
)

// 登记链接协议：写在 HKCU\Software\Classes\<协议> 下，只对当前用户生效，不需要管理员权限。
// clash:// 已经由其他程序（Clash Verge 等）处理时不抢；用户要求接管时，把对方登记在当前用户下的命令和图标
// 备份在同一个键里，交还时恢复。

const (
	classesKey      = `Software\Classes\`
	linkBackupValue = "ProxySwitchBackup"
)

// linkOpenCommand 是打开链接时执行的命令：ProxySwitch.exe "链接"。
func linkOpenCommand(executable string) string {
	return `"` + executable + `" "%1"`
}

// commandProgram 取出命令里的程序路径：带引号的取引号里的内容，否则取第一个空格前的部分。
func commandProgram(command string) string {
	command = strings.TrimSpace(command)
	if rest, quoted := strings.CutPrefix(command, `"`); quoted {
		program, _, _ := strings.Cut(rest, `"`)
		return program
	}
	program, _, _ := strings.Cut(command, " ")
	return program
}

func isOurLinkCommand(command, executable string) bool {
	program := commandProgram(command)
	return program != "" && strings.EqualFold(filepath.Clean(program), filepath.Clean(executable))
}

// linkHandler 是现在处理 scheme 链接的命令（当前用户登记的优先，其次是整台电脑的），ours 表示就是这个程序。
func linkHandler(scheme, executable string) (command string, ours bool) {
	command = readRegistryString(hkeyClassesRoot, scheme+`\shell\open\command`, "")
	return command, isOurLinkCommand(command, executable)
}

// handlerName 是命令里程序的名字，例如 clash-verge。
func handlerName(command string) string {
	program := filepath.Base(commandProgram(command))
	return strings.TrimSuffix(program, filepath.Ext(program))
}

// registerLink 把 scheme 链接登记给 executable。当前用户下已经登记了别的程序时，先把它的命令和图标备份起来。
func registerLink(scheme, description, executable string) error {
	root := classesKey + scheme
	key, err := createRegistryKey(hkeyCurrentUser, root)
	if err != nil {
		return err
	}
	defer key.Close()
	if current, _ := key.String(""); current == "" {
		if err := key.SetString("", "URL:"+description); err != nil {
			return err
		}
	}
	if err := key.SetString("URL Protocol", ""); err != nil {
		return err
	}
	for _, item := range []struct{ path, value string }{
		{root + `\DefaultIcon`, `"` + executable + `",0`},
		{root + `\shell\open\command`, linkOpenCommand(executable)},
	} {
		if err := setBackedUpDefault(item.path, item.value, executable); err != nil {
			return err
		}
	}
	return nil
}

// setBackedUpDefault 设置键的默认值；原来的值是别的程序的，并且还没有备份时先备份。
func setBackedUpDefault(path, value, executable string) error {
	key, err := createRegistryKey(hkeyCurrentUser, path)
	if err != nil {
		return err
	}
	defer key.Close()
	current, _ := key.String("")
	if current != "" && current != value && !isOurLinkCommand(current, executable) {
		if _, err := key.String(linkBackupValue); errors.Is(err, errValueNotFound) {
			if err := key.SetString(linkBackupValue, current); err != nil {
				return err
			}
		}
	}
	return key.SetString("", value)
}

// unregisterLink 取消当前用户下 scheme 链接对 executable 的登记：有备份时恢复原来的程序，没有时删除整个键。
// 现在登记的不是这个程序时不动。
func unregisterLink(scheme, executable string) error {
	root := classesKey + scheme
	command := readRegistryString(hkeyCurrentUser, root+`\shell\open\command`, "")
	if !isOurLinkCommand(command, executable) {
		return nil
	}
	backup := readRegistryString(hkeyCurrentUser, root+`\shell\open\command`, linkBackupValue)
	if backup == "" {
		return deleteRegistryTree(hkeyCurrentUser, root)
	}
	for _, path := range []string{root + `\DefaultIcon`, root + `\shell\open\command`} {
		if err := restoreBackedUpDefault(path, executable); err != nil {
			return err
		}
	}
	return nil
}

// restoreBackedUpDefault 把键的默认值恢复成备份的值；没有备份、现在又是这个程序的值时删掉它。
func restoreBackedUpDefault(path, executable string) error {
	key, err := createRegistryKey(hkeyCurrentUser, path)
	if err != nil {
		return err
	}
	defer key.Close()
	previous, err := key.String(linkBackupValue)
	if err != nil {
		if current, _ := key.String(""); isOurLinkCommand(current, executable) {
			return key.DeleteValue("")
		}
		return nil
	}
	if err := key.SetString("", previous); err != nil {
		return err
	}
	return key.DeleteValue(linkBackupValue)
}

// applyLinks 按配置登记或取消链接：开启时 proxyswitch:// 登记给这个程序，clash:// 在没有其他程序处理时也登记。
func applyLinks(enabled bool) error {
	executable, err := os.Executable()
	if err != nil {
		return err
	}
	if !enabled {
		return errors.Join(unregisterLink(linkScheme, executable), unregisterLink(clashLinkScheme, executable))
	}
	err = registerLink(linkScheme, "ProxySwitch", executable)
	if command, ours := linkHandler(clashLinkScheme, executable); command == "" || ours {
		err = errors.Join(err, registerLink(clashLinkScheme, "clash", executable))
	}
	return err
}

// takeOverClashLinks 让 clash:// 链接改由这个程序处理（机场网站的「一键导入 Clash」按钮）。
func takeOverClashLinks() error {
	executable, err := os.Executable()
	if err != nil {
		return err
	}
	return registerLink(clashLinkScheme, "clash", executable)
}

// TakeOverClashLinks 让机场网站的「一键导入 Clash」按钮改由 ProxySwitch 处理。
func (app *App) TakeOverClashLinks() error {
	return takeOverClashLinks()
}

// currentLinks 是设置页显示的链接登记情况。
func currentLinks() LinksInfo {
	executable, _ := os.Executable()
	command, ours := linkHandler(clashLinkScheme, executable)
	info := LinksInfo{ClashOurs: ours}
	if command != "" {
		info.Clash = handlerName(command)
	}
	return info
}
