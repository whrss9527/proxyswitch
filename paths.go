package main

import (
	"os"
	"path/filepath"
)

// appVersion 在发布构建时由 -ldflags "-X main.appVersion=x.y.z" 注入。
var appVersion = "2.0.0"

const (
	appName        = "ProxySwitch"
	configFileName = "config.jsonc"
	stateFileName  = "state.json"
	logFileName    = "proxyswitch.log"
	repositoryUrl  = "https://github.com/whrss9527/proxyswitch"
)

// Paths 是配置、状态、日志文件的位置。
// exe 同目录下存在 config.jsonc 时为便携模式，所有文件放在 exe 目录；否则放在 %APPDATA%\ProxySwitch。
type Paths struct {
	Dir      string
	Config   string
	State    string
	Log      string
	Portable bool
}

func resolvePaths() Paths {
	directory := ""
	portable := false
	if executable, err := os.Executable(); err == nil {
		executableDir := filepath.Dir(executable)
		if fileExists(filepath.Join(executableDir, configFileName)) {
			directory = executableDir
			portable = true
		}
	}
	if directory == "" {
		base := os.Getenv("APPDATA")
		if base == "" {
			if configDir, err := os.UserConfigDir(); err == nil {
				base = configDir
			} else {
				base = "."
			}
		}
		directory = filepath.Join(base, appName)
	}
	return pathsIn(directory, portable)
}

func pathsIn(directory string, portable bool) Paths {
	return Paths{
		Dir:      directory,
		Config:   filepath.Join(directory, configFileName),
		State:    filepath.Join(directory, stateFileName),
		Log:      filepath.Join(directory, logFileName),
		Portable: portable,
	}
}

func fileExists(path string) bool {
	info, err := os.Stat(path)
	return err == nil && !info.IsDir()
}
