#!/usr/bin/env bash
# ==============================================================================
#  EasySB 软件源：摆目录树、生成索引并签名 / lay out the tree, index and sign it
# ------------------------------------------------------------------------------
#  这是一棵标准 apt 树，GitHub Pages 原样发布在站点根：
#
#    pool/main/e/easysb/easysb_<版本>-1_<架构>.deb    所有套件共用这一份
#    dists/<套件>/main/binary-<架构>/Packages(.gz)    内含相对站点根的 Filename
#    dists/<套件>/Release | InRelease | Release.gpg   套件元数据与签名
#    easysb-archive-keyring.asc                       armored 公钥
#    install.sh                                       一键安装脚本
#
#  A plain apt tree published verbatim at the Pages site root: one shared pool, one
#  dists/<suite> per release, the armored public key and install.sh at the site root.
#
#  同一个 .deb 服务所有套件，版本串不带发行版：EasySB 只依赖 ca-certificates，不分
#  发行版打包反而让各个套件引用同一份字节，升级也简单。
#  One .deb serves every suite; the version string carries no distribution. EasySB only
#  depends on ca-certificates, so a single package shared by every suite is simpler and
#  makes upgrades uniform.
#
#  用法 / Usage: 由 Makefile 调用，变量见下。
#    DIST          .deb 所在目录
#    REPO_DIR      输出树根
#    PKG_NAME / VERSION / PKG_DESC
#    APT_SUITES    空格分隔的套件名
#    DEBARCH_MAP   每项 asset=debian-arch
#    GPG_KEY_ID / GPG_PASSPHRASE_FILE   存在时签名
# ==============================================================================

set -euo pipefail

DIST="${DIST:?DIST 未设置 / required}"
REPO_DIR="${REPO_DIR:?REPO_DIR 未设置 / required}"
PKG_NAME="${PKG_NAME:?PKG_NAME 未设置 / required}"
VERSION="${VERSION:?VERSION 未设置 / required}"
PKG_DESC="${PKG_DESC:-$PKG_NAME}"
APT_SUITES="${APT_SUITES:?APT_SUITES 未设置 / required}"
DEBARCH_MAP="${DEBARCH_MAP:?DEBARCH_MAP 未设置 / required}"

command -v apt-ftparchive >/dev/null 2>&1 || {
  echo "apt-ftparchive 未安装 / missing: apt-get install -y apt-utils" >&2; exit 1; }

# .deb 的版本串：上游版本加一个本地修订号 / the package version: upstream plus revision.
debver="${VERSION}-1"
pool_rel="pool/main/e/${PKG_NAME}"

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

# 清掉旧树再重铺：pool 只有这一份，dists 每次按套件重生成。
# Wipe the old tree and lay it out again: one pool, and dists regenerated per suite.
rm -rf "$REPO_DIR"
mkdir -p "$REPO_DIR/$pool_rel"
for debarch in "${archs[@]}"; do
  cp -f "$DIST/${PKG_NAME}_${debver}_${debarch}.deb" "$REPO_DIR/$pool_rel/"
done

for suite in $APT_SUITES; do
  root="$REPO_DIR/dists/$suite"
  for debarch in "${archs[@]}"; do
    dir="$root/main/binary-$debarch"
    mkdir -p "$dir"

    # Filename 字段是相对站点根的路径，所以从一个把 pool 路径原样复刻出来、只放本架构
    # 包的临时树里生成 Packages。硬链接即可，不复制字节。
    # The Filename field is relative to the site root, so Packages is generated from a
    # temporary tree that mirrors the pool path and holds only this architecture's
    # package. Hard links only; no bytes are copied.
    tmp="$(mktemp -d)"
    mkdir -p "$tmp/$pool_rel"
    for deb in "$REPO_DIR/$pool_rel"/*_"$debarch".deb; do
      ln "$deb" "$tmp/$pool_rel/"
    done
    ( cd "$tmp" && apt-ftparchive packages "$pool_rel" ) > "$dir/Packages"
    gzip -9 -c "$dir/Packages" > "$dir/Packages.gz"
    rm -rf "$tmp"

    cat > "$dir/Release" <<EOF
Archive: stable
Origin: $PKG_NAME
Label: $PKG_NAME
Suite: $suite
Component: main
Architecture: $debarch
EOF
  done

  # Release 不能写进自己的树里再生成：apt-ftparchive 会把已存在的 Release 也算进校验和，
  # 于是文件引用自己。先落到树外，再挪进去。
  # Release cannot be written inside its own tree while it is generated: apt-ftparchive
  # would checksum the file that already exists there and the file would reference
  # itself. It lands outside the tree first and is moved in afterwards.
  tmp="$(mktemp)"
  ( cd "$root" && apt-ftparchive \
      -o "APT::FTPArchive::Release::Origin=$PKG_NAME" \
      -o "APT::FTPArchive::Release::Label=$PKG_NAME" \
      -o "APT::FTPArchive::Release::Suite=$suite" \
      -o "APT::FTPArchive::Release::Codename=$suite" \
      -o "APT::FTPArchive::Release::Architectures=${archs[*]}" \
      -o "APT::FTPArchive::Release::Components=main" \
      -o "APT::FTPArchive::Release::Description=$PKG_DESC" \
      release . ) > "$tmp"
  mv -f "$tmp" "$root/Release"

  if have_key; then
    ( cd "$root" && \
      gpg "${sign[@]}" --armor --detach-sign -u "$GPG_KEY_ID" -o Release.gpg Release && \
      gpg "${sign[@]}" --clearsign   -u "$GPG_KEY_ID" -o InRelease Release )
  fi
  echo "  apt: $suite (${archs[*]})"
done

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
