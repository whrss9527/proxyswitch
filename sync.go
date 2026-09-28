package main

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"
)

// 配置同步：代理配置和设置放在一个会同步的文件夹里（默认是 OneDrive 里的 ProxySwitch 文件夹，也可以是坚果云、Dropbox、
// NAS 共享这类文件夹），在多台电脑之间同步。同步文件是文件夹里的 config.json：{"format", "updated", "device", "config"}，
// config 是配置里和具体电脑无关的部分；代理内核、TUN、局域网共享、编辑器和同步本身这些本机的设置不同步，订阅和规则由每台
// 电脑自己下载。本机改了就写上去，文件夹里的文件变了就读回来；两边都改过时以改动时间晚的为准。

const (
	syncFormat   = 1
	syncFileName = "config.json"
	// syncFolderName 是 OneDrive 里放同步文件的文件夹。
	syncFolderName = "ProxySwitch"
	// syncFolderVariable 把默认的同步文件夹指到别处（开发和测试用）。
	syncFolderVariable = "PROXYSWITCH_SYNC_DIR"
	syncPollInterval   = 2 * time.Second
	// syncPushDelay：本机的配置改了之后等一会儿再写，连续的改动合并成一次。
	syncPushDelay = time.Second
)

// SyncConfig 是配置同步的本机设置：是否开启；Folder 是同步文件夹，留空时用 OneDrive 里的 ProxySwitch 文件夹。
type SyncConfig struct {
	Enabled bool   `json:"enabled"`
	Folder  string `json:"folder"`
}

// syncLocalKeys 是只和这台电脑有关、不同步的设置。
var syncLocalKeys = map[string]bool{"core": true, "tun": true, "share": true, "editor": true, "sync": true, "notify": true}

// syncFile 是同步文件：Updated 是这份配置改动的时间（RFC 3339），Device 是写入它的电脑。
type syncFile struct {
	Format  int             `json:"format"`
	Updated string          `json:"updated"`
	Device  string          `json:"device"`
	Config  json.RawMessage `json:"config"`
}

func (file syncFile) updatedTime() time.Time {
	updated, _ := time.Parse(time.RFC3339Nano, file.Updated)
	return updated
}

// defaultSyncFolder 是默认的同步文件夹：OneDrive 里的 ProxySwitch 文件夹；没有 OneDrive 时为空。
func defaultSyncFolder() string {
	if folder := os.Getenv(syncFolderVariable); folder != "" {
		return folder
	}
	for _, name := range []string{"OneDrive", "OneDriveConsumer", "OneDriveCommercial"} {
		if root := os.Getenv(name); root != "" {
			if info, err := os.Stat(root); err == nil && info.IsDir() {
				return filepath.Join(root, syncFolderName)
			}
		}
	}
	return ""
}

// syncFolderPath 是实际用的同步文件夹。
func syncFolderPath(config SyncConfig) (string, error) {
	if folder := strings.TrimSpace(config.Folder); folder != "" {
		return folder, nil
	}
	if folder := defaultSyncFolder(); folder != "" {
		return folder, nil
	}
	return "", errors.New("没有找到 OneDrive，请填一个会同步的文件夹（坚果云、Dropbox、NAS 共享都可以）")
}

func validateSyncConfig(config SyncConfig) error {
	if folder := strings.TrimSpace(config.Folder); folder != "" && !filepath.IsAbs(folder) {
		return fmt.Errorf("同步文件夹要填完整的路径，例如 D:\\坚果云\\ProxySwitch：%s", folder)
	}
	return nil
}

// syncDeviceName 是写进同步文件的电脑名字。
var syncDeviceName = func() string {
	if name, err := os.Hostname(); err == nil && name != "" {
		return name
	}
	return "未知电脑"
}

// syncedConfigJson 是配置里要同步的部分，键按字母顺序排好，可以直接比较：去掉本机的设置，也去掉订阅配置的代理地址
// （它由本机内核的端口决定，两台电脑的端口不一样时不能来回改）。
func syncedConfigJson(config *Config) ([]byte, error) {
	data, err := json.Marshal(config)
	if err != nil {
		return nil, err
	}
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(data, &fields); err != nil {
		return nil, err
	}
	for key := range syncLocalKeys {
		delete(fields, key)
	}
	var profiles []map[string]json.RawMessage
	if err := json.Unmarshal(fields["profiles"], &profiles); err != nil {
		return nil, err
	}
	for _, profile := range profiles {
		if subscription, ok := profile["subscription"]; ok && string(subscription) != `""` {
			delete(profile, "server")
		}
	}
	if fields["profiles"], err = json.Marshal(profiles); err != nil {
		return nil, err
	}
	return json.Marshal(fields)
}

// withSynced 把同步来的配置套到本机的配置上：同步的部分用同步来的，本机的设置保留。结果和手写的配置文件一样经过
// 解析、规范化和校验。
func withSynced(local *Config, synced json.RawMessage) (*Config, error) {
	var remote map[string]json.RawMessage
	if err := json.Unmarshal(synced, &remote); err != nil || remote == nil {
		return nil, errors.New("同步文件里的配置格式不对")
	}
	data, err := json.Marshal(local)
	if err != nil {
		return nil, err
	}
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(data, &fields); err != nil {
		return nil, err
	}
	for key, value := range remote {
		if !syncLocalKeys[key] {
			fields[key] = value
		}
	}
	merged, err := json.Marshal(fields)
	if err != nil {
		return nil, err
	}
	config, err := parseConfig(string(merged))
	if err != nil {
		return nil, fmt.Errorf("同步来的配置有错误：%v", err)
	}
	return config, nil
}

// profileAddress 是配置连到哪里，合并时用来认出同一个配置。
func profileAddress(profile Profile) string {
	if profile.IsSubscription() {
		return "subscription:" + profile.Subscription
	}
	return profile.Server + "|" + profile.Pac
}

// mergeSyncedConfigs 合并本机和同步文件夹里的配置：以同步文件夹里的代理配置、策略组、自定义规则、规则集和自动切换
// 规则为准，本机独有的追加在后面（代理配置连到同一个地方、id 或名字相同时算同一个，和同步来的重名时本机的改名）；
// 其他设置保留本机的。
func mergeSyncedConfigs(local, remote *Config) (*Config, error) {
	merged := local.Clone()
	merged.Profiles = append([]Profile{}, remote.Profiles...)
	names := map[string]bool{}
	for _, profile := range remote.Profiles {
		names[strings.ToLower(profile.Name)] = true
	}
	ids := map[string]bool{}
	for _, profile := range remote.Profiles {
		ids[profile.Id] = true
	}
	renamed := map[string]string{}
	for _, profile := range local.Profiles {
		duplicate := false
		for _, existing := range remote.Profiles {
			// 手写的配置文件里没有 id 时由名字推导，两台电脑上同名的配置 id 也一样，所以还要比较连到哪里。
			if profileAddress(existing) == profileAddress(profile) && (existing.Id == profile.Id || strings.EqualFold(existing.Name, profile.Name)) {
				duplicate = true
				break
			}
		}
		if duplicate {
			continue
		}
		if ids[profile.Id] {
			// 和同步来的配置 id 相同但连到别处：是另一个配置，重新生成 id。
			profile.Id = ""
		}
		if names[strings.ToLower(profile.Name)] {
			base := profile.Name + "（本机）"
			name := base
			for index := 2; names[strings.ToLower(name)]; index++ {
				name = fmt.Sprintf("%s %d", base, index)
			}
			renamed[profile.Name] = name
			profile.Name = name
		}
		names[strings.ToLower(profile.Name)] = true
		merged.Profiles = append(merged.Profiles, profile)
	}

	merged.PolicyGroups = append([]PolicyGroup{}, remote.PolicyGroups...)
	for _, group := range local.PolicyGroups {
		if remote.FindGroup(group.Name) == nil {
			merged.PolicyGroups = append(merged.PolicyGroups, group)
		}
	}
	merged.CustomRules = append([]CustomRule{}, remote.CustomRules...)
	for _, rule := range local.CustomRules {
		found := false
		for _, existing := range remote.CustomRules {
			found = found || (existing.Type == rule.Type && strings.EqualFold(existing.Value, rule.Value))
		}
		if !found {
			merged.CustomRules = append(merged.CustomRules, rule)
		}
	}
	merged.RuleSets = append([]RuleSet{}, remote.RuleSets...)
	for _, set := range local.RuleSets {
		if remote.FindRuleSet(set.Url) == nil {
			merged.RuleSets = append(merged.RuleSets, set)
		}
	}
	merged.FinalPolicy = remote.FinalPolicy
	merged.AutoSwitch.Rules = append([]NetRule{}, remote.AutoSwitch.Rules...)
	for _, rule := range local.AutoSwitch.Rules {
		if name, found := renamed[rule.Profile]; found && rule.Action == "use" {
			rule.Profile = name
		}
		duplicate := false
		for _, existing := range remote.AutoSwitch.Rules {
			duplicate = duplicate || (existing.Match == rule.Match && strings.EqualFold(existing.Value, rule.Value))
		}
		if !duplicate {
			merged.AutoSwitch.Rules = append(merged.AutoSwitch.Rules, rule)
		}
	}
	if name, found := renamed[merged.AutoSwitch.DefaultProfile]; found {
		merged.AutoSwitch.DefaultProfile = name
	}
	// 和手写的配置一样过一遍解析和校验：补上 id，检查引用的策略组都在。
	data, err := json.Marshal(merged)
	if err != nil {
		return nil, err
	}
	result, err := parseConfig(string(data))
	if err != nil {
		return nil, fmt.Errorf("合并后的配置有错误：%v", err)
	}
	return result, nil
}

// readSyncFile 读同步文件；文件不存在时返回 nil。
func readSyncFile(path string) (*syncFile, error) {
	data, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("读不了同步文件：%v", err)
	}
	var file syncFile
	if err := json.Unmarshal(bytes.TrimPrefix(data, []byte("\xef\xbb\xbf")), &file); err != nil || file.Format < 1 || file.Updated == "" || len(file.Config) == 0 {
		return nil, errors.New("同步文件的格式不对")
	}
	if file.Format > syncFormat {
		return nil, errors.New("同步文件是更新版本的 ProxySwitch 写的，请先更新这台电脑上的 ProxySwitch")
	}
	return &file, nil
}

// writeSyncFile 先写临时文件再改名，别的电脑和同步程序不会读到写了一半的文件。
func writeSyncFile(folder string, file syncFile) error {
	data, err := json.MarshalIndent(file, "", "  ")
	if err != nil {
		return err
	}
	if err := os.MkdirAll(folder, 0o755); err != nil {
		return fmt.Errorf("建不了同步文件夹：%v", err)
	}
	path := filepath.Join(folder, syncFileName)
	temporary := filepath.Join(folder, "."+syncFileName+".tmp")
	if err := os.WriteFile(temporary, append(data, '\n'), 0o644); err != nil {
		return fmt.Errorf("写不了同步文件：%v", err)
	}
	if err := os.Rename(temporary, path); err != nil {
		_ = os.Remove(temporary)
		// 同步程序正开着这个文件时 Windows 上改名会失败，退回直接覆盖。
		if err := os.WriteFile(path, append(data, '\n'), 0o644); err != nil {
			return fmt.Errorf("写不了同步文件：%v", err)
		}
	}
	return nil
}

// resolveSyncConflicts 处理同步程序留下的冲突副本（OneDrive 的 config-电脑名.json、Dropbox 的「冲突副本」等）：
// 连同 config.json 在内，写入时间最晚的那份胜出，写回 config.json，其余副本删掉。返回胜出的来自哪台电脑，没有冲突时为空。
func resolveSyncConflicts(folder string) string {
	entries, err := os.ReadDir(folder)
	if err != nil {
		return ""
	}
	type candidate struct {
		path string
		file *syncFile
	}
	var copies []candidate
	for _, entry := range entries {
		name := strings.ToLower(entry.Name())
		if entry.IsDir() || name == syncFileName || !strings.HasPrefix(name, "config") || !strings.HasSuffix(name, ".json") {
			continue
		}
		path := filepath.Join(folder, entry.Name())
		if file, err := readSyncFile(path); err == nil && file != nil {
			copies = append(copies, candidate{path, file})
		}
	}
	if len(copies) == 0 {
		return ""
	}
	main := filepath.Join(folder, syncFileName)
	candidates := copies
	if file, err := readSyncFile(main); err == nil && file != nil {
		candidates = append(candidates, candidate{main, file})
	}
	sort.SliceStable(candidates, func(i, j int) bool { return candidates[i].file.updatedTime().After(candidates[j].file.updatedTime()) })
	winner := candidates[0]
	if winner.path != main {
		if err := writeSyncFile(folder, *winner.file); err != nil {
			slog.Warn("同步：处理冲突副本失败", "err", err)
			return ""
		}
	}
	for _, copy := range copies {
		_ = os.Remove(copy.path)
	}
	slog.Info("同步：处理了冲突副本", "copies", len(copies), "winner", winner.file.Device)
	return winner.file.Device
}

// syncStamp 是同步文件内容的摘要，用来发现文件变了（修改时间不可靠：同步程序会改它，两次写入也可能落在同一个
// 时钟周期里）。文件不存在或读不了时为空。
func syncStamp(path string) string {
	data, err := os.ReadFile(path)
	if err != nil {
		return ""
	}
	return syncHash(data)
}

// ---------- 同步服务 ----------

const (
	syncOff     = "off"
	syncSyncing = "syncing"
	syncSynced  = "synced"
	syncError   = "error"
)

// SyncPending 是开启同步时，同步文件夹里已经有另一份不一样的配置：来自哪台电脑、什么时候写的、有几个代理配置，
// 等用户选择怎么办。
type SyncPending struct {
	Device        string `json:"device"`
	Updated       string `json:"updated"`
	Profiles      int    `json:"profiles"`
	LocalProfiles int    `json:"local_profiles"`
}

// SyncInfo 是设置页里同步的情况。Folder 是实际用的同步文件夹，DefaultFolder 是 OneDrive 里的文件夹（没有 OneDrive 时为空）；
// State 是 off / syncing / synced / error；Updated 和 Device 是同步的配置最近一次改动的时间和电脑，This 是这台电脑的名字。
type SyncInfo struct {
	Enabled       bool         `json:"enabled"`
	Folder        string       `json:"folder"`
	DefaultFolder string       `json:"default_folder"`
	State         string       `json:"state"`
	Message       string       `json:"message,omitempty"`
	Updated       string       `json:"updated,omitempty"`
	Device        string       `json:"device,omitempty"`
	This          string       `json:"this"`
	Pending       *SyncPending `json:"pending,omitempty"`
}

// syncHash 是配置里同步部分的摘要：比较本机、同步文件和上次同步的配置是否一样时用，也记在状态文件里。
func syncHash(data []byte) string {
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])
}

// syncService 在后台同步配置：每隔两秒看一次本机的配置和同步文件有没有变。
type syncService struct {
	engine   *Engine
	onEngine func(action func()) error
	// applied 在同步改了本机的配置之后、在引擎线程上调用（Windows 版用它更新热键和托盘菜单）。
	applied func()
	now     func() time.Time
	// this 是这台电脑的名字，写进同步文件。
	this string
	kick chan struct{}
	// running 保证同一时间只有一次同步（后台的和设置页上的操作）。
	running sync.Mutex

	mutex sync.Mutex
	state string
	// message 是出错的原因；updated 和 device 是同步的配置最近一次改动的时间和电脑。
	message string
	updated string
	device  string
	// restored 表示已经从状态文件读回上次同步的情况：lastSynced 是上次和同步文件一致时同步部分的摘要，
	// lastFolder 是那时的同步文件夹；lastStamp 是那时同步文件内容的摘要（不记进状态文件，重启后重新读一次）。
	restored   bool
	lastSynced string
	lastFolder string
	lastStamp  string
	// pending 是开启时同步文件夹里已有的另一份配置，pendingSync 是要开启的同步设置，等用户选择。
	pending     *syncFile
	pendingSync SyncConfig
}

func newSyncService(engine *Engine, onEngine func(action func()) error) *syncService {
	return &syncService{engine: engine, onEngine: onEngine, now: time.Now, this: syncDeviceName(), kick: make(chan struct{}, 1), state: syncOff}
}

// run 在后台定时同步，Kick 时立即同步一次。
func (service *syncService) run() {
	ticker := time.NewTicker(syncPollInterval)
	defer ticker.Stop()
	for {
		service.tick()
		select {
		case <-service.kick:
		case <-ticker.C:
		}
	}
}

func (service *syncService) Kick() {
	select {
	case service.kick <- struct{}{}:
	default:
	}
}

func (service *syncService) fail(message string) {
	service.mutex.Lock()
	changed := service.state != syncError || service.message != message
	service.state, service.message = syncError, message
	service.mutex.Unlock()
	if changed {
		slog.Warn("同步失败", "err", message)
	}
}

// localConfig 取出本机的配置（副本）、配置文件的修改时间（本机最近一次改动的时间）和配置文件的错误。
func (service *syncService) localConfig() (config *Config, modified time.Time, problem string) {
	_ = service.onEngine(func() {
		if current := service.engine.Config(); current != nil {
			config = current.Clone()
		}
		modified = service.engine.configStamp.modified
		problem = service.engine.ConfigError()
	})
	return config, modified, problem
}

// restore 在第一次同步前从状态文件读回上次同步的情况，重启后也能分辨是本机改了还是同步文件夹里的改了。
func (service *syncService) restore() {
	service.mutex.Lock()
	restored := service.restored
	service.mutex.Unlock()
	if restored {
		return
	}
	var hash, folder string
	if service.onEngine(func() { hash, folder = service.engine.state.Synced, service.engine.state.SyncedFolder }) != nil {
		return
	}
	service.mutex.Lock()
	defer service.mutex.Unlock()
	service.restored = true
	if service.lastSynced == "" {
		service.lastSynced, service.lastFolder = hash, folder
	}
}

// tick 同步一次：同步文件变了就读回来（两边都改过时改动晚的胜出），本机改了就写上去。
func (service *syncService) tick() {
	service.running.Lock()
	defer service.running.Unlock()
	config, modified, problem := service.localConfig()
	if config == nil || !config.Sync.Enabled {
		service.mutex.Lock()
		if service.pending == nil {
			service.state, service.message = syncOff, ""
		}
		service.lastStamp = ""
		service.mutex.Unlock()
		return
	}
	if problem != "" {
		service.fail("配置文件有错误，改好后继续同步")
		return
	}
	folder, err := syncFolderPath(config.Sync)
	if err != nil {
		service.fail(err.Error())
		return
	}
	localJson, err := syncedConfigJson(config)
	if err != nil {
		service.fail(err.Error())
		return
	}
	local := syncHash(localJson)
	service.restore()
	resolveSyncConflicts(folder)
	path := filepath.Join(folder, syncFileName)
	stamp := syncStamp(path)
	service.mutex.Lock()
	if service.lastFolder != folder {
		service.lastSynced, service.lastStamp, service.lastFolder = "", "", folder
	}
	lastSynced, lastStamp := service.lastSynced, service.lastStamp
	service.mutex.Unlock()
	localChanged := local != lastSynced

	if stamp != "" && stamp == lastStamp {
		// 同步文件没变：本机改了就写上去。等改动停下来一会儿再写，连续的改动合并成一次。
		if localChanged && service.now().Sub(modified) >= syncPushDelay {
			service.push(folder, localJson, modified)
		}
		return
	}
	remote, err := readSyncFile(path)
	if err != nil {
		service.fail(err.Error())
		return
	}
	if remote == nil {
		// 同步文件夹里还没有文件（第一次，或者被删了）：把本机的写上去。
		service.push(folder, localJson, modified)
		return
	}
	remoteConfig, err := withSynced(config, remote.Config)
	if err != nil {
		service.fail(err.Error())
		return
	}
	remoteJson, err := syncedConfigJson(remoteConfig)
	if err != nil {
		service.fail(err.Error())
		return
	}
	remoteHash := syncHash(remoteJson)
	switch {
	case remoteHash == local:
		service.synced(folder, local, stamp, remote)
	case localChanged && remoteHash == lastSynced:
		// 同步文件里没有新的改动，本机改了：写上去。
		service.push(folder, localJson, modified)
	case localChanged && modified.After(remote.updatedTime()):
		// 两边都改过，本机的改动更晚。
		service.push(folder, localJson, modified)
	default:
		service.apply(folder, localJson, remote, remoteHash, stamp)
	}
}

// synced 记下本机和同步文件夹 folder 里的同步文件一致：hash 是同步部分的摘要，stamp 是同步文件内容的摘要。
func (service *syncService) synced(folder, hash, stamp string, remote *syncFile) {
	service.mutex.Lock()
	service.lastSynced, service.lastStamp, service.lastFolder = hash, stamp, folder
	service.state, service.message = syncSynced, ""
	service.updated, service.device = remote.Updated, remote.Device
	service.mutex.Unlock()
	_ = service.onEngine(func() {
		state := service.engine.state
		if state.Synced != hash || state.SyncedFolder != folder {
			state.Synced, state.SyncedFolder = hash, folder
			service.engine.saveState()
		}
	})
}

// push 把本机配置里同步的部分写到同步文件，changed 是本机改动的时间（配置文件的修改时间）。
func (service *syncService) push(folder string, localJson []byte, changed time.Time) {
	if now := service.now(); changed.IsZero() || changed.After(now) {
		changed = now
	}
	file := syncFile{Format: syncFormat, Updated: changed.UTC().Format(time.RFC3339Nano), Device: service.this, Config: localJson}
	if err := writeSyncFile(folder, file); err != nil {
		service.fail(err.Error())
		return
	}
	service.synced(folder, syncHash(localJson), syncStamp(filepath.Join(folder, syncFileName)), &file)
	slog.Info("同步：已写入本机的配置", "folder", folder)
}

// apply 用同步文件里的配置替换本机的，本机的设置保留。本机的配置在这期间又改了时这次不替换，下一次再比。
func (service *syncService) apply(folder string, localJson []byte, remote *syncFile, remoteHash, stamp string) {
	var err error
	applied := false
	if service.onEngine(func() {
		current := service.engine.Config()
		if current == nil || service.engine.ConfigError() != "" {
			return
		}
		if now, marshalErr := syncedConfigJson(current); marshalErr != nil || !bytes.Equal(now, localJson) {
			return
		}
		var result *Config
		if result, err = withSynced(current, remote.Config); err != nil {
			return
		}
		if err = service.engine.SaveConfig(result); err != nil {
			return
		}
		applied = true
		if service.applied != nil {
			service.applied()
		}
	}) != nil {
		return
	}
	if err != nil {
		service.fail("应用同步来的配置失败：" + err.Error())
		return
	}
	if applied {
		service.synced(folder, remoteHash, stamp, remote)
		slog.Info("同步：应用了来自其他电脑的配置", "device", remote.Device)
	}
}

// Info 是设置页里同步的情况。
func (service *syncService) Info(config *Config) SyncInfo {
	info := SyncInfo{DefaultFolder: defaultSyncFolder(), This: service.this}
	if config != nil {
		info.Enabled = config.Sync.Enabled
		info.Folder, _ = syncFolderPath(config.Sync)
	}
	service.mutex.Lock()
	defer service.mutex.Unlock()
	info.State, info.Message, info.Updated, info.Device = service.state, service.message, service.updated, service.device
	switch {
	case !info.Enabled:
		info.State, info.Message = syncOff, ""
	case info.State == syncOff:
		// 刚开启，后台还没同步过。
		info.State = syncSyncing
	}
	if service.pending != nil {
		info.Folder, _ = syncFolderPath(service.pendingSync)
		var fields struct {
			Profiles []json.RawMessage `json:"profiles"`
		}
		_ = json.Unmarshal(service.pending.Config, &fields)
		info.Pending = &SyncPending{Device: service.pending.Device, Updated: service.pending.Updated, Profiles: len(fields.Profiles)}
		if config != nil {
			info.Pending.LocalProfiles = len(config.Profiles)
		}
	}
	return info
}

// saveSettings 在引擎线程上把同步设置写进配置文件：replace 不为空时整个换成它，否则只改同步设置。
func (service *syncService) saveSettings(settings SyncConfig, replace *Config) error {
	var err error
	if runErr := service.onEngine(func() {
		base := replace
		if base == nil {
			current := service.engine.Config()
			if current == nil || service.engine.ConfigError() != "" {
				err = errNoConfig
				return
			}
			base = current.Clone()
		}
		base.Sync = settings
		if err = service.engine.SaveConfig(base); err == nil && replace != nil && service.applied != nil {
			service.applied()
		}
	}); runErr != nil {
		return runErr
	}
	return err
}

// Enable 开启同步，folder 留空时用 OneDrive 里的文件夹。同步文件夹里已经有另一份不一样的配置时先不开启，
// 记下来等用户选择（见 Resolve）。
func (service *syncService) Enable(folder string) error {
	service.running.Lock()
	defer service.running.Unlock()
	settings := SyncConfig{Enabled: true, Folder: strings.TrimSpace(folder)}
	if err := validateSyncConfig(settings); err != nil {
		return err
	}
	path, err := syncFolderPath(settings)
	if err != nil {
		return err
	}
	config, _, problem := service.localConfig()
	if config == nil || problem != "" {
		return errNoConfig
	}
	service.mutex.Lock()
	service.pending = nil
	service.mutex.Unlock()
	resolveSyncConflicts(path)
	remote, err := readSyncFile(filepath.Join(path, syncFileName))
	if err != nil {
		return err
	}
	localJson, err := syncedConfigJson(config)
	if err != nil {
		return err
	}
	if remote != nil {
		remoteConfig, err := withSynced(config, remote.Config)
		if err != nil {
			return err
		}
		remoteJson, err := syncedConfigJson(remoteConfig)
		if err != nil {
			return err
		}
		if !bytes.Equal(remoteJson, localJson) {
			service.mutex.Lock()
			service.pending, service.pendingSync = remote, settings
			service.mutex.Unlock()
			slog.Info("同步：同步文件夹里已有另一份配置，等待选择", "folder", path, "device", remote.Device)
			return nil
		}
	}
	if err := service.saveSettings(settings, nil); err != nil {
		return err
	}
	if remote != nil {
		service.synced(path, syncHash(localJson), syncStamp(filepath.Join(path, syncFileName)), remote)
	} else {
		service.push(path, localJson, service.now())
	}
	slog.Info("同步：已开启", "folder", path)
	return nil
}

// Resolve 处理开启时同步文件夹里已有另一份配置的情况：folder 用同步文件夹里的替换本机的，merge 合并两边，
// local 用本机的覆盖同步文件夹里的，cancel 不开启。
func (service *syncService) Resolve(choice string) error {
	service.running.Lock()
	defer service.running.Unlock()
	service.mutex.Lock()
	remote, settings := service.pending, service.pendingSync
	service.mutex.Unlock()
	if remote == nil {
		return errors.New("没有等待处理的同步")
	}
	clearPending := func() {
		service.mutex.Lock()
		service.pending = nil
		service.mutex.Unlock()
	}
	if choice == "cancel" {
		clearPending()
		return nil
	}
	path, err := syncFolderPath(settings)
	if err != nil {
		return err
	}
	config, _, problem := service.localConfig()
	if config == nil || problem != "" {
		return errNoConfig
	}
	var result *Config
	switch choice {
	case "folder":
		result, err = withSynced(config, remote.Config)
	case "merge":
		if result, err = withSynced(config, remote.Config); err == nil {
			result, err = mergeSyncedConfigs(config, result)
		}
	case "local":
		result = config
	default:
		return fmt.Errorf("不认识的选择 %q", choice)
	}
	if err != nil {
		return err
	}
	resultJson, err := syncedConfigJson(result)
	if err != nil {
		return err
	}
	if err := service.saveSettings(settings, result); err != nil {
		return err
	}
	clearPending()
	if choice == "folder" {
		service.synced(path, syncHash(resultJson), syncStamp(filepath.Join(path, syncFileName)), remote)
	} else {
		service.push(path, resultJson, service.now())
	}
	slog.Info("同步：已开启", "folder", path, "choice", choice)
	return nil
}

// Disable 关闭同步。同步文件夹里的文件留着，别的电脑照常同步。
func (service *syncService) Disable() error {
	service.running.Lock()
	defer service.running.Unlock()
	service.mutex.Lock()
	service.pending = nil
	service.mutex.Unlock()
	config, _, problem := service.localConfig()
	if config == nil || problem != "" {
		return errNoConfig
	}
	settings := config.Sync
	settings.Enabled = false
	if err := service.saveSettings(settings, nil); err != nil {
		return err
	}
	service.mutex.Lock()
	service.state, service.message, service.lastStamp = syncOff, "", ""
	service.mutex.Unlock()
	slog.Info("同步：已关闭")
	return nil
}

// SyncNow 立即同步一次，同步文件没变也重新读一遍。
func (service *syncService) SyncNow() {
	service.mutex.Lock()
	service.lastStamp = ""
	service.mutex.Unlock()
	service.tick()
}

// Folder 是设置页上「打开文件夹」要打开的同步文件夹：等待选择时是要开启的那个，否则是配置里的。还没有时先建好。
func (service *syncService) Folder() (string, error) {
	service.mutex.Lock()
	pending, settings := service.pending != nil, service.pendingSync
	service.mutex.Unlock()
	if !pending {
		config, _, _ := service.localConfig()
		if config == nil {
			return "", errNoConfig
		}
		settings = config.Sync
	}
	folder, err := syncFolderPath(settings)
	if err != nil {
		return "", err
	}
	if err := os.MkdirAll(folder, 0o755); err != nil {
		return "", fmt.Errorf("建不了同步文件夹：%v", err)
	}
	return folder, nil
}

// ---------- 设置页 ----------

func (service *subscriptionService) EnableSync(folder string) error {
	return service.configSync.Enable(folder)
}

func (service *subscriptionService) ResolveSync(choice string) error {
	return service.configSync.Resolve(choice)
}

func (service *subscriptionService) DisableSync() error {
	return service.configSync.Disable()
}

func (service *subscriptionService) SyncNow() {
	service.configSync.SyncNow()
}
