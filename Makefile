# ==============================================================================
#  EasySB Makefile
#
#  常用入口 / the usual entry points:
#    make            构建二进制（等价于 make build）
#    make check      提交前的完整关卡：格式 + vet + 测试
#    make dist       交叉编译发布用的全部架构
#
#  版本号由 VERSION 经 go:embed 编进二进制，所以这里不传 -X main.version；构建标签
#  只有 release/TAGS 一处定义，构建、测试、发布读的都是同一个文件。
#  The version is embedded from VERSION, so nothing passes -X main.version, and the
#  build tag set lives only in release/TAGS: build, test and dist all read that file.
# ==============================================================================

GO      ?= go
BINARY  ?= easysb
DIST    ?= dist

# 标签与版本各读一处文件，绝不在 Makefile 里另抄一份。
# Tags and version each come from one file; they are never re-typed here.
TAGS    ?= $(shell tr -d '[:space:]' < release/TAGS)
VERSION := $(shell tr -d '[:space:]' < VERSION 2>/dev/null)
# COMMIT 可用命令行覆盖（CI 传入完整的 GITHUB_SHA）。
# COMMIT can be overridden on the command line (CI passes the full GITHUB_SHA).
COMMIT  ?= $(shell git rev-parse --short HEAD 2>/dev/null)

# 本地构建保留符号表，便于调试；发布构建去掉，与发布工作流一致。
# A local build keeps symbols for debugging; a release build strips them, matching CI.
LDFLAGS         := $(if $(strip $(COMMIT)),-X main.commit=$(COMMIT))
RELEASE_LDFLAGS := -s -w -checklinkname=0 $(LDFLAGS)

# 发布资产的架构名，与发布工作流一致（armv7 在 Go 里是 GOARCH=arm + GOARM=7）。
# Release asset architectures, matching CI (armv7 is GOARCH=arm with GOARM=7 in Go).
ARCHES := amd64 arm64 armv7 386 riscv64 s390x

# 资产名到 Go 目标三元组的映射，只定义一次，dist 与 dist-asset 共用。
# The asset-name to Go-target mapping, defined once and shared by dist and dist-asset.
GOARCH_amd64   := amd64
GOARCH_arm64   := arm64
GOARCH_armv7   := arm
GOARCH_386     := 386
GOARCH_riscv64 := riscv64
GOARCH_s390x   := s390x
GOARM_armv7    := 7

# 打包元数据 / package metadata. The three formats install the same tree to the
# same paths, so the only per-format facts are the package format itself and that
# format's architecture names; the three mappings sit together and no name is ever
# re-typed in the workflow.
PKG_NAME       ?= easysb
PKG_MAINTAINER ?= MinimaxFlora <zj18139624826@gmail.com>
PKG_VENDOR     ?= MinimaxFlora
PKG_LICENSE    ?= GPL-3.0-or-later
PKG_URL        ?= https://github.com/MinimaxFlora/EasySB
PKG_DESC       ?= EasySB: a sing-box panel with the core compiled in
PKG_SUMMARY    ?= A sing-box panel with the core compiled in
PKG_EXEC       := /usr/bin/easysb
# 三种格式共用的打包暂存树 / the staging tree every format is built from.
STAGE_DIR      ?= $(DIST)/stage
APT_DIR        ?= $(DIST)/apt
# 签名可选：CI 里存在 GPG_PRIVATE_KEY 密钥时会导入并用它签名 / signing is optional:
# when CI has imported a GPG key it is passed here, otherwise the index stays unsigned.
GPG_KEY_ID     ?=

# Debian 架构名 / Debian architecture names: armv7 ships as armhf, 386 as i386.
DEBARCH_amd64   := amd64
DEBARCH_arm64   := arm64
DEBARCH_armv7   := armhf
DEBARCH_386     := i386
DEBARCH_riscv64 := riscv64
DEBARCH_s390x   := s390x

# RPM 架构名 / RPM architecture names, shared by Fedora, RHEL and openSUSE.
RPMARCH_amd64   := x86_64
RPMARCH_arm64   := aarch64
RPMARCH_armv7   := armv7hl
RPMARCH_386     := i686
RPMARCH_riscv64 := riscv64
RPMARCH_s390x   := s390x

# pacman 架构名 / pacman architecture names. Arch ships no i386 and no s390x, so the
# pacman package is only built for the architectures Arch actually has.
PACMAN_ARCHES      := amd64 arm64 armv7 riscv64
PACMANARCH_amd64   := x86_64
PACMANARCH_arm64   := aarch64
PACMANARCH_armv7   := armv7h
PACMANARCH_riscv64 := riscv64

DEB_ARCHS := $(foreach a,$(ARCHES),$(DEBARCH_$(a)))

comma := ,
empty :=
space := $(empty) $(empty)

.DEFAULT_GOAL := build

.PHONY: build build-plain run test test-plain test-race vet fmt fmt-check \
        lint check render screens dist dist-asset release-matrix install tidy \
        version pkg-stage deb deb-asset rpm rpm-asset pacman pacman-asset \
        packages-asset apt-index help clean

# --- 构建 / Build -------------------------------------------------------------

build: ## 构建二进制（带 release/TAGS 标签）
	$(GO) build -trimpath -tags "$(TAGS)" -ldflags "$(LDFLAGS)" -o $(BINARY) .

build-plain: ## 不带标签构建，便于快速迭代
	$(GO) build -trimpath -o $(BINARY) .

# --- 运行 / Run ---------------------------------------------------------------

run: build ## 构建后启动面板
	./$(BINARY)

render: build ## 渲染一帧桌面版式并退出
	./$(BINARY) --render --width 100 --height 40

screens: build ## 渲染所有页面并校验版式（需要 python3）
	python3 scripts/layout_check.py ./$(BINARY)

# --- 校验 / Checks ------------------------------------------------------------

test: ## 运行全部测试（带 release/TAGS 标签）
	$(GO) test -tags "$(TAGS)" ./...

test-plain: ## 不带标签运行测试（覆盖无流量统计的构建）
	$(GO) test ./...

test-race: ## 带竞态检测运行测试
	$(GO) test -tags "$(TAGS)" -race ./...

vet: ## go vet 静态检查
	$(GO) vet ./...

fmt: ## 就地格式化
	gofmt -w .

fmt-check: ## 校验格式，有差异即失败
	@unformatted="$$(gofmt -l .)"; \
	if [ -n "$$unformatted" ]; then \
		echo "需要 gofmt / gofmt needed:"; \
		echo "$$unformatted"; \
		exit 1; \
	fi
	@echo "gofmt: 干净 / clean"

lint: fmt-check vet ## 格式 + vet

check: lint test ## 提交前的完整关卡 / the pre-commit gate

# --- 发布 / Release -----------------------------------------------------------

dist: ## 交叉编译全部发布架构到 dist/
	@set -e; for asset in $(ARCHES); do \
		$(MAKE) --no-print-directory dist-asset ASSET=$$asset; \
	done
	@ls -lh $(DIST)

dist-asset: ## 交叉编译单个发布架构（ASSET=amd64 / arm64 / armv7 / 386 / riscv64 / s390x）
	@test -n "$(ASSET)" || { echo "ASSET 未设置 / ASSET required, one of: $(ARCHES)"; exit 1; }
	@test -n "$(GOARCH_$(ASSET))" || { echo "未知架构 / unknown asset: $(ASSET), one of: $(ARCHES)"; exit 1; }
	@mkdir -p $(DIST)
	GOOS=linux GOARCH=$(GOARCH_$(ASSET)) GOARM=$(GOARM_$(ASSET)) CGO_ENABLED=0 \
		$(GO) build -trimpath -tags "$(TAGS)" \
			-ldflags "$(RELEASE_LDFLAGS)" \
			-o "$(DIST)/easysb-linux-$(ASSET)" .
	@ls -lh "$(DIST)/easysb-linux-$(ASSET)"

release-matrix: ## 打印发布架构矩阵 JSON（发布工作流用来生成动态矩阵）
	@printf '%s\n' '$(ARCHES)' | sed 's/ /","/g; s/^/["/; s/$$/"]/'

# --- 打包 / Packaging ---------------------------------------------------------

deb: build ## 打包全部发布架构的 .deb 到 dist/
	@set -e; for asset in $(ARCHES); do \
		$(MAKE) --no-print-directory deb-asset ASSET=$$asset NO_BUILD=1; \
	done

rpm: build ## 打包全部发布架构的 .rpm 到 dist/
	@set -e; for asset in $(ARCHES); do \
		$(MAKE) --no-print-directory rpm-asset ASSET=$$asset NO_BUILD=1; \
	done

pacman: build ## 打包 Arch 支持的每个架构的 pacman 包到 dist/
	@set -e; for asset in $(PACMAN_ARCHES); do \
		$(MAKE) --no-print-directory pacman-asset ASSET=$$asset NO_BUILD=1; \
	done

# 三种包共用这一棵暂存树：同一个二进制、同一个 sb 快捷指令、同一段由 --print-unit
# 打印出来的 systemd 单元、同一份许可证。各格式只在暂存之后才分叉，所以单元文本与
# 安装路径都只有一处定义。
# The three formats share this staging tree — one binary, one sb shortcut, one systemd
# unit text printed by --print-unit and one license. They only diverge after staging,
# so the unit text and the install paths have a single definition.
pkg-stage: ## 内部：准备打包暂存树（ASSET= 必填；REUSE_DIST=1 复用 dist/；NO_BUILD=1 不重建）
	@test -n "$(ASSET)" || { echo "ASSET 未设置 / ASSET required, one of: $(ARCHES)"; exit 1; }
	@if [ -z "$(NO_BUILD)" ]; then $(MAKE) --no-print-directory build; fi
	@if [ -z "$(REUSE_DIST)" ]; then $(MAKE) --no-print-directory dist-asset ASSET=$(ASSET); fi
	@test -s "$(DIST)/easysb-linux-$(ASSET)" || { echo "$(DIST)/easysb-linux-$(ASSET) 缺失 / missing (or pass REUSE_DIST=1 after a build)"; exit 1; }
	@set -e; stage="$(STAGE_DIR)/$(ASSET)"; \
	rm -rf "$$stage"; \
	mkdir -p "$$stage/usr/bin" \
	         "$$stage/usr/lib/systemd/system" \
	         "$$stage/usr/share/doc/$(PKG_NAME)" \
	         "$$stage/usr/share/licenses/$(PKG_NAME)"; \
	install -m 0755 "$(DIST)/easysb-linux-$(ASSET)" "$$stage$(PKG_EXEC)"; \
	ln -sf $(PKG_NAME) "$$stage/usr/bin/sb"; \
	./$(BINARY) --print-unit node --unit-exec $(PKG_EXEC) > "$$stage/usr/lib/systemd/system/sing-box.service"; \
	./$(BINARY) --print-unit sub  --unit-exec $(PKG_EXEC) > "$$stage/usr/lib/systemd/system/easysb.service"; \
	install -m 0644 LICENSE "$$stage/usr/share/licenses/$(PKG_NAME)/LICENSE"; \
	install -m 0644 LICENSE "$$stage/usr/share/doc/$(PKG_NAME)/copyright"

deb-asset: pkg-stage ## 打包单个架构的 .deb（ASSET=amd64 / arm64 / armv7 / 386 / riscv64 / s390x）
	@test -n "$(DEBARCH_$(ASSET))" || { echo "未知架构 / unknown asset: $(ASSET), one of: $(ARCHES)"; exit 1; }
	@command -v fpm >/dev/null 2>&1 || { echo "fpm 未安装 / fpm missing: sudo gem install --no-document fpm"; exit 1; }
	@set -e; fpm -s dir -t deb --force \
		-n $(PKG_NAME) -v $(VERSION) -a $(DEBARCH_$(ASSET)) \
		--category net --license "$(PKG_LICENSE)" --description "$(PKG_DESC)" \
		--url "$(PKG_URL)" --maintainer "$(PKG_MAINTAINER)" \
		--deb-priority optional --depends ca-certificates \
		--deb-field "Bugs: $(PKG_URL)/issues" \
		--no-deb-generate-changes \
		--after-install packaging/deb/postinst \
		--after-remove packaging/deb/postrm \
		--package "$(DIST)/$(PKG_NAME)_$(VERSION)_$(DEBARCH_$(ASSET)).deb" \
		-C "$(STAGE_DIR)/$(ASSET)" .; \
	ls -lh "$(DIST)/$(PKG_NAME)_$(VERSION)_$(DEBARCH_$(ASSET)).deb"

rpm-asset: pkg-stage ## 打包单个架构的 .rpm（ASSET=amd64 / arm64 / armv7 / 386 / riscv64 / s390x）
	@test -n "$(RPMARCH_$(ASSET))" || { echo "未知架构 / unknown asset: $(ASSET), one of: $(ARCHES)"; exit 1; }
	@command -v fpm >/dev/null 2>&1 || { echo "fpm 未安装 / fpm missing: sudo gem install --no-document fpm"; exit 1; }
	@set -e; fpm -s dir -t rpm --force \
		-n $(PKG_NAME) -v $(VERSION) --iteration 1 -a $(RPMARCH_$(ASSET)) \
		--rpm-summary "$(PKG_SUMMARY)" --license "$(PKG_LICENSE)" \
		--description "$(PKG_DESC)" --url "$(PKG_URL)" \
		--vendor "$(PKG_VENDOR)" --maintainer "$(PKG_MAINTAINER)" \
		--depends ca-certificates \
		--after-install packaging/rpm/post \
		--after-remove packaging/rpm/postun \
		--package "$(DIST)/$(PKG_NAME)-$(VERSION)-1.$(RPMARCH_$(ASSET)).rpm" \
		-C "$(STAGE_DIR)/$(ASSET)" .; \
	ls -lh "$(DIST)/$(PKG_NAME)-$(VERSION)-1.$(RPMARCH_$(ASSET)).rpm"

pacman-asset: pkg-stage ## 打包单个架构的 pacman 包（ASSET=amd64 / arm64 / armv7 / riscv64）
	@test -n "$(PACMANARCH_$(ASSET))" || { echo "未知架构 / unknown asset: $(ASSET), one of: $(PACMAN_ARCHES)"; exit 1; }
	@command -v fpm >/dev/null 2>&1 || { echo "fpm 未安装 / fpm missing: sudo gem install --no-document fpm"; exit 1; }
	@set -e; fpm -s dir -t pacman --force \
		-n $(PKG_NAME) -v $(VERSION) -a $(PACMANARCH_$(ASSET)) \
		--description "$(PKG_DESC)" --url "$(PKG_URL)" \
		--maintainer "$(PKG_MAINTAINER)" --license "$(PKG_LICENSE)" \
		--depends ca-certificates --pacman-compression zstd \
		--package "$(DIST)/$(PKG_NAME)-$(VERSION)-1-$(PACMANARCH_$(ASSET)).pkg.tar.zst" \
		-C "$(STAGE_DIR)/$(ASSET)" .; \
	ls -lh "$(DIST)/$(PKG_NAME)-$(VERSION)-1-$(PACMANARCH_$(ASSET)).pkg.tar.zst"

# 一个架构一次，三种格式都出。发布工作流每个矩阵作业调一次它，避免为同一架构重复
# 构建三遍。
# One call per architecture builds all three formats, so the release workflow does not
# build the same architecture three times.
packages-asset: ## 打包单个架构的全部格式到 dist/（ASSET=…）
	@test -n "$(ASSET)" || { echo "ASSET 未设置 / ASSET required, one of: $(ARCHES)"; exit 1; }
	@if [ -z "$(NO_BUILD)" ]; then $(MAKE) --no-print-directory build; fi
	@if [ -z "$(REUSE_DIST)" ]; then $(MAKE) --no-print-directory dist-asset ASSET=$(ASSET); fi
	@$(MAKE) --no-print-directory deb-asset ASSET=$(ASSET) NO_BUILD=1 REUSE_DIST=1
	@$(MAKE) --no-print-directory rpm-asset ASSET=$(ASSET) NO_BUILD=1 REUSE_DIST=1
	@case " $(PACMAN_ARCHES) " in *" $(ASSET) "*) $(MAKE) --no-print-directory pacman-asset ASSET=$(ASSET) NO_BUILD=1 REUSE_DIST=1 ;; esac

apt-index: ## 生成 apt 源索引到 dist/apt（设置 GPG_KEY_ID 时签名）
	@command -v apt-ftparchive >/dev/null 2>&1 || { echo "apt-ftparchive 未安装 / missing: apt-get install -y apt-utils"; exit 1; }
	@set -e; rm -rf "$(APT_DIR)"; mkdir -p "$(APT_DIR)"; \
	cp -f $(DIST)/*.deb "$(APT_DIR)/"; \
	cd "$(APT_DIR)"; \
	apt-ftparchive packages . | sed 's|^Filename: \./|Filename: |' > Packages; \
	gzip -9 -c Packages > Packages.gz; \
	apt-ftparchive \
		-o APT::FTPArchive::Release::Origin="$(PKG_NAME)" \
		-o APT::FTPArchive::Release::Label="$(PKG_NAME)" \
		-o APT::FTPArchive::Release::Suite=stable \
		-o APT::FTPArchive::Release::Codename=stable \
		-o APT::FTPArchive::Release::Architectures="$(DEB_ARCHS)" \
		-o APT::FTPArchive::Release::Description="$(PKG_DESC)" \
		release . > Release; \
	if [ -n "$(GPG_KEY_ID)" ]; then \
		gpg --batch --yes --armor --detach-sign -u "$(GPG_KEY_ID)" -o Release.gpg Release; \
		gpg --batch --yes --clearsign -u "$(GPG_KEY_ID)" -o InRelease Release; \
		gpg --batch --yes --armor --export "$(GPG_KEY_ID)" > $(PKG_NAME).gpg; \
	fi; \
	ls -lh .

install: build ## 用刚构建的二进制执行安装（需要 root）
	./install.sh --binary ./$(BINARY)

# --- 维护 / Maintenance -------------------------------------------------------

tidy: ## 整理 go.mod / go.sum
	$(GO) mod tidy

version: ## 打印版本、提交、标签与 Go 版本
	@echo "EasySB  $(VERSION)"
	@echo "commit  $(if $(strip $(COMMIT)),$(COMMIT),<none>)"
	@echo "tags    $(TAGS)"
	@echo "go      $$($(GO) version)"

clean: ## 删除构建产物（二进制与 dist/）
	rm -f $(BINARY)
	rm -rf $(DIST)

help: ## 显示本帮助
	@echo "EasySB make 目标 / targets:"
	@grep -hE '^[a-zA-Z0-9_-]+:.*?## ' $(MAKEFILE_LIST) \
		| awk 'BEGIN {FS = ":.*?## "}; {printf "  \033[36m%-16s\033[0m %s\n", $$1, $$2}'
