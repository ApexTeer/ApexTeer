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
# 签名可选：CI 里存在 GPG_PRIVATE_KEY 密钥时会导入并用它签名 / signing is optional:
# when CI has imported a GPG key it is passed here, otherwise the index stays unsigned.
GPG_KEY_ID     ?=
# 口令保护的密钥：口令从 0600 文件读入，不进进程列表 / a passphrase-protected key is
# unlocked from a 0600 file, so the passphrase never shows up in a process list.
GPG_PASSPHRASE_FILE ?=
# 三份源共用的一组 gpg 参数：--batch 供 CI 无人值守，loopback 让口令从文件读入。
# One set of gpg options shared by the three sources: --batch for unattended CI, and
# loopback so a passphrase is read from a file.
GPG_BATCH      := --batch --yes --pinentry-mode loopback

# 软件源目录树 / the repository tree: apt is flat, rpm and pacman are per architecture
# and bin holds the release tarballs.
REPO_DIR       ?= $(DIST)/repo

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
# 源里按架构分目录时用的名字，与包名用的是同一份映射。
RPM_ARCH_DIRS    := $(foreach a,$(ARCHES),$(RPMARCH_$(a)))
PACMAN_ARCH_DIRS := $(foreach a,$(PACMAN_ARCHES),$(PACMANARCH_$(a)))

comma := ,
empty :=
space := $(empty) $(empty)

.DEFAULT_GOAL := build

.PHONY: build build-plain run test test-plain test-race vet fmt fmt-check \
        lint check render screens dist dist-asset tarballs tarball-asset \
        release-matrix install tidy \
        version pkg-stage deb deb-asset rpm rpm-asset pacman pacman-asset \
        packages-asset apt-index rpm-index pacman-index repo help clean

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

dist: ## 交叉编译全部发布架构到 dist/（二进制 + 发布压缩包）
	@set -e; for asset in $(ARCHES); do \
		$(MAKE) --no-print-directory tarball-asset ASSET=$$asset; \
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

# 发布压缩包：里面的可执行文件就叫 easysb，解包后可直接 install。dist/easysb-linux-<架构>
# 只是中间产物，任何一种方式都不会把它单独发出去。
# The release tarball carries an executable named `easysb` so it can be unpacked straight
# into place. dist/easysb-linux-<arch> is only an intermediate and is never published alone.
tarballs: ## 打包全部发布架构的 .tar.gz 到 dist/
	@set -e; for asset in $(ARCHES); do \
		$(MAKE) --no-print-directory tarball-asset ASSET=$$asset; \
	done

tarball-asset: ## 打包单个架构的发布压缩包（ASSET=…）
	@test -n "$(ASSET)" || { echo "ASSET 未设置 / ASSET required, one of: $(ARCHES)"; exit 1; }
	@test -n "$(GOARCH_$(ASSET))" || { echo "未知架构 / unknown asset: $(ASSET), one of: $(ARCHES)"; exit 1; }
	@test -s "$(DIST)/easysb-linux-$(ASSET)" || $(MAKE) --no-print-directory dist-asset ASSET=$(ASSET)
	@set -e; tmp="$$(mktemp -d)"; \
	install -m 0755 "$(DIST)/easysb-linux-$(ASSET)" "$$tmp/easysb"; \
	install -m 0644 LICENSE "$$tmp/LICENSE"; \
	install -m 0644 README.md "$$tmp/README.md"; \
	tar -C "$$tmp" -czf "$(DIST)/$(PKG_NAME)-$(VERSION)-linux-$(ASSET).tar.gz" \
		easysb LICENSE README.md; \
	rm -rf "$$tmp"; \
	ls -lh "$(DIST)/$(PKG_NAME)-$(VERSION)-linux-$(ASSET).tar.gz"
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
		--package "$(DIST)/$(PKG_NAME)_$(VERSION)_linux_$(DEBARCH_$(ASSET)).deb" \
		-C "$(STAGE_DIR)/$(ASSET)" .; \
	ls -lh "$(DIST)/$(PKG_NAME)_$(VERSION)_linux_$(DEBARCH_$(ASSET)).deb"

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
		--package "$(DIST)/$(PKG_NAME)_$(VERSION)_linux_$(RPMARCH_$(ASSET)).rpm" \
		-C "$(STAGE_DIR)/$(ASSET)" .; \
	ls -lh "$(DIST)/$(PKG_NAME)_$(VERSION)_linux_$(RPMARCH_$(ASSET)).rpm"

pacman-asset: pkg-stage ## 打包单个架构的 pacman 包（ASSET=amd64 / arm64 / armv7 / riscv64）
	@test -n "$(PACMANARCH_$(ASSET))" || { echo "未知架构 / unknown asset: $(ASSET), one of: $(PACMAN_ARCHES)"; exit 1; }
	@command -v fpm >/dev/null 2>&1 || { echo "fpm 未安装 / fpm missing: sudo gem install --no-document fpm"; exit 1; }
	@set -e; fpm -s dir -t pacman --force \
		-n $(PKG_NAME) -v $(VERSION) -a $(PACMANARCH_$(ASSET)) \
		--description "$(PKG_DESC)" --url "$(PKG_URL)" \
		--maintainer "$(PKG_MAINTAINER)" --license "$(PKG_LICENSE)" \
		--depends ca-certificates --pacman-compression zstd \
		--package "$(DIST)/$(PKG_NAME)_$(VERSION)_linux_$(PACMANARCH_$(ASSET)).pkg.tar.zst" \
		-C "$(STAGE_DIR)/$(ASSET)" .; \
	ls -lh "$(DIST)/$(PKG_NAME)_$(VERSION)_linux_$(PACMANARCH_$(ASSET)).pkg.tar.zst"

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

# --- 软件源 / Repository ------------------------------------------------------

# 源根目录 dist/repo 的四个子目录各是一种客户端要的东西：apt 是扁平的 deb 源，
# rpm 与 pacman 按架构分目录，bin 放发布压缩包。安装脚本 install.sh 只认这四个路径。
# The repository root dist/repo has four subtrees, one per client: a flat apt archive,
# per-architecture rpm and pacman trees, and bin for the release tarballs. install.sh
# knows only these four paths.
#
# 三份索引的署名密钥是同一个，各源只按本生态的惯例换文件名与编码：apt 的公钥必须是非
# armored 的二进制 keyring（Signed-By 走 apt-key/gpgv，armored 读不出里面的 key），
# Release.gpg 与 InRelease 才是 armored；rpm-md 与 pacman 发 armored 公钥，各自的签名
# 则按客户端查找的名字与编码放。
# The three indexes are signed by one key; each source only changes the file name and
# encoding its ecosystem expects: apt's public key must be a non-armored binary keyring
# (Signed-By goes through apt-key/gpgv, which cannot read an armored file) with armored
# Release.gpg and InRelease, while the rpm-md and pacman trees publish an armored key and
# keep the signature encodings those clients look for.
apt-index: ## 生成 apt 扁平源到 dist/repo/apt（设置 GPG_KEY_ID 时签名）
	@command -v apt-ftparchive >/dev/null 2>&1 || { echo "apt-ftparchive 未安装 / missing: apt-get install -y apt-utils"; exit 1; }
	@set -e; sign='$(GPG_BATCH)'; \
	if [ -n "$(GPG_PASSPHRASE_FILE)" ]; then sign="$$sign --passphrase-file $(GPG_PASSPHRASE_FILE)"; fi; \
	apt="$(REPO_DIR)/apt"; rm -rf "$$apt"; mkdir -p "$$apt"; \
	cp -f $(DIST)/*.deb "$$apt/"; \
	cd "$$apt"; \
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
		gpg $$sign --armor --detach-sign -u "$(GPG_KEY_ID)" -o Release.gpg Release; \
		gpg $$sign --clearsign -u "$(GPG_KEY_ID)" -o InRelease Release; \
		gpg --batch --yes --export "$(GPG_KEY_ID)" > $(PKG_NAME).gpg; \
	fi; \
	ls -lh .

# rpm 要签两处：包本身（gpgcheck）与每个架构目录的 repodata/repomd.xml（repo_gpgcheck）。
# --addsign 里的 __gpg_sign_cmd 换掉 rpm 默认的 gpg 调用，好让口令同样从文件读入；
# rpm 自己不查 PATH，发行版宏里的 %{__gpg} 还可能指向本机没装的 gpg2，所以先用
# command -v 找到 gpg，再把它的绝对路径写进这条命令。createrepo_c 必须在签名之后跑，
# 否则索引里的校验和与签名后的包对不上。
# rpm signs two things: each package (gpgcheck) and repodata/repomd.xml per architecture
# (repo_gpgcheck). The __gpg_sign_cmd override replaces rpm's own gpg call so the
# passphrase is read from the same file. rpm does not search PATH, and a distro's
# %{__gpg} may name a gpg2 that is not installed, so the absolute path is resolved with
# command -v first. createrepo_c must run after signing, or the checksums in the index no
# longer match the signed packages.
rpm-index: ## 生成 rpm-md 源到 dist/repo/rpm/<架构>（设置 GPG_KEY_ID 时签名，需要 createrepo_c）
	@command -v createrepo_c >/dev/null 2>&1 || { echo "createrepo_c 未安装 / missing: apt-get install -y createrepo-c"; exit 1; }
	@set -e; sign='$(GPG_BATCH)'; \
	if [ -n "$(GPG_PASSPHRASE_FILE)" ]; then sign="$$sign --passphrase-file $(GPG_PASSPHRASE_FILE)"; fi; \
	if [ -n "$(GPG_KEY_ID)" ]; then \
		command -v rpm >/dev/null 2>&1 || { echo "rpm 未安装 / missing: apt-get install -y rpm"; exit 1; }; \
		gpg_bin="$$(command -v gpg)"; \
		mkdir -p "$(REPO_DIR)/rpm"; \
		gpg --batch --yes --armor --export "$(GPG_KEY_ID)" > "$(REPO_DIR)/rpm/RPM-GPG-KEY-easysb"; \
	fi; \
	for arch in $(RPM_ARCH_DIRS); do \
		dir="$(REPO_DIR)/rpm/$$arch"; \
		rm -rf "$$dir"; mkdir -p "$$dir"; \
		pkg="$$dir/$(PKG_NAME)_$(VERSION)_linux_$$arch.rpm"; \
		cp -f "$(DIST)/$(PKG_NAME)_$(VERSION)_linux_$$arch.rpm" "$$dir/"; \
		if [ -n "$(GPG_KEY_ID)" ]; then \
			rpm --addsign --define "_gpg_name $(GPG_KEY_ID)" \
				--define "__gpg_sign_cmd $$gpg_bin $$sign -u %{_gpg_name} --output %{__signature_filename} --detach-sign %{__plaintext_filename}" \
				"$$pkg"; \
		fi; \
		createrepo_c --quiet "$$dir"; \
		if [ -n "$(GPG_KEY_ID)" ]; then \
			gpg $$sign --armor --detach-sign -u "$(GPG_KEY_ID)" \
				-o "$$dir/repodata/repomd.xml.asc" "$$dir/repodata/repomd.xml"; \
		fi; \
	done

# repo-add 把 `easysb.db` 留成指向 `.db.tar.gz` 的符号链接。上传走的是普通 FTP，
# 会跳过符号链接，所以这里换成真实文件：先写到 .new，再用 mv 顶掉那个链接，
# `.tar.gz` 本身仍然保留。
# repo-add leaves `easysb.db` as a symlink to `easysb.db.tar.gz`, and the FTP upload
# skips symlinks, so each is replaced with a real file: written as .new, then mv over
# the link. The `.tar.gz` itself is kept as well.
#
# pacman 也签两处：每个包旁的 `.sig`，以及数据库 `easysb.db` 的 `.sig`。签名自己做而不是
# 交给 `repo-add --sign`，因为那把签名推给 gpg 时没有传口令的入口，带口令的密钥会卡住；
# 自己签完把同一份数据库签名挂到 `.db.sig` 与 `.db.tar.gz.sig` 两个名字上，镜像上两份都在。
# pacman signs two things as well: the `.sig` next to each package, and one for the
# database `easysb.db`. Signing is done here rather than through `repo-add --sign`, which
# gives gpg no way to take a passphrase and would block on a protected key; the database
# signature is then published under both the `.db.sig` and `.db.tar.gz.sig` names.
pacman-index: ## 生成 pacman 源到 dist/repo/pacman/<架构>（设置 GPG_KEY_ID 时签名，需要 repo-add）
	@command -v repo-add >/dev/null 2>&1 || { echo "repo-add 未安装 / missing: pacman/libarchive 提供的 repo-add"; exit 1; }
	@set -e; sign='$(GPG_BATCH)'; \
	if [ -n "$(GPG_PASSPHRASE_FILE)" ]; then sign="$$sign --passphrase-file $(GPG_PASSPHRASE_FILE)"; fi; \
	if [ -n "$(GPG_KEY_ID)" ]; then \
		mkdir -p "$(REPO_DIR)/pacman"; \
		gpg --batch --yes --armor --export "$(GPG_KEY_ID)" > "$(REPO_DIR)/pacman/easysb.asc"; \
	fi; \
	for arch in $(PACMAN_ARCH_DIRS); do \
		dir="$(REPO_DIR)/pacman/$$arch"; \
		rm -rf "$$dir"; mkdir -p "$$dir"; \
		pkg="$$dir/$(PKG_NAME)_$(VERSION)_linux_$$arch.pkg.tar.zst"; \
		cp -f "$(DIST)/$(PKG_NAME)_$(VERSION)_linux_$$arch.pkg.tar.zst" "$$dir/"; \
		if [ -n "$(GPG_KEY_ID)" ]; then \
			gpg $$sign --detach-sign -u "$(GPG_KEY_ID)" "$$pkg"; \
		fi; \
		( cd "$$dir" && repo-add --quiet "$(PKG_NAME).db.tar.gz" *.pkg.tar.zst ); \
		for part in db files; do \
			cp -f "$$dir/$(PKG_NAME).$$part.tar.gz" "$$dir/$(PKG_NAME).$$part.new"; \
			mv -f "$$dir/$(PKG_NAME).$$part.new" "$$dir/$(PKG_NAME).$$part"; \
		done; \
		if [ -n "$(GPG_KEY_ID)" ]; then \
			gpg $$sign --detach-sign -u "$(GPG_KEY_ID)" -o "$$dir/$(PKG_NAME).db.sig" "$$dir/$(PKG_NAME).db"; \
			cp -f "$$dir/$(PKG_NAME).db.sig" "$$dir/$(PKG_NAME).db.tar.gz.sig"; \
		fi; \
	done

# 站点页面不走静态 index.html：置备脚本让 Caddy 用 dist/repo 里这份模板渲染目录列表，
# 所以站点首页既是文件列表，又带着安装说明。模板和图标都放进 .easysb/ 这个点目录，
# Caddyfile 的点号文件规则（@hidden path /.*）把它们挡在列表之外，读者在根目录只看得到
# 四个源目录；图标另有一条精确路径的路由负责送出。
# The site page is not a static index.html: provisioning points Caddy at this template, so
# the home page is the directory listing plus the install notes. The template and the icon
# live in the dot-directory .easysb/, which the Caddyfile's @hidden path /.* rule keeps off
# the listing, so the root shows the four source directories alone; a dedicated exact-path
# route serves the icon.
repo: apt-index rpm-index pacman-index ## 组装完整软件源到 dist/repo（apt / rpm / pacman / bin）
	@set -e; bin="$(REPO_DIR)/bin"; rm -rf "$$bin"; mkdir -p "$$bin"; \
	cp -f $(wildcard $(DIST)/$(PKG_NAME)-*-linux-*.tar.gz) "$$bin/"; \
	mkdir -p "$(REPO_DIR)/.easysb"; \
	cp -f packaging/server/browse.html "$(REPO_DIR)/.easysb/browse.html"; \
	cp -f packaging/server/favicon.svg "$(REPO_DIR)/.easysb/favicon.svg"; \
	echo "源目录树 / repository tree:"; \
	find "$(REPO_DIR)" -type f | sort | sed 's|^|  |'

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
