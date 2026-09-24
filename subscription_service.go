package main

import (
	"bytes"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"time"
)

// 订阅的后台下载和设置页上的订阅操作，Windows 版和开发模式共用。onEngine 在引擎所在的线程上执行
// （Windows 上排队到 UI 线程，开发模式加锁执行）；网络请求和等待内核都在调用方的 goroutine 里进行。

const (
	subscriptionCheckInterval = 10 * time.Minute
	coreWaitTimeout           = 20 * time.Second
	geoDownloadTimeout        = 5 * time.Minute
)

type subscriptionService struct {
	engine   *Engine
	core     *Core
	onEngine func(action func()) error
	kick     chan struct{}

	mutex      sync.Mutex
	installing *InstallProgress
}

func newSubscriptionService(engine *Engine, core *Core, onEngine func(action func()) error) *subscriptionService {
	service := &subscriptionService{engine: engine, core: core, onEngine: onEngine, kick: make(chan struct{}, 1)}
	engine.core = core
	engine.downloadsNeeded = service.Kick
	return service
}

// Kick 让后台尽快检查需要下载的订阅和地理数据。
func (service *subscriptionService) Kick() {
	select {
	case service.kick <- struct{}{}:
	default:
	}
}

// Run 在后台下载到期的订阅和地理数据，有新订阅时立即下载，否则每隔一段时间检查一次。
func (service *subscriptionService) Run() {
	ticker := time.NewTicker(subscriptionCheckInterval)
	defer ticker.Stop()
	for {
		service.downloadDue()
		select {
		case <-service.kick:
		case <-ticker.C:
		}
	}
}

func (service *subscriptionService) downloadDue() {
	var due []Profile
	var paths []string
	if service.onEngine(func() { due, paths = service.engine.SubscriptionsDue(), service.engine.DownloadPaths() }) != nil {
		return
	}
	for _, profile := range due {
		result, err := fetchSubscription(profile.Subscription, paths)
		_ = service.onEngine(func() { _ = service.engine.RecordSubscription(profile.Id, profile.Subscription, result, err) })
	}
	geoDue := false
	// 订阅下载后内核可能已经可用，地理数据可以经内核下载。
	if service.onEngine(func() { geoDue, paths = service.engine.GeoDue(), service.engine.DownloadPaths() }) != nil || !geoDue {
		return
	}
	err := downloadGeoData(service.engine.paths.Core, paths)
	_ = service.onEngine(func() { service.engine.RecordGeoDownload(err) })
}

// ---------- 设置页上的订阅操作 ----------

// SubscriptionCheck 是在编辑配置时检查订阅地址的结果。
type SubscriptionCheck struct {
	Format   string `json:"format"`
	Nodes    int    `json:"nodes"`
	Name     string `json:"name,omitempty"`
	Upload   int64  `json:"upload,omitempty"`
	Download int64  `json:"download,omitempty"`
	Total    int64  `json:"total,omitempty"`
	Expire   int64  `json:"expire,omitempty"`
}

// subscriptionProfile 在引擎线程上找到订阅配置，同时取出下载用的网络路径。
func (service *subscriptionService) subscriptionProfile(profileId string) (profile Profile, paths []string, err error) {
	if runErr := service.onEngine(func() {
		config := service.engine.Config()
		if config == nil {
			err = errNoConfig
			return
		}
		found := config.FindProfileById(profileId)
		if found == nil || !found.IsSubscription() {
			err = errors.New("没有这个订阅配置")
			return
		}
		profile, paths = *found, service.engine.DownloadPaths()
	}); runErr != nil {
		return profile, nil, runErr
	}
	return profile, paths, err
}

// SubscriptionNodes 列出订阅里的节点和最近测得的延迟。
func (service *subscriptionService) SubscriptionNodes(profileId string) (CoreNodes, error) {
	if _, _, err := service.subscriptionProfile(profileId); err != nil {
		return CoreNodes{}, err
	}
	return service.core.Nodes(profileId)
}

// SelectNode 选中节点（空表示自动选择）：记在配置文件里，等内核切换后返回最新的节点列表。
func (service *subscriptionService) SelectNode(profileId, node string) (CoreNodes, error) {
	var err error
	var generation int
	if runErr := service.onEngine(func() {
		err = service.engine.SelectNode(profileId, node)
		generation = service.engine.CoreGeneration()
	}); runErr != nil {
		return CoreNodes{}, runErr
	}
	if err != nil {
		return CoreNodes{}, err
	}
	if err := service.core.Wait(generation, coreWaitTimeout); err != nil {
		return CoreNodes{}, err
	}
	return service.core.Nodes(profileId)
}

// TestNodes 测试订阅里所有节点的延迟，返回带测试结果的节点列表。
func (service *subscriptionService) TestNodes(profileId string) (CoreNodes, error) {
	if _, _, err := service.subscriptionProfile(profileId); err != nil {
		return CoreNodes{}, err
	}
	testUrl := defaultTestUrl
	_ = service.onEngine(func() {
		if config := service.engine.Config(); config != nil {
			testUrl = config.TestUrl
		}
	})
	if _, err := service.core.TestDelays(profileId, testUrl); err != nil {
		return CoreNodes{}, fmt.Errorf("测速失败：%v", err)
	}
	return service.core.Nodes(profileId)
}

// UpdateSubscription 立即重新下载订阅，等内核读到新的节点后返回。
func (service *subscriptionService) UpdateSubscription(profileId string) error {
	profile, paths, err := service.subscriptionProfile(profileId)
	if err != nil {
		return err
	}
	result, downloadErr := fetchSubscription(profile.Subscription, paths)
	var generation int
	if runErr := service.onEngine(func() {
		err = service.engine.RecordSubscription(profile.Id, profile.Subscription, result, downloadErr)
		generation = service.engine.CoreGeneration()
	}); runErr != nil {
		return runErr
	}
	if err != nil {
		return err
	}
	return service.core.Wait(generation, coreWaitTimeout)
}

// CheckSubscription 下载订阅检查地址是否可用，给编辑配置的对话框显示节点数和流量信息，不保存。
func (service *subscriptionService) CheckSubscription(address string) (SubscriptionCheck, error) {
	var paths []string
	if err := service.onEngine(func() { paths = service.engine.DownloadPaths() }); err != nil {
		return SubscriptionCheck{}, err
	}
	result, err := fetchSubscription(address, paths)
	if err != nil {
		return SubscriptionCheck{}, err
	}
	return SubscriptionCheck{Format: result.Format, Nodes: result.Nodes, Name: result.Name, Upload: result.Info.Upload, Download: result.Info.Download, Total: result.Info.Total, Expire: result.Info.Expire}, nil
}

// InstallCore 下载内核，下载进度经 CoreInstalling 提供；装好后让内核用上新程序。
func (service *subscriptionService) InstallCore() error {
	service.mutex.Lock()
	if service.installing != nil {
		service.mutex.Unlock()
		return errors.New("正在下载内核")
	}
	service.installing = &InstallProgress{}
	service.mutex.Unlock()
	defer func() {
		service.mutex.Lock()
		service.installing = nil
		service.mutex.Unlock()
	}()
	var paths []string
	if err := service.onEngine(func() { paths = service.engine.DownloadPaths() }); err != nil {
		return err
	}
	err := installCore(service.engine.paths.Core, paths, func(received, total int64) {
		service.mutex.Lock()
		service.installing = &InstallProgress{Received: received, Total: total}
		service.mutex.Unlock()
	})
	if err != nil {
		return err
	}
	var generation int
	if err := service.onEngine(func() {
		service.engine.syncCore()
		generation = service.engine.CoreGeneration()
		service.Kick()
	}); err != nil {
		return err
	}
	return service.core.Wait(generation, coreWaitTimeout)
}

// CoreInstalling 返回正在下载的内核的进度，没有在下载时返回 nil。
func (service *subscriptionService) CoreInstalling() *InstallProgress {
	service.mutex.Lock()
	defer service.mutex.Unlock()
	return service.installing
}

// fillState 补上设置页状态里内核的运行情况，以及正在使用的订阅实际在用的节点。
func (service *subscriptionService) fillState(state *SettingsState) {
	status := service.core.Status()
	state.Core.Running, state.Core.Error = status.Running, status.Error
	state.Core.Downloadable = coreDownloadable()
	state.Core.Installing = service.CoreInstalling()
	if state.Status.State != statusOn || state.Config == nil || !status.Running {
		return
	}
	if profile := state.Config.FindProfile(state.Status.Profile); profile != nil && profile.IsSubscription() {
		state.Status.Node = service.core.CurrentNode(profile.Id)
	}
}

// delaysNotice 是托盘菜单里「全部测速」的结果：有几个节点能用，最快的是哪个。
func delaysNotice(profile Profile, delays map[string]int, err error) Notice {
	if err != nil {
		return Notice{Level: noticeWarning, Title: "测速失败", Text: profile.Name + "\n" + err.Error()}
	}
	if len(delays) == 0 {
		return Notice{Level: noticeWarning, Title: profile.Name + "：没有能用的节点", Text: "所有节点都连不上，订阅可能已经过期", Page: "proxies"}
	}
	fastest, best := "", 0
	for name, delay := range delays {
		if fastest == "" || delay < best || (delay == best && name < fastest) {
			fastest, best = name, delay
		}
	}
	return Notice{Level: noticeInfo, Title: fmt.Sprintf("%s：%d 个节点能用", profile.Name, len(delays)), Text: fmt.Sprintf("最快：%s %d ms", fastest, max(1, best)), Icon: iconStateOn, Color: profile.Color}
}

// ---------- 地理数据 ----------

// downloadGeoData 下载大陆直连规则用到的地理数据到内核的工作目录：每个文件依次尝试各个下载地址和网络路径，
// 检查内容后再放到内核查找的位置。
func downloadGeoData(dir string, paths []string) error {
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	for _, geo := range coreGeoFiles {
		target := filepath.Join(dir, geo.Name)
		if fileExists(target) {
			continue
		}
		var lastErr error
		for _, address := range geo.Urls {
			for _, proxyUrl := range paths {
				var buffer bytes.Buffer
				lastErr = copyDownload(updateClient(proxyUrl, geoDownloadTimeout), address, &buffer, 0, nil)
				if lastErr == nil {
					lastErr = validateGeoData(geo.Name, buffer.Bytes())
				}
				if lastErr == nil {
					lastErr = writeFileAtomically(target, buffer.Bytes())
				}
				if lastErr == nil {
					break
				}
			}
			if lastErr == nil {
				break
			}
		}
		if lastErr != nil {
			return fmt.Errorf("下载 %s 失败：%v", geo.Name, lastErr)
		}
	}
	return nil
}

// validateGeoData 粗略检查地理数据的内容，避免把出错时返回的网页当成数据：
// mmdb 文件末尾有 MaxMind 的元数据标记，geosite.dat 是以 0x0a 开头的 protobuf。
func validateGeoData(name string, data []byte) error {
	switch {
	case len(data) < 64<<10:
		return errors.New("文件太小，不是有效的数据")
	case filepath.Ext(name) == ".mmdb" && !bytes.Contains(data[max(0, len(data)-256<<10):], []byte("\xab\xcd\xefMaxMind.com")):
		return errors.New("不是有效的 mmdb 文件")
	case filepath.Ext(name) == ".dat" && data[0] != 0x0a:
		return errors.New("不是有效的 geosite 文件")
	}
	return nil
}

func writeFileAtomically(path string, data []byte) error {
	temporary := path + ".download"
	if err := os.WriteFile(temporary, data, 0o644); err != nil {
		return err
	}
	if err := os.Rename(temporary, path); err != nil {
		_ = os.Remove(temporary)
		return err
	}
	return nil
}
