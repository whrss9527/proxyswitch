//go:build windows

package main

import (
	"errors"
	"strings"
)

// 用户级环境变量 HTTP_PROXY / HTTPS_PROXY / NO_PROXY（HKCU\Environment）。
// 写完广播 WM_SETTINGCHANGE，之后新开的终端即可看到；已经打开的终端需要重开。

const environmentKey = `Environment`

var environmentProxyNames = []string{"HTTP_PROXY", "HTTPS_PROXY", "NO_PROXY"}

func setEnvironmentProxy(proxyUrl, noProxy string) error {
	key, err := createRegistryKey(hkeyCurrentUser, environmentKey)
	if err != nil {
		return err
	}
	defer key.Close()
	errs := []error{key.SetString("HTTP_PROXY", proxyUrl), key.SetString("HTTPS_PROXY", proxyUrl)}
	if strings.TrimSpace(noProxy) != "" {
		errs = append(errs, key.SetString("NO_PROXY", noProxy))
	} else {
		errs = append(errs, key.DeleteValue("NO_PROXY"))
	}
	broadcastSettingChange("Environment")
	return errors.Join(errs...)
}

func clearEnvironmentProxy() error {
	key, err := createRegistryKey(hkeyCurrentUser, environmentKey)
	if err != nil {
		return err
	}
	defer key.Close()
	var errs []error
	for _, name := range environmentProxyNames {
		errs = append(errs, key.DeleteValue(name))
	}
	broadcastSettingChange("Environment")
	return errors.Join(errs...)
}

// readEnvironmentProxy 返回用户级 HTTP_PROXY / HTTPS_PROXY / NO_PROXY 的当前值。
func readEnvironmentProxy() map[string]string {
	values := map[string]string{}
	for _, name := range environmentProxyNames {
		values[name] = readRegistryString(hkeyCurrentUser, environmentKey, name)
	}
	return values
}
