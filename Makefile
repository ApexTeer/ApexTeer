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

# 这两处文件读不到就地报错。空标签会静默丢掉 with_v2ray_api 之类的能力位，空版本号
# 会产出 easysb__linux_amd64.deb 这种畸形资产名——两者都是发出去之后才发现的问题，
# 在这里停下比发一个坏包便宜。VERSION 缺失时 2>/dev/null 正好把原因也吞掉，所以这
# 个守卫是唯一会说话的地方。
# Both of these have to be readable, so they fail here rather than downstream: empty
# tags silently drop capability bits such as with_v2ray_api, and an empty version yields
# asset names like easysb__linux_amd64.deb. The 2>/dev/null above swallows the reason
# VERSION could not be read, which makes this guard the only thing that can speak.
ifeq ($(strip $(TAGS)),)
$(error release/TAGS is missing or empty: the build tag set is defined there)
endif
ifeq ($(strip $(VERSION)),)
$(error VERSION is missing or empty: it is the release number embedded in the binary)
endif

# 本地构建保留符号表，便于调试；发布构建去掉，与发布工作流一致。
# A local build keeps symbols for debugging; a release build strips them, matching CI.
LDFLAGS         := $(if $(strip $(COMMIT)),-X main.commit=$(COMMIT))
RELEASE_LDFLAGS := -s -w -checklinkname=0 $(LDFLAGS)

# 发布只支持 Debian 与 Ubuntu 实际在用的两种服务器架构：EasySB 的 BBR 内核只发
# x86_64 与 arm64，把发行包收缩到同一集合，源里就没有装不上的架构。
# The release covers the two server architectures Debian and Ubuntu actually run: the BBR
# kernels EasySB installs are published for x86_64 and arm64 alone, so shrinking the
# packages to the same set leaves no architecture in the source that cannot be installed.
ARCHES := amd64 arm64

# 资产名到 Go 目标三元组的映射，只定义一次，dist 与 dist-asset 共用。
# The asset-name to Go-target mapping, defined once and shared by dist and dist-asset.
GOARCH_amd64 := amd64
GOARCH_arm64 := arm64

# 打包元数据 / package metadata. One format, one staging tree: the .deb is the only
# package EasySB ships.
PKG_NAME       ?= easysb
PKG_MAINTAINER ?= MinimaxFlora <zj18139624826@gmail.com>
PKG_LICENSE    ?= GPL-3.0-or-later
PKG_URL        ?= https://github.com/EasySBTeam/EasySB
PKG_DESC       ?= EasySB: a sing-box panel with the core compiled in
PKG_EXEC       := /usr/bin/easysb
# 打包暂存树 / the staging tree the .deb is built from.
STAGE_DIR      ?= $(DIST)/stage
# UPX 把打进包的二进制压小：Go 二进制里有大量可压缩的只读段，压完 .deb 小一半以上，
# 下载与软件源同步都跟着变快。UPX 缺失时原样打包，构建不会因此失败。
# UPX shrinks the binary that goes into the package: Go binaries carry large compressible
# read-only sections, and the .deb lands at less than half the size. When UPX is missing
# the binary is packaged as built, so the build does not depend on it.
UPX       ?= upx
UPX_FLAGS ?= --best --lzma
# 签名可选：CI 里存在 GPG_PRIVATE_KEY 密钥时会导入并用它签名 / signing is optional:
# when CI has imported a GPG key it is passed here, otherwise the index stays unsigned.
GPG_KEY_ID     ?=
# 口令保护的密钥：口令从 0600 文件读入，不进进程列表 / a passphrase-protected key is
# unlocked from a 0600 file, so the passphrase never shows up in a process list.
GPG_PASSPHRASE_FILE ?=
# 源共用的一组 gpg 参数：--batch 供 CI 无人值守，loopback 让口令从文件读入。
# One set of gpg options shared by the sources: --batch for unattended CI, and loopback
# so a passphrase is read from a file.
GPG_BATCH      := --batch --yes --pinentry-mode loopback

# 软件源目录树 / the repository tree: a flat apt repository, every file in one directory.
REPO_DIR       ?= $(DIST)/repo
# 源对外发布的根地址：GitHub Release 的最新资产目录，install.sh 的默认 REPO_URL 必须与
# 它一致，两处一起改。
# Public root URL of the sources: the latest release's asset directory. install.sh's
# default REPO_URL has to match, so the two move together.
REPO_URL       ?= https://github.com/EasySBTeam/EasySB/releases/latest/download

# Debian 架构名 / Debian architecture names.
DEBARCH_amd64 := amd64
DEBARCH_arm64 := arm64

# 脚本按资产名取 Debian 架构名，映射仍只有 DEBARCH_* 一处定义。
# The scripts look Debian's architecture name up by asset; the mapping still lives only
# in DEBARCH_*.
DEBARCH_MAP := $(foreach a,$(ARCHES),$(a)=$(DEBARCH_$(a)))

comma := ,
empty :=
space := $(empty) $(empty)

.DEFAULT_GOAL := build

.PHONY: build build-plain run test test-plain test-race vet fmt fmt-check \
        lint check render screens dist dist-asset \
        release-matrix tidy \
        panel panel-edge \
        version pkg-stage deb deb-asset packages-asset \
        apt-index repo install help clean

# --- 构建 / Build -------------------------------------------------------------

build: ## 构建二进制（带 release/TAGS 标签）
	$(GO) build -trimpath -tags "$(TAGS)" -ldflags "$(LDFLAGS)" -o $(BINARY) .

build-plain: ## 不带标签构建，便于快速迭代
	$(GO) build -trimpath -o $(BINARY) .

# --- 面板前端 / Panel bundle ---------------------------------------------------

# 前端由 EasySB-Frontend 的 Release 承载；这里把它取进 public/dist 供 go:embed。
# 发布构建必须先跑这一步，否则二进制里只有占位文件；本地要用真实控制台时也跑它。
# The front end is carried by an EasySB-Frontend release; this pulls it into public/dist
# for go:embed. A release build runs it first, otherwise the binary only carries the
# placeholder; run it locally too when you want the real console.
panel: ## 取最新正式面板前端到 public/dist
	bash scripts/fetch-panel.sh

panel-edge: ## 取 edge 滚动面板前端到 public/dist
	PANEL_CHANNEL=edge bash scripts/fetch-panel.sh

# 发布构建必须带真实前端，否则打出来的包有完整 API 却没有控制台——v6.0.0 就是这样发出去的，
# 面板只返回 568 字节的占位页。占位页的横幅里带「占位」二字，这里据此停下。
# A release build has to carry the real front end. Without it the package ships a complete
# API and no console, which is what v6.0.0 did: the panel served a 568-byte placeholder.
# The placeholder banner contains "占位", which is what this refuses to ship.
#
# 只负责判断，不负责获取：取前端是 panel 的事，这里只保证"没有就别打包"。两者分开，才能让
# 这个判断在离线环境里也能单独跑，也才有一条能测的失败路径。
# This only judges, it does not fetch: fetching is panel's job, and this only guarantees that
# nothing is packaged without it. Keeping them apart is what lets the judgement run offline
# on its own, and what gives the failure path something to test.
.PHONY: panel-check
panel-check: ## 内部：确认 public/dist 是真实前端而不是占位页
	@if grep -q '占位' public/dist/index.html 2>/dev/null; then \
		echo "public/dist/index.html 仍是占位页，发布构建会得到一个没有控制台的面板。" >&2; \
		echo "public/dist/index.html is still the placeholder: the release would ship a" >&2; \
		echo "panel with no console." >&2; \
		exit 1; \
	fi
	@test -f public/dist/index.html || { echo "public/dist/index.html 缺失 / missing: run 'make panel'" >&2; exit 1; }
	@echo "面板前端已就位 / panel front end present"

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

vet: ## go vet 静态检查（发布标签与无标签各一遍）
	$(GO) vet -tags "$(TAGS)" ./...
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

dist: ## 交叉编译全部发布架构到 dist/（二进制 + .deb）
	@set -e; for asset in $(ARCHES); do \
		$(MAKE) --no-print-directory deb-asset ASSET=$$asset; \
	done
	@ls -lh $(DIST)

dist-asset: ## 交叉编译单个发布架构（ASSET=amd64 / arm64）
	@test -n "$(ASSET)" || { echo "ASSET 未设置 / ASSET required, one of: $(ARCHES)"; exit 1; }
	@test -n "$(GOARCH_$(ASSET))" || { echo "未知架构 / unknown asset: $(ASSET), one of: $(ARCHES)"; exit 1; }
	@mkdir -p $(DIST)
	GOOS=linux GOARCH=$(GOARCH_$(ASSET)) CGO_ENABLED=0 \
		$(GO) build -trimpath -tags "$(TAGS)" \
			-ldflags "$(RELEASE_LDFLAGS)" \
			-o "$(DIST)/easysb-linux-$(ASSET)" .
	@ls -lh "$(DIST)/easysb-linux-$(ASSET)"
release-matrix: ## 打印发布架构矩阵 JSON（发布工作流用来生成动态矩阵）
	@printf '%s\n' '$(ARCHES)' | sed 's/ /","/g; s/^/["/; s/$$/"]/'

# --- 打包 / Packaging ---------------------------------------------------------

# 配方里显式再取一次，而不是只依赖前置顺序。make 按书写顺序生成前置，`build` 写在前面就
# 会先跑，二进制会带着占位页编出来；即使把 pkg-fetch-panel 挪到前面，那也只是碰巧靠顺序
# 成立，顺序一改就静默出错。所以把"先取前端"直接写成配方里的第一步。
#
# Fetch inside the recipe rather than relying on prerequisite order. Make builds
# prerequisites in the order written, so with `build` first the binary is compiled with the
# placeholder; moving pkg-fetch-panel to the front would work only by accident of ordering,
# and would fail silently the moment someone reorders. "Fetch first" is therefore the first
# step of the recipe itself.
deb: ## 打包全部发布架构的 .deb 到 dist/
	@$(MAKE) --no-print-directory pkg-fetch-panel
	@$(MAKE) --no-print-directory build
	@set -e; for asset in $(ARCHES); do \
		$(MAKE) --no-print-directory deb-asset ASSET=$$asset NO_BUILD=1; \
	done

# 先取前端，再判断它到没到：`panel` 在前，`panel-check` 在后。不要改成只留 panel-check
# 或只留 panel——
#
# 只有 panel-check 时，任何一条没先跑 `make panel` 的打包路径都会在打包中途停下，而且
# 停在一个"你没先取前端"的提示上；调用者其实是在问"给我打个包"，取前端本该是打包自己的
# 事。工作流里确实有一步 `make panel`，但只要那一行被删掉、被条件挡住、或者谁换个入口
# 直接调 `make packages-asset`，打包就断在半路——这正是 6d01319 那次运行发生的事。
#
# 只有 panel 时，离线环境会变成一次网络失败，而不是一句"你手里这个是占位页"。
#
# 两个都要：panel 负责去取，panel-check 负责在取不到时明确说不，并且说清原因。
#
# Fetch first, then judge: `panel` before `panel-check`. Do not collapse them into one.
#
# With only panel-check, any packaging path that did not happen to run `make panel` first
# stops in the middle of packaging with a "you did not fetch the front end" message, when the
# caller only asked for a package - fetching is packaging's own business. The workflow does
# have a `make panel` step, but if that line is deleted, gated behind a condition, or someone
# enters through `make packages-asset` directly, packaging breaks midway. That is what
# happened on 6d01319.
#
# With only panel, an offline build becomes an opaque network failure instead of a clear
# "the file you have is the placeholder".
#
# Keep both: panel fetches, panel-check refuses plainly when there is nothing real to package.
#
# 依赖必须挂在**每个会编译二进制的入口**上，不能只挂在 pkg-stage 上。编译发生在各个目标的
# 配方里，而配方只在它自己的前置全部完成后才开始；`packages-asset` 的配方先跑
# `$(MAKE) build` 和 `$(MAKE) dist-asset`，它经 `deb-asset` 才间接依赖到 pkg-stage，那时
# 二进制已经用占位页编好了——`make --debug=b` 的 "Must remake target 'build'" 排在
# "Must remake target 'panel'" 之前就是这么来的。所以 packages-asset 与 deb 各自显式依赖
# 这一步。
#
# The dependency has to hang off *every* entry point that compiles a binary, not just
# pkg-stage. Compiling happens inside each target's own recipe, and a recipe only starts once
# its own prerequisites are done; packages-asset's recipe runs `$(MAKE) build` and
# `$(MAKE) dist-asset` first, and it reaches pkg-stage only indirectly through deb-asset, by
# which time the binary has already been compiled with the placeholder. That is exactly why
# `make --debug=b` prints "Must remake target 'build'" before "Must remake target 'panel'".
# packages-asset and deb therefore depend on this step explicitly.
.PHONY: pkg-fetch-panel
pkg-fetch-panel: panel panel-check ## 内部：取前端并确认它是真实产物，打包前的唯一入口

# 暂存树只有一种格式在读，所以它同时是 UPX 压缩的唯一入口：压缩发生在文件离开 dist/
# 进入 stage/ 的时候，dist/ 里那份交叉编译产物保持原样。
# One format reads this staging tree, so it is also the single place UPX runs: compression
# happens as the file leaves dist/ for stage/, leaving the cross-compiled dist/ copy alone.
#
# 这里仍然留着 panel-check：直接调 pkg-stage 时，"手里是占位页"要有一句明确的拒绝，
# 而不是编译出一个没有控制台的包。取前端由上层的 pkg-fetch-panel 负责。
# panel-check stays here as well: called directly, pkg-stage should refuse plainly when what
# it has is the placeholder rather than compile a console-less package. Fetching is the
# upper layer's job, through pkg-fetch-panel.
pkg-stage: panel-check ## 内部：准备打包暂存树（ASSET= 必填；REUSE_DIST=1 复用 dist/；NO_BUILD=1 不重建）
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
	if command -v $(UPX) >/dev/null 2>&1; then \
		$(UPX) $(UPX_FLAGS) "$$stage$(PKG_EXEC)" >/dev/null; \
		echo "  upx: $$(du -h "$$stage$(PKG_EXEC)" | cut -f1)"; \
	else \
		echo "  upx 缺失，按原样打包 / upx missing, packaging the binary as built"; \
	fi; \
	ln -sf $(PKG_NAME) "$$stage/usr/bin/sb"; \
	./$(BINARY) --print-unit node --unit-exec $(PKG_EXEC) > "$$stage/usr/lib/systemd/system/sing-box.service"; \
	./$(BINARY) --print-unit sub  --unit-exec $(PKG_EXEC) > "$$stage/usr/lib/systemd/system/easysb.service"; \
	install -m 0644 LICENSE "$$stage/usr/share/licenses/$(PKG_NAME)/LICENSE"; \
	install -m 0644 LICENSE "$$stage/usr/share/doc/$(PKG_NAME)/copyright"

deb-asset: pkg-stage ## 打包单个架构的 .deb（ASSET=amd64 / arm64）
	@test -n "$(DEBARCH_$(ASSET))" || { echo "未知架构 / unknown asset: $(ASSET), one of: $(ARCHES)"; exit 1; }
	@command -v fpm >/dev/null 2>&1 || { echo "fpm 未安装 / fpm missing: sudo gem install --no-document fpm"; exit 1; }
	@set -e; fpm -s dir -t deb --force \
		-n $(PKG_NAME) -v $(VERSION) --iteration 1 -a $(DEBARCH_$(ASSET)) \
		--category net --license "$(PKG_LICENSE)" --description "$(PKG_DESC)" \
		--url "$(PKG_URL)" --maintainer "$(PKG_MAINTAINER)" \
		--deb-priority optional --depends ca-certificates \
		--deb-field "Bugs: $(PKG_URL)/issues" \
		--no-deb-generate-changes \
		--after-install packaging/deb/postinst \
		--after-remove packaging/deb/postrm \
		--package "$(DIST)/$(PKG_NAME)_$(VERSION)-1_$(DEBARCH_$(ASSET)).deb" \
		-C "$(STAGE_DIR)/$(ASSET)" .; \
	ls -lh "$(DIST)/$(PKG_NAME)_$(VERSION)-1_$(DEBARCH_$(ASSET)).deb"

# 一个架构一次。发布工作流每个矩阵作业调一次它：构建本地二进制给 --print-unit 取单元
# 文本，再复用上面下载来的交叉编译产物打一个 .deb。
# One call per architecture; the release workflow calls it once per matrix job: it builds
# the local binary for --print-unit and packages the cross-compiled artifact it downloaded.
packages-asset: ## 打包单个架构的 .deb 到 dist/（ASSET=…）
	@test -n "$(ASSET)" || { echo "ASSET 未设置 / ASSET required, one of: $(ARCHES)"; exit 1; }
	@$(MAKE) --no-print-directory pkg-fetch-panel
	@if [ -z "$(NO_BUILD)" ]; then $(MAKE) --no-print-directory build; fi
	@if [ -z "$(REUSE_DIST)" ]; then $(MAKE) --no-print-directory dist-asset ASSET=$(ASSET); fi
	@$(MAKE) --no-print-directory deb-asset ASSET=$(ASSET) NO_BUILD=1 REUSE_DIST=1

# --- 软件源 / Repository ------------------------------------------------------

# 源根目录 dist/repo 是一棵扁平 apt 仓库，所有文件在同一层，由 GitHub Release 原样承载：
# 同一份 .deb 被所有发行版共用，因此没有 dists/<套件> 分层。签名公钥、install.sh 与索引
# 都在这一层，对应 caddy 风格的落点 /usr/share/keyrings/easysb-archive-keyring.gpg。
# The repository root dist/repo is a flat apt repository, every file in one directory,
# served verbatim by a GitHub Release: the same .deb serves every distribution, so there
# is no per-suite dists/ split. The signing key, install.sh and the indexes all sit in
# that one directory, the caddy-style drop point for
# /usr/share/keyrings/easysb-archive-keyring.gpg.
#
# 摆放、索引与签名都在 packaging/repo/index.sh 里；这个目标只把 Makefile 里那份架构表
# 传进去，表仍然只有这一处定义。
# Laying out, indexing and signing live in packaging/repo/index.sh; this target only hands
# it the architecture table, which is still defined in exactly one place.
apt-index: ## 生成 apt 源到 dist/repo（设置 GPG_KEY_ID 时签名）
	@DIST='$(DIST)' REPO_DIR='$(REPO_DIR)' \
	 PKG_NAME='$(PKG_NAME)' VERSION='$(VERSION)' PKG_DESC='$(PKG_DESC)' \
	 DEBARCH_MAP='$(DEBARCH_MAP)' \
	 GPG_KEY_ID='$(GPG_KEY_ID)' GPG_PASSPHRASE_FILE='$(GPG_PASSPHRASE_FILE)' \
	 bash packaging/repo/index.sh

repo: apt-index ## 组装完整软件源到 dist/repo（Packages / Release / install.sh / 公钥）

install: deb ## 安装刚构建的本机 .deb（需要 root，仅 Debian / Ubuntu）
	@sudo apt-get install -y "$(DIST)/$(PKG_NAME)_$(VERSION)-1_$(shell dpkg --print-architecture).deb"

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
