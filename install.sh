#!/usr/bin/env bash
# ==============================================================================
#  EasySB 安装脚本 / EasySB installer
#  项目地址 Homepage : https://github.com/EasySBTeam/EasySB
# ==============================================================================
#  一条命令装完，与 Docker 官方的 get.docker.com 同一条路：装好签名公钥，登记唯一的
#  apt 源，再交给 apt 安装。软件源是 GitHub Release 上的一棵扁平 apt 仓库，所有发行版
#  共用同一份包。只支持 Debian 12+ 与 Ubuntu 24.04+（EasySB 的 BBR 内核也只发这两个
#  发行版）。
#  One command does the whole job, the way get.docker.com does it: install the signing
#  key, register the single apt source and let apt install. The source is a flat apt
#  repository hosted on the GitHub Release, one package shared by every distribution. Only
#  Debian 12+ and Ubuntu 24.04+ are supported, the same releases the BBR kernels cover.
#
#  用法 / Usage:
#    curl -fsSL https://github.com/EasySBTeam/EasySB/releases/latest/download/install.sh | sudo bash
#    bash install.sh [--repo-url URL] [--lang C|E]
# ==============================================================================

set -euo pipefail

# 软件源根地址：GitHub Release 的最新资产目录。install.sh 与 Makefile 的 REPO_URL 是同
# 一个地址，两处一起改。
# Public root of the sources: the latest release's asset directory. This and the
# Makefile's REPO_URL are the same address, so the two defaults move together.
REPO_URL="${EASYSB_REPO_URL:-https://github.com/EasySBTeam/EasySB/releases/latest/download}"
# 签名公钥与源列表的落点，caddy 风格：公钥给 signed-by，源单独一份 .list。
# Where the key and the source list land, caddy style: the key feeds signed-by and the
# source is its own .list file.
KEYRING='/usr/share/keyrings/easysb-archive-keyring.gpg'
SOURCES='/etc/apt/sources.list.d/easysb.list'
# 我们出过包的套件 / the suites we publish.
SUPPORTED_SUITES=' bookworm trixie noble '

LANG_MODE='C'
SUDO=''

if [ -t 1 ] && [ -z "${NO_COLOR:-}" ]; then
  C_RESET=$'\033[0m'; C_CYAN=$'\033[36m'; C_GREEN=$'\033[32m'; C_YELLOW=$'\033[33m'; C_RED=$'\033[31m'
else
  C_RESET=''; C_CYAN=''; C_GREEN=''; C_YELLOW=''; C_RED=''
fi
_zh()  { [ "$LANG_MODE" = 'C' ]; }
log()  { printf '%s%s%s\n' "$C_CYAN"   "==> $*" "$C_RESET"; }
ok()   { printf '%s%s%s\n' "$C_GREEN"  "  ✓ $*" "$C_RESET"; }
warn() { printf '%s%s%s\n' "$C_YELLOW" "  ! $*" "$C_RESET" >&2; }
die()  { printf '%s%s%s\n' "$C_RED"    "  ✗ $*" "$C_RESET" >&2; exit 1; }
say()  { if _zh; then printf '%s\n' "$1"; else printf '%s\n' "$2"; fi; }

usage() {
  cat <<'EOF'
EasySB install.sh

  --repo-url URL    软件源根地址 / package source root
  --lang C|E        输出语言 / output language
  -h, --help        显示帮助 / show this help
EOF
}

while [ "$#" -gt 0 ]; do
  case "$1" in
    --repo-url) REPO_URL="${2:-}"; shift 2 ;;
    --repo-url=*) REPO_URL="${1#*=}"; shift ;;
    --lang) LANG_MODE="${2:-C}"; shift 2 ;;
    --lang=*) LANG_MODE="${1#*=}"; shift ;;
    -h|--help) usage; exit 0 ;;
    *) die "unknown argument: $1" ;;
  esac
done
case "$LANG_MODE" in
  E|e|EN|en|en_US) LANG_MODE='E' ;;
  *) LANG_MODE='C' ;;
esac
REPO_URL="${REPO_URL%/}"

# 非 root 时用 sudo；连 sudo 都没有就停下，别让后面的 apt 半途而废。
# Fall back to sudo when not root; stop without sudo rather than half-run apt.
if [ "$(id -u)" -ne 0 ]; then
  command -v sudo >/dev/null 2>&1 || die "$(say '需要 root 权限' 'root privileges required')"
  SUDO='sudo'
fi
as_root() { if [ -n "$SUDO" ]; then $SUDO "$@"; else "$@"; fi; }

# 从 /etc/os-release 取发行版与套件名。只认我们出过包的三条套件；套件名优先用
# VERSION_CODENAME，取不到再按 VERSION_ID 映射。
# Read the distribution and suite from /etc/os-release. Only the suites we publish are
# accepted; VERSION_CODENAME is preferred, with a VERSION_ID mapping as the fallback.
detect_suite() {
  [ -r /etc/os-release ] || die "$(say '找不到 /etc/os-release' '/etc/os-release not found')"
  local id codename vid
  id="$(. /etc/os-release 2>/dev/null; printf '%s' "${ID:-}")"
  codename="$(. /etc/os-release 2>/dev/null; printf '%s' "${VERSION_CODENAME:-}")"
  vid="$(. /etc/os-release 2>/dev/null; printf '%s' "${VERSION_ID:-}")"
  case "$id" in
    debian|ubuntu) ;;
    *) die "$(say "只支持 Debian 与 Ubuntu: $id" "only Debian and Ubuntu are supported: $id")" ;;
  esac
  if [ -z "$codename" ]; then
    case "$id:$vid" in
      debian:12) codename='bookworm' ;;
      debian:13) codename='trixie' ;;
      ubuntu:24.04) codename='noble' ;;
    esac
  fi
  case "$SUPPORTED_SUITES" in
    *" $codename "*) ;;
    *) die "$(say "这个发行版还没有软件源: ${id} ${vid:-$codename}" "no package source for this distribution: ${id} ${vid:-$codename}")" ;;
  esac
  printf '%s' "$codename"
}

main() {
  local suite tmpkey
  suite="$(detect_suite)"
  log "EasySB installer · ${suite} · ${REPO_URL}"

  # 取公钥与写源要用的命令先装齐 / install what fetching the key and writing the source need
  as_root env DEBIAN_FRONTEND=noninteractive apt-get update -qq
  as_root env DEBIAN_FRONTEND=noninteractive apt-get install -y -qq ca-certificates curl gnupg

  log "$(say '安装签名公钥' 'Installing the signing key')"
  as_root install -m 0755 -d /etc/apt/keyrings
  tmpkey="$(mktemp)"
  curl -fsSL --connect-timeout 15 "$REPO_URL/easysb-archive-keyring.asc" -o "$tmpkey" \
    || die "$(say '下载签名公钥失败' 'failed to download the signing key'): $REPO_URL/easysb-archive-keyring.asc"
  gpg --batch --yes --dearmor -o "$tmpkey.gpg" "$tmpkey"
  as_root install -m 0644 "$tmpkey.gpg" "$KEYRING"
  as_root chmod a+r "$KEYRING"
  rm -f "$tmpkey" "$tmpkey.gpg"
  ok "$KEYRING"

  # 扁平 apt 仓库没有 dists/<套件> 分层，发行版字段固定为 ./，所有发行版共用同一份索引
  # 与同一份 .deb；detect_suite 只用于提前拒绝没有包的发行版。
  # A flat apt repository has no dists/<suite> split, so the distribution field is ./ and
  # every distribution shares one index and one .deb; detect_suite only rejects a
  # distribution we do not ship for.
  log "$(say '登记软件源' 'Registering the package source')"
  as_root tee "$SOURCES" >/dev/null <<EOF
deb [signed-by=${KEYRING}] ${REPO_URL} ./
EOF
  ok "$SOURCES"

  log "$(say '通过 apt 安装 easysb' 'Installing easysb through apt')"
  as_root env DEBIAN_FRONTEND=noninteractive apt-get update -qq
  as_root env DEBIAN_FRONTEND=noninteractive apt-get install -y easysb

  printf '\n'
  ok "$(say '安装完成，运行 sb 启动' 'Installation complete, run sb to start')"
}

main "$@"
