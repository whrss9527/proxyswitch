# ProxySwitch 构建脚本。在 Linux / macOS 上交叉编译 Windows 版，纯 Go，不需要 CGO。
#   make            编译 amd64 和 arm64 两个 exe 到 dist/
#   make test       静态检查 + 单元测试
#   make ui-test    设置页的浏览器自动化测试（需要 Python Playwright）
#   make dev        在本机预览设置页（系统设置用内存模拟）
#   make resources  重新生成 exe 的图标、版本信息和清单资源（需要 goversioninfo）
#   发布：make VERSION=2.1.0 resources windows

VERSION ?= 2.0.0
GO ?= go
GOVERSIONINFO ?= $(shell $(GO) env GOPATH)/bin/goversioninfo
LDFLAGS := -H windowsgui -s -w -X main.appVersion=$(VERSION)

.PHONY: windows amd64 arm64 resources icons test vet ui-test dev clean

windows: amd64 arm64

amd64:
	GOOS=windows GOARCH=amd64 CGO_ENABLED=0 $(GO) build -trimpath -ldflags "$(LDFLAGS)" -o dist/ProxySwitch.exe .

arm64:
	GOOS=windows GOARCH=arm64 CGO_ENABLED=0 $(GO) build -trimpath -ldflags "$(LDFLAGS)" -o dist/ProxySwitch-arm64.exe .

resources:
	$(GOVERSIONINFO) -64 -o resource_windows_amd64.syso -file-version $(VERSION).0 -product-version $(VERSION) -propagate-ver-strings versioninfo.json
	$(GOVERSIONINFO) -arm -64 -o resource_windows_arm64.syso -file-version $(VERSION).0 -product-version $(VERSION) -propagate-ver-strings versioninfo.json

icons:
	python3 tools/make_icons.py

vet:
	$(GO) vet ./...
	GOOS=windows GOARCH=amd64 $(GO) vet ./...
	GOOS=windows GOARCH=arm64 $(GO) vet ./...

test: vet
	$(GO) test ./...

ui-test:
	python3 tools/ui_test.py

dev:
	$(GO) run . --dev-settings --web=web

clean:
	rm -rf dist
