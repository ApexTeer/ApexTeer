# EasySB Optimization — Independent Review (Phase 8)

Read-only review of the eight rounds of optimization changes present in the working
tree, followed by a minimal-scope fix pass for the defects the review itself
confirmed. This document records what was actually inspected, what was found, and
what remains.

## 0. Scene and scope

| Fact | Value |
| :--- | :--- |
| Branch / HEAD | `master` / `37dae5f` (the clone commit) |
| `VERSION` | `6.0.0`; the only tag is `v6.0.0` |
| `git reflog` | exactly one entry — `clone` — so no reset, rebase or history rewrite occurred |
| Staged / committed during review | nothing; every change is unstaged working-tree state |
| Tags, releases, packages created | none |
| Files changed | 39 modified + 5 new paths |
| Diff size | `3151 insertions(+), 442 deletions(-)` (was 3126/437 before this review's two fixes and doc corrections) |

All unstaged changes are attributable to rounds 1–8. `internal/filelock/filelock.go`,
`lock_unix.go` and `lock_windows.go` are **byte-identical to HEAD** apart from the
`nolockprobe` build-tag experiment described in §3.4, which was fully reverted and
verified gone (`git diff` on those three files is empty).

Verification environment (measured, not assumed):

```
go       : go1.27.1 linux/amd64        GOTOOLCHAIN: local
GOOS/ARCH: linux/amd64                 CGO_ENABLED: 1
os       : Debian GNU/Linux 13 (trixie)  cpu: 16 cores   gcc: 14.2.0
TAGS     : with_clash_api,with_quic,with_utls,with_v2ray_api,with_wireguard
```

The Windows host has no systemd, `apt` or `dpkg`, so the Linux checker is the
authoritative one and is what every number below comes from.

## 1. What was inspected

Not read from the phase reports — read from the working tree.

- **P0 lock/concurrency**: `internal/filelock/{filelock,lock_unix}.go` in full;
  `internal/atomicfile/atomicfile.go` in full and its test; `internal/state/state.go`
  `Locked`/`Modify`/`UpdateNodeDeployed`/`Save`; `internal/deploy/deploy.go`
  `ApplyStore` + `withNodeLock`/`withUserLock`; `internal/provision/provision.go`
  `Run` + its two helpers; `internal/user/migrate.go` `withLock`/`MigrateV2`;
  `internal/stats/loop.go` `Tick`/`markApplied`/`transitions`/`Done`;
  `internal/subd/server.go` `Run`; `internal/toolbox/board.go`.
  Every lock-acquisition site in the tree was enumerated and ordered (§2.1).
- **P0 credentials/configuration recovery**: `internal/user/store.go`
  `Load`/`parseStore`/`quarantine`/`recoverFromBackup`/`Save` and `ForgetNode`;
  `internal/node/node.go` the same set; `internal/cert/{cert,acme}.go` `writeFile`,
  `pairIn`, `installPair`, `legacyDirs`, `Paths`, `ResolveActive`; `internal/user/user.go`
  `EnsureCredentials`/`ensureCredential` for idempotency.
- **P0 panel security**: `internal/panel/{config,run,auth,handlers_auth,server,handlers_nodes,handlers_security,handlers_domains}.go`;
  all 22 route entries in `routes()`; every `s.opts.Log` call in the package;
  `docs/panel-{installation,architecture,troubleshooting}.md` against the code.
- **P1 consistency**: node/user deletion cascade, panel port collision, state
  migration, config-apply flows, and the panel/TUI/provision deploy paths.
- **P1 test effectiveness**: read the new tests for concurrency, recovery, routes,
  port collision, error disclosure, `Ready`, and stale temps; ran the whole suite
  shuffled; ran an A/B experiment with the lock genuinely removed (§3).

## 2. Confirmed-correct key fixes

### 2.1 Lock ordering is globally consistent — no inversion, no nesting

Every acquisition site was listed and ordered. The only two-lock pattern is
**node lock → release → user lock**, in all four places that do it:
`deploy.ApplyStore`, `provision.Run`, `panel.handleDeleteNode`,
`tui.deleteNode`. The state lock (`state.Modify`) is never held while acquiring a
store lock, and no store lock is ever held while acquiring the state lock; the
panel's in-process `writeMu` is always taken **before** a store file lock, never
after. `flock` on a second descriptor in the same process does block, so this
ordering is load-bearing — and it holds. In particular `state.Modify` holds the
lock across its callback, so a callback calling back into the state file would
self-deadlock; all ten callbacks were read individually and none of them re-enter
(§2.2).

### 2.2 No slow work under a state lock

All ten `state.Modify` callbacks are pure mutations. The two that need data from
outside (`netutil.PublicIP` in `panel.issueCertificate` and `tui` issuance) compute
it **before** taking the lock; `tui.editSubPort` loads the node store before the
lock; `cert.Remove`/`cert.Paths` run outside it. `cfg.Save()` does **not**
re-acquire the lock, so `Modify` → `Save` cannot deadlock.

### 2.3 Credentials are persisted before the core uses them

`ApplyStore` repairs and **saves** the account store, then derives the routable set
from what it saved, then deploys outside both locks
(`internal/deploy/deploy.go:247-266`). `provision.Run` writes both stores before
`opt.ApplyStore` and before `issueCertificate`
(`internal/provision/provision.go:340-368`).

The one interaction that could have corrupted a spec-supplied credential is safe:
`provision` sets credentials via `applyAccounts`, then `ApplyStore` runs
`store.Repair` over the same accounts. `ensureCredential`
(`internal/user/user.go:218-232`) only fills a field that is **empty**, so Repair is
idempotent and cannot overwrite what the spec just applied.

### 2.4 The `Applied` flag is self-consistent and self-healing

`ApplyStore` step 4 reloads the store under the lock before `MarkApplied`, so the
flag is always set against the file as it is. If a concurrent writer slips in
between, the flag can briefly disagree with what the core serves — but
`transitions()` (`internal/stats/loop.go:264-275`) compares `Applied` against the
current routable set every cycle, so the next cycle restarts the core and
converges. Benign and bounded.

### 2.5 `atomicfile` mechanics

- Temp file is created with `os.CreateTemp` in the **target's own directory**, so
  the rename stays on one filesystem and is atomic; `O_EXCL` in `CreateTemp` means
  it cannot be steered through a pre-planted symlink.
- `fsync` on the file before the rename, and a best-effort directory flush after,
  with the doc comment correctly distinguishing the two losses they cover.
- `Chmod` is applied to the temp file **before** the rename, and `Chmod` is not
  filtered by umask — so a credential file lands at exactly the requested mode
  regardless of the caller's umask. Verified experimentally earlier in the project
  (mode survives `umask 0777`).
- Every failure path removes its own temp file; the deferred `Close` is a backstop
  and a double close is harmless.
- `copyToBackup` reads the target and calls `write(...)` with `backup=false`, so
  the recursion terminates at depth 1 and never creates `.bak.bak`.
- An empty path is refused with `fs.ErrInvalid` rather than resolving to `"."`.

### 2.6 Certificate pair handling

`installPair` now verifies the CA's certificate/key pair with `tls.X509KeyPair`
**before writing either file** (`internal/cert/acme.go`), so a mismatched response
is refused while the working pair keeps serving. `writeFile` is atomic per file, so
neither half is ever truncated; the first-issuance interruption leaves a state
`pairIn` reports as no pair. The residual renewal window is inherent to replacing
two paths and is documented in three places (§4.1).

### 2.7 Route authorization

`routes()` is the single API surface and `Handler()` registers from it. All 22
entries were checked: 55 method registrations in total, exactly three public
(`POST /auth/login`, `POST /auth/logout`, `GET /terminal/ws`), the last because the
terminal authenticates inside its handler. `require` runs before the CSRF-header
check, so an unauthenticated request gets 401 regardless of method.

## 3. Defects found, with evidence

### 3.1 CONFIRMED, fixed — `TestLockExcludesASecondHolder` could hang on its own failure

`internal/filelock/filelock_test.go:125-133` (before the fix) called `t.Fatalf`
from inside the `select` **while the holder lock was still held**. `t.Fatalf` runs
`runtime.Goexit`, which executes deferred calls in the failing goroutine — so
`defer holder.Unlock()` did run — but the second goroutine was already blocked in
`Acquire` and nothing would ever release it. The intended diagnosis ("a second
Acquire succeeded while the first lock was held") would instead surface as the
5-second timeout branch, i.e. a misleading failure.

Evidence: the structure is visible in the diff; the diagnosis is a Goexit/defer
ordering argument, not a flake observed. Fixed by capturing the result, releasing
the holder **before** any assertion, and only then calling `t.Fatalf`. Verified:
5/5 pass, and the test still detects a genuinely absent lock (§3.2).

### 3.2 Test-strength A/B: the concurrency tests **do** detect a missing lock

This was the review's main open question, and the answer required an experiment
rather than an argument.

Method: a temporary `//go:build nolockprobe && !windows` file replaced `lockFile`
/`unlockFile` with no-ops and `lock_unix.go` was excluded under that tag, so the
lock was **genuinely absent** while the public API was unchanged. Then the same
tests were run.

Result — with the lock genuinely absent:

| Test | Without lock |
| :--- | :--- |
| `TestLockedKeepsConcurrentWriters` (`internal/node`) | **fails 10/10** |
| `TestLockedKeepsConcurrentWriters` (`internal/user`) | **fails 10/10** |
| `TestLockExcludesASecondHolder` (`internal/filelock`) | fails 5/5 |
| `TestUpdateBoardMergesConcurrentWriters` (`internal/toolbox`, pre-existing) | fails 5/5 |

With the lock present, all pass, including 20 consecutive plain and 20 `-race`
package runs of `internal/subd` and 20 plain runs of `deploy`/`user`/`node`.

An earlier intermediate conclusion in this review — that these tests "pass with the
lock disabled, so they prove nothing" — was **wrong**: my first surgery discarded
only `lockFile`'s *return value* (`if err := lockFile(f); err != nil && false`), so
the lock was still genuinely acquired. That run was meaningless and has been
discarded. Recording it because it is exactly the kind of false negative this
section exists to catch.

The experiment was fully reverted: `internal/filelock/probe_nolock.go` deleted,
`lock_unix.go`'s build tag restored to `!windows`, and `git diff` on
`filelock.go`/`lock_unix.go`/`lock_windows.go` is empty.

### 3.3 CONFIRMED, not fixed — `--provision` writes the whole state file from one snapshot

`internal/provision/provision.go` loads the state once (line ~309), mutates it, and
calls `opt.SaveConfig(cfg)` twice (lines ~364 and ~372). `SaveConfig` is a
whole-document save, so anything another actor wrote to `easysb.conf` between that
load and those saves is discarded — the same class as F2, which rounds 1–7 fixed
everywhere **except** this path.

Impact: low. `--provision` is a one-shot CLI invoked on a host being brought up, and
the window is one process's lifetime; the panel/TUI/subscription paths were the
concurrent ones and are fixed. Recorded rather than changed because converting this
to `state.Modify` is a behaviour change to a deployment entry point and belongs in
its own round with its own tests.

### 3.4 CONFIRMED, fixed — panel documentation contradicted the new default

`docs/panel-installation.md` §3 still read *"浏览器打开 `http://<主机>:2095`"* and
§3's closing paragraph still said *"默认监听所有接口且为明文 HTTP … 用反向代理/防火墙
限制来源"* — the **old** `0.0.0.0` default. With the new loopback default the
instruction cannot work, and the advice contradicts §8 of
`docs/panel-architecture.md`, which says not to expose it. Fixed: §3 now lists the
three supported access paths (SSH port-forward, panel TLS, reverse proxy) and warns
that any reachable listener must be TLS-protected because the panel is
root-equivalent. §4 gained a note that the port-forward case needs no change, and
§5 gained an explicit "an upgrade does not change the configured listen address"
paragraph. `docs/panel-architecture.md` §8 now records the same upgrade fact.
Verified: no remaining `http://<host>:2095` or "监听所有接口" text anywhere in
`docs/`.

### 3.5 NOT a defect — the loopback default does not change an upgrade

Checked against the original code, not assumed:

- `git show HEAD:internal/panel/config.go` confirms the previous version **already
  wrote** `fmt.Sprintf("PANEL_LISTEN=%q", c.Listen)` in `Save` and **already read**
  `values["PANEL_LISTEN"]` in `LoadConfig`.
- `DefaultListen` is therefore consulted only when the key is absent.
- `EnsureConfig` returns immediately unless `PANEL_PASSWORD_HASH` is empty, so an
  existing deployment's file is not rewritten on startup.

Consequence: a fresh install writes `PANEL_LISTEN="127.0.0.1"`; an **existing**
deployment reads its persisted value (typically `0.0.0.0`) and keeps its external
reachability. `EASYSB_PANEL_LISTEN` is applied after the file read and therefore
overrides it. This is the answer to the review's explicit question, and it is now
stated in both affected documents.

### 3.6 NOT a defect — error classification is preserved

- `401` authentication, `400` malformed body, `404` unknown route and "not found",
  `403` cookie-write without the CSRF header, `422` a pair that will not load,
  `429` rate-limited login, `500` internal — all still distinct.
- The core's own rejection reason is passed through verbatim
  (`handlers_nodes.go:345-351`), which is the whole reason `ErrRejected` is wrapped
  rather than replaced.
- `storeError` now returns `500` where it previously returned `400` for a store
  failure that is not the client's fault; that is a correction, not a loss.
- References correlate to the log: `internalError` writes
  `"panel: <ref> <summary>: <err>"` (`handlers_nodes.go:374-378`) with the same
  `ref` returned to the client, and the ref is 8 characters of `secret.Token()`
  (~48 bits), i.e. collision-free for this volume in practice.
- No secret reaches the log: every `s.opts.Log` call in the package was read. They
  carry an IP, an action name, a request line (query string stripped,
  `EscapedPath`), or a sub-error. No password, token, cookie, private key or config
  body. The one plaintext admin password is printed by `cmd/panel.go` to the
  console on first run by design, not logged by the panel.

### 3.7 NOT a defect — no order-dependent or timing-fragile tests

- Whole tree has **no `t.Parallel()`**, so the package-level test seams
  (`checkConfig`, `serviceActive`, `serviceDo`, `serviceWriteUnit`, `configDir`,
  `configPath`) cannot race. Each test that reassigns one restores it via
  `t.Cleanup`.
- 5 shuffled runs (`-shuffle=on -count=1`) of each of `deploy`, `provision`,
  `panel`, `user`, `cert`, `state`, `subd`, `stats`, `node`: **0 failures** — no
  hidden inter-test dependency or leaked seam.
- The timing-based tests were examined individually. `filelock`'s 300 ms
  observation window passes 25/25 and is now structurally safe (§3.1).
  `subd`'s `<-ready` wake-up is a channel, not a sleep, and the regression it
  guards was confirmed to fail by construction (§3.8).
- Tests that start goroutines (`deploy_test.go:622`, `filelock_test.go:111`,
  `node_test.go:323`, `store_test.go:554`, `subd/server_test.go:452,505,562`,
  `toolbox/board_test.go:78`) all synchronise via `sync.WaitGroup` or a buffered
  channel; none outlives its test now that `subd.Run` awaits its accounting loop.

### 3.8 Previously fixed during round 7, re-confirmed here

`subd.Run` no longer returns while the accounting loop it started is still writing:
`stats.Loop` exposes `Done()` and `Run` stops and awaits the loop on **every**
return path, including the serve-error path (which gained its own derived context).
The bind now happens **before** the loop starts, so a failed bind leaves nothing
running. `TestRunClosesReadyWhenItCannotBind` pins the `Ready` contract and was
confirmed to hang for its full timeout when the close is removed.

## 4. Compatibility assessment

### 4.1 Certificate renewal window — closing it needs a different design

The reviewer asked whether a versioned directory with an atomically switched
pointer would reduce the inconsistency window. Assessment:

- **A versioned directory plus a switched pointer is not the same thing** — the
  pointer is itself a file, so switching it is another single-file atomic rename.
  That *does* collapse the pair into one atomic operation and is a genuinely better
  design in principle.
- **But nothing in this project can consume it.** The certificate paths are
  consumed in three independent places that all expect a fixed pair:
  `cert.Paths()` returns `<dir>/<domain>/{fullchain.cer,private.key}` and is called
  by `ResolveActive`, `Usable`, `ExpiryOf`, `dueForRenewal`, `loadResource` and the
  panel's security handler; the rendered `config.json` embeds those **literal
  paths** into the sing-box config, so the core opens them directly and knows
  nothing about a pointer; and the subscription listener plus the documented renewal
  timer unit text both name the same files. Migrating means changing the rendered
  config schema and every reader at once, on a live deployment whose running core
  holds the old paths open.
- **Conclusion: do not migrate in this round.** The change is a config-rendering and
  on-disk-layout migration, which is exactly the category the review brief says must
  not be undertaken without validation. It is recorded here as a design option with
  its blast radius, not as a defect.
- What is already correct: per-file atomicity, pre-write pair verification, and the
  first-issuance failure mode. The remaining risk requires the process to die
  between two renames during a renewal, on a host whose core is serving.

### 4.2 Error response shape

Unchanged for every category a client can act on (see §3.6). The only text change is
that a genuine internal failure now carries a reference instead of raw OS text. The
`POST /security/tls` refusal keeps its `422` and now says
`"the certificate and key could not be loaded as a pair (ref …)"`; the operator
reads the matching log line. This is a deliberate, documented behaviour change.

### 4.3 Release workflow F19 — reproduction recorded, deliberately not changed

Confirmed by reading `.github/workflows/easysb-go-release.yml`:

- **Trigger**: `push` to `master` touching any of `main.go`, `internal/**`,
  `go.mod`, `go.sum`, `VERSION`, `release/TAGS`, `Makefile`, `packaging/**`,
  `scripts/**`, `install.sh`, or the workflow itself; plus `workflow_dispatch`.
  The `release` job is skipped for pull requests.
- **Tag**: `tag_name: v${{ needs.prepare.outputs.version }}`, where `version` is
  `tr -d '[:space:]' < VERSION` (line 108). Nothing consults existing tags.
- **Conclusion**: with `VERSION` still `6.0.0` and `v6.0.0` already existing, any
  qualifying push re-publishes `v6.0.0`. `softprops/action-gh-release` updates an
  existing release for a tag that already exists, and `make_latest: true` keeps it
  the latest. Same-named assets are overwritten; the "prune old assets" step then
  removes anything not in the current `dist/repo`; the "prune old releases" loop
  deletes every tag except the current one.
- **Impact**: `https://github.com/EasySBTeam/EasySB/releases/latest/download` — the
  address `install.sh` and every configured apt source use — can start serving
  **different bytes under an unchanged version string**. Because the package
  revision is fixed at `-1` (`easysb_6.0.0-1_amd64.deb`), `apt` sees the same
  version and will **not** upgrade an installed host, so the practical effects are
  a bytes-vs-version mismatch (bad for provenance/SBOM and support), a release whose
  notes name no change, and a window where a mismatched `Packages`/`.deb` pair is
  live while the job runs.
- **Not changed this round**, per the brief. Note for whoever picks it up: a guard
  is a few lines in the `prepare` or `release` job (fail when the computed tag
  exists and `VERSION` did not move), and the `concurrency`/
  `cancel-in-progress: true` group means a cancellation mid-prune is also worth
  considering.

## 5. Scenarios that could not be verified here

Stated plainly; none of these are claimed to pass.

| Scenario | Why it cannot be verified here |
| :--- | :--- |
| `systemctl` operations end to end | WSL has no systemd as PID 1. `internal/service`, the packaged units and the first-deploy path are exercised only through injected stubs. |
| The `CommandTimeout` actually firing | needs a real hanging `systemctl`; verified by inspection only. |
| `.deb` build (`make deb`), `make pkg-stage`, UPX | `fpm` and `upx` are absent. |
| `make repo`, apt index signing | `apt-ftparchive` and the GPG key are absent. |
| The release workflow | needs a push; analysed by reading, per the brief. |
| ACME issuance against a real CA | no domain, no port 80, and it would spend rate limit. `EASYSB_ACME_STAGING` is the intended path but is still out of scope for a review. |
| Real multi-process lock contention | `internal/filelock`'s cross-process behaviour is tested through separate `os.OpenFile` descriptors in one process; genuine multi-process contention was reasoned about, not executed. |
| Panel TLS end to end in a browser | needs a browser and a listener; the cookie/`Secure` logic is unit-tested instead. |

## 6. Must-fix items

| # | Item | State |
| :--- | :--- | :--- |
| 1 | `TestLockExcludesASecondHolder` could hang on its own failure (§3.1) | **fixed** in this review |
| 2 | Panel installation docs contradicted the new default (§3.4) | **fixed** in this review |

No other must-fix defect was found. In particular there is **no** lock-order
inversion, **no** slow work under the state lock, **no** path by which a credential
reaches the core before it is persisted, and **no** unauthenticated API route.

## 7. Suggested but not required

1. **Convert `provision.Run`'s state write to `state.Modify`** (§3.3). Small,
   closes the last instance of the F2 class; needs its own round because it changes
   a deployment entry point.
2. **Add an explicit lost-update test that does not depend on timing** — e.g.
   assert that an unlocked `Load`+save pair demonstrably loses a concurrent write
   (the experiment in §3.2 shows this fails 1-of-8 every time). It would document
   *why* the lock exists rather than only that it works.
3. **A guard for F19** (§4.3), once release policy is decided.
4. **A versioned certificate directory with a switched pointer** (§4.1) — a real
   design improvement, but a config-schema migration and not for this round.
5. **`state.Config.Save` does not use `WriteKeepingBackup`**, so `easysb.conf` has
   no `.bak`. Deliberately left alone: the state file is human-edited and
   recoverable by re-entering a handful of keys, unlike the credential stores.
   Worth a conscious decision rather than a silent one.

## 8. Verification performed for this review

All on Debian 13 / go1.27.1 / linux-amd64 with `release/TAGS`.

| Check | Result |
| :--- | :--- |
| `gofmt -l .` | clean |
| `go vet -tags <TAGS> ./...` | exit 0 |
| `go vet ./...` (no tags) | exit 0 |
| `go test -tags <TAGS> -count=1 ./...` | **38/38 packages ok, 0 failures** |
| `go test -count=1 ./...` (no tags) | **38/38 packages ok, 0 failures** |
| `go test -tags <TAGS> -race -count=1 ./...` | **38/38 packages ok, 0 failures, no data race** |
| `GOOS=linux GOARCH=amd64` release build | 44,241,056 bytes (baseline 44,216,480; +24,576) |
| `GOOS=linux GOARCH=arm64` release build | 40,960,160 bytes |
| `--version` | `EasySB 6.0.0` |
| `--render --width 100 --height 40` | exit 0, 40 lines |
| Targeted: state/lock/recovery/routes/cert/session | 28 tests, all PASS |
| Shuffled (`-shuffle=on`) ×5 on 9 packages | 0 failures |
| Stability: `internal/subd` 30 plain + 20 `-race` | 0 failures |
| Stability: `deploy`+`user`+`node` 20 plain | 0 failures |
| A/B lock removal (§3.2) | 4 tests fail without the lock; experiment reverted |

The binary grew 24,576 bytes because of the new tests' equivalent code paths plus
the new packages' production code (`atomicfile`, `node.Empty`) and the extra
verification calls; UPX is applied at packaging time, so the published package is
unaffected by this figure either way.

## 9. Merge recommendation

**Fix-then-commit — and the fixes are already applied.**

The two must-fix items (§3.1, §3.4) are done and verified in the working tree. What
remains before committing is a decision, not a repair:

1. **Read the two user-visible behaviour changes and accept them explicitly.**
   - The panel now defaults to `127.0.0.1`, which affects **fresh installs only** —
     existing deployments keep their persisted `PANEL_LISTEN` (§3.5). This is the
     one change that alters what an operator experiences, and it is the reason the
     docs were corrected in this review.
   - Internal errors now return a generic message with a reference instead of raw
     OS text.
2. **Decide F19** (§4.3). It is untouched by design; the reproduction conditions are
   recorded above so the decision can be made on evidence.
3. **Review the diff.** It is 39 files / ~3,150 lines, which is large for one
   sitting. The natural seams are: (a) `internal/atomicfile` + the store writers,
   (b) the lock/ordering changes in `deploy`/`state`/`provision`/`stats`/`subd`,
   (c) `internal/panel` (routes, errors, listen default), (d) `internal/cert`,
   (e) tests and docs.

Nothing in the review found a data-loss path, a deadlock, an unauthenticated route,
a credential reaching the core before it is persisted, or an order-dependent test.
The residual risks are the ones already documented: the renewal window (§4.1,
inherent), `state.Load` never failing (deliberate), `provision`'s whole-file state
save (§3.3, low impact), and F19 (gated).

**The changes remain uncommitted, unstaged, and unpublished.** No commit, push, tag
or package was created, and `git reflog` still shows only the clone.
