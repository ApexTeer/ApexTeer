#!/usr/bin/env bash
# ==============================================================================
#  EasySB 软件源：摆成扁平树、生成索引并签名 / lay out a flat tree, index and sign it
# ------------------------------------------------------------------------------
#  源是"平凡"（flat）apt 仓库：所有文件都在同一层，GitHub Release 直接承载它们，
#  仓库地址就是 https://github.com/MinimaxFlora/EasySB/releases/latest/download。
#
#    Packages / Packages.gz                        索引，Filename 都是同层文件名
#    Release / InRelease / Release.gpg             索引元数据与签名
#    easysb_<版本>-1_<架构>.deb                     各架构安装包
#    easysb-archive-keyring.asc                    armored 公钥
#    install.sh                                    一键安装脚本
#
#  A flat (trivial) apt repository: every file sits in one directory, which a GitHub
#  Release can serve as-is, so the source URL is
#  https://github.com/MinimaxFlora/EasySB/releases/latest/download.
#
#  同一个 .deb 服务所有发行版：EasySB 只依赖 ca-certificates，不分发行版打包反而让
#  各个套件引用同一份字节，升级也简单，因此这里没有 dists/<套件> 分层。
#  One .deb serves every distribution: EasySB only depends on ca-certificates, so a single
#  package is simpler and upgrades uniformly; there is no per-suite dists/ split.
#
#  用法 / Usage: 由 Makefile 调用，变量见下。
#    DIST          .deb 所在目录
#    REPO_DIR      输出树根
#    PKG_NAME / VERSION / PKG_DESC
#    DEBARCH_MAP   每项 asset=debian-arch
#    GPG_KEY_ID / GPG_PASSPHRASE_FILE   存在时签名
# ==============================================================================

set -euo pipefail

DIST="${DIST:?DIST 未设置 / required}"
REPO_DIR="${REPO_DIR:?REPO_DIR 未设置 / required}"
PKG_NAME="${PKG_NAME:?PKG_NAME 未设置 / required}"
VERSION="${VERSION:?VERSION 未设置 / required}"
PKG_DESC="${PKG_DESC:-$PKG_NAME}"
DEBARCH_MAP="${DEBARCH_MAP:?DEBARCH_MAP 未设置 / required}"

command -v apt-ftparchive >/dev/null 2>&1 || {
  echo "apt-ftparchive 未安装 / missing: apt-get install -y apt-utils" >&2; exit 1; }

# .deb 的版本串：上游版本加一个本地修订号 / the package version: upstream plus revision.
debver="${VERSION}-1"

# 签名参数：CI 无人值守（--batch），口令从 0600 文件读入，不进进程列表。
# Signing options: --batch for unattended CI, and the passphrase read from a 0600 file so
# it never reaches a process list.
sign=()
have_key() { [ -n "${GPG_KEY_ID:-}" ]; }
if have_key; then
  sign=(--batch --yes --pinentry-mode loopback)
  if [ -n "${GPG_PASSPHRASE_FILE:-}" ]; then
    sign+=(--passphrase-file "$GPG_PASSPHRASE_FILE")
  fi
fi

# 每个资产名换成 Debian 架构名，映射只有 Makefile 的 DEBARCH_* 一处定义。
# Map each asset name to Debian's architecture name; the mapping lives only in DEBARCH_*.
archs=()
for item in $DEBARCH_MAP; do
  asset="${item%%=*}"
  debarch="${item#*=}"
  src="$DIST/${PKG_NAME}_${debver}_${debarch}.deb"
  [ -s "$src" ] || { echo "缺少包 / missing package: $src" >&2; exit 1; }
  archs+=("$debarch")
done

# 清掉旧树再重铺：扁平布局每次全部重生成。
# Wipe the old tree and lay it out again: the flat layout is regenerated in full.
rm -rf "$REPO_DIR"
mkdir -p "$REPO_DIR"
for debarch in "${archs[@]}"; do
  cp -f "$DIST/${PKG_NAME}_${debver}_${debarch}.deb" "$REPO_DIR/"
done

# 索引从"只有 .deb"的目录生成：apt-ftparchive 只读 .deb，产出的 Filename 是 ./<名字>。
# 把前缀 ./ 去掉，apt 拼出的下载地址才是干净的 <源根>/<名字>（GitHub Release 资产没有
# 目录层级，/./ 虽多半会被归一化，但不值得依赖）。
# Index a directory that holds only .deb files: apt-ftparchive reads just the packages and
# writes Filename as ./<name>. Strip the ./ so apt builds a clean <root>/<name> URL; a
# GitHub Release has no directories, and relying on /./ being normalised is not worth it.
tmp_pkg="$(mktemp)"
(
  cd "$REPO_DIR"
  apt-ftparchive packages .
) | sed 's|^Filename: \./|Filename: |' > "$tmp_pkg"
mv -f "$tmp_pkg" "$REPO_DIR/Packages"
gzip -9 -c "$REPO_DIR/Packages" > "$REPO_DIR/Packages.gz"

# Release 不能写进它要校验的那棵树里再生成：apt-ftparchive 会把目录里已存在的 Release
# 也算进校验和，于是文件引用自己。先落到 $REPO_DIR 之外的临时文件，再挪回去。
# Release cannot be written inside the tree it checksums while it is generated:
# apt-ftparchive would checksum the Release file that already exists there and the file
# would reference itself. It lands in a temp file outside $REPO_DIR and is moved in after.
tmp_rel="$(mktemp)"
(
  cd "$REPO_DIR"
  apt-ftparchive \
    -o "APT::FTPArchive::Release::Origin=$PKG_NAME" \
    -o "APT::FTPArchive::Release::Label=$PKG_NAME" \
    -o "APT::FTPArchive::Release::Suite=stable" \
    -o "APT::FTPArchive::Release::Codename=stable" \
    -o "APT::FTPArchive::Release::Architectures=${archs[*]}" \
    -o "APT::FTPArchive::Release::Components=main" \
    -o "APT::FTPArchive::Release::Description=$PKG_DESC" \
    release .
) > "$tmp_rel"
mv -f "$tmp_rel" "$REPO_DIR/Release"

if have_key; then
  (
    cd "$REPO_DIR"
    gpg "${sign[@]}" --armor --detach-sign -u "$GPG_KEY_ID" -o Release.gpg Release
    gpg "${sign[@]}" --clearsign   -u "$GPG_KEY_ID" -o InRelease Release
  )
fi

# 公钥一律从密钥环导出成 armored 文件，install.sh 直接 dearmor 到
# /usr/share/keyrings/easysb-archive-keyring.gpg。
# The public key is exported from the keyring as an armored file; install.sh dearmors it
# straight to /usr/share/keyrings/easysb-archive-keyring.gpg.
if have_key; then
  gpg --batch --yes --armor --export "$GPG_KEY_ID" > "$REPO_DIR/easysb-archive-keyring.asc"
else
  echo "  未设置 GPG_KEY_ID，索引未签名 / no GPG_KEY_ID, publishing an unsigned index"
fi

cp -f install.sh "$REPO_DIR/install.sh"

echo "源目录树 / repository tree:"
find "$REPO_DIR" -type f | sort | sed 's|^|  |'
