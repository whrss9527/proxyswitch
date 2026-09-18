package main

import (
	"os"
	"path/filepath"
)

const (
	appName        = "ProxySwitch"
	appVersion     = "1.1.0"
	configFileName = "config.jsonc"
	stateFileName  = "state.json"
	logFileName    = "proxyswitch.log"
)

// Paths 是配置、状态、日志文件的位置。
// 便携模式：exe 同目录下存在 config.jsonc 时，一切文件都放在 exe 目录；
// 否则放在 %APPDATA%\ProxySwitch。
type Paths struct {
	Dir      string
	Config   string
	State    string
	Log      string
	Portable bool
}

func resolvePaths() Paths {
	var dir string
	portable := false
	if exe, err := os.Executable(); err == nil {
		exeDir := filepath.Dir(exe)
		if fileExists(filepath.Join(exeDir, configFileName)) {
			dir = exeDir
			portable = true
		}
	}
	if dir == "" {
		base := os.Getenv("APPDATA")
		if base == "" {
			if d, err := os.UserConfigDir(); err == nil {
				base = d
			} else {
				base = "."
			}
		}
		dir = filepath.Join(base, appName)
	}
	return Paths{
		Dir:      dir,
		Config:   filepath.Join(dir, configFileName),
		State:    filepath.Join(dir, stateFileName),
		Log:      filepath.Join(dir, logFileName),
		Portable: portable,
	}
}

func fileExists(path string) bool {
	st, err := os.Stat(path)
	return err == nil && !st.IsDir()
}
