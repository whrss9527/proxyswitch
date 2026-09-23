package main

// memorySystem 把代理设置保存在内存里，用于开发模式和测试，不改动本机任何设置。
// Failures 按生效范围注入错误，用来测试部分失败的处理。
type memorySystem struct {
	System   SystemProxyState
	Env      map[string]string
	Git      string
	Npm      map[string]string
	Failures map[string]error
	Writes   int
}

func newMemorySystem() *memorySystem {
	return &memorySystem{
		System:   SystemProxyState{AutoDetect: true},
		Env:      map[string]string{},
		Npm:      map[string]string{},
		Failures: map[string]error{},
	}
}

func (system *memorySystem) ReadSystemProxy() (SystemProxyState, error) {
	return system.System, nil
}

func (system *memorySystem) WriteSystemProxy(state SystemProxyState) error {
	if err := system.Failures[targetSystem]; err != nil {
		return err
	}
	system.Writes++
	system.System = state
	return nil
}

func (system *memorySystem) SetEnvProxy(proxyUrl, noProxy string) error {
	if err := system.Failures[targetEnv]; err != nil {
		return err
	}
	system.Env = map[string]string{"HTTP_PROXY": proxyUrl, "HTTPS_PROXY": proxyUrl, "NO_PROXY": noProxy}
	return nil
}

func (system *memorySystem) ClearEnvProxy() error {
	if err := system.Failures[targetEnv]; err != nil {
		return err
	}
	system.Env = map[string]string{}
	return nil
}

func (system *memorySystem) SetGitProxy(proxyUrl string) error {
	if err := system.Failures[targetGit]; err != nil {
		return err
	}
	system.Git = proxyUrl
	return nil
}

func (system *memorySystem) ClearGitProxy() error {
	if err := system.Failures[targetGit]; err != nil {
		return err
	}
	system.Git = ""
	return nil
}

func (system *memorySystem) SetNpmProxy(proxyUrl, noProxy string) error {
	if err := system.Failures[targetNpm]; err != nil {
		return err
	}
	system.Npm = map[string]string{"proxy": proxyUrl, "https-proxy": proxyUrl, "noproxy": noProxy}
	return nil
}

func (system *memorySystem) ClearNpmProxy() error {
	if err := system.Failures[targetNpm]; err != nil {
		return err
	}
	system.Npm = map[string]string{}
	return nil
}
