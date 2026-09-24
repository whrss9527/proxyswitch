# ProxySwitch 构建脚本。在 Linux / macOS 上交叉编译 Windows 版，纯 Go，不需要 CGO。
#   make            编译 amd64 和 arm64 两个 exe 到 dist/
#   make test       静态检查 + 单元测试
#   make ui-test    设置页的浏览器自动化测试（需要 Python Playwright）
#   make dev        在本机预览设置页（系统设置用内存模拟）；CORE=mihomo 程序的路径 可以试用订阅
#   make resources  重新生成 exe 的图标、版本信息和清单资源（需要 goversioninfo）
#   make core-sha256  下载固定版本的官方内核（Windows 的 zip），输出它们的 SHA-256
#   发布：eval "$$(make -s core-sha256)"; make VERSION=2.1.0 CORE_SHA256_AMD64=... CORE_SHA256_ARM64=... resources windows

VERSION ?= 2.0.0
GO ?= go
GOVERSIONINFO ?= $(shell $(GO) env GOPATH)/bin/goversioninfo
# 官方内核 zip 的 SHA-256，Windows 版在程序里下载内核时按它校验；留空则这个版本不能在程序里下载内核。
CORE_SHA256_AMD64 ?=
CORE_SHA256_ARM64 ?=
CORE_VERSION := $(shell sed -n 's/^const coreVersion = "\(.*\)"$$/\1/p' core_config.go)
CORE_RELEASE := https://github.com/MetaCubeX/mihomo/releases/download/$(CORE_VERSION)
LDFLAGS := -H windowsgui -s -w -X main.appVersion=$(VERSION) -X main.coreSha256Amd64=$(CORE_SHA256_AMD64) -X main.coreSha256Arm64=$(CORE_SHA256_ARM64)

.PHONY: windows amd64 arm64 resources icons test vet ui-test dev core-sha256 clean

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
	$(GO) run . --dev-settings --web=web $(if $(CORE),--core=$(CORE))

# 下载到 dist/core，并检查 zip 里有程序，输出可以直接 eval 的变量。
core-sha256:
	@mkdir -p dist/core
	@set -e; for arch in amd64-v1 arm64; do \
		curl -fsSL -o dist/core/mihomo-windows-$$arch.zip $(CORE_RELEASE)/mihomo-windows-$$arch-$(CORE_VERSION).zip; \
		unzip -l dist/core/mihomo-windows-$$arch.zip | grep -q '\.exe$$' || { echo "dist/core/mihomo-windows-$$arch.zip 里没有程序" >&2; exit 1; }; \
	done
	@echo "CORE_SHA256_AMD64=$$(sha256sum dist/core/mihomo-windows-amd64-v1.zip | cut -d' ' -f1)"
	@echo "CORE_SHA256_ARM64=$$(sha256sum dist/core/mihomo-windows-arm64.zip | cut -d' ' -f1)"

clean:
	rm -rf dist
