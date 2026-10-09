# Release Safety Audit — F19 / P3-1

Read-only audit of the release pipeline. **No workflow, tag, release, asset or
package was created, modified or deleted.** The four commits from the previous stage
were left exactly as they were.

- **Audited revision**: `7ca1c31`, working tree clean, four commits on top of the
  clone point `37dae5f`
- **Tags**: `v6.0.0` (the only one, unchanged)
- **Scope**: `.github/workflows/easysb-go-release.yml`,
  `packaging/repo/index.sh`, `packaging/deb/{postinst,postrm}`,
  `Makefile` (`pkg-stage`/`deb-asset`/`packages-asset`/`release-matrix`), `VERSION`,
  `install.sh`
- **Not in scope, per instruction**: implementing the fix, touching `provision.Run`,
  any schema or certificate-layout change

---

## 1. The pipeline, stage by stage

The five things the brief asks to separate are genuinely separate, and only the last
two can change anything a user sees.

| Stage | Where | When it runs | Writes anything outside the runner? |
| :--- | :--- | :--- | :--- |
| **1. Gate (CI)** | `make check` → `lint` + `test` (workflow L92-93); `make test-race` (L99-100) | every qualifying `push`, every `pull_request`, `workflow_dispatch` | **No** |
| **2. Fetch front end** | `make panel` (L77-86), `if: github.event_name != 'pull_request'` | non-PR runs only | No (downloads into `public/dist` for `go:embed`) |
| **3. Build artifacts** | `build` job (L116-172), matrix from `make release-matrix` (L102-104) | after `prepare` | No (per-arch `.deb` uploaded as a workflow artifact) |
| **4. Official Release** | `release` job (L174+), `softprops/action-gh-release` (L229-255) | `needs: [prepare, build]` and `if: github.event_name != 'pull_request'` → i.e. **push to master or `workflow_dispatch` only** | **Yes** — creates/updates the GitHub Release and its assets |
| **5. APT repository** | `make repo` (L226-227) builds and signs into `dist/repo`; the release step's `files: dist/repo/*` (L254-255) uploads that tree to the same Release | inside stage 4 | **Yes** — the flat apt source *is* the Release |

The gate and the publish path are decoupled in the right direction: a pull request
builds and packages but cannot publish (L175).

### 1.1 Trigger conditions

```
L14-17  push: branches: [master]
L18-29  paths: main.go, internal/**, go.mod, go.sum, VERSION, release/TAGS,
                Makefile, packaging/**, scripts/**, install.sh,
                .github/workflows/easysb-go-release.yml
L33-35  pull_request: branches: [master]
L36     workflow_dispatch
```

Anything under `internal/**` qualifies — which is every change in this optimization.

### 1.2 Tag and version source

There is exactly one version source, and no stage consults existing tags before
publishing:

```
L106-108  - name: 读取版本
          run: echo "version=$(tr -d '[:space:]' < VERSION)" >> "$GITHUB_OUTPUT"
L233      tag_name: v${{ needs.prepare.outputs.version }}
L234      name:     v${{ needs.prepare.outputs.version }}
```

The package version comes from the same file, via the Makefile:

```
Makefile L238-249  fpm -s dir -t deb --force -n easysb -v $(VERSION) --iteration 1 \
                     -a $(DEBARCH_$(ASSET)) ... \
                     --package "$(DIST)/easysb_$(VERSION)-1_$(DEBARCH_$(ASSET)).deb"
packaging/repo/index.sh L44  debver="${VERSION}-1"
packaging/repo/index.sh L64  src="$DIST/${PKG_NAME}_${debver}_${debarch}.deb"
```

With `VERSION=6.0.0` the computed tag is `v6.0.0` and the package version is
`6.0.0-1`, filename `easysb_6.0.0-1_amd64.deb`.

**Nothing reads tags before publishing.** The only two places that look at tags come
*after* the upload:

```
L276  gh release view  "$version" --json assets    # prune old assets (post-upload)
L296  gh release list --limit 100 --json tagName   # prune old releases (post-upload)
```

This was verified by grep over the workflow for any pre-publish tag or
release-existence check: there is none.

### 1.3 Why the tag already existing does **not** stop the run

`softprops/action-gh-release` creates the release when the tag is new and **updates
the existing release** when it is not; same-named assets are replaced by default
(`overwrite_files` defaults to true). The repository relies on that behaviour
explicitly — L257-260:

> `action-gh-release` only overwrites same-named files, so old assets linger after a
> rename; pruning after the upload keeps the previous assets if the upload fails.

So a second run with an unchanged `VERSION` republishes the same release with new
bytes rather than failing. `make_latest: true` (L235) keeps it as `latest`.

*Evidence limitation*: the action's behaviour is taken from its documented API and
from this repository's own description of it, not from executing it. Network policy
in this environment blocks fetching the action's source, so I could not re-read it
first-hand. The downstream consequences below hold under **either** behaviour — if
the action failed instead of updating, the outcome would be a failed release job, not
a corrupted one — so the audit's conclusions do not depend on resolving this.

### 1.4 Failure and rollback behaviour

There is **no rollback**. Steps run in order and each is idempotent only in the sense
that a re-run redoes it:

| Step | Partial-failure consequence |
| :--- | :--- |
| `组装并签名软件源` (L226-227) | fails before any upload; nothing published. The safe failure mode. |
| `发布到 Release` (L229-255) | the only step that mutates public state. Assets are uploaded one at a time, so a failure or cancellation part-way leaves a **mixed** set: some assets new, some old. |
| `清理旧资产` (L261-276) | prunes assets not produced by this run. If it fails, stale assets linger; `latest/download` still serves the current names. |
| `清理旧 release` (L285-296) | deletes every release and tag except the current one. If it fails, an older release survives (visible on the Release page, contrary to the intended invariant). |

The mixed-asset window is the sharpest failure mode and deserves stating plainly:
`Packages` / `Release` / `InRelease` carry **checksums of the `.deb` files**. While
the upload is in progress, a client running `apt update` can fetch a new `InRelease`
against an old `.deb`, which apt reports as a hash mismatch. The window is the length
of the upload. Because the filenames do not change between two releases of the same
version (see §2), F19 makes this window not just *possible* but *certain* to produce
mismatched bytes whenever a re-release happens.

There is also a cancellation hazard from the workflow's own concurrency settings:

```
L45-47  concurrency:
          group: easysb-go-release-${{ github.ref }}
          cancel-in-progress: true
```

Two pushes to `master` in quick succession put both in one group, and the second
cancels the first. A cancellation that lands during step 5 leaves exactly the mixed
state described above, with nothing that repairs it until the next run completes.

---

## 2. F19 reproduced statically

The chain, each link from the files above:

1. A push to `master` touches `internal/**` → the run qualifies (L18-20).
2. `prepare` computes `version=6.0.0` → `tag_name=v6.0.0` (L106-108, L233).
3. `build` packages `easysb_6.0.0-1_{amd64,arm64}.deb` (Makefile L238-249).
4. `release` creates or updates the release for `v6.0.0`, which **already exists**
   in the repository (it is the only tag).
5. `dist/repo/*` is uploaded: same filenames, new bytes, and `Packages` /
   `Release` / `InRelease` carry **new checksums for the same version string**.
6. The prune steps delete assets and other releases.

`VERSION` on disk is `6.0.0` and `v6.0.0` is already tagged at the clone point, so
this is not hypothetical: the next qualifying push reproduces it.

### 2.1 Risk assessment

| Risk | Severity | Why |
| :--- | :--- | :--- |
| **Same version, different bytes** | **High** | `6.0.0-1` is the only identifier a user or a mirror sees, and it no longer identifies one build. `releases/latest/download` can serve a different `easysb_6.0.0-1_amd64.deb` than it did yesterday. |
| **APT cannot upgrade** | **High** | `apt` compares versions. An installed `6.0.0-1` sees a candidate `6.0.0-1` and does nothing, so the fixed bytes never reach a host that already has the version — the operator must `apt install --reinstall` or `dpkg -i` by hand, and has no signal that they should. |
| **Checksum mismatch during upload** | Medium | see §1.4. Self-heals when the run finishes; guaranteed to matter on a re-release because the bytes differ while the names do not. |
| **Duplicate assets / stale assets** | Low–Medium | same-named assets are replaced; the prune step removes leftovers. A failed or cancelled prune leaves stale assets, which the prune step's own comment acknowledges. |
| **Release notes name no change** | Low | the body is a fixed template naming only the version (L236-253), so a re-release's notes are identical to the previous one. |
| **`workflow_dispatch` re-publishes on demand** | Low | a manual run of the same commit is idempotent (identical bytes) **as long as the commit is the same**; that is the diagnostic value of the `commit` stamp in `--version`, and it is why the guard proposed in §3 compares SHAs rather than only checking existence. |
| **Historical tag/asset deletion** | Note | the prune deletes every other release **and its tag** (`--cleanup-tag`). That is a deliberate published-history policy (`docs/pitfalls.md`), not a defect — but it means a release cannot be "restored" after a bad publish. |

### 2.2 What makes a re-release *detectable*

Two facts make the fix easy and make the damage diagnosable:

- `--version` prints `6.0.0 (<short-commit>)` whenever a commit was stamped
  (`cmd` package: `versionLine` adds `buildCommit[:7]`), and the release workflow
  passes `COMMIT=${GITHUB_SHA}` into `make packages-asset` (L158). So an installed
  host **can** be asked which commit it runs, even though the package version cannot.
- The package version cannot encode the commit: `fpm -v $(VERSION) --iteration 1`
  and `debver="${VERSION}-1"` are the only inputs.

---

## 3. Smallest compatible fix — proposed, not implemented

Two options; I recommend the first.

### Option A (recommended): fail the run when the computed tag already exists

Add one step in the **`prepare`** job, after `读取版本` (L106-108), so it runs before
anything is built or published and costs one API call:

```yaml
      # 版本号是发布的唯一标识：包版本只有 <VERSION>-1，所以同一个 tag 再发一次
      # 会让同名资产变成不同字节，而 apt 比较版本号后不会升级。VERSION 未变而 tag
      # 已存在时在这里停下，要求显式升版。
      # The version is the release's only identifier: the package version is just
      # <VERSION>-1, so re-publishing a tag replaces same-named assets with different
      # bytes while apt, comparing versions, declines to upgrade. Stop here when the
      # tag exists and VERSION has not moved, and require an explicit bump.
      - name: 防止重复发布同一版本
        env:
          GH_TOKEN: ${{ github.token }}
        run: |
          set -euo pipefail
          tag="v${{ steps.ver.outputs.version }}"
          errf="$(mktemp)"
          # 只把明确的 "不存在" 当作首发；其它失败都停下来，避免在 API 抖动时盲发。
          # Only an explicit "not found" counts as a first publish; any other failure
          # stops the run rather than publishing on a transient API error.
          if gh release view "$tag" --json tagName >/dev/null 2>"$errf"; then
            exists=yes
          elif grep -qiE 'not found|could not find|HTTP 404' "$errf"; then
            exists=no
          else
            echo "::error::无法确认 $tag 是否存在 / cannot confirm whether $tag exists: $(cat "$errf")"
            exit 1
          fi
          rm -f "$errf"
          if [ "$exists" = no ]; then
            echo "首次发布 $tag"
            exit 0
          fi
          # 已存在：只有指向本次提交的重跑才放行，这样上传失败后可以安全重试。
          have="$(gh api "repos/$GITHUB_REPOSITORY/git/ref/tags/$tag" --jq '.object.sha' 2>/dev/null || true)"
          if [ -n "$have" ] && [ "$have" = "$GITHUB_SHA" ]; then
            echo "$tag 已存在且指向本次提交，视为重跑，继续"
            exit 0
          fi
          echo "::error::$tag 已存在（${have:-unknown}），需升版后才能发布 / tag $tag already exists"
          exit 1
```

**The guard's logic was exercised**, with `gh` stubbed so nothing touched the network
and no release was created. Four cases, all as intended:

| Case | Expected | Observed |
| :--- | :--- | :--- |
| tag absent (first publish) | proceed | exit 0, `首次发布 v6.0.0` |
| tag exists, points at this commit (retry after a failed upload) | proceed | exit 0, `已存在且指向本次提交，视为重跑` |
| tag exists, points at another commit (**the F19 case**) | stop | exit 1, `::error::v6.0.0 已存在（bbbb2222），需升版` |
| GitHub API unavailable | stop | exit 1, `::error::无法确认 … HTTP 503` |

That fourth case is a correction to the first draft of this step, which used
`gh release view … || true` and would have **published blind** on a transient API
failure. Requiring an explicit `not found` to mean "first publish" closes it.

One limitation to record: the SHA comparison assumes a lightweight tag, which is what
this workflow creates. If a maintainer ever pushed an *annotated* tag by hand,
`repos/.../git/ref/tags/<tag>` would return the tag object's SHA rather than the
commit's, and a legitimate same-commit retry would be refused. The failure direction
is safe (the run stops; bumping `VERSION` or a `workflow_dispatch` on the same commit
still publishes), but a `git/tags/<sha>` dereference would be more exact.

Properties:

- **Additive.** Reads two API endpoints and exits; it changes no existing step, no
  Makefile target and no packaging script.
- **Idempotent for a retry of the same commit.** A run that failed during upload can
  be re-run: the tag points at that very commit, so the guard passes and the upload
  completes. This matters because the guard would otherwise make a failed release
  unrecoverable without a pointless version bump.
- **Blocks exactly the harmful case**: the tag exists and points at a *different*
  commit, i.e. different bytes about to be published under an unchanged version.
- **Does not touch `v6.0.0` or any published asset.** It only reads.
- **Effect on the existing `v6.0.0`**: nothing changes for already-published users.
  The next time code changes without bumping `VERSION`, the run stops instead of
  republishing — which is the fix.

Caveat worth recording: the guard compares against `GITHUB_SHA` of the *push*. For a
`workflow_dispatch` run the SHA is the selected ref's tip, which behaves the same way.

### Option B (complementary, not a substitute): make the package version unique

Give the package a revision that carries the commit, e.g. `--iteration
"1+$(COMMIT)"` or `1~<shortsha>`, so two builds are distinguishable and apt will
upgrade between them. This is **not** a substitute for Option A: it makes the apt
source bump itself on every push, which turns every merge into a release a user can
install — a much larger behaviour change than this audit is authorised to propose. It
is recorded as a direction, not a recommendation.

### 3.1 Verifiable checks for the three failure classes

Each is observable without publishing anything, and each can be added independently.

**1. Duplicate tag (F19 itself)**

- *Static (this stage*): replay the workflow's own shell — `tr -d '[:space:]' <
  VERSION` → compare with `git tag`. In this checkout that yields `v6.0.0` and a
  matching tag, which is the reproduction.
- *In CI*: Option A's guard. Its verdict is visible in the job log
  (`首次发布` / `已存在且指向本次提交，视为重跑` / `::error::…`).
- *Test*: a shell-level test over the guard script — existing tag with a different
  SHA fails, same SHA passes, absent tag passes. It needs no network if `gh` is
  stubbed, and no release is created.

**2. Version mismatch (VERSION vs the artifact)**

- The invariants are mechanical and cheap to assert before upload:
  - `git tag --points-at HEAD` contains `v$(cat VERSION)` (or no tag does);
  - the built `.deb` control version equals `$(cat VERSION)-1`;
  - the `Filename:` values in `Packages` are exactly the `.deb` basenames present in
    `dist/repo`;
  - `Packages`, `Release` and `InRelease` agree on the checksum of every `.deb` (the
    existing `packaging/repo/index_test.go` already exercises this layout).
- Where this could live, in increasing order of invasiveness: a step in `prepare`
  after `读取版本`; a `make` target the workflow calls; or an extension of the
  existing `packaging/repo/index_test.go`, which already has the fixture machinery.

**3. Release failing part-way**

- Observe the **asset set**: after a run, the assets of `v<VERSION>` must be exactly
  the files in `dist/repo` (the prune step already computes that list as `keep`,
  L267 — it just does not assert it).
- A post-publish verification step could re-read
  `gh release view v<VERSION> --json assets` and fail the job when the set differs, so
  a half-uploaded release is *visible in the run result* rather than silently served.
- The mixed-checksum window in §1.4 cannot be closed by ordering alone, because
  assets upload one at a time. What can be done cheaply is to **not widen** it:
  keep `cancel-in-progress` from interrupting the release job, or accept the window
  and document it. Changing that setting is a separate decision, not part of Option A.

### 3.2 Rollback plan for Option A

- **To revert**: delete the added step. Nothing else refers to it; no state is
  created by it; no tag, asset or package is touched by it. Reverting restores
  today's behaviour exactly.
- **If the guard misbehaves** (e.g. the API is unavailable): make it fail *open* only
  if that is wanted — as written, a failed `gh api` yields an empty `have`, which takes
  the `::error::` branch and stops the publish. A release can still be produced
  deliberately by `workflow_dispatch` on the same commit, or by bumping `VERSION`.
- **Nothing published is affected by adding or removing it.**

---

## 4. Behaviour changes reviewed at code level

The brief asks for code-level compatibility evidence for the two accepted behaviour
changes, not just documentation.

### 4.1 Fresh installs bind loopback; upgrades do not change

Traced through the actual call graph rather than the docs:

- **Every writer of the listen value**: `PANEL_LISTEN` appears in only three places —
  the reader (`config.go:110`), the writer (`config.go:185`), and the env-var name
  (`config.go:67`). Nothing else writes it.
- **Every caller of `panel.Config.Save`** (the only way the file is written):
  - `config.go:164` — inside `EnsureConfig`;
  - `handlers_panel.go:137` — the operator explicitly changing panel settings;
  - `handlers_security.go:143` — the operator enabling/disabling TLS;
  - `handlers_auth.go:102` — the operator changing the password.
  The last three all begin with `LoadConfig` and therefore carry the **persisted**
  `Listen` forward; none of them sets it to a default.
- **`EnsureConfig` is the only one on the startup path**, and it returns immediately
  when `PANEL_PASSWORD_HASH` is non-empty (`config.go:146-150`). An existing
  deployment has a hash, so **the file is not rewritten at all**.
- **`install.sh` never mentions the panel** (verified by grep: no `panel`/`PANEL`
  match), the `.deb` staging tree contains only `usr/bin/easysb`,
  `usr/bin/sb`, `usr/lib/systemd/system/*` and doc files (Makefile L216-233), so
  `/etc/sing-box/easysb-panel.conf` is not a package conffile, and both maintainer
  scripts (`postinst`, `postrm`) only run `systemctl daemon-reload`. There is no
  packaging path that could rewrite it.

Measured, not asserted: I planted a config exactly as a previous version would have
left it (`PANEL_LISTEN="0.0.0.0"` plus a hash) and ran the new `EnsureConfig`
followed by `LoadConfig`. Result — file **byte-identical**, effective listen
`"0.0.0.0"`; a missing file yields `"127.0.0.1"`; a malformed value is passed
through rather than silently replaced, so the bind fails loudly instead of the panel
coming up somewhere unexpected; and `EASYSB_PANEL_LISTEN` overrides the file. (The
probe was a temporary test file, deleted afterwards; `git status` is clean.)

### 4.2 Generic error plus reference

- Classification is preserved: 401 authentication, 400 malformed body, 404 unknown
  route and not-found, 403 CSRF-header, 422 a pair that will not load, 429
  rate-limited login, 500 internal.
- The core's own rejection text is still passed through verbatim
  (`handlers_nodes.go:345-351`).
- The reference correlates to the log: `internalError` writes
  `"panel: <ref> <summary>: <err>"` (`handlers_nodes.go:374-378`) with the same `ref`
  returned to the client; the ref is 8 characters of `secret.Token()`.
- Every `s.opts.Log` call in the package was read: none carries a password, token,
  cookie, private key or config body.

---

## 5. Production scenarios that remain unverified

Stated explicitly; none of these is claimed to work.

| Scenario | Why it cannot be verified here |
| :--- | :--- |
| The release workflow actually running | needs a push to `master`; analysing it statically is what this audit did instead |
| `action-gh-release`'s exact behaviour on an existing tag | network policy blocks fetching the action's source; taken from its documented API and this repository's own description of it ([action README](https://github.com/softprops/action-gh-release), [issue #403 on uploading to an existing tag](https://github.com/softprops/action-gh-release/issues/403)) |
| Whether `v6.0.0` on the remote currently matches this checkout | a local clone cannot tell; the prune step deletes all other releases, so the remote state cannot be inferred from history either |
| Real `apt` upgrade behaviour against the live source | needs a Debian host with the source configured and a published `.deb`; the version-comparison reasoning is from `apt`'s documented semantics, not an execution |
| `make deb` / `make pkg-stage` / `make repo` end to end | `fpm`, `upx`, `apt-ftparchive` and the GPG key are absent in this environment |
| GPG signing of `Release`/`InRelease` | no key available |
| Real systemd, real ACME issuance | no systemd as PID 1; no domain or port 80 |
| The mixed-checksum upload window | would require observing a live upload |

---

## 6. Conclusion

F19 is **confirmed by static analysis** and is reachable on the next qualifying push,
because `VERSION` is still `6.0.0` while `v6.0.0` already exists. The mechanism is
that the release tag is derived only from `VERSION`, nothing checks the tag before
publishing, and the package version (`6.0.0-1`) does not encode the commit — so a
re-release replaces same-named assets with different bytes that `apt` will not
install over the identical version it already has.

The proposed fix is one additive step in `prepare` that fails the run when the tag
exists and points at a different commit. It reads only, changes no existing step,
leaves `v6.0.0` and every published asset alone, and permits a same-commit retry so a
failed release stays recoverable.

**No fix has been applied**, and no tag, release, asset, workflow or package was
modified. The working tree is clean at `7ca1c31`; nothing was pushed.
