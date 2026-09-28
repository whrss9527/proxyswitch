package main

import "testing"

func TestWslConfig(t *testing.T) {
	for _, item := range []struct {
		name, text, want string
	}{
		{"没有文件", "", "[wsl2]\nnetworkingMode=mirrored\nautoProxy=true\n"},
		{"已有其他设置", "[wsl2]\nmemory=8GB\n", "[wsl2]\nnetworkingMode=mirrored\nautoProxy=true\nmemory=8GB\n"},
		{"改掉 NAT 和关掉的自动代理", "[wsl2]\r\nnetworkingMode = NAT\r\nautoProxy=false # 公司网络\r\n", "[wsl2]\r\nnetworkingMode=mirrored\r\nautoProxy=true\r\n"},
		{"别的段落保留", "# 注释\n[experimental]\nautoMemoryReclaim=gradual\n\n", "# 注释\n[experimental]\nautoMemoryReclaim=gradual\n\n[wsl2]\nnetworkingMode=mirrored\nautoProxy=true\n"},
		{"带 BOM", "\ufeff[WSL2]\nnetworkingmode=mirrored\n", "\ufeff[WSL2]\nautoProxy=true\nnetworkingMode=mirrored\n"},
		{"别的段落里的同名设置不动", "[experimental]\nnetworkingMode=nat\n[wsl2]\n", "[experimental]\nnetworkingMode=nat\n[wsl2]\nnetworkingMode=mirrored\nautoProxy=true\n"},
	} {
		got := setWslProxy(item.text)
		if got != item.want {
			t.Errorf("%s：\n%q\n应为\n%q", item.name, got, item.want)
		}
		if mirrored, autoProxy := wslConfigValues(got); !mirrored || !autoProxy {
			t.Errorf("%s：设置后应读到镜像网络和自动代理", item.name)
		}
		if setWslProxy(got) != got {
			t.Errorf("%s：已经设置好时不应再改", item.name)
		}
		if mirrored, _ := wslConfigValues(resetWslProxy(got)); mirrored {
			t.Errorf("%s：撤销后应恢复默认的 NAT 网络", item.name)
		}
	}
	for _, item := range []struct {
		name, text, want string
	}{
		{"一键设置写的", "[wsl2]\nnetworkingMode=mirrored\nautoProxy=true\n", "[wsl2]\n"},
		{"其他设置和段落保留", "\ufeff# 注释\r\n[wsl2]\r\nmemory=8GB\r\nnetworkingMode = mirrored\r\n[experimental]\r\nautoProxy=false\r\n", "\ufeff# 注释\r\n[wsl2]\r\nmemory=8GB\r\n[experimental]\r\nautoProxy=false\r\n"},
		{"没有设置过", "[wsl2]\nmemory=8GB", "[wsl2]\nmemory=8GB"},
	} {
		if got := resetWslProxy(item.text); got != item.want {
			t.Errorf("撤销（%s）：\n%q\n应为\n%q", item.name, got, item.want)
		}
	}
	if mirrored, autoProxy := wslConfigValues(""); mirrored || !autoProxy {
		t.Error("没有设置时默认是 NAT 网络、自动代理开启")
	}
	if mirrored, autoProxy := wslConfigValues("[wsl2]\nnetworkingMode=mirrored\nautoProxy=false\n"); !mirrored || autoProxy {
		t.Error("应读出关掉的自动代理")
	}
	if (WslInfo{Mirrored: true}).Ready() || !(WslInfo{Mirrored: true, AutoProxy: true}).Ready() {
		t.Error("镜像网络和自动代理都开着才算设置好")
	}
}
