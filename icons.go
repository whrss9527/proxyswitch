package main

import _ "embed"

// 托盘图标（.ico，内含多种尺寸），编译时嵌入 exe。

//go:embed assets/on.ico
var iconOnICO []byte

//go:embed assets/off.ico
var iconOffICO []byte
