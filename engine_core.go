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

// cachedRules 是读过的某个版本的转换结果。
type cachedRules struct {
	revision string
	manifest *ruleManifest
}

// ruleSetRetry 是规则集还没下载成功时重试的间隔：还没下载下来的规则集先跳过，要尽快补上。
const ruleSetRetry = 10 * time.Minute

// loadedManifest 返回完整配置类规则集转换好的规则，读不出来时返回 nil。
func (engine *Engine) loadedManifest(set RuleSet, info *RuleSetInfo) *ruleManifest {
	id := set.Id()
	if cached, found := engine.ruleCache[id]; found && cached.revision == info.Revision {
		return cached.manifest
	}
	manifest, err := readRuleManifest(engine.paths.Core, id, info.Revision)
	if err != nil {
		slog.Warn("读取分流规则失败", "rule_set", set.Name, "err", err)
		return nil
	}
	engine.ruleCache[id] = cachedRules{info.Revision, manifest}
	return manifest
}

// composeRules 按规则集的顺序拼出内核的分流规则和其中的规则集文件，最后一条是 MATCH（其余流量）。内置的直接展开，纯列表交给
// 内核的 rule-provider，完整配置用转换好的规则。这里不联网：还没下载好的规则集先跳过，后台下载好后内核重新加载。
func (engine *Engine) composeRules() ([]string, map[string]CoreRuleProvider) {
	groups := engine.config.GroupNames()
	var rules []string
	providers := map[string]CoreRuleProvider{}
	fileFinal := ""
	for _, set := range engine.config.RuleSets {
		if set.Disabled {
			continue
		}
		if set.IsBuiltin() {
			rules = append(rules, builtinRules(set.Url, corePolicyTarget(policyOr(set.Policy, rulePolicyDirect), groups))...)
			continue
		}
		info := engine.state.RuleSets[set.Id()]
		if info == nil || info.Revision == "" {
			continue
		}
		switch info.Kind {
		case ruleSetProvider:
			behavior := set.Behavior
			if behavior == "" {
				behavior = info.Behavior
			}
			providers[set.Id()] = CoreRuleProvider{Behavior: behavior, Format: info.Format, Path: ruleDir(set.Id()) + "/" + info.Revision, Optional: true}
			rules = append(rules, "RULE-SET,"+set.Id()+","+corePolicyTarget(policyOr(set.Policy, rulePolicyProxy), groups))
		case ruleSetConvert:
			manifest := engine.loadedManifest(set, info)
			if manifest == nil {
				continue
			}
			for _, rule := range manifest.Rules {
				rules = append(rules, rule.line(set.Policy, groups))
			}
			for name, provider := range manifest.Providers {
				providers[name] = provider
			}
			// 跟随规则文件时用最后一个没改去向的完整配置的 FINAL。
			if set.Policy == "" && manifest.Final != "" {
				fileFinal = ruleSetTarget("", manifest.Final, manifest.FinalName, groups)
			}
		}
	}
	final := coreTopGroup
	switch {
	case engine.config.FinalPolicy != "":
		final = corePolicyTarget(engine.config.FinalPolicy, groups)
	case fileFinal != "":
		final = fileFinal
	}
	return append(rules, "MATCH,"+final), providers
}

func policyOr(policy, fallback string) string {
	if policy == "" {
		return fallback
	}
	return policy
}

// coreSettings 按配置和已下载的订阅算出内核应处于的状态。正在使用的订阅是最近使用的配置（如果它是订阅），
// 它按规则分流并且规则已下载好时用它的规则，否则用内置的大陆直连。
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
	settings.CustomRules = customRuleLines(engine.config.CustomRules, engine.config.GroupNames())
	for index := range engine.config.Profiles {
		profile := &engine.config.Profiles[index]
		if profile.IsSubscription() && engine.subscriptionLoaded(profile) {
			settings.Subscriptions = append(settings.Subscriptions, CoreSubscription{Id: profile.Id, Node: profile.Node, Revision: engine.state.Subscriptions[profile.Id].Updated})
		}
	}
	if selected := engine.selectedProfile(); selected != nil && selected.IsSubscription() && engine.subscriptionLoaded(selected) {
		settings.Active, settings.Mode = selected.Id, selected.Mode
	}
	if settings.Mode == "rule" && len(settings.Subscriptions) > 0 {
		settings.Rules, settings.RuleProviders = engine.composeRules()
	}
	if settings.GroupSource = engine.groupSource(); settings.GroupSource != "" {
		settings.PolicyGroups = engine.config.PolicyGroups
	}
	status := engine.Status()
	if engine.config.Share.Enabled {
		settings.Share = engine.coreShare(status, settings.Active)
	}
	settings.Tun = engine.tunWanted(status, settings.Active)
	return settings
}

// groupSource 是策略组的节点来源：正在使用的订阅配置，最近用的不是订阅时是第一个已下载的订阅（内核的 ProxySwitch 组
// 这时默认选它）；没有已下载的订阅时为空。
func (engine *Engine) groupSource() string {
	if engine.config == nil {
		return ""
	}
	if selected := engine.selectedProfile(); selected != nil && selected.IsSubscription() && engine.subscriptionLoaded(selected) {
		return selected.Id
	}
	for index := range engine.config.Profiles {
		if profile := &engine.config.Profiles[index]; profile.IsSubscription() && engine.subscriptionLoaded(profile) {
			return profile.Id
		}
	}
	return ""
}

// tunWanted 表示内核应开启 TUN 模式：开启了 TUN，并且正开着订阅配置 active。代理关了或者换成别的配置时，
// 虚拟网卡也关掉，免得接管不该走内核的流量。
func (engine *Engine) tunWanted(status Status, active string) bool {
	return engine.config.Tun.Enabled && active != "" && status.State == statusOn && status.Profile != nil && status.Profile.Id == active
}

// syncCore 把内核应处于的状态交给内核（异步生效），记下这次设定的序号，调用方可以用它等待生效。
func (engine *Engine) syncCore() {
	if engine.core != nil {
		settings := engine.coreSettings()
		engine.syncedShare, engine.syncedTun = settings.Share, settings.Tun
		engine.coreGeneration = engine.core.Sync(settings)
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
	if path, isFile := localFilePath(profile.Subscription); isFile {
		// 本机的文件改过就重新读，不用等到每天更新的时候（修改时间在将来的不算，免得反复读）。
		if stat, err := os.Stat(path); err == nil && stat.ModTime().After(attempted) && !stat.ModTime().After(now) {
			return true
		}
	}
	retry := now.Sub(attempted) >= subscriptionRetry || attempted.After(now)
	if info.Updated == "" || !fileExists(engine.subscriptionPath(profile.Id)) {
		return retry
	}
	updated, _ := time.Parse(time.RFC3339Nano, info.Updated)
	return (now.Sub(updated) >= subscriptionInterval || updated.After(now)) && retry && !engine.UpdatesPaused()
}

// UpdatesPaused 表示在按流量计费的网络上，暂停每天自动更新订阅和分流规则（pause_on_metered）。第一次下载、
// 地址改了之后的下载和手动更新不受影响。
func (engine *Engine) UpdatesPaused() bool {
	return engine.config != nil && engine.config.PauseOnMetered && engine.network.Metered
}

// DownloadPaths 是下载订阅、内核和地理数据时依次尝试的网络路径：直连，然后是正在使用的代理，最后是内核。
// 系统代理是其他程序设置的（例如从 Clash 换过来时它还开着）也经它试一次，PAC 除外。
func (engine *Engine) DownloadPaths() []string {
	paths := []string{""}
	add := func(proxyUrl string) {
		if proxyUrl != "" && !containsString(paths, proxyUrl) {
			paths = append(paths, proxyUrl)
		}
	}
	switch status := engine.Status(); {
	case status.State == statusOn && status.Profile != nil && status.Profile.Server != "":
		add(serverToUrl(status.Profile.Server))
	case status.State == statusExternal && status.System.ProxyEnabled && !status.System.PacEnabled:
		add(serverToUrl(status.System.Server))
	}
	if engine.config != nil && len(engine.coreSettings().Subscriptions) > 0 {
		add("http://" + coreServer(engine.config.Core.Port))
	}
	return paths
}

// proxiesFirst 把直连挪到最后：内核和地理数据放在 GitHub 等国外网站上，国内直连常常很慢，有代理时先经代理下载。
func proxiesFirst(paths []string) []string {
	var ordered []string
	for _, path := range paths {
		if path != "" {
			ordered = append(ordered, path)
		}
	}
	return append(ordered, "")
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
		notice := Notice{Level: noticeInfo, Title: "订阅已就绪：" + profile.Name, Text: fmt.Sprintf("共 %d 个节点，可以开启了", result.Nodes), Icon: iconStateOn, Color: profile.Color, Tag: noticeTagSubscription}
		if status := engine.Status(); status.State != statusOn || status.Profile == nil || status.Profile.Id != profile.Id {
			notice.Actions = []NoticeAction{{Label: "开启", Link: useProfileLink(profile.Name)}}
		}
		engine.notify(notice)
	}
	return nil
}

// forgetSubscriptions 删除已不在配置里的订阅的记录和文件，已删除的规则集的记录和文件，以及旧版按配置下载的分流规则。
func (engine *Engine) forgetSubscriptions() {
	changed := false
	for profileId := range engine.state.Subscriptions {
		if profile := engine.config.FindProfileById(profileId); profile == nil || !profile.IsSubscription() {
			delete(engine.state.Subscriptions, profileId)
			_ = os.Remove(engine.subscriptionPath(profileId))
			changed = true
		}
	}
	for profileId := range engine.state.Rules {
		removeRuleRevisions(engine.paths.Core, profileId, "")
		changed = true
	}
	engine.state.Rules = nil
	used := map[string]bool{}
	for _, set := range engine.config.RuleSets {
		used[set.Id()] = true
	}
	for id := range engine.state.RuleSets {
		if !used[id] {
			delete(engine.state.RuleSets, id)
			delete(engine.ruleCache, id)
			removeRuleRevisions(engine.paths.Core, id, "")
			changed = true
		}
	}
	if changed {
		engine.saveState()
	}
}

// RuleSetsDue 返回该下载的规则集：启用的、不是内置的；新添加的立即下载，没下载成功的隔一会儿重试；下载成功后每天更新一次，
// 有引用的列表没下载到时隔一段时间重试；本机的文件改过就重新读。没有订阅配置时不下载（用不上）。
func (engine *Engine) RuleSetsDue() []RuleSet {
	if engine.config == nil || !engine.config.HasSubscriptions() {
		return nil
	}
	now := engine.now()
	var due []RuleSet
	for _, set := range engine.config.RuleSets {
		if !set.Disabled && !set.IsBuiltin() && engine.ruleSetDue(set, now) {
			due = append(due, set)
		}
	}
	return due
}

func (engine *Engine) ruleSetDue(set RuleSet, now time.Time) bool {
	info := engine.state.RuleSets[set.Id()]
	if info == nil {
		return true
	}
	attempted, _ := time.Parse(time.RFC3339Nano, info.Attempted)
	if path, isFile := localFilePath(set.Url); isFile {
		// 本机的文件改过就重新读（修改时间在将来的不算，免得反复读）。
		if stat, err := os.Stat(path); err == nil && stat.ModTime().After(attempted) && !stat.ModTime().After(now) {
			return true
		}
	}
	if info.Revision == "" {
		return now.Sub(attempted) >= ruleSetRetry || attempted.After(now)
	}
	updated, _ := time.Parse(time.RFC3339Nano, info.Updated)
	retry := now.Sub(attempted) >= subscriptionRetry || attempted.After(now)
	stale := now.Sub(updated) >= subscriptionInterval || updated.After(now) || info.FailedSets > 0
	return stale && retry && !engine.UpdatesPaused()
}

// RecordRuleSet 记下一次规则集下载的结果：成功时换用新版本、删除旧版本，内核随之重新加载；失败时继续用上次下载的。
// 下载期间规则集被删掉（或地址改了）时忽略这次结果。
func (engine *Engine) RecordRuleSet(address string, result ruleSetDownload, downloadErr error) error {
	if engine.config == nil {
		return errNoConfig
	}
	set := engine.config.FindRuleSet(address)
	if set == nil {
		return errors.New("这个规则集已经删除了")
	}
	if engine.state.RuleSets == nil {
		engine.state.RuleSets = map[string]*RuleSetInfo{}
	}
	id := set.Id()
	info := engine.state.RuleSets[id]
	if info == nil {
		info = &RuleSetInfo{}
		engine.state.RuleSets[id] = info
	}
	now := engine.now()
	info.Attempted = now.Format(time.RFC3339Nano)
	if downloadErr != nil {
		info.Error = downloadErr.Error()
		engine.saveState()
		slog.Warn("下载规则集失败", "rule_set", set.Name, "err", downloadErr)
		return downloadErr
	}
	*info = RuleSetInfo{
		Updated: now.Format(time.RFC3339Nano), Attempted: info.Attempted, Kind: result.Kind, Revision: result.Revision, Format: result.Format, Behavior: result.Behavior,
		Rules: result.Count, Sets: result.Sets, FailedSets: result.FailedSets, Skipped: result.Skipped, Final: result.Final, FinalName: result.FinalName, Geo: result.Geo,
	}
	engine.saveState()
	removeRuleRevisions(engine.paths.Core, id, result.Revision)
	slog.Info("规则集已更新", "rule_set", set.Name, "kind", result.Kind, "rules", result.Count, "failed_sets", result.FailedSets, "skipped", result.Skipped)
	engine.syncCore()
	if engine.GeoDue() {
		engine.requestDownloads()
	}
	return nil
}

// SetMode 切换订阅配置的分流模式（rule 按规则分流 / global 全部走节点），保存到配置文件，内核随之切换。
func (engine *Engine) SetMode(profileId, mode string) error {
	if engine.config == nil {
		return errNoConfig
	}
	if mode != "rule" && mode != "global" {
		return fmt.Errorf("分流模式 %q 不认识", mode)
	}
	profile := engine.config.FindProfileById(profileId)
	if profile == nil || !profile.IsSubscription() {
		return errors.New("没有这个订阅配置")
	}
	if profile.Mode == mode {
		engine.syncCore()
		return nil
	}
	updated := engine.config.Clone()
	updated.FindProfileById(profileId).Mode = mode
	return engine.SaveConfig(updated)
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

// SelectGroupNode 记下手动选择的策略组选中的成员（见 PolicyGroup.Node），内核随后切换。
func (engine *Engine) SelectGroupNode(name, node string) error {
	if engine.config == nil {
		return errNoConfig
	}
	group := engine.config.FindGroup(name)
	if group == nil {
		return fmt.Errorf("没有叫「%s」的策略组", name)
	}
	if group.Type != groupSelect {
		return fmt.Errorf("策略组「%s」自动挑选节点，不能手动选择", name)
	}
	if group.Node == node {
		engine.syncCore()
		return nil
	}
	updated := engine.config.Clone()
	updated.FindGroup(name).Node = node
	return engine.SaveConfig(updated)
}

// GeoDue 表示需要下载地理数据：有按规则分流的订阅配置，启用了内置的国内直连或者用到 GEOIP / GEOSITE 的规则集，
// 数据还没下载，并且离上次尝试已过了重试间隔。
func (engine *Engine) GeoDue() bool {
	if engine.config == nil || !engine.geoNeeded() {
		return false
	}
	now := engine.now()
	attempted, _ := time.Parse(time.RFC3339Nano, engine.state.GeoAttempted)
	if now.Sub(attempted) < geoRetryInterval && !attempted.After(now) {
		return false
	}
	return !engine.geoReady()
}

func (engine *Engine) geoNeeded() bool {
	ruleMode := false
	for index := range engine.config.Profiles {
		if profile := &engine.config.Profiles[index]; profile.IsSubscription() && profile.Mode == "rule" {
			ruleMode = true
		}
	}
	if !ruleMode {
		return false
	}
	for _, set := range engine.config.RuleSets {
		if set.Disabled {
			continue
		}
		if info := engine.state.RuleSets[set.Id()]; set.IsBuiltin() || info != nil && info.Geo {
			return true
		}
	}
	return false
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

// RuleSetState 是设置页里一个规则集的情况：Id 是它在内核里的名字；Kind 是加载方式（下载前按地址猜，下载后以认出来的为准）；
// Downloaded 表示已经下载好、正在使用；Count 是规则数（纯列表的数目在内核读进来后由内核报告）；其余见 RuleSetInfo。
type RuleSetState struct {
	Id         string `json:"id"`
	Kind       string `json:"kind"`
	Downloaded bool   `json:"downloaded"`
	Updated    string `json:"updated,omitempty"`
	Error      string `json:"error,omitempty"`
	Behavior   string `json:"behavior,omitempty"`
	Count      int    `json:"count,omitempty"`
	Sets       int    `json:"sets,omitempty"`
	FailedSets int    `json:"failed_sets,omitempty"`
	Skipped    int    `json:"skipped,omitempty"`
	Final      string `json:"final,omitempty"`
	FinalName  string `json:"final_name,omitempty"`
}

// ruleSetStates 给设置页的规则集情况，按地址查。
func (engine *Engine) ruleSetStates() map[string]RuleSetState {
	states := map[string]RuleSetState{}
	if engine.config == nil {
		return states
	}
	for _, set := range engine.config.RuleSets {
		state := RuleSetState{Id: set.Id(), Kind: set.guessKind()}
		if info := engine.state.RuleSets[set.Id()]; info != nil {
			state.Updated, state.Error = info.Updated, info.Error
			if info.Revision != "" {
				state.Kind, state.Downloaded, state.Behavior, state.Count = info.Kind, true, info.Behavior, info.Rules
				state.Sets, state.FailedSets, state.Skipped, state.Final, state.FinalName = info.Sets, info.FailedSets, info.Skipped, info.Final, info.FinalName
			}
		}
		if set.IsBuiltin() {
			state.Downloaded, state.Count = true, len(builtinRules(set.Url, ""))
		}
		if set.Behavior != "" {
			state.Behavior = set.Behavior
		}
		states[set.Url] = state
	}
	return states
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
