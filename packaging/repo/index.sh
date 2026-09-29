#!/usr/bin/env bash
# ==============================================================================
#  EasySB 软件源：摆目录树、生成索引并签名 / lay out the trees, index and sign them
# ------------------------------------------------------------------------------
#  目录形状照 Docker 官方源（download.docker.com/linux）来：
#
#    linux/<发行版>/gpg                                        同一把公钥，armored
#    linux/debian/dists/<套件>/pool/stable/<架构>/*.deb
#    linux/debian/dists/<套件>/stable/binary-<架构>/Packages
#    linux/debian/dists/<套件>/Release | InRelease | Release.gpg
#    linux/<发行版>/easysb.repo                                rpm：登记源只要这一份
#    linux/<发行版>/<发行版号>/<基架>/stable/Packages/*.rpm
#    linux/<发行版>/<发行版号>/<基架>/stable/repodata/…
#
#  The layout follows Docker's official sources (download.docker.com/linux).
#
#  包来自 packages.sh 打好的暂存目录（REPO_PKGS），这里只负责摆放、索引与签名，所以
#  同一份 rpm 可以同时出现在 centos、rhel、rocky 的同名 release 下而不必重打。
#  The packages come from the staging directory packages.sh fills (REPO_PKGS); this only
#  lays them out and indexes them, so one rpm can appear under centos, rhel and rocky
#  for the same release without being rebuilt.
#
#  用法 / Usage: index.sh apt|rpm
# ==============================================================================

set -euo pipefail

mode="${1:-}"
case "$mode" in
  apt|rpm) ;;
  *) echo "用法 / usage: index.sh apt|rpm" >&2; exit 1 ;;
esac

REPO_DIR="${REPO_DIR:?REPO_DIR 未设置 / required}"
REPO_PKGS="${REPO_PKGS:?REPO_PKGS 未设置 / required}"

map_get() {
  local map="$1" key="$2" item
  for item in $map; do
    case "$item" in
      "$key"=*) printf '%s' "${item#*=}"; return 0 ;;
    esac
  done
  return 1
}

# 签名参数：CI 无人值守（--batch），口令从 0600 文件读入，不进进程列表。
# Signing options: --batch for unattended CI, and the passphrase read from a 0600 file so
# it never reaches a process list.
sign=()
if [ -n "${GPG_KEY_ID:-}" ]; then
  sign=(--batch --yes --pinentry-mode loopback)
  if [ -n "${GPG_PASSPHRASE_FILE:-}" ]; then
    sign+=(--passphrase-file "$GPG_PASSPHRASE_FILE")
  fi
fi

have_key() { [ -n "${GPG_KEY_ID:-}" ]; }

# 公钥一律从密钥环导出成 armored 文件，与 Docker 的 linux/<发行版>/gpg 一致。
# The public key is exported from the keyring as an armored file, matching Docker's
# linux/<distro>/gpg.
export_pubkey() {
  have_key || return 0
  local out="$1"
  mkdir -p "$(dirname "$out")"
  gpg --batch --yes --armor --export "$GPG_KEY_ID" > "$out"
}

# ------------------------------------------------------------------------------
# apt：每个发行版一棵树，套件目录下 pool 与 binary-<架构> 并列
# apt: one tree per distribution, pool beside binary-<arch> under the suite
# ------------------------------------------------------------------------------
apt_index() {
  command -v apt-ftparchive >/dev/null 2>&1 || {
    echo "apt-ftparchive 未安装 / missing: apt-get install -y apt-utils" >&2; exit 1; }

  for spec in ${DEB_SUITES:-}; do
    IFS=/ read -r distro suite version_id assets <<< "$spec"
    local root="$REPO_DIR/linux/$distro"
    local -a archs=()
    local asset debarch pool_rel pkgdir_rel

    for asset in ${assets//,/ }; do
      debarch="$(map_get "$DEBARCH_MAP" "$asset")"
      archs+=("$debarch")
      pool_rel="dists/$suite/pool/stable/$debarch"
      pkgdir_rel="dists/$suite/stable/binary-$debarch"
      mkdir -p "$root/$pool_rel" "$root/$pkgdir_rel"

      debver="${VERSION}-1~${distro}.${version_id}~${suite}"
      cp -f "${REPO_PKGS}/${PKG_NAME}_${debver}_${debarch}.deb" "$root/$pool_rel/"

      # Filename 字段是相对发行版树根的路径，所以 Packages 必须在树根下生成；这正是
      # Docker 索引里 dists/<套件>/pool/... 的来处。
      # The Filename field is relative to the distribution root, so Packages is generated
      # from there; that is where Docker's dists/<suite>/pool/... form comes from.
      ( cd "$root" && apt-ftparchive packages "$pool_rel" ) > "$root/$pkgdir_rel/Packages"
      gzip -9 -c "$root/$pkgdir_rel/Packages" > "$root/$pkgdir_rel/Packages.gz"
      cat > "$root/$pkgdir_rel/Release" <<EOF
Component: stable
Architecture: $debarch
Suite: $suite
Origin: $PKG_NAME
Label: $PKG_NAME
EOF
    done

    # Release 不能写进自己的树里再生成：apt-ftparchive 会把已存在的 Release 也算进校验和，
    # 于是文件引用自己。先落到树外，再挪进去。
    # Release cannot be written inside its own tree while it is generated: apt-ftparchive
    # would checksum the file that already exists there and the file would reference
    # itself. It lands outside the tree first and is moved in afterwards.
    local tmp; tmp="$(mktemp)"
    ( cd "$root" && apt-ftparchive \
        -o "APT::FTPArchive::Release::Origin=$PKG_NAME" \
        -o "APT::FTPArchive::Release::Label=$PKG_NAME" \
        -o "APT::FTPArchive::Release::Suite=$suite" \
        -o "APT::FTPArchive::Release::Architectures=${archs[*]}" \
        -o "APT::FTPArchive::Release::Components=stable" \
        -o "APT::FTPArchive::Release::Description=$PKG_DESC" \
        release "dists/$suite" ) > "$tmp"
    mv -f "$tmp" "$root/dists/$suite/Release"

    if have_key; then
      ( cd "$root/dists/$suite" && \
        gpg "${sign[@]}" --armor --detach-sign -u "$GPG_KEY_ID" -o Release.gpg Release && \
        gpg "${sign[@]}" --clearsign   -u "$GPG_KEY_ID" -o InRelease Release )
    fi
    export_pubkey "$root/gpg"
    echo "  apt: $distro/$suite (${archs[*]})"
  done
}

# ------------------------------------------------------------------------------
# rpm：每个发行版一份 easysb.repo，每个 release / 基架一份 rpm-md 目录
# rpm: one easysb.repo per distribution and one rpm-md tree per release / base arch
# ------------------------------------------------------------------------------
rpm_index() {
  command -v createrepo_c >/dev/null 2>&1 || {
    echo "createrepo_c 未安装 / missing: apt-get install -y createrepo-c" >&2; exit 1; }

  # Docker 的 rpm 索引用 zstd 压 repodata；createrepo_c 0.17（Debian 12、Ubuntu 22.04）还
  # 没把这个类型编进去，遇到它要退回 gz，否则整棵树生成到一半就停。空目录上试一次就知道
  # 手上这份支持哪种；压缩方式不属于对外的约定，dnf 两种都读。
  # Docker's rpm index compresses repodata with zstd; createrepo_c 0.17 (Debian 12, Ubuntu
  # 22.04) has no such type compiled in and would abort mid-tree, so fall back to gz there.
  # One run against an empty directory tells which types this build knows; the compression
  # type is not part of the contract and dnf reads either.
  local repodata_compress='gz' probe
  probe="$(mktemp -d)"
  if createrepo_c --quiet --general-compress-type zstd "$probe" >/dev/null 2>&1; then
    repodata_compress='zstd'
  fi
  rm -rf "$probe"

  local gpg_wrap=''
  if have_key; then
    command -v rpm >/dev/null 2>&1 || {
      echo "rpm 未安装 / missing: apt-get install -y rpm" >&2; exit 1; }
    # 口令要读文件，但 rpm 的 __gpg_sign_cmd 不能整条替换：文件名占位符由 rpm 自己注入，
    # 写法还跟着版本变。只把 %{__gpg} 指到包装脚本，真正要加的参数在那里补。
    # The passphrase must come from a file, but rpm's __gpg_sign_cmd cannot be replaced
    # wholesale: rpm injects the file-name placeholders itself and their spelling moves
    # between versions. Only %{__gpg} points at a wrapper, which adds the options.
    gpg_wrap="$(mktemp)"
    printf '#!/bin/sh\n[ "$1" = gpg ] && shift\nexec %s %s "$@"\n' \
      "$(command -v gpg)" "$(printf '%s ' "${sign[@]}")" > "$gpg_wrap"
    chmod +x "$gpg_wrap"
  fi

  local seen_distros=''
  for spec in ${RPM_TREES:-}; do
    IFS=/ read -r distro releasever suffix assets <<< "$spec"

    if ! printf ' %s ' "$seen_distros" | grep -q " $distro "; then
      seen_distros="$seen_distros $distro"
      mkdir -p "$REPO_DIR/linux/$distro"
      {
        printf '[easysb]\nname=EasySB\n'
        printf 'baseurl=%s/linux/%s/$releasever/$basearch/stable\n' "$REPO_URL" "$distro"
        printf 'enabled=1\ngpgcheck=%s\n' "$(have_key && echo 1 || echo 0)"
        have_key && printf 'gpgkey=%s/linux/%s/gpg\n' "$REPO_URL" "$distro"
      } > "$REPO_DIR/linux/$distro/easysb.repo"
      export_pubkey "$REPO_DIR/linux/$distro/gpg"
    fi

    local asset rpmarch dir
    for asset in ${assets//,/ }; do
      rpmarch="$(map_get "$RPMARCH_MAP" "$asset")"
      dir="$REPO_DIR/linux/$distro/$releasever/$rpmarch/stable"
      rm -rf "$dir"; mkdir -p "$dir/Packages"
      pkg="$dir/Packages/${PKG_NAME}-${VERSION}-${suffix}.${rpmarch}.rpm"
      cp -f "${REPO_PKGS}/${PKG_NAME}-${VERSION}-${suffix}.${rpmarch}.rpm" "$pkg"
      if have_key; then
        # rpm 的签名库在 GPG_TTY 未设置、stdin 又不是终端时，会自己去推导终端并失败，于是
        # 每次都打一条 «Could not set GPG_TTY to stdin: Inappropriate ioctl for device»。
        # CI 的 stdin 永远是管道，这条警告必然出现却毫无影响：口令走 --passphrase-file，
        # gpg 处于 --batch，根本不会提示。随便给 GPG_TTY 一个值就能跳过那段推导。
        # rpm's signing library derives the terminal itself when GPG_TTY is unset and stdin is
        # not a tty, fails, and warns «Could not set GPG_TTY to stdin: Inappropriate ioctl for
        # device» every time. CI's stdin is always a pipe, so the warning is guaranteed and
        # harmless: the passphrase comes from --passphrase-file and gpg runs --batch, so it
        # never prompts. Any value for GPG_TTY skips that derivation.
        GPG_TTY=/dev/null rpm --addsign \
          --define "_gpg_name $GPG_KEY_ID" --define "__gpg $gpg_wrap" "$pkg"
      fi
      # createrepo_c 必须在签名之后跑，否则索引里的校验和与签过名的包对不上。
      # createrepo_c has to run after signing, or the checksums in the index no longer
      # match the signed packages.
      createrepo_c --quiet --no-database --general-compress-type "$repodata_compress" "$dir"
      if have_key; then
        gpg "${sign[@]}" --armor --detach-sign -u "$GPG_KEY_ID" \
          -o "$dir/repodata/repomd.xml.asc" "$dir/repodata/repomd.xml"
      fi
    done
    echo "  rpm: $distro/$releasever/$suffix"
  done
  [ -z "$gpg_wrap" ] || rm -f "$gpg_wrap"
}

mkdir -p "$REPO_DIR"
case "$mode" in
  apt) apt_index ;;
  rpm) rpm_index ;;
esac
