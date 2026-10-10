#!/usr/bin/env bash
# 拒绝重新发布一个已经存在的版本标签。
#
# 用法 / Usage: bash scripts/guard-duplicate-release.sh v6.0.1
#
# 调用点是 release 作业，不是 prepare：这个判断只在真正要发布时才有意义，放在 prepare
# 里会让每一次 push 到 master 的运行都失败，连只想验证改动的运行也一起挡掉，而未升版的
# 提交本不该需要一次升版才能跑完校验。
#
# Called from the release job, not from prepare: the judgement only matters when actually
# publishing. In prepare it failed every push to master, including runs that only wanted to
# validate a change, and an un-bumped commit should not need a version bump to be tested.
#
# 为什么必须存在 / Why this has to exist: 包版本只有 <VERSION>-1（Makefile 的
# fpm -v/--iteration 与 packaging/repo/index.sh 的 debver）。同一个 tag 再发一次会把同名
# 资产换成不同字节，而 apt 比较版本号后不会升级——修复后的字节永远到不了已经装了
# <VERSION>-1 的机器，用户也收不到该升级的信号。所以宁可停下，要求显式升版。
#
# The package version is just <VERSION>-1 (fpm -v/--iteration in the Makefile and debver in
# packaging/repo/index.sh). Re-publishing a tag replaces same-named assets with different
# bytes while apt, comparing versions, declines to upgrade, so the fixed bytes never reach a
# host that already has <VERSION>-1 and the user gets no signal that they should upgrade.
# Stopping and demanding an explicit version bump is the lesser evil.
set -euo pipefail

tag="${1:?用法 / usage: $0 v<version>}"
: "${GITHUB_REPOSITORY:?需要在 GitHub Actions 中运行 / must run inside GitHub Actions}"
: "${GITHUB_SHA:?需要在 GitHub Actions 中运行 / must run inside GitHub Actions}"

errf="$(mktemp)"
trap 'rm -f "$errf"' EXIT

# 先按 ref 读，而不是查 release：release 可能已被清理策略收走，而 tag 本身才是"这个版本
# 是否发布过"的判据。失败关闭：只有明确的 404 才算"不存在"，其余（限流、鉴权、网络、
# 格式变化）一律停下，绝不因 API 抖动而盲发。
#
# Read the ref rather than the release: the prune step can retire a release while its tag
# survives, and the tag is what decides whether this version shipped. Fail closed: only an
# explicit 404 means "absent"; anything else (rate limit, auth, network, a changed response
# shape) stops the run instead of publishing blind on a transient API error.
if ref="$(gh api "repos/$GITHUB_REPOSITORY/git/ref/tags/$tag" \
             --jq '"[\(.object.type)]\(.object.sha)"' 2>"$errf")"; then
  :
elif grep -qE 'HTTP 404|Not Found' "$errf"; then
  # 只有 API 明确回答"没有这个 ref"才是首发。判断按整行匹配错误输出，而不是只看是否
  # 出现 "404"，避免响应体里的其它数字造成误判。
  #
  # Only an explicit "no such ref" from the API means a first publish. The match is anchored
  # to the error output rather than looking for "404" anywhere, so a stray number cannot be
  # mistaken for it.
  echo "首次发布 $tag / first publish"
  exit 0
else
  echo "::error::无法确认 $tag 是否存在，拒绝发布 / cannot confirm whether $tag exists: $(cat "$errf")"
  exit 1
fi

objtype="${ref%%]*}"; objtype="${objtype#[}"
sha="${ref##*]}"

# 只认 commit 与 tag 两种：ref 指向别的东西（tree 等）说明响应不是预期形状，此时不能把
# 它的 sha 当提交比对，否则会给出"升版"这种错误的处置建议。
#
# Only `commit` and `tag` are recognised: anything else (a tree, say) means the response is
# not the shape expected, and treating its sha as a commit would advise the wrong remedy.
if [ "$objtype" != "commit" ] && [ "$objtype" != "tag" ]; then
  echo "::error::$tag 指向未知对象类型 '$objtype' / $tag points at an unexpected object type"
  exit 1
fi

# git/ref/tags 对注记标签返回标签对象自身的 sha，对轻量标签返回提交 sha。注记标签要再解
# 引用一次，否则会把标签对象的 sha 错当成目标提交，同提交重试会被误拒。
#
# git/ref/tags returns the tag object's own sha for an annotated tag and the commit sha for a
# lightweight one. An annotated tag needs one more dereference, or the tag object's sha is
# mistaken for the target commit and a legitimate same-commit retry is refused.
if [ "$objtype" = "tag" ]; then
  if ! sha="$(gh api "repos/$GITHUB_REPOSITORY/git/tags/$sha" --jq '.object.sha' 2>"$errf")"; then
    echo "::error::$tag 是注记标签但无法解引用 / $tag is an annotated tag that cannot be dereferenced: $(cat "$errf")"
    exit 1
  fi
fi

# 只接受 40 位十六进制：结果无法识别时按失败关闭处理。
# Accept only a 40-hex sha; an unrecognised result fails closed.
case "$sha" in
  *[!0-9a-f]*) echo "::error::$tag 的目标提交无法识别 / cannot resolve $tag to a commit: $sha"; exit 1 ;;
esac
if [ "${#sha}" -ne 40 ]; then
  echo "::error::$tag 的目标提交无法识别 / cannot resolve $tag to a commit: $sha"
  exit 1
fi

# 已存在：只有指向本次提交的重跑才放行，这样上传失败后可以安全重试。
# Exists: only a retry of this very commit may continue, so a failed upload stays recoverable
# without a pointless version bump.
if [ "$sha" = "$GITHUB_SHA" ]; then
  echo "$tag 已存在且指向本次提交，视为重跑 / $tag already points at this commit, continuing as a retry"
  exit 0
fi

echo "::error::$tag 已存在（$sha），需升版后才能发布 / $tag already exists at $sha; bump VERSION to publish"
exit 1
