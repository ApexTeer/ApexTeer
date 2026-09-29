#!/usr/bin/env bash
# ==============================================================================
#  EasySB 软件源：按发行版打包 / per-distribution packages for the sources
# ------------------------------------------------------------------------------
#  照 Docker 官方源的规矩，包不只按架构出，还按“发行版 + 发行版号”各出一份：Debian /
#  Ubuntu 的每一条套件、Fedora / RHEL 系的每一个 release，版本串里都带着发行版信息
#  （`5.0.0-1~debian.12~bookworm`、`5.0.0-1.el9`）。系统从旧发行版升到新发行版时，
#  包管理器据此判定要升级，所以这一份份包不是重复劳动。
#
#  Docker's official sources build a package per distribution release, not just per
#  architecture: every Debian / Ubuntu suite and every Fedora / RHEL release carries the
#  distribution in its version string (`5.0.0-1~debian.12~bookworm`, `5.0.0-1.el9`). That
#  is what makes an upgrade of the distribution also upgrade the package, so the variants
#  are not duplicate work.
#
#  产物是平的，落在一个暂存目录里，目录树由 index.sh 摆：rpm 侧同一份包会被 centos /
#  rhel / rocky 的同一个 release 共用，放在这里按“后缀 + 架构”只打一次。
#  The output is flat, in one staging directory that index.sh lays out: on the rpm side the
#  same package serves centos, rhel and rocky for one release, so it is built once per
#  suffix and architecture here.
#
#  用法 / Usage: 由 Makefile 调用，参数与变量见下。
#    packages.sh <asset>
#    DEB_SUITES  每项 distro/suite/version-id/asset,asset,...
#    RPM_TREES   每项 distro/releasever/suffix/asset,asset,...
#    DEBARCH_MAP 每项 asset=debian-arch
#    RPMARCH_MAP 每项 asset=rpm-arch
# ==============================================================================

set -euo pipefail

asset="${1:-}"
[ -n "$asset" ] || { echo "用法 / usage: packages.sh <asset>" >&2; exit 1; }

REPO_PKGS="${REPO_PKGS:?REPO_PKGS 未设置 / required}"
STAGE_DIR="${STAGE_DIR:?STAGE_DIR 未设置 / required}"

map_get() {
  local map="$1" key="$2" item
  for item in $map; do
    case "$item" in
      "$key"=*) printf '%s' "${item#*=}"; return 0 ;;
    esac
  done
  return 1
}

debarch="$(map_get "${DEBARCH_MAP:-}" "$asset")" || {
  echo "未知架构 / unknown asset: $asset" >&2; exit 1
}
rpmarch="$(map_get "${RPMARCH_MAP:-}" "$asset")" || {
  echo "未知架构 / unknown asset: $asset" >&2; exit 1
}

stage="${STAGE_DIR}/${asset}"
[ -d "$stage" ] || { echo "暂存树缺失 / staging tree missing: $stage" >&2; exit 1; }
command -v fpm >/dev/null 2>&1 || { echo "fpm 未安装 / fpm missing: gem install --no-document fpm" >&2; exit 1; }

mkdir -p "$REPO_PKGS"

# 架构列表是逗号分隔的，用空格判成员，避免把 amd64 错判进 arm64。
# The architecture list is comma separated; membership is tested with spaces so amd64
# never matches arm64.
has_asset() { case ",$1," in *",$asset,"*) return 0 ;; *) return 1 ;; esac; }

build_deb() {
  local debver="$1" out="$2"
  [ -s "$out" ] && { echo "  已存在 / exists: $(basename "$out")"; return 0; }
  fpm -s dir -t deb --force \
    -n "$PKG_NAME" -v "$debver" -a "$debarch" \
    --category net --license "$PKG_LICENSE" --description "$PKG_DESC" \
    --url "$PKG_URL" --maintainer "$PKG_MAINTAINER" \
    --deb-priority optional --depends ca-certificates \
    --deb-field "Bugs: ${PKG_URL}/issues" \
    --no-deb-generate-changes \
    --after-install packaging/deb/postinst \
    --after-remove packaging/deb/postrm \
    --package "$out" -C "$stage" .
  echo "  deb: $(basename "$out")"
}

build_rpm() {
  local suffix="$1" out="$2"
  [ -s "$out" ] && { echo "  已存在 / exists: $(basename "$out")"; return 0; }
  fpm -s dir -t rpm --force \
    -n "$PKG_NAME" -v "$VERSION" --iteration "1.${suffix}" -a "$rpmarch" \
    --rpm-summary "$PKG_SUMMARY" --license "$PKG_LICENSE" \
    --description "$PKG_DESC" --url "$PKG_URL" \
    --vendor "$PKG_VENDOR" --maintainer "$PKG_MAINTAINER" \
    --depends ca-certificates \
    --after-install packaging/rpm/post \
    --after-remove packaging/rpm/postun \
    --package "$out" -C "$stage" .
  echo "  rpm: $(basename "$out")"
}

echo "== 按发行版打包 / per-distribution packages: ${asset} (${debarch} / ${rpmarch})"

for spec in ${DEB_SUITES:-}; do
  IFS=/ read -r distro suite version_id assets <<< "$spec"
  has_asset "$assets" || continue
  debver="${VERSION}-1~${distro}.${version_id}~${suite}"
  build_deb "$debver" "${REPO_PKGS}/${PKG_NAME}_${debver}_${debarch}.deb"
done

for spec in ${RPM_TREES:-}; do
  IFS=/ read -r _distro _releasever suffix assets <<< "$spec"
  has_asset "$assets" || continue
  build_rpm "$suffix" "${REPO_PKGS}/${PKG_NAME}-${VERSION}-${suffix}.${rpmarch}.rpm"
done
