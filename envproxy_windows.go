//go:build windows

package main

import (
	"fmt"
	"strings"
)

// 用户级环境变量 HTTP_PROXY / HTTPS_PROXY / NO_PROXY（写 HKCU\Environment）。
// 写完广播 WM_SETTINGCHANGE，之后新开的终端/程序即可看到；已经打开的终端需要重开。

const envRegKey = `Environment`

var envProxyNames = []string{"HTTP_PROXY", "HTTPS_PROXY", "NO_PROXY"}

func setUserEnvProxy(proxyURL, noProxy string) error {
	k, err := regCreate(hkeyCurrentUser, envRegKey)
	if err != nil {
		return err
	}
	defer k.close()
	var errs []string
	if err := k.setString("HTTP_PROXY", proxyURL); err != nil {
		errs = append(errs, err.Error())
	}
	if err := k.setString("HTTPS_PROXY", proxyURL); err != nil {
		errs = append(errs, err.Error())
	}
	if strings.TrimSpace(noProxy) != "" {
		if err := k.setString("NO_PROXY", noProxy); err != nil {
			errs = append(errs, err.Error())
		}
	} else {
		if err := k.deleteValue("NO_PROXY"); err != nil {
			errs = append(errs, err.Error())
		}
	}
	broadcastSettingChange("Environment")
	if len(errs) > 0 {
		return fmt.Errorf("%s", strings.Join(errs, "; "))
	}
	return nil
}

func clearUserEnvProxy() error {
	k, err := regCreate(hkeyCurrentUser, envRegKey)
	if err != nil {
		return err
	}
	defer k.close()
	var errs []string
	for _, n := range envProxyNames {
		if err := k.deleteValue(n); err != nil {
			errs = append(errs, err.Error())
		}
	}
	broadcastSettingChange("Environment")
	if len(errs) > 0 {
		return fmt.Errorf("%s", strings.Join(errs, "; "))
	}
	return nil
}

// readUserEnvProxy 返回当前用户级 HTTPS_PROXY/HTTP_PROXY 的值（没有则为空）。
func readUserEnvProxy() string {
	k, err := regOpen(hkeyCurrentUser, envRegKey, keyRead)
	if err != nil {
		return ""
	}
	defer k.close()
	if v, err := k.getString("HTTPS_PROXY"); err == nil && v != "" {
		return v
	}
	if v, err := k.getString("HTTP_PROXY"); err == nil {
		return v
	}
	return ""
}
