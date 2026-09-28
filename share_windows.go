//go:build windows

package main

import (
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"time"
	"unsafe"
)

// 局域网共享在 Windows 上的部分：共享期间阻止睡眠、让防火墙放行内核、托盘菜单和命令行的开关。

var (
	procSetThreadExecutionState = kernel32.NewProc("SetThreadExecutionState")
	procGetSystemPowerStatus    = kernel32.NewProc("GetSystemPowerStatus")
)

const (
	esContinuous     = 0x80000000
	esSystemRequired = 0x00000001

	shareFirewallRule    = "ProxySwitch 局域网共享"
	shareFirewallTimeout = time.Minute
)

type systemPowerStatus struct {
	acLineStatus        byte
	batteryFlag         byte
	batteryLifePercent  byte
	systemStatusFlag    byte
	batteryLifeTime     uint32
	batteryFullLifeTime uint32
}

// onBatteryPower 表示现在用电池供电。台式机、读不到电源状态时算接着电源。
func onBatteryPower() bool {
	var status systemPowerStatus
	if result, _, _ := procGetSystemPowerStatus.Call(uintptr(unsafe.Pointer(&status))); result == 0 {
		return false
	}
	return status.acLineStatus == 0
}

// sleepGuard 在局域网共享期间阻止电脑自动睡眠（显示器照常可以关），插拔电源后在下一次刷新时重新决定。
// SetThreadExecutionState 的效果跟着调用它的线程，所以只在一直存在的 UI 线程上用。
type sleepGuard struct {
	holding bool
	status  string
}

// update 按共享设置和电源决定是否阻止睡眠，返回状态（见 shareAwake）。
func (guard *sleepGuard) update(share ShareConfig) string {
	status := shareAwake(share, onBatteryPower())
	if hold := status == "holding"; hold != guard.holding {
		flags := uintptr(esContinuous)
		if hold {
			flags |= esSystemRequired
		}
		procSetThreadExecutionState.Call(flags)
		guard.holding = hold
		if hold {
			slog.Info("局域网共享期间保持唤醒：已阻止自动睡眠")
		} else {
			slog.Info("不再阻止睡眠", "status", status)
		}
	}
	guard.status = status
	return status
}

// allowFirewall 让 Windows 防火墙放行 program 的传入连接：先删掉针对它的入站规则（包括第一次询问时点了「取消」
// 自动加上的阻止规则，阻止规则比允许规则优先），再加一条允许规则，公用和专用网络都生效。
func allowFirewall(program string) error {
	if strings.ContainsAny(program, `"%`) {
		return fmt.Errorf("内核的路径里有特殊字符，请手动在 Windows 防火墙里允许：%s", program)
	}
	parameters := fmt.Sprintf(`/c netsh advfirewall firewall delete rule name=all dir=in program="%[1]s" & netsh advfirewall firewall add rule name="%[2]s" dir=in action=allow program="%[1]s" enable=yes profile=any`, program, shareFirewallRule)
	if err := runElevated(systemDir()+`\cmd.exe`, parameters, shareFirewallTimeout); err != nil {
		var exit elevatedExitError
		switch {
		case errors.As(err, &exit):
			return fmt.Errorf("netsh 没有执行成功（退出码 %d）", exit.code)
		case errors.Is(err, errElevatedTimeout):
			return errors.New("等待超时，请稍后在 Windows 防火墙设置里检查")
		}
		return err
	}
	slog.Info("已在 Windows 防火墙里放行内核", "program", program)
	return nil
}

// ---------- App ----------

// AllowShareFirewall 让 Windows 防火墙放行内核，局域网里的设备才连得上共享入口。
func (app *App) AllowShareFirewall() error {
	var program string
	if err := app.tray.RunOnUi(func() { program = app.engine.coreBinary() }); err != nil {
		return err
	}
	if !fileExists(program) {
		return errCoreMissing
	}
	return allowFirewall(program)
}

// SetShare 是设置页的开关：等内核开好共享入口，再立即更新托盘和防睡眠。
func (app *App) SetShare(enabled bool) error {
	err := app.subscriptionService.SetShare(enabled)
	_ = app.tray.RunOnUi(app.refresh)
	return err
}

// setShareFromUi 是托盘菜单和命令行的开关，在 UI 线程上调用，不等内核：入口没开起来时 updateShare 会提示。
func (app *App) setShareFromUi(enabled bool) error {
	if err := app.engine.SetShareEnabled(enabled); err != nil {
		app.notify(Notice{Level: noticeError, Title: "局域网共享没有打开", Text: err.Error(), Page: "share"})
		return err
	}
	if enabled {
		app.notify(Notice{Level: noticeInfo, Title: "局域网共享已开启", Text: app.shareAddressText(), Page: "share"})
	} else {
		app.notify(Notice{Level: noticeInfo, Title: "局域网共享已关闭", Text: "设备上填的代理记得改回来，不然它们上不了网"})
	}
	app.refresh()
	return nil
}

// shareAddress 是局域网里的设备要填的代理服务器地址，这台电脑没连上局域网时为空。
func (app *App) shareAddress() string {
	config := app.engine.Config()
	if config == nil {
		return ""
	}
	if addresses := localAddresses(); len(addresses) > 0 {
		return fmt.Sprintf("%s:%d", addresses[0].Ip, config.Share.Port)
	}
	return ""
}

func (app *App) shareAddressText() string {
	address := app.shareAddress()
	if address == "" {
		return "这台电脑现在没有连上局域网"
	}
	host, port, _ := strings.Cut(address, ":")
	return fmt.Sprintf("在 PS5 / Switch 上把代理服务器设为 %s，端口 %s", host, port)
}

// copyShareAddress 把设备上要填的地址复制到剪贴板。
func (app *App) copyShareAddress() {
	address := app.shareAddress()
	if address == "" {
		app.notify(Notice{Level: noticeWarning, Title: "没有局域网地址", Text: "这台电脑现在没有连上局域网"})
		return
	}
	if err := setClipboardText(app.tray.window, address); err != nil {
		app.notify(Notice{Level: noticeError, Title: "复制失败", Text: err.Error()})
		return
	}
	app.notify(Notice{Level: noticeInfo, Title: "已复制 " + address, Text: app.shareAddressText(), Page: "share"})
}

// updateShare 在每次刷新托盘时调用：本机的代理被其他程序改了之后让共享跟着变，按设置阻止睡眠，
// 共享入口没开起来（例如端口被占用）时提示一次。
func (app *App) updateShare(status Status) {
	share := ShareConfig{}
	if config := app.engine.Config(); config != nil && app.subscriptionService != nil {
		share = config.Share
	}
	app.sleep.update(share)
	if app.subscriptionService == nil {
		return
	}
	app.engine.RefreshCore(status)
	shareError := ""
	if share.Enabled {
		shareError = app.core.Status().Share.Error
	}
	if shareError != app.shareError {
		app.shareError = shareError
		if shareError != "" {
			app.notify(Notice{Level: noticeWarning, Title: "局域网共享没有开起来", Text: shareError, Page: "share"})
		}
	}
}

// shareCommand 执行命令行的 share on / off / toggle（不写表示切换）。
func (app *App) shareCommand(argument string) error {
	config := app.engine.Config()
	if config == nil {
		return errNoConfig
	}
	enabled, err := shareCommandTarget(argument, config.Share.Enabled)
	if err != nil {
		return err
	}
	return app.setShareFromUi(enabled)
}
