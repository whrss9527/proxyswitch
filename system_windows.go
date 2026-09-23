//go:build windows

package main

// windowsSystem 是 ProxySystem 的 Windows 实现：改真实的系统代理、用户环境变量、git 和 npm 配置。
type windowsSystem struct{}

func (windowsSystem) ReadSystemProxy() (SystemProxyState, error) {
	state, _, err := readSystemProxy()
	return state, err
}

func (windowsSystem) WriteSystemProxy(state SystemProxyState) error {
	return writeSystemProxy(state)
}

func (windowsSystem) SetEnvProxy(proxyUrl, noProxy string) error {
	return setEnvironmentProxy(proxyUrl, noProxy)
}

func (windowsSystem) ClearEnvProxy() error {
	return clearEnvironmentProxy()
}

func (windowsSystem) SetGitProxy(proxyUrl string) error {
	return setGitProxy(proxyUrl)
}

func (windowsSystem) ClearGitProxy() error {
	return clearGitProxy()
}

func (windowsSystem) SetNpmProxy(proxyUrl, noProxy string) error {
	return setNpmProxy(proxyUrl, noProxy)
}

func (windowsSystem) ClearNpmProxy() error {
	return setNpmProxy("", "")
}
