package main

import (
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"time"
)

// 引擎里与订阅和代理内核有关的部分：按配置和已下载的订阅算出内核应处于的状态并交给内核，
// 决定哪些订阅该下载，记录下载结果，选择节点。与引擎的其他部分一样只在 UI 线程上调用。

// ProxyCore 是订阅使用的代理内核。Windows 和开发模式里是 *Core，测试里是记录设定的假实现。
type ProxyCore interface {
	Sync(settings CoreSettings) int
}

const geoRetryInterval = time.Hour

// coreBinary 是内核程序的位置：配置里指定的，或 ProxySwitch 下载到工作目录的。
func (engine *Engine) coreBinary() string {
	if engine.config != nil && engine.config.Core.Path != "" {
		return engine.config.Core.Path
	}
	return filepath.Join(engine.paths.Core, coreBinaryName())
}

func (engine *Engine) subscriptionPath(profileId string) string {
	return filepath.Join(engine.paths.Core, filepath.FromSlash(subscriptionFile(profileId)))
}

// subscriptionLoaded 表示这个订阅已经按当前地址下载成功，可以交给内核。
func (engine *Engine) subscriptionLoaded(profile *Profile) bool {
	info := engine.state.Subscriptions[profile.Id]
	return info != nil && info.Updated != "" && info.Source == subscriptionSource(profile.Subscription) && fileExists(engine.subscriptionPath(profile.Id))
}

// geoReady 表示大陆直连规则用到的地理数据都已下载。
func (engine *Engine) geoReady() bool {
	for _, geo := range coreGeoFiles {
		if !fileExists(filepath.Join(engine.paths.Core, geo.Name)) {
			return false
		}
	}
	return true
}

// coreSettings 按配置和已下载的订阅算出内核应处于的状态。正在使用的订阅是最近使用的配置（如果它是订阅）。
func (engine *Engine) coreSettings() CoreSettings {
	settings := CoreSettings{Dir: engine.paths.Core, Mode: "rule"}
	if engine.config == nil {
		return settings
	}
	settings.Binary = engine.coreBinary()
	if info, err := os.Stat(settings.Binary); err == nil {
		settings.BinaryStamp = info.ModTime().Format(time.RFC3339Nano)
	}
	settings.Port = engine.config.Core.Port
	settings.TestUrl = engine.config.TestUrl
	settings.GeoReady = engine.geoReady()
	for index := range engine.config.Profiles {
		profile := &engine.config.Profiles[index]
		if profile.IsSubscription() && engine.subscriptionLoaded(profile) {
			settings.Subscriptions = append(settings.Subscriptions, CoreSubscription{Id: profile.Id, Node: profile.Node, Revision: engine.state.Subscriptions[profile.Id].Updated})
		}
	}
	if selected := engine.selectedProfile(); selected != nil && selected.IsSubscription() && engine.subscriptionLoaded(selected) {
		settings.Active, settings.Mode = selected.Id, selected.Mode
	}
	return settings
}

// syncCore 把内核应处于的状态交给内核（异步生效），记下这次设定的序号，调用方可以用它等待生效。
func (engine *Engine) syncCore() {
	if engine.core != nil {
		engine.coreGeneration = engine.core.Sync(engine.coreSettings())
	}
}

// CoreGeneration 是最近一次交给内核的设定的序号。
func (engine *Engine) CoreGeneration() int {
	return engine.coreGeneration
}

// requestDownloads 通知后台尽快检查需要下载的订阅和地理数据。
func (engine *Engine) requestDownloads() {
	if engine.downloadsNeeded != nil {
		engine.downloadsNeeded()
	}
}

// subscriptionProblem 说明订阅配置现在为什么不能开启：内核还没下载，或订阅还没下载成功。
func (engine *Engine) subscriptionProblem(profile *Profile) error {
	if !profile.IsSubscription() {
		return nil
	}
	if engine.core == nil {
		// 命令行在托盘程序没有运行时直接执行：内核会随命令一起退出，不能开启订阅配置。
		return errors.New("使用订阅的配置需要 ProxySwitch 保持运行，请先打开 ProxySwitch")
	}
	if !fileExists(engine.coreBinary()) {
		if engine.config.Core.Path != "" {
			return fmt.Errorf("找不到代理内核：%s", engine.config.Core.Path)
		}
		return errCoreMissing
	}
	if engine.subscriptionLoaded(profile) {
		return nil
	}
	engine.requestDownloads()
	if info := engine.state.Subscriptions[profile.Id]; info != nil && info.Error != "" && info.Source == subscriptionSource(profile.Subscription) {
		return errors.New("订阅没有下载成功：" + info.Error)
	}
	return errors.New("订阅还在下载，请稍后再试")
}

// SubscriptionsDue 返回该下载的订阅：新添加的、地址改了的立即下载；下载成功后每天更新一次，失败后隔一段时间重试。
func (engine *Engine) SubscriptionsDue() []Profile {
	if engine.config == nil {
		return nil
	}
	now := engine.now()
	var due []Profile
	for _, profile := range engine.config.Profiles {
		if profile.IsSubscription() && engine.subscriptionDue(&profile, now) {
			due = append(due, profile)
		}
	}
	return due
}

func (engine *Engine) subscriptionDue(profile *Profile, now time.Time) bool {
	info := engine.state.Subscriptions[profile.Id]
	if info == nil || info.Source != subscriptionSource(profile.Subscription) {
		return true
	}
	attempted, _ := time.Parse(time.RFC3339Nano, info.Attempted)
	retry := now.Sub(attempted) >= subscriptionRetry || attempted.After(now)
	if info.Updated == "" || !fileExists(engine.subscriptionPath(profile.Id)) {
		return retry
	}
	updated, _ := time.Parse(time.RFC3339Nano, info.Updated)
	return (now.Sub(updated) >= subscriptionInterval || updated.After(now)) && retry
}

// DownloadPaths 是下载订阅和地理数据时依次尝试的网络路径：直连，然后是正在使用的代理，最后是内核。
func (engine *Engine) DownloadPaths() []string {
	paths := []string{""}
	add := func(proxyUrl string) {
		if proxyUrl != "" && !containsString(paths, proxyUrl) {
			paths = append(paths, proxyUrl)
		}
	}
	if status := engine.Status(); status.State == statusOn && status.Profile != nil && status.Profile.Server != "" {
		add(serverToUrl(status.Profile.Server))
	}
	if engine.config != nil && len(engine.coreSettings().Subscriptions) > 0 {
		add("http://" + coreServer(engine.config.Core.Port))
	}
	return paths
}

// RecordSubscription 记下一次订阅下载的结果：成功时写入订阅文件，让内核重新读取。address 是下载时的订阅地址，
// 下载期间地址被改掉或配置被删除时忽略这次结果。
func (engine *Engine) RecordSubscription(profileId, address string, result subscriptionDownload, downloadErr error) error {
	if engine.config == nil {
		return errNoConfig
	}
	profile := engine.config.FindProfileById(profileId)
	if profile == nil || profile.Subscription != address {
		return errors.New("订阅地址已经改了")
	}
	if engine.state.Subscriptions == nil {
		engine.state.Subscriptions = map[string]*SubscriptionInfo{}
	}
	info := engine.state.Subscriptions[profileId]
	source := subscriptionSource(address)
	if info == nil || info.Source != source {
		// 新的地址：旧地址下载的内容不再有效。
		_ = os.Remove(engine.subscriptionPath(profileId))
		info = &SubscriptionInfo{Source: source}
		engine.state.Subscriptions[profileId] = info
	}
	now := engine.now()
	info.Attempted = now.Format(time.RFC3339Nano)
	if downloadErr == nil {
		downloadErr = writeSubscriptionFile(engine.paths.Core, profileId, result.Content)
	}
	if downloadErr != nil {
		info.Error = downloadErr.Error()
		engine.saveState()
		slog.Warn("下载订阅失败", "profile", profile.Name, "err", downloadErr)
		return downloadErr
	}
	loaded := info.Updated != ""
	*info = SubscriptionInfo{
		Source: source, Updated: now.Format(time.RFC3339Nano), Attempted: info.Attempted, Nodes: result.Nodes,
		Upload: result.Info.Upload, Download: result.Info.Download, Total: result.Info.Total, Expire: result.Info.Expire,
	}
	engine.saveState()
	slog.Info("订阅已更新", "profile", profile.Name, "nodes", result.Nodes)
	engine.syncCore()
	if !loaded {
		engine.notify(Notice{Level: noticeInfo, Title: "订阅已就绪：" + profile.Name, Text: fmt.Sprintf("共 %d 个节点，可以开启了", result.Nodes), Icon: iconStateOn, Color: profile.Color})
	}
	return nil
}

// forgetSubscriptions 删除已不在配置里的订阅的记录和文件。
func (engine *Engine) forgetSubscriptions() {
	changed := false
	for profileId := range engine.state.Subscriptions {
		if profile := engine.config.FindProfileById(profileId); profile == nil || !profile.IsSubscription() {
			delete(engine.state.Subscriptions, profileId)
			_ = os.Remove(engine.subscriptionPath(profileId))
			changed = true
		}
	}
	if changed {
		engine.saveState()
	}
}

// SelectNode 记下订阅配置选中的节点（空表示自动选择），内核随后切换。
func (engine *Engine) SelectNode(profileId, node string) error {
	if engine.config == nil {
		return errNoConfig
	}
	profile := engine.config.FindProfileById(profileId)
	if profile == nil || !profile.IsSubscription() {
		return errors.New("没有这个订阅配置")
	}
	if profile.Node == node {
		engine.syncCore()
		return nil
	}
	updated := engine.config.Clone()
	updated.FindProfileById(profileId).Node = node
	return engine.SaveConfig(updated)
}

// GeoDue 表示需要下载大陆直连规则用到的地理数据：有使用大陆直连的订阅，数据还没下载，并且离上次尝试已过了重试间隔。
func (engine *Engine) GeoDue() bool {
	if engine.config == nil {
		return false
	}
	needed := false
	for _, profile := range engine.config.Profiles {
		if profile.IsSubscription() && profile.Mode == "rule" {
			needed = true
		}
	}
	if !needed {
		return false
	}
	now := engine.now()
	attempted, _ := time.Parse(time.RFC3339Nano, engine.state.GeoAttempted)
	if now.Sub(attempted) < geoRetryInterval && !attempted.After(now) {
		return false
	}
	return !engine.geoReady()
}

// RecordGeoDownload 记下一次地理数据下载，成功时让内核用上大陆直连规则。
func (engine *Engine) RecordGeoDownload(err error) {
	engine.state.GeoAttempted = engine.now().Format(time.RFC3339Nano)
	engine.saveState()
	if err != nil {
		slog.Warn("下载地理数据失败", "err", err)
		return
	}
	slog.Info("地理数据已更新")
	engine.syncCore()
}

// PrepareExit 在程序退出前调用：内核随程序一起停止，正在使用的订阅配置要先关闭，否则系统代理会指向没有程序监听的端口。
// 记下这个配置，下次启动后重新开启。
func (engine *Engine) PrepareExit() {
	status := engine.Status()
	if status.State != statusOn || status.Profile == nil || !status.Profile.IsSubscription() {
		return
	}
	profileId := status.Profile.Id
	engine.deactivate()
	engine.state.Resume = profileId
	engine.saveState()
	slog.Info("退出前关闭了使用订阅的代理，下次启动后重新开启", "profile", status.Profile.Name)
}

// subscriptionInfos 给设置页的订阅下载情况。地址改了还没重新下载的订阅显示为空（正在下载）。
func (engine *Engine) subscriptionInfos() map[string]SubscriptionInfo {
	infos := map[string]SubscriptionInfo{}
	if engine.config == nil {
		return infos
	}
	for _, profile := range engine.config.Profiles {
		if !profile.IsSubscription() {
			continue
		}
		var copied SubscriptionInfo
		if info := engine.state.Subscriptions[profile.Id]; info != nil && info.Source == subscriptionSource(profile.Subscription) {
			copied = *info
			copied.Source = ""
		}
		infos[profile.Id] = copied
	}
	return infos
}

// coreInfo 给设置页的内核情况中与平台无关的部分；是否在运行、能否下载由调用方补上。
func (engine *Engine) coreInfo() CoreInfo {
	info := CoreInfo{Version: coreVersion, Port: defaultCorePort}
	if engine.config == nil {
		return info
	}
	info.Path = engine.coreBinary()
	info.Custom = engine.config.Core.Path != ""
	info.Port = engine.config.Core.Port
	info.Installed = fileExists(info.Path)
	info.GeoReady = engine.geoReady()
	if !info.Custom && info.Installed {
		info.InstalledVersion = readCoreVersion(engine.paths.Core)
	}
	return info
}
