#!/usr/bin/env bash
# 把 EasySB-Frontend 发布的前端产物取进 public/dist，public/public.go 的 go:embed 从
# 那里打进二进制。
# 前端由 EasySB-Frontend 的 Actions 构建、以 Release 资产发布；本仓库不提交编译产物，
# 只在构建前跑一次本脚本，缺 index.html 时立即失败，避免发一个没有控制台的包。
# Fetch the panel bundle published by EasySB-Frontend into public/dist, where the
# go:embed in public/public.go picks it up. The bundle is built by EasySB-Frontend's
# Actions and published as a release asset; this repository never commits the built SPA.
# Run this once before a build. It fails fast when the extracted bundle has no index.html,
# so a package without a console is never produced.
#
# 用法 / Usage:
#   bash scripts/fetch-panel.sh                 # 最新正式 Release
#   PANEL_CHANNEL=edge bash scripts/fetch-panel.sh   # 滚动预发布 edge
#   PANEL_REPO=owner/repo bash scripts/fetch-panel.sh
#   PANEL_ASSET_URL=https://…/easysb-panel-dist-v0.1.0.tar.gz \
#     bash scripts/fetch-panel.sh                # 直接给资产地址，跳过 API
set -euo pipefail

# 取哪个仓库、哪个渠道、放到哪里都可以用环境变量覆盖。
# Repository, channel and destination are all overridable from the environment.
#
# The front end moved from EasySB-Panel to EasySB-Frontend. GitHub redirects the old
# name, which is why this kept working, but a redirected request is one rename away from
# a 404 in CI and it costs an extra hop on every build: the current name is used directly.
REPO="${PANEL_REPO:-EasySBTeam/EasySB-Frontend}"
CHANNEL="${PANEL_CHANNEL:-release}"
DEST="${PANEL_DEST:-public/dist}"
ASSET_PREFIX="${PANEL_ASSET_PREFIX:-easysb-panel-dist-}"

# GITHUB_TOKEN 存在时带上，避开匿名限流，也让私有源可用。
# When GITHUB_TOKEN is present it is sent along, which avoids the anonymous rate limit
# and works against a private source.
auth=()
if [ -n "${GITHUB_TOKEN:-}" ]; then
  auth=(-H "Authorization: Bearer ${GITHUB_TOKEN}")
fi

if [ "$CHANNEL" = "edge" ]; then
  endpoint="https://api.github.com/repos/${REPO}/releases/tags/edge"
else
  endpoint="https://api.github.com/repos/${REPO}/releases/latest"
fi

# 直接给出资产地址就跳过 API，离线或本地联调时用得上。
# A direct asset URL skips the API, which is handy offline or when testing locally.
if [ -n "${PANEL_ASSET_URL:-}" ]; then
  url="$PANEL_ASSET_URL"
  echo "面板资产 / panel asset: ${url}"
else
  echo "面板来源 / panel source: ${REPO} (${CHANNEL})"
  json="$(curl -fsSL --retry 3 --retry-delay 2 --max-time 60 \
    "${auth[@]}" -H 'Accept: application/vnd.github+json' "$endpoint")"

  # 从 release JSON 里挑出资产地址。只做字符串筛选，不依赖 jq，脚本在任何 runner 都能跑。
  # Pick the asset URL out of the release JSON with plain string filtering, so the script
  # runs on any runner without requiring jq.
  url="$(printf '%s' "$json" \
    | grep -o '"browser_download_url":[^,]*' \
    | sed 's/.*"\(https[^"]*\)".*/\1/' \
    | grep "/${ASSET_PREFIX}" \
    | grep '\.tar\.gz$' \
    | head -n 1)"
fi

if [ -z "$url" ]; then
  echo "未找到面板资产 / no ${ASSET_PREFIX}*.tar.gz asset in ${REPO} (${CHANNEL})" >&2
  exit 1
fi
echo "下载 / downloading: ${url}"

tmp="$(mktemp -d)"
trap 'rm -rf "${tmp}"' EXIT
curl -fsSL --retry 3 --retry-delay 2 --max-time 120 "${auth[@]}" "$url" -o "${tmp}/panel.tar.gz"

# 就地解包：占位说明 README.md 保留下来，其余文件被这次的产物覆盖。CI 都是全新检出，
# 不会留下上一次的哈希文件。
# Extract in place: the placeholder README.md stays, everything else is overwritten by
# this bundle. CI always runs on a fresh checkout, so no stale hashed file survives.
mkdir -p "$DEST"
tar -xzf "${tmp}/panel.tar.gz" -C "$DEST"

if [ ! -f "${DEST}/index.html" ]; then
  echo "解包后缺少 index.html / extracted bundle has no index.html" >&2
  exit 1
fi

# 占位页的标题里带「占位」二字。取到占位页说明资产本身是占位产物，或者解包覆盖失败：
# 两种情况下打出来的包都没有控制台，而 v6.0.0 正是这样发出去的。这里直接失败，不让一个
# 没有前端的面板进入 release。
# The placeholder page announces itself with "占位" in its title. Landing on it means the
# asset was itself the placeholder, or the extraction did not overwrite the tracked file;
# either way the package would ship without a console, which is what happened to v6.0.0.
# Fail here so a panel with no front end cannot reach a release.
if grep -q '占位' "${DEST}/index.html"; then
  echo "取到的是占位前端，不是真实产物 / the fetched bundle is the placeholder, not the real front end" >&2
  echo "  来源 / source: ${url}" >&2
  echo "  ${DEST}/index.html 仍是占位页 / is still the placeholder page" >&2
  exit 1
fi
echo "面板已就位 / panel bundle in place: ${DEST}"
