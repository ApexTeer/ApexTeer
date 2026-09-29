#!/usr/bin/env bash
# ==============================================================================
#  EasySB 安装脚本 / EasySB installer
#  项目地址 Homepage : https://github.com/MinimaxFlora/EasySB
# ==============================================================================
#  三种安装方式，各走各的路 / three ways in, each kept apart:
#
#    --method auto     一键：下载发布压缩包装到本机（默认）
#                      one-click: fetch the release tarball and install it locally
#    --method repo     源安装：添加软件源，用系统包管理器安装
#                      repository: add the package source and install through the OS
#    --method package  手动安装：安装一个已经下载好的安装包文件
#                      manual: install a package file you already downloaded
#
#  内核已编译进面板：sing-box 是面板自己的依赖，装完就有，不需要再下载内核。
#  The core is compiled into the panel: sing-box is a dependency of the binary itself,
#  so a finished install already carries it and nothing downloads a core.
#
#  用法 / Usage:
#    bash install.sh                          # 一键安装或升级
#    bash install.sh --method repo            # 从软件源安装
#    bash install.sh --method package --package ./easysb_5.0.0_linux_amd64.deb
#    bash install.sh --from-source            # 强制从源码构建
#    bash install.sh --binary ./easysb        # 使用本地已编译好的二进制
#    bash install.sh --lang E                 # 英文输出
#
#  版本号没有常量：源码树内取根目录 VERSION，独立运行时从默认分支读取同一个文件。
#  The version is not a constant: the in-tree VERSION inside a checkout, otherwise the
#  same file read from the default branch.
# ==============================================================================

set -euo pipefail

REPO='MinimaxFlora/EasySB'
# 软件源根地址 / package source root. install.sh、发布工作流与服务器置备脚本用的是同一处。
REPO_URL="${EASYSB_REPO_URL:-https://sb.kejizero.xyz}"
# VERSION / RELEASE_TAG 由 resolve_version 填充（见下），这里不写死任何版本号。
# VERSION / RELEASE_TAG are filled in by resolve_version; no version is hardcoded.
VERSION=''
RELEASE_TAG=''
PREFIX="${PREFIX:-/usr/local}"
BIN_NAME='easysb'
# 源码构建必须带这些标签：with_quic 是 Hysteria2 / TUIC，with_utls 是 Reality，
# with_v2ray_api 是账号流量统计；缺了 with_v2ray_api 面板会照常部署可用节点，只是
# 不计流量。release/TAGS 是唯一的标签来源，工作流读同一个文件；这份常量只在这份
# 脚本离开源码树、读不到 release/TAGS 时兜底，因此必须与它保持一致。
# A source build needs these tags: with_quic for Hysteria2 / TUIC, with_utls for
# Reality, and with_v2ray_api for per-account counters (without it the panel still
# deploys a working node, it just cannot count). release/TAGS is the single source of
# truth and the workflow reads the same file; this constant is only the fallback for
# when this script runs outside the source tree, so it has to match it.
DEFAULT_TAGS='with_quic,with_utls,with_v2ray_api'

LANG_MODE='C'
METHOD='auto'
FROM_SOURCE=0
LOCAL_BINARY=''
LOCAL_PACKAGE=''
SUDO=''

# ------------------------------------------------------------------------------
# 输出辅助 / Output helpers
# ------------------------------------------------------------------------------
if [ -t 1 ] && [ -z "${NO_COLOR:-}" ]; then
  C_RESET=$'\033[0m'; C_CYAN=$'\033[36m'; C_BLUE=$'\033[34m'
  C_GREEN=$'\033[32m'; C_YELLOW=$'\033[33m'; C_RED=$'\033[31m'; C_DIM=$'\033[2m'
else
  C_RESET=''; C_CYAN=''; C_BLUE=''; C_GREEN=''; C_YELLOW=''; C_RED=''; C_DIM=''
fi

_zh() { [ "$LANG_MODE" = 'C' ]; }

log()  { printf '%s%s%s\n' "$C_CYAN"   "==> $*" "$C_RESET"; }
ok()   { printf '%s%s%s\n'  "$C_GREEN"  "  ✓ $*" "$C_RESET"; }
warn() { printf '%s%s%s\n'  "$C_YELLOW" "  ! $*" "$C_RESET" >&2; }
err()  { printf '%s%s%s\n'  "$C_RED"    "  ✗ $*" "$C_RESET" >&2; }
dim()  { printf '%s%s%s\n'  "$C_DIM"    "    $*" "$C_RESET"; }

die() {
  err "$@"
  exit 1
}

# 中英双语提示 / bilingual notice
say() {
  local cn="$1" en="$2"
  if _zh; then printf '%s\n' "$cn"; else printf '%s\n' "$en"; fi
}

# ------------------------------------------------------------------------------
# 参数解析 / Argument parsing
# ------------------------------------------------------------------------------
usage() {
  cat <<'EOF'
EasySB install.sh

  --method auto|repo|package
                    安装方式 / how to install
                      auto    下载发布压缩包装到本机（默认）
                      repo    添加软件源，用系统包管理器安装
                      package 安装 --package 指定的安装包文件
  --package PATH    配合 --method package：.deb / .rpm / .pkg.tar.zst / .tar.gz
  --repo-url URL    软件源根地址 / package source root
  --lang C|E        输出语言 / output language
  --from-source     强制从源码构建 / force build from source
  --binary PATH     使用指定二进制 / use a local binary
  --version V       指定版本（默认取当前发布版本）/ pin a version
  -h, --help        显示帮助 / show this help

面板只用终端自带字形，不再安装 Nerd Font；为兼容旧脚本，--no-font 与
--font-only 仍被接受，但不再做任何事。
The panel only uses glyphs shipped with terminal fonts and no longer installs
a Nerd Font; --no-font and --font-only are still accepted for old scripts but
no longer do anything.
EOF
}

while [ "$#" -gt 0 ]; do
  case "$1" in
    --method) METHOD="${2:-}"; shift 2 ;;
    --method=*) METHOD="${1#*=}"; shift ;;
    --package) LOCAL_PACKAGE="${2:-}"; shift 2 ;;
    --package=*) LOCAL_PACKAGE="${1#*=}"; shift ;;
    --repo-url) REPO_URL="${2:-}"; shift 2 ;;
    --repo-url=*) REPO_URL="${1#*=}"; shift ;;
    --version) VERSION="${2:-}"; shift 2 ;;
    --version=*) VERSION="${1#*=}"; shift ;;
    --lang) LANG_MODE="${2:-C}"; shift 2 ;;
    --lang=*) LANG_MODE="${1#*=}"; shift ;;
    --no-font|--font-only) shift ;;
    --from-source) METHOD='auto'; FROM_SOURCE=1; shift ;;
    --binary) METHOD='auto'; LOCAL_BINARY="${2:-}"; shift 2 ;;
    --binary=*) METHOD='auto'; LOCAL_BINARY="${1#*=}"; shift ;;
    -h|--help) usage; exit 0 ;;
    *) die "unknown argument: $1" ;;
  esac
done
case "$LANG_MODE" in
  C|c|CN|zh|zh_CN) LANG_MODE='C' ;;
  E|e|EN|en|en_US) LANG_MODE='E' ;;
  *) LANG_MODE='C' ;;
esac
case "$METHOD" in
  auto|repo|package) ;;
  *) die "$(say "未知安装方式: $METHOD（auto / repo / package）" "unknown method: $METHOD (auto / repo / package)")" ;;
esac
REPO_URL="${REPO_URL%/}"

# ------------------------------------------------------------------------------
# 系统探测 / System detection
# ------------------------------------------------------------------------------
PKG_MGR=''
ARCH=''
OS_ID=''

detect_system() {
  local id_like=''
  if [ -r /etc/os-release ]; then
    # 在子 shell 里取这两个字段，不要 source 进本 shell：os-release 自带一个 VERSION
    # 字段（Debian 13 上是 "13 (trixie)"、12 上是 "12 (bookworm)"），source 会把脚本
    # 自己的 VERSION 覆盖成这个系统版本串，resolve_version 随后把它当成用户指定的版本号，
    # 拼出一个不存在的下载地址，只剩一句 404。
    # Read these two fields in a subshell instead of sourcing: os-release has a VERSION
    # field of its own ("13 (trixie)" on Debian 13, "12 (bookworm)" on 12), and sourcing it
    # overwrites the script's VERSION, which resolve_version then takes for a version the
    # user pinned and turns into a dead download URL whose only symptom is a bare 404.
    OS_ID="$(. /etc/os-release 2>/dev/null; printf '%s' "${ID:-linux}")"
    id_like="$(. /etc/os-release 2>/dev/null; printf '%s' "${ID_LIKE:-}")"
  else
    OS_ID='linux'
  fi

  if command -v apt-get >/dev/null 2>&1; then PKG_MGR='apt'
  elif command -v dnf >/dev/null 2>&1; then PKG_MGR='dnf'
  elif command -v yum >/dev/null 2>&1; then PKG_MGR='yum'
  elif command -v apk >/dev/null 2>&1; then PKG_MGR='apk'
  elif command -v pacman >/dev/null 2>&1; then PKG_MGR='pacman'
  elif command -v zypper >/dev/null 2>&1; then PKG_MGR='zypper'
  else PKG_MGR='unknown'; fi

  case "$(uname -m)" in
    x86_64|amd64) ARCH='amd64' ;;
    aarch64|arm64) ARCH='arm64' ;;
    armv7l|armv7) ARCH='armv7' ;;
    armv6l|armv6) ARCH='armv6' ;;
    i386|i486|i586|i686) ARCH='386' ;;
    riscv64) ARCH='riscv64' ;;
    s390x) ARCH='s390x' ;;
    *) die "unsupported architecture: $(uname -m)" ;;
  esac

  # OS_ID 与 ID_LIKE 只进日志那一行，选包管理器看的是哪个命令真的在 PATH 里。
  # OS_ID and ID_LIKE only feed the log line; the package manager is whichever command
  # is actually on PATH.
  : "$id_like"
}

# 独立运行时从默认分支读取 VERSION 文件（与 internal/update 是同一个地址），而不是
# 在脚本里写死版本号。用 raw 文件而非 GitHub API：匿名 API 有 60 次/小时的限流，raw
# 没有。
# Standalone runs read the VERSION file from the default branch — the same URL
# internal/update uses — instead of pinning a number. The raw file is used rather than
# the GitHub API because anonymous API calls are rate limited to 60 per hour.
latest_version() {
  local url="https://raw.githubusercontent.com/${REPO}/master/VERSION" v
  v="$(curl -fsSL -A 'EasySB-installer' --connect-timeout 15 "$url" 2>/dev/null)" || return 1
  v="$(printf '%s' "$v" | tr -d '[:space:]')"
  [ -n "$v" ] || return 1
  printf '%s' "$v"
}

# VERSION 的唯一来源；RELEASE_TAG 永远由它派生，脚本里不再出现第二个版本号。
# The single source of VERSION; RELEASE_TAG is always derived from it, so the script
# carries no second copy of the number.
# 版本号应该是 v?数字(.数字)*。检查一道，是为了让被污染的值在拼地址之前就报出来，
# 而不是去下载一个不存在的压缩包、只得到一句 404。
# A version is v?digits(.digits)*. The check exists so a polluted value is reported before
# it becomes a URL, rather than surfacing as a download of something that is not there.
valid_version() {
  case "$1" in
    ''|*[!0-9.]*) return 1 ;;
  esac
  return 0
}

resolve_version() {
  local dir v

  # 命令行指定的版本优先，其次本地二进制自带版本号。
  # An explicit --version wins; next a local binary carries its own version.
  if [ -n "$VERSION" ]; then
    VERSION="$(printf '%s' "$VERSION" | tr -d '[:space:]')"
    VERSION="${VERSION#v}"
    valid_version "$VERSION" || die "$(say "版本号不合法: $VERSION" "not a version number: $VERSION")"
    RELEASE_TAG="v${VERSION}"
    return 0
  fi
  if [ -n "$LOCAL_BINARY" ]; then
    VERSION="$( { "$LOCAL_BINARY" --version 2>/dev/null || true; } | sed -n 's/^EasySB[[:space:]]*\([^[:space:]]*\).*/\1/p' | head -1)" || VERSION=''
    RELEASE_TAG=''
    return 0
  fi

  # 源码树内以根目录 VERSION 为准（发布工作流读的就是它）。
  # Inside a checkout the root VERSION file wins; it is what the release workflow reads.
  dir="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
  if [ -r "$dir/VERSION" ]; then
    v="$(tr -d '[:space:]' < "$dir/VERSION")"
    if [ -n "$v" ]; then
      VERSION="$v"
      RELEASE_TAG="v${VERSION}"
      return 0
    fi
  fi

  # 独立运行（curl | bash）：从默认分支读取当前版本，而不是固定写死。
  # Standalone (curl | bash): read the current version from the default branch instead
  # of pinning one.
  VERSION="$(latest_version)" || VERSION=''
  [ -n "$VERSION" ] || die "$(say '无法获取最新版本号，请在源码树内运行或检查网络' 'cannot determine the latest version; run inside the source tree or check the network')"
  RELEASE_TAG="v${VERSION}"
}

# 发布压缩包名，与 Makefile 和 internal/update 的拼法一致。
# The release archive name, spelled the same as the Makefile and internal/update.
tarball_name() {
  printf 'easysb-%s-linux-%s.tar.gz' "$VERSION" "$ARCH"
}

# ------------------------------------------------------------------------------
# 提权 / Privileges
# ------------------------------------------------------------------------------
as_root() {
  if [ "$(id -u)" -eq 0 ]; then
    "$@"
  elif [ -n "$SUDO" ]; then
    $SUDO "$@"
  else
    die "$(say '需要 root 权限' 'root privileges required')"
  fi
}

setup_sudo() {
  if [ "$(id -u)" -ne 0 ] && command -v sudo >/dev/null 2>&1; then
    SUDO='sudo'
  fi
}

# ------------------------------------------------------------------------------
# 依赖安装 / Dependency installation
# ------------------------------------------------------------------------------
pkg_install() {
  [ "$#" -eq 0 ] && return 0
  case "$PKG_MGR" in
    apt)
      as_root env DEBIAN_FRONTEND=noninteractive apt-get update -qq
      as_root env DEBIAN_FRONTEND=noninteractive apt-get install -y -qq "$@"
      ;;
    dnf)    as_root dnf install -y -q "$@" ;;
    yum)    as_root yum install -y -q "$@" ;;
    apk)    as_root apk add --no-cache "$@" ;;
    pacman) as_root pacman -Sy --noconfirm --needed "$@" ;;
    zypper) as_root zypper -n install "$@" ;;
    *)      return 1 ;;
  esac
}

# 一键方式需要的运行期命令 / the runtime commands the one-click path needs
ensure_runtime_deps() {
  log "$(say '检查运行时依赖' 'Checking runtime dependencies')"
  # 证书用面板内置的 lego 申请，验证在面板自己的进程里完成，所以不再需要 socat 这类
  # 帮 acme.sh 监听 80 端口的工具。
  # Certificates come from the lego client compiled into the panel, which answers the
  # challenge from its own process, so the helper acme.sh needed to hold port 80 is gone.
  local missing=()
  for cmd in curl openssl jq tar gzip; do
    command -v "$cmd" >/dev/null 2>&1 || missing+=("$cmd")
  done

  if [ "${#missing[@]}" -gt 0 ]; then
    say "安装缺失依赖: ${missing[*]}" "Installing missing packages: ${missing[*]}"
    local pkgs=("${missing[@]}")
    pkg_install "${pkgs[@]}" || warn "$(say '部分依赖安装失败，请手动安装' 'some packages failed, install manually')"
  fi
  for cmd in curl openssl jq tar gzip; do
    command -v "$cmd" >/dev/null 2>&1 && ok "$cmd" || warn "$cmd $(say '缺失' 'missing')"
  done

  # qrencode 可选，用于终端二维码 / optional, terminal QR codes
  if command -v qrencode >/dev/null 2>&1; then
    ok 'qrencode'
  else
    if pkg_install qrencode >/dev/null 2>&1; then
      ok 'qrencode'
    else
      dim "$(say 'qrencode 未安装，二维码改为内置渲染' 'qrencode absent, built-in QR renderer is used')"
    fi
  fi
}

# 构建依赖 Go / Go toolchain for source builds
ensure_go() {
  if command -v go >/dev/null 2>&1 && go version >/dev/null 2>&1; then
    ok "$(go version)"
    return 0
  fi
  log "$(say '未检测到 Go，正在安装工具链' 'Go not found, installing toolchain')"
  case "$PKG_MGR" in
    apt)    pkg_install golang-go ;;
    dnf|yum) pkg_install golang ;;
    apk)    pkg_install go ;;
    pacman) pkg_install go ;;
    zypper) pkg_install go ;;
    *)      _install_go_tarball ;;
  esac
  command -v go >/dev/null 2>&1 || _install_go_tarball
  command -v go >/dev/null 2>&1 || die "$(say 'Go 安装失败' 'failed to install Go')"
  ok "$(go version)"
}

# 官方 tarball 兜底 / Fallback: official tarball
_install_go_tarball() {
  local goversion='1.27.1' goarch tgz tmp
  case "$ARCH" in
    amd64) goarch='amd64' ;;
    arm64) goarch='arm64' ;;
    armv7) goarch='armv6l' ;;
    386)   goarch='386' ;;
    *)     return 1 ;;
  esac
  tgz="go${goversion}.linux-${goarch}.tar.gz"
  tmp="$(mktemp -d)"
  say "下载 Go ${goversion} (${goarch})" "Downloading Go ${goversion} (${goarch})"
  if ! curl -fsSL --connect-timeout 20 -o "$tmp/$tgz" "https://go.dev/dl/$tgz"; then
    warn "$(say '下载 Go 失败' 'failed to download Go')"
    return 1
  fi
  as_root tar -C /usr/local -xzf "$tmp/$tgz" || return 1
  export PATH="/usr/local/go/bin:$PATH"
  return 0
}

# ------------------------------------------------------------------------------
# 方式一：一键 / Method one: one-click
# ------------------------------------------------------------------------------
download_tarball() {
  local url="https://github.com/${REPO}/releases/download/${RELEASE_TAG}/$(tarball_name)"
  local out="$1"
  say "尝试下载发布压缩包" "Trying the release tarball"
  dim "$url"
  curl -fsSL -A 'EasySB-installer' --connect-timeout 15 -o "$out" "$url" 2>/dev/null || return 1
  [ -s "$out" ] || return 1
  return 0
}

build_from_source() {
  local out="$1" srcdir tags
  srcdir="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
  [ -f "$srcdir/go.mod" ] || { warn "$(say '未找到源码' 'source tree not found')"; return 1; }
  ensure_go
  tags="$DEFAULT_TAGS"
  [ -r "$srcdir/release/TAGS" ] && tags="$(tr -d '[:space:]' < "$srcdir/release/TAGS")"
  say "正在从源码构建" "Building from source"
  dim "tags: $tags"
  ( cd "$srcdir" && CGO_ENABLED=0 go build -trimpath -tags "$tags" \
      -ldflags "-s -w" -o "$out" . ) || return 1
  [ -s "$out" ] || return 1
  return 0
}

# 从发布压缩包取出 easysb 可执行文件 / pull the easysb executable out of a tarball
unpack_binary() {
  local tgz="$1" out="$2" dir found
  dir="$(mktemp -d)"
  if ! tar -xzf "$tgz" -C "$dir" 2>/dev/null; then
    rm -rf "$dir"; return 1
  fi
  found="$(find "$dir" -maxdepth 2 -type f -name easysb -print -quit)"
  if [ -z "$found" ]; then
    rm -rf "$dir"; return 1
  fi
  cp -f "$found" "$out"
  rm -rf "$dir"
}

install_binary_file() {
  local bin="$1"
  as_root install -m 0755 "$bin" "$PREFIX/bin/$BIN_NAME"
  as_root ln -sf "$PREFIX/bin/$BIN_NAME" "$PREFIX/bin/sb"
  ok "$(say '已安装' 'installed'): $PREFIX/bin/$BIN_NAME"
  ok "$(say '快捷指令' 'shortcut'): sb"
}

install_auto() {
  ensure_runtime_deps
  local tmp bin
  tmp="$(mktemp -d)"
  bin="$tmp/easysb"

  if [ -n "$LOCAL_BINARY" ]; then
    [ -s "$LOCAL_BINARY" ] || die "$(say '指定的二进制不存在' 'given binary not found'): $LOCAL_BINARY"
    cp -f "$LOCAL_BINARY" "$bin"
  elif [ "$FROM_SOURCE" -eq 0 ] && download_tarball "$tmp/pkg.tar.gz" && unpack_binary "$tmp/pkg.tar.gz" "$bin"; then
    ok "$(say '已获取发布压缩包' 'release tarball downloaded')"
  elif [ -x "$(dirname "${BASH_SOURCE[0]}")/easysb" ]; then
    cp -f "$(dirname "${BASH_SOURCE[0]}")/easysb" "$bin"
    ok "$(say '使用仓库内已编译二进制' 'using in-tree binary')"
  else
    build_from_source "$bin" || die "$(say '无法获取 EasySB 二进制' 'cannot obtain EasySB binary')"
  fi

  chmod 0755 "$bin"
  install_binary_file "$bin"
  rm -rf "$tmp"
}

# ------------------------------------------------------------------------------
# 方式二：源安装 / Method two: repository
# ------------------------------------------------------------------------------
apt_add_repo() {
  local keyring='/etc/apt/keyrings/easysb.gpg' tmpkey trusted=''
  tmpkey="$(mktemp)"
  as_root install -d -m 0755 /etc/apt/keyrings
  if curl -fsSL --connect-timeout 15 "$REPO_URL/apt/easysb.gpg" -o "$tmpkey" 2>/dev/null && [ -s "$tmpkey" ]; then
    as_root install -m 0644 "$tmpkey" "$keyring"
    ok "$(say '已安装签名密钥' 'signing key installed'): $keyring"
  else
    # 拿不到公钥时退回信任该源：HTTPS 仍保证传输不被替换，只是不校验索引签名。
    # Without the public key fall back to trusting the source: HTTPS still protects the
    # transport, only the index signature goes unchecked.
    warn "$(say '未取到签名密钥，改用信任该源' 'no signing key, trusting the source instead')"
    trusted='Trusted: yes'
  fi
  rm -f "$tmpkey"

  if [ -n "$trusted" ]; then
    as_root tee /etc/apt/sources.list.d/easysb.sources >/dev/null <<EOF
Types: deb
URIs: ${REPO_URL}/apt
Suites: ./
Trusted: yes
EOF
  else
    as_root tee /etc/apt/sources.list.d/easysb.sources >/dev/null <<EOF
Types: deb
URIs: ${REPO_URL}/apt
Suites: ./
Signed-By: ${keyring}
EOF
  fi
  ok "/etc/apt/sources.list.d/easysb.sources"

  as_root env DEBIAN_FRONTEND=noninteractive apt-get update -qq
  as_root env DEBIAN_FRONTEND=noninteractive apt-get install -y easysb
}

# 源带签名时按本格式的写法打开校验：包签名（gpgcheck）与 repomd.xml.asc（repo_gpgcheck），
# 公钥先导入 rpmdb，这样 dnf 与 zypper 都能认。公钥不在时退回不校验并说明原因，而不是让
# dnf 半路报 GPG check FAILED。
# When the source is signed, both checks go on the way this format expects: package
# signatures (gpgcheck) and repomd.xml.asc (repo_gpgcheck). The key is imported into the
# rpmdb so both dnf and zypper accept it. Without the key the entry stays permissive and
# says so, instead of letting dnf fail midway with GPG check FAILED.
rpm_add_repo() {
  local tmpkey check=0 keyline=''
  tmpkey="$(mktemp)"
  if curl -fsSL --connect-timeout 15 "$REPO_URL/rpm/RPM-GPG-KEY-easysb" -o "$tmpkey" 2>/dev/null && [ -s "$tmpkey" ] \
    && as_root rpm --import "$tmpkey" 2>/dev/null; then
    check=1
    keyline="gpgkey=${REPO_URL}/rpm/RPM-GPG-KEY-easysb"
    ok "$(say '已导入签名密钥，开启 GPG 校验' 'signing key imported, GPG checking on')"
  else
    warn "$(say '未取到可用的签名密钥，本源的 GPG 校验保持关闭' 'no usable signing key, GPG checking stays off for this source')"
  fi
  rm -f "$tmpkey"

  as_root mkdir -p /etc/yum.repos.d
  as_root tee /etc/yum.repos.d/easysb.repo >/dev/null <<EOF
[easysb]
name=EasySB
baseurl=${REPO_URL}/rpm/\$basearch
enabled=1
type=rpm-md
gpgcheck=${check}
repo_gpgcheck=${check}
${keyline}
EOF
  ok "/etc/yum.repos.d/easysb.repo"

  case "$PKG_MGR" in
    dnf)
      # dnf5 用 config-manager addrepo 把仓库登记进它的配置；dnf4 读的就是这个文件，
      # 不需要再登记。两者都认 baseurl 里的 $basearch。
      # dnf5 registers the repository through config-manager addrepo; dnf4 reads the
      # file as written. Both expand $basearch in the baseurl.
      if dnf --version 2>/dev/null | head -1 | grep -q 'dnf5'; then
        as_root dnf config-manager addrepo --from-repofile=/etc/yum.repos.d/easysb.repo
      else
        dim "$(say 'dnf4：仓库文件已就位' 'dnf4: the repo file is in place')"
      fi
      as_root dnf install -y easysb
      ;;
    yum) as_root yum install -y easysb ;;
    zypper)
      as_root zypper -n addrepo -f "${REPO_URL}/rpm/\$basearch" easysb >/dev/null 2>&1 || true
      as_root zypper -n --gpg-auto-import-keys refresh easysb
      as_root zypper -n install easysb
      ;;
    *) die "$(say '这个系统没有可用的 RPM 包管理器' 'no RPM package manager on this system')" ;;
  esac
}

pacman_add_repo() {
  local conf='/etc/pacman.conf' tmpkey fpr siglevel='Optional TrustAll'
  tmpkey="$(mktemp)"
  if curl -fsSL --connect-timeout 15 "$REPO_URL/pacman/easysb.asc" -o "$tmpkey" 2>/dev/null && [ -s "$tmpkey" ]; then
    fpr="$(gpg --batch --with-colons --import-options show-only --import "$tmpkey" 2>/dev/null \
      | awk -F: '/^fpr:/{print $10; exit}' || true)"
    # pacman 只在本地信任过这把密钥后才认它的签名，所以导入之后还要 lsign 一次；指纹由这份
    # 公钥自己算出来，不写死在脚本里。
    # pacman trusts a signature only after the key has been locally signed, so the import is
    # followed by an lsign. The fingerprint is read from the key itself, never hardcoded.
    if [ -n "$fpr" ]; then
      if [ ! -d /etc/pacman.d/gnupg ]; then
        as_root pacman-key --init >/dev/null 2>&1 || true
      fi
      if as_root pacman-key --add "$tmpkey" >/dev/null 2>&1 && as_root pacman-key --lsign-key "$fpr" >/dev/null 2>&1; then
        siglevel='Required DatabaseRequired'
        ok "$(say '已导入并本地信任签名密钥' 'signing key imported and locally trusted'): $fpr"
      else
        warn "$(say '签名密钥导入失败，本源的校验保持宽松' 'could not import the signing key, this source stays permissive')"
      fi
    fi
  else
    warn "$(say '未取到签名密钥，本源的校验保持宽松' 'no signing key, this source stays permissive')"
  fi
  rm -f "$tmpkey"

  if ! grep -qE '^\[easysb\]' "$conf" 2>/dev/null; then
    as_root tee -a "$conf" >/dev/null <<EOF

[easysb]
SigLevel = ${siglevel}
Server = ${REPO_URL}/pacman/\$arch
EOF
    ok "$(say '已写入' 'written'): $conf"
  else
    dim "$(say '源已存在，跳过' 'source already present, skipping')"
  fi
  as_root pacman -Sy --noconfirm
  as_root pacman -S --noconfirm easysb
}

install_repo() {
  log "$(say '添加软件源并安装' 'Adding the package source and installing')"
  dim "repo: $REPO_URL"
  case "$PKG_MGR" in
    apt)    apt_add_repo ;;
    dnf|yum|zypper) rpm_add_repo ;;
    pacman) pacman_add_repo ;;
    *) die "$(say "这个系统（$PKG_MGR）没有对应的软件源写法" "no repository recipe for $PKG_MGR")" ;;
  esac
  ok "$(say '安装完成' 'installed')"
}

# ------------------------------------------------------------------------------
# 方式三：手动安装一个包文件 / Method three: a package file you already have
# ------------------------------------------------------------------------------
install_package_file() {
  local file="$1"
  [ -s "$file" ] || die "$(say '安装包不存在' 'package not found'): $file"
  log "$(say "安装本地安装包" 'Installing a local package'): $file"

  case "$file" in
    *.tar.gz)
      local tmp bin
      tmp="$(mktemp -d)"
      bin="$tmp/easysb"
      unpack_binary "$file" "$bin" || die "$(say '压缩包里没有 easysb' 'the tarball carries no easysb')"
      chmod 0755 "$bin"
      install_binary_file "$bin"
      rm -rf "$tmp"
      ;;
    *.deb)
      if command -v apt-get >/dev/null 2>&1; then
        as_root dpkg -i "$file" || as_root env DEBIAN_FRONTEND=noninteractive apt-get install -y -f
      else
        die "$(say '这个系统没有 dpkg' 'dpkg is not available here')"
      fi
      ;;
    *.rpm)
      if command -v dnf >/dev/null 2>&1; then
        as_root dnf install -y "$file"
      elif command -v zypper >/dev/null 2>&1; then
        as_root zypper -n --no-gpg-checks install "$file"
      elif command -v rpm >/dev/null 2>&1; then
        as_root rpm -Uvh --replacepkgs "$file"
      else
        die "$(say '这个系统没有 RPM 包管理器' 'no RPM package manager here')"
      fi
      ;;
    *.pkg.tar.zst|*.pkg.tar.xz)
      command -v pacman >/dev/null 2>&1 || die "$(say '这个系统没有 pacman' 'pacman is not available here')"
      as_root pacman -U --noconfirm "$file"
      ;;
    *)
      die "$(say "无法识别的安装包: $file" "unrecognized package: $file")"
      ;;
  esac
  ok "$(say '安装完成' 'installed')"
}

# ------------------------------------------------------------------------------
# 主流程 / Main
# ------------------------------------------------------------------------------
finish_note() {
  printf '\n'
  ok "$(say '安装完成，运行 sb 启动' 'Installation complete, run sb to start')"
  dim "$(say '内核已随面板安装，无需再装 sing-box' 'The sing-box core came with the panel, nothing else to install')"
}

main() {
  setup_sudo
  detect_system

  case "$METHOD" in
    package)
      [ -n "$LOCAL_PACKAGE" ] || die "$(say '--method package 需要 --package 指定文件' '--method package needs --package FILE')"
      resolve_version
      log "EasySB installer · ${OS_ID}/${ARCH} · method=package${VERSION:+ · v${VERSION}}"
      install_package_file "$LOCAL_PACKAGE"
      ;;
    repo)
      log "EasySB installer · ${OS_ID}/${ARCH} · method=repo · pkg=${PKG_MGR}"
      install_repo
      ;;
    auto)
      resolve_version
      log "EasySB installer · ${OS_ID}/${ARCH} · method=auto${VERSION:+ · v${VERSION}}"
      install_auto
      ;;
  esac

  finish_note
}

main "$@"
