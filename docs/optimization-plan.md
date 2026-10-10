# EasySB Optimization Plan

Tasks are ordered by priority. Every task states the problem, the files and
functions involved, the existing behaviour, the risk and blast radius, the chosen
solution and why, the tests to add, and the command that verifies it.

Companion document: [`docs/optimization-audit.md`](optimization-audit.md), which
carries the evidence for every finding referenced here as **F<n>**.

## Rules this plan follows

- One batch per round; no two batches are worked at once, so a regression can
  always be attributed.
- Nothing that deletes a file, changes a data format, changes a build tag, changes
  release policy, or changes user-visible behaviour is applied without explicit
  approval. Those are marked **needs approval**.
- Every change is verified on Linux (the supported platform) with the repository's
  own gate: `gofmt -l .`, `go vet -tags "<TAGS>" ./...`,
  `go test -tags "<TAGS>" -count=1 ./...`, `go test -tags "<TAGS>" -race ./...`.
- A task is not complete until an existing test or a new one fails without the fix
  and passes with it.

---

## P0 — data safety and service stability

### P0-1 · Lock the node read inside `ApplyStore` — **F1**

| | |
| :--- | :--- |
| **Files** | `internal/deploy/deploy.go` (`ApplyStore` :230-253, `LoadNodes` :257-266) |
| **Current behaviour** | `nodes, err := LoadNodes(nodesPath)` at :231 runs with no lock, ~17 lines before `user.Locked` at :235. The rendered document is built from that snapshot at :248. |
| **Impact** | A node created by the panel between :231 and :248 is silently absent from the config the core is restarted with, while `easysb-nodes.json` still marks it enabled. The panel and the subscription document advertise a listener that is not running. |
| **Solution** | Take the node lock for the read. Read the node store under `node.Locked(path)` (or re-read it immediately before `Apply`), so the snapshot the document is built from is the one on disk at deploy time. |
| **Why this way** | It is the same `node.Locked` helper the panel and TUI already use for node writes, so the fix introduces no new mechanism and no new lock ordering — no code path takes the node lock and then the account lock, and `ApplyStore` already takes the account lock second, so the order is preserved. |
| **Tests** | New test in `internal/deploy/deploy_test.go`: mutate the node store from a second goroutine between the two reads and assert the deployed document reflects the newer state. Today there is no `ApplyStore` test at all. |
| **Verify** | `go test -tags "<TAGS>" -count=1 ./internal/deploy/...` and `-race` |
| **Risk** | Low. Adds one lock acquisition; cannot deadlock (no inverse order exists). |

### P0-2 · Save repaired credentials before the core is restarted — **F9**

| | |
| :--- | :--- |
| **Files** | `internal/deploy/deploy.go` (`ApplyStore` :247-252) |
| **Current behaviour** | `Repair` generates credentials in memory (:247) → `Apply` restarts the core with them (:248) → `MarkApplied` (:251) → `Save` (:252). |
| **Impact** | A crash between :248 and :252 leaves the running core authenticating credentials that are not on disk. `user.Load` never invents credentials (`internal/user/store.go:62-66`), and the next `Repair` generates *different* ones — every already-imported client breaks. |
| **Solution** | Persist before deploying: `Repair` → `Save` → `Apply` → `MarkApplied` → `Save`. The second save is needed because `MarkApplied` changes `Applied` on each account. |
| **Why this way** | The panel's own create/update path already saves before applying (`handlers_users.go:146` under the lock), so this makes `ApplyStore` consistent with the path that is already correct rather than inventing a new ordering. |
| **Tests** | New test: make `Apply` fail and assert the generated credentials are already on disk; assert a second `Repair` does not change them. |
| **Verify** | `go test -tags "<TAGS>" -count=1 ./internal/deploy/...` |
| **Risk** | Low, but it makes the write happen on every apply instead of after success. Acceptable: the credentials are deterministic once generated, and the store must record them to be readable at all. |

### P0-3 · Stop overwriting whole files from stale snapshots — **F2** *(approved and done, round 5)*

| | |
| :--- | :--- |
| **Files** | `internal/state/state.go` (`Save` :247-305, `Load` :148-157); writers at `internal/panel/server.go:472-486`, `internal/panel/handlers_domains.go:82-91, 120-124, 191-200, 254-259`, `internal/tui/params.go:38-41, 61-63`, `internal/tui/domain.go:131-135`, `internal/tui/actions.go:61-79`, `internal/provision/provision.go:354, 362` |
| **Current behaviour** | `Config.Save` rewrites the entire document from the receiver, preserving only the unrecognised keys that were in its own `raw` map. `internal/state` does not import `filelock`. |
| **Impact** | `panel/server.go` snapshots at :472 and saves at :486, **across** the multi-second `deploy.ApplyStore` at :474. A concurrent TUI change to `SUB_SYNC_SECONDS` in that window is silently erased; the reverse order erases `DOMAIN`/`CERT_DOMAIN` (every subscription URL loses its host) or resets `NODE_DEPLOYED`, making the next apply take the first-deploy branch. |
| **Solution (proposed)** | Add `state.Locked()` mirroring `user.Locked`/`node.Locked` (`filelock.Acquire` + load-under-lock), and use it at every write site, so a read-modify-write on `easysb.conf` is atomic the same way the two JSON stores already are. |
| **Alternative considered** | Merge-on-save: re-read the file inside `Save` and overlay only this writer's keys. Rejected as the primary approach because it cannot express a deliberate key drop, and `Save` already relies on dropping migrated legacy keys — the lock is the smaller and more faithful change. |
| **Why this needed approval** | It changes the concurrency behaviour of a file the legacy shell tool also understands, and it touches four packages. |
| **Tests** | A concurrency test in the shape of `TestLockedKeepsConcurrentWriters` for the state file; a test that two interleaved read-modify-write cycles both survive. |
| **Verify** | `go test -tags "<TAGS>" -race -count=1 ./internal/state/... ./internal/tui/... ./internal/panel/...` |
| **Risk** | Medium blast radius (four packages), low per-site risk. |

### P0-4 · Lock the provision read-modify-write — **F3**

| | |
| :--- | :--- |
| **Files** | `internal/provision/provision.go` (`Run` :301-364; the writes land at :408 `nodeStore.Add` and :435/:450 `userStore.Update`/`Add`) |
| **Current behaviour** | Both stores are loaded with `node.Load` (:328) and `user.Load` (:337) — no lock — then mutated. |
| **Impact** | **Silent data loss.** An account or node created by the panel between the load and the save is appended-to from a stale snapshot and then renamed over (`internal/user/store.go:269`). The panel has already returned 201. Reachable from the shipped CLI (`cmd/cmd.go:112-116`). |
| **Solution** | Wrap each read-modify-write group in `node.Locked` / `user.Locked`, releasing each before the next stage, and keep `ApplyStore` outside both. |
| **Why this way** | Same helper, same discipline as every other writer — the fix makes `provision` stop being the exception rather than adding a new pattern. |
| **Tests** | Existing `internal/provision/provision_test.go` (5.7 KB) already drives `Run` with injected options; extend it with an interleaved writer, and assert that a change made after the load survives the provision. |
| **Verify** | `go test -tags "<TAGS>" -race -count=1 ./internal/provision/...` |
| **Risk** | Low. `provision` is a first-deploy path, so contention is minimal, but the loss is unrecoverable when it happens. |

### P0-5 · Make the accounting baseline write atomic — **F6**

| | |
| :--- | :--- |
| **Files** | `internal/stats/loop.go` (`saveSample` :345-359, `loadSample` :315-340) |
| **Current behaviour** | `_ = os.WriteFile(path, data, 0o600)` — the only state writer in the program that is neither atomic nor error-checked. `loadSample` silently ignores a parse failure. |
| **Impact** | A torn file means the next cycle only re-establishes a baseline, so every byte since the last successful save is never charged — quota **undercounting**, i.e. free traffic, which `loop.go:241-243` itself calls the worse of the two errors. |
| **Solution** | `os.CreateTemp` in the same directory + `os.Rename`, matching `user.Store.Save`/`state.Config.Save`; report the error through the loop's existing logging instead of discarding it. |
| **Why this way** | Consistency with the five writers that already do it correctly; no new mechanism. |
| **Tests** | Extend `internal/stats/loop_test.go`: assert the sample round-trips and that a failed write is surfaced rather than swallowed. |
| **Verify** | `go test -tags "<TAGS>" -count=1 ./internal/stats/...` |
| **Risk** | Low. |

### P0-6 · Stop three writers from bypassing the safe path — **F7, F8**

| | |
| :--- | :--- |
| **Files** | `internal/cert/cert.go` (`writeFile` :303-311, called from `internal/cert/acme.go` `installPair` :438-453), `internal/deploy/deploy.go` (`writeConfigFile` :108-119, `WriteServerConfig` :93-102) |
| **Current behaviour** | (a) `installPair` writes `private.key` then `fullchain.cer` **in place**, truncating. (b) `writeConfigFile` writes `config.json` with `os.WriteFile` and **no `sbcore.Check`**. |
| **Impact** | (a) A crash mid-**renewal** leaves a new key beside the old certificate; `pair.ok()` only checks existence and non-zero size, so `Paths` still reports a pair and the next core start is refused. `docs/pitfalls.md:223-227` claims the opposite guarantee, which holds only for a first issuance — a doc/behaviour divergence that must be reconciled either way. (b) `writeConfigFile` reintroduces exactly the failure mode `ApplyConfig` was built to remove, on the file that carries every account's UUID and password. |
| **Solution** | (a) Write both files to temp names inside the domain directory and rename key-then-certificate, then confirm with `tls.X509KeyPair` that the pair actually matches. (b) **Preferred: delete `writeConfigFile` and `WriteServerConfig`** (`WriteServerConfig` has no production caller; only `deploy_test.go:182, 200` uses it). Fallback if deletion is not approved: make `writeConfigFile` do temp+`Rename`, and have `WriteServerConfig` route through `sbcore.Check` like `ApplyConfig`. |
| **Why this way** | (a) temp+rename is what every other writer here does, and the `X509KeyPair` confirmation is what makes `pair.ok()` mean what the docs already claim. (b) Deleting dead code that reintroduces a removed hazard is smaller than fixing it, and the audited code has no caller — but the brief forbids unreviewed deletion, so it is presented for approval with the exact paths and the evidence that nothing depends on them. |
| **Tests** | (a) A test that interrupts `installPair` between the two writes and asserts `Paths` no longer reports a pair. (b) If kept, a test that `WriteServerConfig` refuses a document the core rejects. |
| **Verify** | `go test -tags "<TAGS>" -count=1 ./internal/cert/... ./internal/deploy/...` |
| **Risk** | (a) Low. (b) Deletion removes an exported function — an API break only for out-of-tree importers, which is impossible for `internal/`. |

### P0-7 · Give a corrupt store a recovery path — **F5** *(approved and done, round 5)*

| | |
| :--- | :--- |
| **Files** | `internal/user/store.go` (`Load` :35-79), `internal/node/node.go` (`Load` ~:150), `internal/state/state.go` (`Load` :148-157), `internal/sysinfo/sysinfo.go` (`ReadKeyValues` :467-492) |
| **Current behaviour** | A parse failure is a hard error with no `.bak`, no quarantine and no rebuild path. `state.Load` cannot fail at all — malformed lines are skipped and an unreadable file yields `Default()`. |
| **Impact** | One truncated `easysb-users.json` makes the subscription endpoint answer **500 for every client** and stops quota enforcement; a truncated `easysb.conf` silently becomes defaults and makes the next apply take the first-deploy branch. |
| **Solution (proposed)** | Quarantine the bad file as `<path>.corrupt-<timestamp>`, then try `<path>.bak`, which a successful `Save` writes; only fail when both are unusable. Report which route was taken in the error/log. |
| **Why this needed approval** | It introduces a new on-disk artefact (`.bak`) and a recovery policy on load, which is a data-format-adjacent decision the brief reserves for explicit approval. |
| **Tests** | Truncate each store and assert recovery, plus a test that a successful save leaves a `.bak`. |
| **Verify** | `go test -tags "<TAGS>" -count=1 ./internal/user/... ./internal/node/... ./internal/state/...` |
| **Risk** | Medium: load-path behaviour changes for every component. Must not mask a genuinely invalid file. |

### P0-8 · Take the slow work out of the lock — **F4** *(approved and done, rounds 3 and 5)*

| | |
| :--- | :--- |
| **Files** | `internal/deploy/deploy.go` (`ApplyStore` :230-253, `Apply` :131-155, `restartAfterChange` :163-175), `internal/service/service.go` (:185-197), `internal/stats/loop.go` (:147-186), `internal/panel/handlers_util.go` (:86-119) |
| **Current behaviour** | The account lock is held across `cert.ResolveActive`, `sbcore.Check` (the full engine build — `sbcore.go:70-71` calls it the slowest step of a deploy), `systemctl daemon-reload`, and up to three more `systemctl` execs with **no per-call timeout** (`service.go:185-193`), plus a 750 ms sleep on the retry path. The accounting loop's context is the process-lifetime context (`cmd/subd.go:25`), so it never times out. |
| **Impact** | A wedged `systemctl` blocks the accounting loop holding the account lock; a panel write then blocks on the file lock **while holding `writeMu`** (`handlers_util.go:107` then `:109`), so the panel's entire write API is dead until restart. |
| **Solution (proposed)** | Keep only load-mutate-save inside the lock and move render/check/restart outside it; give every `service.Do` a bounded `context.WithTimeout`; do not hold `writeMu` across a file-lock wait. |
| **Why this needed approval** | This was the largest structural change proposed. Moving work out of a lock changes what is atomic, so it must be designed and reviewed as a whole rather than patched, and it interacted with P0-1 and P0-3. It was therefore done last, after both. |
| **Tests** | A test with a stubbed slow `service.Do` asserting a concurrent store write is not blocked for the duration; a test asserting `service.Do` times out. |
| **Verify** | `go test -tags "<TAGS>" -race -count=1 ./internal/deploy/... ./internal/stats/... ./internal/panel/...` |
| **Risk** | High. Do not attempt before P0-1 and P0-3 land, and do not attempt in the same round as either. |

### P0-9 · Security fixes that change no behaviour — **F16, F14, F15, F13**

| | |
| :--- | :--- |
| **Files** | `internal/panel/handlers_auth.go` (:94-105), `internal/panel/auth.go` (:106-111), `internal/panel/server.go` (:245), `internal/cert/cert.go` (`legacyDirs` :128-140), `internal/panel/config.go` (:248-259) |
| **Current behaviour** | (a) A password change revokes no session, so a stolen session survives remediation for the full 12 h TTL. (b) The access log records `r.URL.Path`, the *decoded* path, which may contain `%0A` and forge a log line. (c) `legacyDirs` joins the **raw** domain onto a path, bypassing the `domainDir` guard that the primary lookup uses. (d) The first-run password falls back to the fixed string `"change-me-on-first-login"` if `crypto/rand` fails — failing open. |
| **Solution** | (a) Add `revokeAll()` and call it from `handleChangePassword`. (b) Log `r.URL.EscapedPath()` (strip C0 bytes). (c) Validate the domain through `cleanDomain` before the legacy fallbacks, keeping the read-only behaviour. (d) Propagate the `crypto/rand` error and refuse to start. |
| **Why these are safe now** | Each is a strict improvement with no change to a successful request's outcome: (a) only affects other sessions, (b) only changes log text, (c) only narrows which legacy paths are probed, (d) only changes what happens in a state that currently cannot be reached on Linux. No API, config format or deployment default moves. |
| **Tests** | (a) Change the password and assert the old token is rejected. (b) Request a path containing `%0A` and assert the log holds one line. (c) A `cert.Paths` test with a traversal-shaped domain asserting nothing outside `CertDir` is probed. |
| **Verify** | `go test -tags "<TAGS>" -count=1 ./internal/panel/... ./internal/cert/...` |
| **Risk** | Low. |

### P0-10 · Panel transport defaults — **F10** *(approved and done, round 5)*

| | |
| :--- | :--- |
| **Files** | `internal/panel/config.go` (:45 `DefaultListen`, :78-84 `DefaultConfig`), `internal/panel/run.go` (:45-49), `internal/panel/auth.go` (:175-197), `internal/panel/handlers_auth.go` (:37-38) |
| **Current behaviour** | The panel defaults to `0.0.0.0` and serves **plain HTTP**, and the session cookie's `Secure` flag follows `cfg.TLS` — so on the documented TLS-terminating reverse-proxy deployment the cookie is not `Secure`. No HSTS exists anywhere. |
| **Impact** | The admin password is POSTed as JSON over an unencrypted listener, and the session cookie rides any `http://` request to the host. Every authenticated request is root-equivalent (no `User=` on the unit, PTY shell at `terminal.go:87`). |
| **Solution (proposed)** | Default to `127.0.0.1` (or generate a self-signed pair with `TLS=true` on first run), require an explicit opt-in to bind a public address without TLS, set `Secure: true` unconditionally with a `__Host-` cookie name, and send HSTS on TLS responses. |
| **Why this needed approval** | It changes the shipping default of a deployed service and can make an existing panel unreachable until the operator adapts — a user-visible behaviour change the brief reserves for approval. |
| **Tests** | Assert the defaults; assert the cookie always carries `Secure`, `HttpOnly` and the `__Host-` name. |
| **Verify** | `go test -tags "<TAGS>" -count=1 ./internal/panel/...` |
| **Risk** | Medium for existing deployments; this is a documented recommendation in `docs/panel-architecture.md:134` that was never implemented in the defaults. |

---

## P1 — architecture and maintainability

### P1-1 · Test the untested critical paths

| | |
| :--- | :--- |
| **Files** | new `internal/node/node_test.go`, new `internal/filelock/filelock_test.go`, extend `internal/deploy/deploy_test.go` |
| **Problem** | `internal/node` and `internal/filelock` have **no test file at all**; `deploy.ApplyStore` is untested and `internal/panel/panel_test.go:32` injects a no-op `Apply`, so the panel's real write path is never executed. This is precisely why P0-1, P0-2 and P0-4 existed undetected. |
| **Solution** | Cover `node.Store.Validate` (protocol membership, port 1-65535, name length and control characters, duplicate name and port), the `node.Locked` RMW cycle under concurrency, the `filelock` acquire/contend/release contract on Linux, and `ApplyStore` end to end with injected `checkConfig`. |
| **Verify** | `go test -tags "<TAGS>" -race -count=1 ./internal/node/... ./internal/filelock/... ./internal/deploy/...` |
| **Risk** | None — test-only. Do this **before** P0-1 and P0-4 so the fixes land with their tests. |

### P1-2 · Enumerate route authorization in a test

| | |
| :--- | :--- |
| **Files** | `internal/panel/security_test.go`, `internal/panel/server.go` (:108-183) |
| **Problem** | All 55 route registrations are correctly guarded today (only login, logout and the self-authenticating terminal sit outside `require`), but **nothing asserts it**, so a new route registered without `s.require` passes CI. |
| **Solution** | A table-driven test that walks the router and asserts every `/api/v1` route except the documented exceptions rejects an unauthenticated request. |
| **Verify** | `go test -tags "<TAGS>" -count=1 ./internal/panel/...` |
| **Risk** | None. Note the shared harness sets `AllowAnonymous: true` (`panel_test.go:31`), so the new test must build its own service. |

### P1-3 · Converge the node-deletion cascade — **F11**

| | |
| :--- | :--- |
| **Files** | `internal/panel/handlers_nodes.go` (:204-231), `internal/tui/nodes.go` (:469-512) |
| **Problem** | The panel deletes the node's credential **and** usage entry from every account (`handlers_nodes.go:216-217`); the TUI only calls `u.Deselect(id)` (`nodes.go:497`). Same operation, two different end states. |
| **Solution** | Extract the cascade into one function in `internal/node` or `internal/user` and call it from both entry points. |
| **Why** | The brief's rule is that one business rule has one authoritative implementation; this is the only genuine duplication the audit found, so it is the only place worth extracting. |
| **Tests** | Assert both entry points leave identical state for the same deletion. |
| **Verify** | `go test -tags "<TAGS>" -count=1 ./internal/tui/... ./internal/panel/... ./internal/user/...` |
| **Risk** | Low, but it changes TUI-observable state (usage/credential entries survive in the TUI path today), so the intended end state must be agreed before coding. |

### P1-4 · Validate at the API boundary — **F12** (and the F15 follow-up)

| | |
| :--- | :--- |
| **Files** | `internal/panel/handlers_panel.go` (:80-89), `internal/panel/handlers_domains.go` (:47-50, :249-258), `internal/panel/handlers_nodes.go` (:52-68) |
| **Problem** | Validation exists one layer down and on the write path only. The panel port change is range-checked but never checked for collision, although `node.CheckSubPort` exists and `docs/panel-integration-audit.md:214-215` claims the panel calls it. `domains/activate` accepts any string. Node parameter *values* are only trimmed. |
| **Solution** | Call `node.CheckSubPort` (plus a trial `net.Listen`) before persisting the panel port; reject an activate for a domain not in `cert.Domains()`; shape-check the domain and email before `Preflight`. |
| **Why** | These are boundaries the brief names explicitly, and the collision case has a concrete cost: a panel pointed at a busy port persists a config that cannot bind, making the panel unreachable and recoverable only from a shell. |
| **Tests** | Port collision returns 422 and persists nothing; an unknown domain activation is refused; malformed domain/email refused. |
| **Verify** | `go test -tags "<TAGS>" -count=1 ./internal/panel/...` |
| **Risk** | Low; a stricter boundary can reject input that previously "worked" by failing later, which is the point. |

---

## P2 — error handling and diagnostics

### P2-1 · Do not return raw internal errors to clients

| | |
| :--- | :--- |
| **Files** | `internal/panel/handlers_nodes.go` (:302, :311), `internal/panel/handlers_panel.go` (:20, :64, :99, :151, :168), `internal/panel/handlers_security.go` (:122) |
| **Problem** | Internal error text is returned verbatim, against the project's own rule at `docs/panel-integration-audit.md:210-211`. `POST /security/tls` thereby becomes an arbitrary-path existence/permission oracle. Bounded (admin-only, and an admin is already root) but against the stated convention. |
| **Solution** | Log the detailed error; return a stable generic message plus a correlation the log carries. |
| **Verify** | `go test -tags "<TAGS>" -count=1 ./internal/panel/...` |
| **Risk** | Low; may make a support case harder to diagnose, hence the correlation id. |

### P2-2 · Reconcile `docs/pitfalls.md` with the certificate-pair guarantee

| | |
| :--- | :--- |
| **Files** | `docs/pitfalls.md` (:223-227), `internal/cert/cert.go`, `internal/cert/acme.go` |
| **Problem** | The doc states that a failure between the two writes leaves no pair rather than a mismatched one. That holds for a first issuance only; on a renewal the old certificate is still present, and `pair.ok()` accepts it. |
| **Solution** | Whichever way P0-6 is resolved, the doc and the code must agree — either the fix restores the documented guarantee, or the doc is corrected. Documentation and implementation must not diverge. |
| **Verify** | The P0-6 test is the evidence. |
| **Risk** | None. |

---

## P3 — release, housekeeping, performance

### P3-1 · Guard against re-releasing an unmoved version — **F19** *(needs approval)*

| | |
| :--- | :--- |
| **Files** | `.github/workflows/easysb-go-release.yml` (:14-29 triggers, :229-235 release, :285-296 prune) |
| **Problem** | Every push to `master` touching a watched path publishes `v<VERSION>`. `VERSION` is `6.0.0` and tag `v6.0.0` exists, with HEAD 29 commits ahead, so every push re-publishes `v6.0.0` with a body naming no changes. The prune step then deletes every other release and tag by design. |
| **Solution (proposed)** | Fail the release job when the computed tag already exists and `VERSION` has not moved, so cutting a release is deliberate. Optionally exclude the `release` job from `cancel-in-progress` so a cancellation cannot interrupt the prune. |
| **Why this needs approval** | It is release-policy change, which the brief reserves. |
| **Verify** | A dry run of the workflow logic; cannot be fully verified without pushing. Will be marked **not executed** unless a run is authorised. |
| **Risk** | Medium — it gates publishing, so a mistake blocks releases. |

### P3-2 · Clean up crash leftovers — **F18**

| | |
| :--- | :--- |
| **Files** | the four JSON stores and `internal/stats/loop.go` |
| **Problem** | A crash mid-save leaves a `*.tmp-<rand>` sibling that nothing removes. They are 0600, so this is a slow disk leak rather than a disclosure. |
| **Solution** | On load, remove sibling temp files older than a threshold; or remove them on the next successful save. |
| **Verify** | A test that a stale temp file is cleaned and a fresh one is not. |
| **Risk** | Low, but "older than a threshold" needs a defensible value to avoid deleting a live writer's temp file. |

### P3-3 · Flush before rename — **F20**

| | |
| :--- | :--- |
| **Files** | `internal/user/store.go` (:239-273), `internal/node/node.go` (:314-343), `internal/state/state.go` (:247-305), `internal/panel/config.go` (:145-185) |
| **Problem** | No state writer calls `f.Sync()`. The rename is atomic against concurrent readers, but with no data flush a hard power loss can leave a zero-length target. |
| **Solution** | `Sync()` before `Close()` in the four stores. |
| **Verify** | `go test -tags "<TAGS>" -count=1` for the four packages; the effect is not directly testable, so the evidence is the code path plus the existing round-trip tests. |
| **Risk** | Low; adds one syscall per save. |

### P3-4 · Performance work — **nothing proposed**

No measurement in this audit shows a bottleneck. The brief forbids optimising
without evidence, and none of the candidates (JSON encode/decode, lock contention,
config re-rendering) has been profiled. If performance work is wanted, the first
step is a benchmark and a profile, reported before any change, not a speculative
cache or object pool. The one latency observation worth recording — the 750 ms
`restartAfterChange` sleep occurring inside the account lock and the panel's
`writeMu` (`internal/deploy/deploy.go:169-174`) — is a symptom of P0-8, not a
separate performance task.

---

## Round plan

| Round | Content | Gate |
| :--- | :--- | :--- |
| 1 | Audit + this plan. No source change. | Baseline recorded: gofmt clean, vet 0, tests 0 failures, race clean on Linux. |
| 2 | **Done.** P0-5, P0-9 (all four), P0-6(a), P0-6(b), P1-1, plus T5. See the status table below. | Full gate on Linux + race, all green. Release binary byte-identical in size. |
| 3 | **Done.** P0-1, P0-2, P0-4, plus the deploy's lock scope. See the Round 3 status below. | Full gate on Linux + race, all green. |
| 4 | **Done.** P1-2, P1-3, P1-4, P2-1. See the Round 4 status below. | Full gate on Linux + race, all green. |
| 5 | **Done.** P0-3, P0-7, P0-8, P0-10, plus F17. See the Round 5 status below. | Full gate on Linux + race, all green. |
| 6 | **Done.** P3-2. See the Round 6 status below. | Full gate on Linux + race, all green. |
| — | **Everything else in this plan is complete except P3-1**, which changes release policy and needs approval. | — |

Anything still requiring approval at the end of a round is reported as
**not executed**, never as done.

## Round 2 status

Delivered, with the Linux gate green (gofmt clean, `go vet` exit 0 with and
without tags, all packages `ok`, `-race` clean):

| Item | State | Evidence |
| :--- | :--- | :--- |
| P0-5 — atomic, error-reporting accounting baseline | **Done** | `internal/stats/loop.go` `saveSample`; `internal/atomicfile` tests |
| P0-6(a) — atomic certificate writes | **Done** | `internal/cert/cert.go` `writeFile`, `internal/cert/acme.go` `installPair`; `TestInstallPairLeavesNoPairWhenInterruptedBeforeTheCertificate`, `TestWriteFileReplacesRatherThanTruncates` |
| P0-6(b) — remove the dead unvalidated config writer | **Done** (approved) | `internal/deploy/deploy.go`; `TestApplyConfigKeepsCredentialsRootOnly`, `TestApplyConfigLeavesNoTemporaryBehind` |
| P0-9 — password change revokes sessions | **Done** | `internal/panel/auth.go` `revokeAll`, `internal/panel/handlers_auth.go` |
| P0-9 — access log cannot be forged | **Done** | `internal/panel/server.go` `withLogging` uses `EscapedPath()` |
| P0-9 — legacy certificate lookup validates the domain | **Done** | `internal/cert/cert.go` `legacyDirs` |
| P0-9 — first-run password fails closed | **Done** | `internal/panel/config.go` `GeneratePassword` returns an error; `EnsureConfig` propagates it |
| P1-1 — tests for `internal/node` | **Done** | new `internal/node/node_test.go` (11 tests) |
| P1-1 — tests for `internal/filelock` | **Done** | new `internal/filelock/filelock_test.go` (6 tests) |
| P1-1 — tests for `deploy.ApplyStore` | **Done** | new tests in `internal/deploy/deploy_test.go`, driven through a new `service.Active`/`Do`/`WriteUnit` injection seam that mirrors the existing `checkConfig` one |
| T5 — the platform-portable panel test | **Done** | `internal/panel/security_test.go` marshals its request bodies |
| F20 — flush before rename | **Done**, folded into `internal/atomicfile` | all five state writers now `Sync()` before the rename |

**Adversarial review of this round.** The diff was reviewed by a second agent
against the exact change set, with instructions to find defects rather than confirm
the work. It found one real defect and several smaller ones, all now addressed:

| Finding | Outcome |
| :--- | :--- |
| **HIGH — the `installPair` comment (and `docs/pitfalls.md`) asserted a guarantee the code does not provide on a renewal.** Key-first only leaves "no certificate" on a *first* issuance; on a renewal the old `fullchain.cer` is still present, so the interruption leaves a mismatched pair that `pairIn` reports as a pair. The accompanying test used a fresh directory, so nothing caught it. | **Fixed as documentation, not as a fabricated guarantee.** `installPair`'s comment, `docs/pitfalls.md`, `docs/optimization-audit.md` and the CHANGELOG now distinguish the two cases, and `TestInstallPairRenewalInterruptionStillReportsAPair` pins the current renewal behaviour so a future `pairIn` change fails a test instead of surprising an operator. Closing it for real is a read-path change and stays open as F7. |
| `TestWriteRejectsAnEmptyPath` passed for the wrong reason: `Write` never rejected an empty path; `filepath.Dir("")` is `"."`, so it wrote a real file into the working directory and failed on the rename. | **Fixed.** `Write` returns `fs.ErrInvalid` for an empty path; the test asserts the sentinel. |
| `atomicfile`'s doc claimed `Sync` prevents a power loss leaving the target "empty or partially populated", but the rename itself is only durable after the parent directory is flushed. | **Fixed.** `Write` flushes the directory after the rename (best-effort) and the doc now separates the two losses. |
| `TestLockExcludesASecondHolder` discarded the second `Acquire`'s error, so a close-for-any-reason read as success. | **Fixed.** The result is carried out as a value and the error asserted. |
| `TestWriteFileReplacesRatherThanTruncates` cannot distinguish the fixed code from the truncating code it replaced, and the same blind spot exists in `atomicfile_test.go`. | **Acknowledged in the code.** The test's comment now states what it does and does not show; the atomicity property is guaranteed by construction and is not observable from a post-call read. |
| `internal/prefs/prefs.go` and `internal/toolbox/board.go` kept their own copies of the same non-flushed pattern, while the new package called itself the single implementation. | **Fixed.** Both migrated to `internal/atomicfile`; the audit's "five JSON stores" claim is corrected. |
| The only caller of `revokeAll` had no test. | **Fixed.** `TestChangePasswordRevokesEverySession` and `TestChangePasswordRejectsAWrongCurrentPassword` added. |
| The `panel.Config.Save` comment inverted the old behaviour (the temp file was already 0600; no window ever leaked). | **Fixed.** The comment now states the actual change. |

The reviewer found no defect in `atomicfile.Write`'s mechanics (temp always beside
the target, mode and umask correct, no temp leak on any error path, idempotent
double close), in behaviour preservation of the replaced writers, in the deletion of
`WriteServerConfig`/`writeConfigFile` (zero remaining references), in the `deploy`
injection (no `t.Parallel()` in the repository, so no concurrent reassignment), or
in the `EscapedPath()` switch.

**Unplanned finding from this round.** While adding the `ApplyStore` test, the
first attempt failed with *"an account that was applied is not marked as applied"*.
The cause is real and was not in the audit: `ApplyStore` (`internal/deploy/deploy.go`)
only reaches `store.MarkApplied` and `store.Save()` **after** `Apply` returns nil,
so when the service step fails the store is left unmarked while `config.json` has
already been replaced. That is **F4's blast radius reached through the error path**,
not a test artifact, and it is now covered by the test that asserts the rejection
reason. It belongs to P0-8 and is recorded here rather than fixed, because moving
it changes what the store means after a partial deploy.

**Rejected during this round.** A stricter `cert.pairIn` that verified the
certificate/key match on **read** was implemented, tested, and then reverted: it
made `ResolveActive` treat a mismatched pair as "no pair" and quietly serve a
self-signed placeholder instead of the certificate the operator installed, and it
made `dueForRenewal` read an unparsable pair as "no certificate" and silently
re-order one. `TestDueForRenewal` caught the second case
(*"a pair that cannot be parsed should be reported"*). A mismatch check on the
**write** path was also tried and rejected: a renewal legitimately writes a new
certificate over the old key before it writes the new key, so refusing that breaks
every renewal. What remains is per-file atomicity, which is the strongest guarantee
two separate paths can offer; `pairIn` keeps its documented existence-and-size test
and now carries a comment recording why the stricter version was refused.

**Not executed this round.** Anything requiring root, a live `systemd`, `apt` or a
real network: `internal/service` end-to-end, the packaged systemd units, the
`.deb` build (`fpm`/`upx` absent here), and `make repo`'s `apt-ftparchive` signing.
The Windows test run still fails two `internal/update` tests for the documented
environmental reason (`apt-get`/`dpkg` are not on Windows); every other package
passes on Windows now.

## Round 3 status — the concurrency data-loss fixes

| Item | State | Evidence |
| :--- | :--- | :--- |
| P0-1 — `ApplyStore` reads the node store under its lock | **Done** | `internal/deploy/deploy.go` `withNodeLock`. This also makes `docs/architecture.md`'s claim true again: it says both stores are written under their own lock before the core is touched, and the node read was the one part that was not. |
| P0-2 — repaired credentials are persisted before the core is touched | **Done** | `ApplyStore` saves under the account lock and takes the routable set from what it saved. `TestApplyStoreSavesRepairedCredentialsBeforeItTouchesTheCore` — **verified load-bearing**. |
| P0-4 — `provision.Run` guards both read-modify-write cycles | **Done** | `internal/provision/provision.go` `withNodeLock` / `withUserLock`, taken in sequence and never nested. Verified by inspection. |
| — the deploy runs outside both store locks | **Done** | Part of F4's damage (P0-8 remains open for the `systemctl` timeouts). `TestApplyStoreDoesNotHoldAStoreLockAcrossTheDeploy` — **verified load-bearing**. |
| — `internal/node` gains `node.Empty()` | **Done** | Needed so "no node store" can be represented without locking an empty path; a zero `node.Store` cannot be built outside the package. |

### What the tests prove, and what they do not

The credential-ordering and lock-scope tests were each confirmed to fail against a
**temporary, uncommitted revert** of the fix they cover, and to fail with the
intended message:

| Test | Message against the reverted code |
| :--- | :--- |
| `TestApplyStoreSavesRepairedCredentialsBeforeItTouchesTheCore` | *"the core was restarted with credentials the store had not recorded: a crash here leaves the core authenticating a uuid that is nowhere on disk"* |
| `TestApplyStoreDoesNotHoldAStoreLockAcrossTheDeploy` | *"the deploy is holding the store lock"* |

The two `provision` guards are **regression guards, not proofs of the lock**. Both
pass against a revert of the P0-4 fix, because a single-process test cannot schedule
a write into the window between another function's read and its save: making the
writer wait for the lock deadlocks the very save it is waiting for, and a
timing-based attempt passes or fails on goroutine scheduling. A third test of that
shape was written and then **deleted** rather than kept, because it could pass
without the fix and would therefore have implied coverage that did not exist. P0-4
is recorded as verified by inspection, and both guards say so in their own comments.
The same reasoning applies to P0-1: its fix is verified by inspection, and the
node-visibility test is named `TestApplyStoreDeploysEveryNodeOnDisk` rather than for
the lock discipline it cannot distinguish.

## Round 4 status — authorization, convergence and disclosure

| Item | State | Evidence |
| :--- | :--- | :--- |
| P1-2 — enumerate route authorization | **Done** | `internal/panel/server.go`: `route` / `routes()` / `handlerFor()` hold the whole API surface in one table, and `Handler()` registers from it. `TestEveryRouteRequiresASession` sweeps every non-public entry; `TestEveryRouteHasAHandler` catches a table entry with no handler |
| P1-3 — one deletion cascade | **Done** | `internal/user` `Store.ForgetNode`, called by `handleDeleteNode` and by the TUI's node delete. `TestForgetNodeRemovesEveryTraceOfADeletedNode`, `…IsSafeWhenNothingMatches`, `…ReachesEveryAccount` |
| P1-4 — validate the panel's own port | **Done** | `internal/panel/handlers_panel.go` `checkPanelPort`, run before anything is persisted. `TestPanelPortCollisionIsRefused` |
| P2-1 — do not disclose internal errors | **Done** | `applyError` / `storeError` are the two central mappers; `internalError` logs the detail under a reference. `TestInternalErrorsAreNotDisclosed` |

### The route sweep is decisively load-bearing

`TestEveryRouteRequiresASession` was checked against a temporary, uncommitted change
that registered every route **without** `require`. It failed on ~40 routes with
messages such as:

```
GET /api/v1/system answered 200 without a session, want 401 or 405
POST /api/v1/core/apply answered 200 without a session, want 401 or 405
GET /api/v1/logs answered 200 without a session, want 401 or 405
```

Two things about the design are deliberate. The table lists the **terminal** as
public, because it authenticates inside its handler and answers a failed handshake
with its own status; the sweep therefore skips it, and that skip is the mechanism
that would let someone make another route public. Second, the sweep is scoped to
`routes()`: a handler registered directly in `Handler()` outside the table would not
be swept. That is why the table is the only registration path — the remaining direct
registrations are the two catch-alls, `/api/` and `/`, which `handleUnknownAPI` and
`handleStatic` serve and which the sweep is not meant to cover.

### The disclosure rule is two-sided on purpose
P2-1 is not "hide every error". A configuration the core refused keeps the core's own
message end to end, because that text names the field the operator has to fix and is
the whole reason `ErrRejected` is wrapped rather than replaced. A store validation
message and a "not found" keep their wording and their 400/404, because they are
answers to the request. What changed is the third case: a failure that belongs to the
host — an unreadable store, a corrupt file, systemd — is now a generic 500 carrying a
short reference, with the detail in the log. The concrete leak the audit named is
closed with it: `POST /security/tls` used to return the `*tls` error verbatim
(`open /etc/shadow: permission denied`), which made the endpoint a path existence and
permission oracle.

## Round 5 status — state integrity, timeouts and transport defaults

| Item | State | Evidence |
| :--- | :--- | :--- |
| P0-3 — `easysb.conf` is written under a lock | **Done** | `internal/state` `Locked` / `Modify` / `UpdateNodeDeployed`; every writer in the panel and the TUI now expresses its change as a function of the current file. `TestModifyDoesNotLoseAConcurrentChange`, `TestUpdateNodeDeployedTouchesNothingElse`, `TestLockedExcludesAConcurrentHolder` |
| P0-7 — a corrupt store is recoverable | **Done** | `atomicfile.WriteKeepingBackup` + quarantine/recover in `internal/user` and `internal/node`. Recovery tests in both packages |
| P0-8 — external commands are bounded | **Done** | `internal/service` `commandTimeout` on `Do`, 30s on `Active`; the accounting loop no longer applies under the account lock |
| P0-10 — panel transport defaults | **Done** | `DefaultListen` is `127.0.0.1`, `ListenEnv` is the documented override with a startup warning, and the cookie's `Secure` follows the request's scheme. `TestPanelListensOnLoopbackByDefault`, `TestListenEnvOverridesTheBinding`, `TestIsLoopbackClassification`, `TestRequestIsSecureFollowsTheScheme` |
| F17 — `MigrateV2` was unguarded | **Done** | The read-rewrite runs under `withLock`; the version is checked before the lock and again under it |

### A pre-existing bug that P0-3 exposed

Replacing the TUI's whole-document save with targeted writes revealed that
`internal/tui/domain.go` assigned `ACMEEmail` and `ServerIP` to a loaded copy and
**never saved it**. It went unnoticed because the domain write at the end of the same
function happened to save a copy that still carried them; the moment that write
became targeted, the values were silently dropped. They are now persisted with their
own `state.Modify`. This is the second time in this work that narrowing a write has
surfaced a latent defect (the first was `ApplyStore`'s `Applied` flag), which is a
reasonable argument for the approach rather than against it.

### What is deliberately unchanged

`state.Load` still cannot fail: `sysinfo.ReadKeyValues` skips a malformed line and
returns an empty map for an unreadable file, so a damaged `easysb.conf` becomes
defaults rather than an error. That is recorded as a residual risk rather than fixed
here, because the state file is the one document an operator edits by hand and the
legacy shell tool also writes - making `Load` start failing would turn a
slightly-wrong state into a panel that will not start, which is a worse outcome than
the one it replaces. The corrupt-store recovery in P0-7 targets the two JSON stores,
where the loss is credentials that cannot be regenerated and the format is not
hand-edited.

## Round 6 status — crash leftovers

| Item | State | Evidence |
| :--- | :--- | :--- |
| P3-2 — clear the temp files a killed process leaves | **Done** | `internal/atomicfile` `removeStaleTemps`, called after a successful write. `TestWriteRemovesStaleTempsOfItsOwnTarget` |
| T-ready — `subd.Options.Ready` was left open when the listener could not bind | **Done** | Found while investigating a flake; `TestRunClosesReadyWhenItCannotBind`, **verified load-bearing** |
| T-loop — `subd.Run` returned without stopping the accounting loop it started | **Done** | The actual cause of the flake (the first explanation was wrong — see below). `stats.Loop.Done()`, and `Run` awaits the loop on every return path. 0 failures in 50 package runs after, vs 1-in-10 before |

The design points worth recording, because both are about not making things worse:

- **The age threshold is part of the contract, not a tuning knob.** Writers serialize
  on the store's file lock, and the lock is released between a process's writes, so a
  second process can legitimately have a *fresh* temporary file in the directory while
  this one runs. Deleting by name alone would destroy a concurrent writer's in-flight
  document. An hour is far longer than the write, so the threshold is a safety margin
  rather than a guess.
- **Scope is the target's own prefix, not the directory.** A writer removes
  `base+".tmp-*"` siblings of the file it just wrote and nothing else, so it cannot
  touch another target's temporary file. Deletion failures are ignored: this is
  housekeeping after a successful write, and failing the write because an old file
  could not be removed would be worse than leaving it.

## Remaining work

One item is left in the whole plan: **P3-1**, which changes release policy (fail the
release job when the computed tag already exists and `VERSION` has not moved) and
therefore needs approval. Nothing else in P0–P3 is open.

### A flake investigated to root cause — and a correction

The round-6 gate failed once on `internal/subd` `TestRunAnnouncesTheVersion`, then
passed on re-run and on isolated runs. It was traced to a real defect, but the **first
explanation recorded here was wrong** and is corrected below; that correction is the
point of keeping this section.

**What was first written.** The test picks a port with `freePort()` — listen on
`127.0.0.1:0`, read the port, close the listener — and `Run` then binds
`0.0.0.0:<port>`. That is a time-of-check-to-time-of-use race, and it was recorded as
the cause, with the consequence described as "bounded: a lost race produces a clear
assertion failure instead of a hang".

**What the failure text actually said.** Chasing it further produced the real message:

```
TestRunAnnouncesTheVersion
    testing.go:1617: TempDir RemoveAll cleanup: unlinkat /tmp/TestRunAnnouncesTheVersion…: directory not empty
```

That is not a failed bind. It is the test's temporary directory being deleted while
something still had files open in it. The mechanism is a goroutine lifetime bug:

- `Run` started the accounting loop with `go loop.Run(ctx)` and then returned through
  `server.Shutdown` **without waiting for it**. `Loop.Run` stops asynchronously and
  writes the account store and its baseline during a cycle.
- So `Run` returning did not mean "everything Run started has stopped". `TestRun…`
  cancelled, waited for `Run`, and its `t.TempDir()` cleanup then raced the loop's
  last write.

**The fixes**, each a real defect rather than test hygiene:

1. `internal/stats`: `Loop.Done()` is closed when `Run` returns, so a caller that
   started it in a goroutine can wait for it.
2. `internal/subd`: `Run` stops and awaits the loop on **every** return path. The loop
   also gets its own derived context, so the serve-error path stops it too.

**Measured effect.** Before: 1 failure in 10–15 race runs of the package. After:
**0 failures in 30 plain and 20 race runs** of `internal/subd`, and 0 in 15 race runs
of `internal/stats`.

**The TOCTOU race in `freePort()` is real but was not the cause**, and is deliberately
left alone: closing it means having `Run` accept a listener instead of an address,
which changes the function's contract for a test-only convenience. With the `Ready`
fix above (round 6), a lost race now produces a clear assertion failure rather than a
ten-second timeout, so its consequence is bounded.

