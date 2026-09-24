//go:build windows

package main

import (
	"fmt"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"time"
)

// 在程序里更新：下载新版本放到 exe 旁边，把正在运行的 exe 改名为 .old（Windows 允许给运行中的程序改名），
// 新版本放到原来的位置后启动它，当前进程随即退出。新版本等旧进程退出后才开始运行，并删除 .old。

const updatedFromArgument = "--updated-from="

// InstallUpdate 下载并安装新版本，成功后片刻内退出，由新版本接着运行并打开设置页。
func (app *App) InstallUpdate(progress func(received, total int64)) error {
	executable, err := os.Executable()
	if err != nil {
		return err
	}
	if resolved, err := filepath.EvalSymlinks(executable); err == nil {
		executable = resolved
	}
	downloaded := executable + ".download"
	info, err := downloadLatestRelease(app.ActiveProxyUrl(), downloaded, progress)
	if err != nil {
		return err
	}
	previous, err := replaceExecutable(executable, downloaded)
	if err != nil {
		_ = os.Remove(downloaded)
		return err
	}
	command := exec.Command(executable, updatedFromArgument+strconv.Itoa(os.Getpid()))
	if err := command.Start(); err != nil {
		_ = os.Remove(executable)
		_ = os.Rename(previous, executable)
		return fmt.Errorf("无法启动新版本：%v", err)
	}
	_ = command.Process.Release()
	slog.Info("新版本已下载，正在重新启动", "version", info.Latest, "path", executable)
	go func() {
		// 稍等一下，让设置页先收到结果。UI 线程忙时稍后重试，新版本在等这个进程退出。
		time.Sleep(time.Second)
		for app.tray.RunOnUi(func() {
			// 重新启动不算退出，不按 disable_on_exit 关闭代理。
			app.exitHandled = true
			app.tray.Quit()
		}) != nil {
			time.Sleep(time.Second)
		}
	}()
	return nil
}

// replaceExecutable 把正在运行的程序改名，再把下载的新版本放到原来的位置，返回旧程序现在的路径。
func replaceExecutable(executable, downloaded string) (string, error) {
	previous := executable + ".old"
	if err := os.Remove(previous); err != nil && !os.IsNotExist(err) {
		// 上次更新留下的旧程序还删不掉，换一个名字。
		previous = fmt.Sprintf("%s.%d.old", executable, time.Now().Unix())
	}
	if err := renameWithRetry(executable, previous); err != nil {
		return "", fmt.Errorf("无法替换程序文件，请到发布页手动下载（%v）", err)
	}
	if err := renameWithRetry(downloaded, executable); err != nil {
		_ = os.Rename(previous, executable)
		return "", fmt.Errorf("无法替换程序文件，请到发布页手动下载（%v）", err)
	}
	return previous, nil
}

// renameWithRetry 改名失败时稍等重试：杀毒软件扫描刚下载的程序时会短暂占用文件。
func renameWithRetry(source, destination string) error {
	var err error
	for attempt := 0; attempt < 10; attempt++ {
		if err = os.Rename(source, destination); err == nil {
			return nil
		}
		time.Sleep(300 * time.Millisecond)
	}
	return err
}

// finishUpdate 在新版本启动时调用：等旧版本进程退出（它还占着单实例互斥量），再删除更新留下的文件。
func finishUpdate(previousPid int) {
	if handle, _, _ := procOpenProcess.Call(synchronize, 0, uintptr(previousPid)); handle != 0 {
		procWaitForSingleObject.Call(handle, 15000)
		procCloseHandle.Call(handle)
	}
	executable, err := os.Executable()
	if err != nil {
		return
	}
	// 除了 .old，还可能有改名时加了时间戳的旧程序和没下载完的文件。
	leftovers, _ := filepath.Glob(executable + ".*.old")
	leftovers = append(leftovers, executable+".old", executable+".download")
	for _, path := range leftovers {
		// 旧进程刚退出时文件可能还没释放，重试几次。
		for attempt := 0; attempt < 10; attempt++ {
			if err := os.Remove(path); err == nil || os.IsNotExist(err) {
				break
			}
			time.Sleep(200 * time.Millisecond)
		}
	}
}

// watchUpdates 每小时看一次是否该自动检查更新（每天最多检查一次），发现新版本时提示一次。
func (app *App) watchUpdates() {
	// 开机自启时网络可能还没就绪，稍等再查。
	time.Sleep(2 * time.Minute)
	for {
		due := false
		_ = app.tray.RunOnUi(func() { due = app.engine.UpdateCheckDue() })
		if due {
			info, err := checkLatestRelease(app.ActiveProxyUrl())
			if err != nil {
				slog.Warn("自动检查更新失败", "err", err)
			}
			_ = app.tray.RunOnUi(func() {
				if err == nil && info.Newer {
					app.latestUpdate = &info
				}
				if app.engine.RecordUpdateCheck(info, err) {
					text := "点这里查看更新内容"
					if info.CanInstall {
						text += "，可以在设置里一键更新"
					}
					app.notify(Notice{Level: noticeInfo, Title: "发现新版本 " + info.Latest, Text: text, Icon: iconStateOn, Color: profilePalette[1], Page: "about"})
				}
			})
		}
		time.Sleep(time.Hour)
	}
}
