# EasySB Optimization Audit

Baseline audit of the EasySB Go + sing-box project. Every statement below was
taken from the actual code, tests and build configuration in this checkout, or
from a command whose output is recorded in §2. Nothing here is inferred from the
project description.

- **Audited revision**: `37dae5f` (`master`), 29 commits past tag `v6.0.0`
- **VERSION**: `6.0.0`
- **Date of audit**: first optimization round
- **Scope**: audit only. No file in the repository was modified while producing
  this document. The `Status` column below was added afterwards, in round 2, to
  record which findings have since been fixed; see
  [`docs/optimization-plan.md`](optimization-plan.md) for the evidence.

---

## 1. How this audit was produced

The workspace `D:\EasySB` was **empty** at the start of the round: there was no
checkout, no `.git`, and no source of any kind. The repository was therefore
cloned from `https://github.com/EasySBTeam/EasySB` (public, default branch
`master`). The resulting tree is a pristine clone with **no local modifications**,
which is what makes every measurement below an unmodified baseline.

Because the development host is Windows and the project targets Linux (systemd,
`/etc/sing-box`, `apt`, `flock`, `sysctl`), a Linux baseline was also established:
a Go 1.27.1 toolchain was installed into `$HOME` inside the existing WSL Debian 13
instance (a supported target distribution), and the full suite was run there.
Where the two disagree, the Linux result is the authoritative one, because that is
what CI (`ubuntu-26.04`) and the release process run.

Audit methods: reading the entry point and every package listed in
`docs/architecture.md`; `gofmt`/`go vet`/`go test`/`go test -race`; and targeted
searches for the failure modes the review brief names (non-atomic writes, ignored
errors, unlocked read-modify-write, shell construction, unvalidated input).

---

## 2. Recorded baseline

### 2.1 Git state

| Item | Value |
| :--- | :--- |
| `git status --porcelain` | *(empty — clean tree)* |
| `git branch --show-current` | `master` |
| `git log -5 --oneline` | `37dae5f Merge pull request #62 …`, `72ffd03 chore(panel): drop the unused internal/panel/VERSION`, `484680b refactor: move the CLI into cmd/ and the panel bundle to public/dist`, `3942a2d ci(panel): serve a placeholder index.html before a bundle is fetched`, `801c38c ci(panel): skip the panel fetch on pull requests` |
| Tags | exactly one: `v6.0.0` |
| `git describe --tags` | `v6.0.0-29-g37dae5f` |
| `VERSION` | `6.0.0` |

### 2.2 Toolchain and size

| Item | Value |
| :--- | :--- |
| `go.mod` | `go 1.27.1`, module `github.com/EasySBTeam/EasySB` |
| Go used | `go1.27.1 linux/amd64` and `go1.27.0 windows/amd64` (both fine) |
| sing-box carried | `github.com/sagernet/sing-box v1.14.2` (with `github.com/sagernet/sing v0.9.6`) |
| Build tags (`release/TAGS`) | `with_clash_api,with_quic,with_utls,with_v2ray_api,with_wireguard` |
| Module graph | `go.mod` 204 lines, 195 direct requires, `go.sum` 589 lines |
| Source size | 211 `.go` files (151 source + 60 `_test.go`), ~47,383 lines total |
| Release binary (`GOOS=linux GOARCH=amd64 CGO_ENABLED=0 -trimpath -s -w -checklinkname=0`) | **44,216,480 bytes (43 MiB)** |
| Release build wall time | ~24 s (`real 0m24.015s`) on WSL Debian 13 |
| `./easysb --version` | `EasySB 6.0.0` |

No UPX figure is recorded on purpose: `pkg-stage` UPX-compresses the *staged*
copy, and the brief explicitly says not to treat a UPX delta as a source
improvement. 43 MiB is the honest pre-compression number.

### 2.3 Gate results

Linux (WSL Debian 13, `go1.27.1`, tags from `release/TAGS`) — **authoritative**:

| Command | Result |
| :--- | :--- |
| `gofmt -l .` | **no output — tree is gofmt clean** |
| `go vet -tags "<TAGS>" ./...` | **exit 0** |
| `go test -tags "<TAGS>" -count=1 ./...` | **exit 0 — all 38 packages `ok`** (3 have no test files: `easysb`, `cmd`, `filelock`, `netutil`, `node`, `public`) |
| `go test -tags "<TAGS>" -race -count=1 ./...` | **exit 0 — race detector clean, no data race reported** |

Windows (`go1.27.0`, same tags) — recorded for completeness: `go build ./...`
exit 0, `go vet ./...` exit 0, `go test ./...` **exit 1** with three failures, all
of which are Windows artifacts and **none of which is a repository defect**:

| Failing test | Cause | Verdict |
| :--- | :--- | :--- |
| `internal/update` `TestRunAptStreamsOutput` | `exec: "apt-get": executable file not found in %PATH%` | environment — apt does not exist on Windows |
| `internal/update` `TestInstalledVersion` | `dpkg-query` absent, so the version reads empty | environment |
| `internal/panel` `TestSecurityReportsFirewallAndValidatesTLS` | the test interpolates a raw Windows path (`D:\Users\…`) into a JSON string literal at `internal/panel/security_test.go:64-65`; `\U` is an invalid JSON escape, so the request is rejected as malformed (400) before validation is reached, while the test expects 422 | **test-portability, not an application bug.** The same test passes on Linux. It is a latent trap: it makes the Windows test run permanently red for a reason unrelated to the code under test. Tracked as **T5** in the plan. |

**Known-issue baseline: zero failing tests, zero vet findings, zero races on the
supported platform.** Any failure introduced later is therefore attributable.

---

## 3. Architecture map

The map below is the dependency direction actually present in the imports, not the
intended one. `→` means "imports".

```
                     main.go  (owns VERSION embed + commit stamp)
                        │
                      cmd/            flags, subcommands, store migration
        ┌───────┬───────┼────────┬──────────┬─────────┐
      (TUI)  (panel)  (serve)  (core run) (--tool) (--provision)
        │       │        │         │          │         │
        ▼       ▼        ▼         ▼          ▼         ▼
   internal/tui  internal/panel  internal/subd   internal/sbcore   internal/toolbox/*   internal/provision
        │            │              │                 │                  │                    │
        └────────────┴──────┬───────┴─────────────────┘                  │                    │
                            ▼                                            │                    │
                   ┌─────────────────┐                                   │                    │
                   │  internal/deploy │◄─── the single write path ──────┘                    │
                   └────────┬────────┘                                                        │
             ┌──────────────┼───────────────┬──────────────┬──────────────┐                   │
             ▼              ▼               ▼              ▼              ▼                   │
      internal/config  internal/node  internal/user  internal/cert  internal/service          │
             │              │               │              │              │                   │
             └──────────────┴───────┬───────┴──────────────┘              │                   │
                                    ▼                                     │                   │
                          internal/state · internal/filelock             │                   │
                          internal/sysinfo · internal/secret             │                   │
                                                                         │                   │
      leaves/adapters: internal/i18n · icons · theme · ui · prefs · firewall · bbr ·         │
                       download · update · uninstall · unlock · netutil · stats ─────────────┘
```

### 3.1 Layers that are genuinely healthy

- **One write path.** TUI, panel and `--provision` all reach the core through
  `internal/deploy` (`Apply` / `ApplyConfig` / `ApplyStore`). There is no second
  renderer and no second config writer, which is exactly the property the brief
  asks for. `internal/config.Build` is the only place a server document is
  produced, and `internal/deploy` is the only place it is installed.
- **One source of truth per fact.** `VERSION` is embedded (`main.go:18-19`) with
  no `main.version` fallback; the build tags exist once in `release/TAGS` and the
  Makefile reads that file; the release arch matrix is generated from the
  Makefile's `ARCHES` (`make release-matrix`), so no arch list is retyped in CI.
- **Business rules live below the entry points.** The panel's HTTP handlers call
  `internal/node`, `internal/user`, `internal/cert`, `internal/firewall` directly
  rather than reimplementing them; `docs/architecture.md:109` claims the panel "is
  not a second implementation of the business logic", and the audit confirms it.
- **Test coverage is real and broad.** 60 test files; `go test` green; a genuine
  concurrency test exists (`internal/user/store_test.go` `TestLockedKeepsConcurrentWriters`,
  8 goroutines), an acceptance test proves the carried core accepts the rendered
  document (`internal/deploy/deploy_test.go:83-113`), and CI runs the race
  detector separately (`.github/workflows/easysb-go-release.yml:99-100`).
- **Documentation is unusually good.** `docs/architecture.md`, `pitfalls.md`,
  `conventions.md` each record *why* a decision was made and what was already
  tried, which is what made this audit evidence-based rather than speculative.

### 3.2 Answers to the questions the brief asks

| Question | Answer, with evidence |
| :--- | :--- |
| Which business logic is duplicated? | **Almost none.** The one real divergence found: deleting a node cascades differently in the two entry points — `internal/panel/handlers_nodes.go:216-217` deletes the node's credential **and** usage entry from every account, while `internal/tui/nodes.go:497` only calls `u.Deselect(id)`. Same operation, two different end states. **A2** in the plan. |
| Unnecessary cycles or strong coupling? | No import cycle exists (Go would refuse to build). Coupling that is real but intentional: everything funnels through `internal/deploy`, which imports 8 packages. |
| Which operations modify production config directly? | `internal/deploy.ApplyConfig` (`deploy.go:190-224`) is the only writer of `/etc/sing-box/config.json`. It is correctly ordered: render → write temp → `sbcore.Check` → `os.Rename`. **The problem is not this path — it is that three *other* writers bypass it** (§5.1, F2/F3/F4). |
| Which operations can be triggered concurrently? | The TUI, the panel (one process per HTTP request goroutine) and `easysb --serve` + its accounting loop are **separate processes** all mutating the same files. `internal/filelock` is the cross-process guard. It is applied inconsistently: account store ✔, node store ✔ (except provision), `easysb.conf` **not at all**, `easysb-stats.json` not at all. |
| Which errors are ignored, overwritten, or only printed? | The surface is **small** (4 sites found by exhaustive search, not 40): `internal/stats/loop.go:358` (`_ = os.WriteFile`, a state write — real), `internal/panel/server.go:402` (`_ = json.NewEncoder(w).Encode`, response write — acceptable), `internal/panel/terminal.go:104-105` (best-effort kill/wait — acceptable), and `internal/deploy/deploy.go:150/153/168` (`_ = service.Do(...)` best-effort systemd — documented intent). So the project does **not** have a systemic ignored-error problem; it has one specific one. |
| Which critical logic lacks tests? | `internal/node` (**no test file at all**), `internal/filelock` (none), `internal/deploy.ApplyStore` (untested), `internal/config` protocol rendering for the tagged protocols only runs under `deploy_release_test.go`. The panel's real write path is never executed by a test because `panel_test.go:32` injects a no-op `Apply`. See §6. |
| Which functions require root? | Everything that writes `/etc/sing-box/*`, writes systemd units (`internal/service`, `internal/subd/unit.go`, `internal/panel/service.go`, `internal/cert/unit.go`, `internal/firewall`), calls `systemctl`, runs `dpkg`/`apt` (`internal/update`, `internal/bbr`), and the panel itself (its unit has **no `User=`** and it exposes a PTY root shell at `terminal.go:87` — so every authenticated panel request is root-equivalent). |
| Which upgrade operations could destroy user data? | No state-format migration happens on upgrade in the ordinary case (`docs/conventions.md`: `VERSION` is embedded; the JSON stores carry `version` fields and `user.Load` refuses a version it cannot read rather than guessing). The destructive risks are **not** migrations but the writer ordering bugs in §5.1 and the total absence of a backup/recovery path (§5.2, F5). |

---

## 4. Findings index

Severity reflects **actual reachability** on a supported deployment, not worst-case
theory. Each finding names the files and functions, the existing behaviour, the
impact, and how to verify a fix.

### Cross-cutting severity summary

| # | Finding | Severity | Area | Status |
| :--- | :--- | :--- | :--- | :--- |
| F1 | `deploy.ApplyStore` reads the node store **outside** any lock, then deploys from that stale snapshot | **High** | concurrency | **fixed (round 3)** — the read runs under `node.Locked` |
| F2 | `easysb.conf` has **no lock at all**; three actors overwrite the whole document from stale in-memory snapshots | **High** | concurrency | **fixed (round 5)** — `state.Locked` / `state.Modify`, and every writer expresses its change as a function of the current file |
| F3 | `provision.Run` read-modify-writes **both** stores with no lock — silent account/node loss | **High** | concurrency | **fixed (round 3)** — both cycles run under their store's lock |
| F4 | The account lock is held across `systemctl restart` + full core engine build/check, with no timeout on `systemctl` | **High** | stability | **fixed (rounds 3 and 5)** — the deploy runs outside both store locks, the accounting loop no longer applies under the account lock, and `service.Do`/`Active` now carry their own deadline |
| F5 | No backup and no recovery path for a corrupt/truncated store; `state.Load` cannot even fail | **High** | data safety | **fixed (round 5)** — the two credential-bearing stores quarantine an unusable file and restore the previous content from a sibling backup. `state.Load` still cannot fail; that is unchanged and noted below. |
| F6 | `stats.saveSample` writes the accounting baseline with a non-atomic `os.WriteFile` and discards the error | Medium | data safety | **fixed (round 2)** |
| F7 | `cert.installPair` writes key then certificate in place; a crash mid-renewal leaves a mismatched pair that still reports as "a pair" | Medium | data safety | **closed as far as it can be (rounds 2 and 3)** — writes are atomic, the CA's pair is verified to match *before* anything is written, and the first-issuance interruption leaves a state `pairIn` reports as no pair. The renewal window itself is inherent to a two-file replacement; see the F7 note below for why the read-path fix was rejected. |
| F8 | `deploy.writeConfigFile` is a non-atomic, **unvalidated** config writer (dead code) | Medium | data safety | **fixed (round 2)** — removed, as approved; the test that pinned the 0600 rule now runs against `ApplyConfig` |
| F9 | Repair-then-deploy-then-save: credentials are used by the running core before they are persisted | Medium | data safety | **fixed (round 3)** — the repaired store is saved before the deploy and the routable set is taken from what was saved |
| F10 | Panel defaults to `0.0.0.0` over plain HTTP, and the session cookie's `Secure` flag follows `cfg.TLS` | **High** | security | **fixed (round 5)** — the default is loopback, `EASYSB_PANEL_LISTEN` is the deliberate override with a startup warning, and the cookie's `Secure` now follows the request's scheme |
| F11 | Node deletion cascades differently in the panel and the TUI (divergent business rules) | Medium | maintainability | **fixed (round 4)** — one `user.Store.ForgetNode`, called by both |
| F12 | Panel port change only range-checks; `node.CheckSubPort` is never called from the panel although the docs claim it | Medium | security/stability | **fixed (round 4)** — the panel's own port is checked against the subscription port and every node |
| F13 | Hardcoded fallback admin password if `crypto/rand` fails (fails **open**) | Low | security | **fixed (round 2)** |
| F14 | Log forging via the decoded request path (`r.URL.Path` may contain `%0A`) | Low | security | **fixed (round 2)** |
| F15 | `cert.Paths`/`legacyDirs` join a **raw** domain onto a path for the legacy read fallbacks | Low | security | **fixed (round 2)** |
| F16 | Password change revokes no session | Low | security | **fixed (round 2)** |
| F17 | `MigrateV2` read-modify-writes the account store with no lock | Low | concurrency | **fixed (round 5)** — it runs under `user.Locked` |
| F18 | Crash leaves orphaned `*.tmp-*` siblings that nothing ever cleans | Low | housekeeping | **fixed (round 6)** — a successful write removes its own target's temp files older than the safety margin |
| F19 | Release workflow publishes `v6.0.0` again on any push to `master` while `VERSION` stays `6.0.0` | Medium | release | open (P3-1, needs approval) |
| F20 | No `fsync` anywhere on the state path | Low | data safety | **fixed (round 2)** — folded into `internal/atomicfile` |
| T5 | `internal/panel/security_test.go` interpolates a raw Windows path into JSON, so the file is permanently red on a Windows checkout | Low | test portability | **fixed (round 2)** |
| T-route | A route registered without the `require` wrapper would be reachable by anyone, and nothing asserted otherwise | **High** (if made) | security | **fixed (round 4)** — the API surface is now one table, and a test sweeps every entry |
| T-raw-err | Raw internal error text returned to clients, including filesystem paths | Low | security | **fixed (round 4)** — user-input wording kept, internal detail logged under a reference |

### Note on F7, and on a fix that was rejected

F7's guarantee needs stating precisely, because the original phrasing above
over-claimed and the first attempt at a fix repeated the mistake. Two separate files
cannot be replaced as one, so an interruption between `installPair`'s two writes
always leaves a half pair, and **which** half depends on whether the domain already
had a certificate:

| Case | State left behind | What `Paths` reports |
| :--- | :--- | :--- |
| first issuance | a key, no certificate | **no pair** — `pairIn` needs both files, so `ResolveActive` falls back to the placeholder and the issuance is retried |
| renewal | the new key beside the **old** certificate | **a pair** — it exists and is non-empty, but the two do not match, which is the combination sing-box refuses to start with |

What rounds 2 and 3 actually fixed is narrower than the original finding implied, and
what remains is inherent:

- `writeFile` was `os.WriteFile` + `os.Chmod`, which **truncates in place**, so a
  single file could be left half written. Both halves are now written atomically.
- `installPair` now verifies that the pair the CA returned **matches, before writing
  either file**, so a response whose key does not belong to its certificate is refused
  while the working pair is still serving, instead of being installed and discovered
  later by the core.
- The first-issuance interruption leaves a key with no certificate, which `pairIn`
  reports as no pair.

The window between the two renames on a **renewal** is what remains, and it cannot be
closed: two paths cannot be replaced as one. A stricter **read-path** check (verify
the certificate/key match in `pairIn`) and a **write-path** refusal against the
existing files were both implemented during round 2 and then **reverted**, because
each broke behaviour that is correct: the read-path check made `ResolveActive`
silently serve a self-signed placeholder over an operator's certificate and made
`dueForRenewal` auto-renew an unparsable pair instead of reporting it
(`TestDueForRenewal` caught this: *"a pair that cannot be parsed should be
reported"*), and the write-path refusal rejected the legitimate mid-renewal state
where a new certificate sits beside the old key. The remaining window is therefore
documented as a residual risk — in `installPair`, in `docs/pitfalls.md`, and pinned by
`TestInstallPairRenewalInterruptionStillReportsAPair`, which fails if a future change
to `pairIn` alters the read-path consequences without that decision being made
deliberately.

Deliberately **not** listed as findings, because the audit found them sound:
password hashing (bcrypt `DefaultCost`), token entropy (256-bit `crypto/rand`),
`HttpOnly`+`SameSite=Strict`, server-side authorization on **all** routes (all 55
registrations enumerated; only login/logout and the self-authenticating terminal
sit outside the `require` middleware), CSRF (`X-EasySB-Panel` header + no CORS
anywhere), path traversal in the panel (`fs.ValidPath`), command injection (no
shell string exists in the repository; every external call is argv-based), JSON
body limits (`1 MiB` + `DisallowUnknownFields`), proxy-header spoofing (no
`X-Forwarded-For` handling exists at all, so the client IP cannot be forged),
config-file modes (0600 via `CreateTemp`), and the atomic temp+rename discipline
in the JSON stores.

*(Round-2 correction: this line originally said "five JSON stores", and the audit
below said those five were uniform. Two further writers kept their own copies of the
same pattern — `internal/prefs/prefs.go` and `internal/toolbox/board.go` — and
neither flushed. Both now go through `internal/atomicfile` as well, so the claim is
true as of round 2 rather than then.)*

---

## 5. Detail on the findings that matter

### 5.1 Deployment ordering and the concurrency model (F1, F2, F3, F4, F9)

The intended model is stated in `docs/architecture.md:239-241`: *"The node store and
the account store are each written under their own lock before the core is
touched, so a failed render or a refused config leaves the previous deployment
running."* `ApplyConfig` honours the second half of that sentence well. Three
gaps break the first half.

**(a) F1 — `internal/deploy/deploy.go:230-253`**

```go
230  func ApplyStore(ctx context.Context, cfg state.Config, nodesPath, accountsPath string) error {
231      nodes, err := LoadNodes(nodesPath)          // ← unlocked read
...
235      store, lock, err := user.Locked(accountsPath)
239      defer lock.Unlock()
...
248      if err := Apply(ctx, cfg, nodes, store.Routable(now)); err != nil {
```

The account lock does not protect the node file, and `nodes` is captured ~17 lines
before the critical section begins. **Interleaving**: the TUI enters `ApplyStore`
and reads `nodes = {N1}` at line 231; the panel adds `N2` under `withNodeLock`
(`internal/panel/handlers_util.go:93`) and restarts the core with `{N1,N2}`; the TUI
then takes the account lock, sees `sameAsLive(live) == false`
(`deploy.go:135`), and restarts the core from a document that **lacks N2**.
`easysb-nodes.json` still marks N2 enabled, the panel lists it, and
`internal/subscribe` advertises it — but nothing is listening on its port.

**(b) F2 — `internal/state/state.go:247-305`, no lock imports.**

`Config.Save` rewrites the entire document from the receiver plus only the
unrecognised keys that were in *its own* `raw` map (`state.go:276-278`). Every
writer therefore discards every key another writer changed since it loaded.
`panel/server.go:472-486` is the worst case because it snapshots at line 472 and
saves at line 486 — **across** the multi-second `deploy.ApplyStore` at line 474.
`panel/handlers_domains.go:82-91` then `:120-124` does the same across a 10-60 s
ACME issuance. A TUI change to `SUB_SYNC_SECONDS` in that window is silently
erased; the reverse order erases `DOMAIN`/`CERT_DOMAIN` (every subscription URL
loses its host) or resets `NODE_DEPLOYED` to `no`, which makes the next apply take
the first-deploy branch.

**(c) F3 — `internal/provision/provision.go:328` and `:337`.**

```go
328      nodeStore, err := node.Load(opt.NodesPath)        // no lock
332      nodes, err := applyNodes(spec, nodeStore, log)    // → store.Add(n)  (provision.go:408)
337      userStore, err := user.Load(opt.AccountsPath)     // no lock
341      applyAccounts(spec, userStore, nodes, now, log)   // → store.Update / store.Add (:435, :450)
```

Reachable from the shipped CLI: `cmd/cmd.go:112-116` runs `runProvision` when
`--provision` is passed, and `provision.Default()` uses the real
`sysinfo.NodesFile`/`UsersFile`. An account created by the panel between line 337
and line 450 is appended-to from a stale snapshot and then `os.Rename`d over
(`internal/user/store.go:269`) — the account is **gone**, and the panel already
returned 201. This is the most severe of the three, because it loses data
outright rather than leaving it unapplied.

**(d) F4 — the lock is held across the slowest work in the program.**

Exactly what runs between `user.Locked` (`deploy.go:235`) and `lock.Unlock()`
(`:239`): `store.Repair` (`:247`, generates credentials), then `Apply` (`:248`),
which runs `cert.ResolveActive` (may write two certificate files),
`service.Active` (`systemctl is-active`), `sbcore.Check` — the full sing-box engine
built and closed in-process, which `internal/sbcore/sbcore.go:70-71` itself calls
"the slowest step of a deploy" — and then `service.WriteUnit`
(`systemctl daemon-reload`) plus up to three more `systemctl` execs
(`internal/service/service.go:185-193`, **no per-call timeout**), or
`restartAfterChange`'s `restart` + 750 ms sleep + `restart`.

The same window exists in the accounting loop
(`internal/stats/loop.go:147` … `173` … `186`), whose context is the
**process-lifetime** context from `cmd/subd.go:25`, so a wedged `systemctl` never
times out. Concrete damage: the loop blocks in `systemctl restart`; a panel write
enters `withUserLock`, takes `s.writeMu` (`internal/panel/handlers_util.go:107`)
and then blocks on the file lock at `:109` **while already holding `writeMu`** — so
every panel write queues behind it and the panel's write API is dead until the
panel is restarted.

**(e) F9 — `internal/deploy/deploy.go:247-252`.** `Repair` generates credentials in
memory (`:247`), `Apply` renders and restarts the core with them (`:248`), and only
then does `store.Save()` persist them (`:252`). A SIGKILL/OOM/power loss in that
window leaves the running core authenticating credentials that are not on disk;
`user.Load` deliberately never invents credentials
(`internal/user/store.go:62-66`), and the next `Repair` generates *different*
values, breaking every already-imported client. Narrow (only a hand-edited or
legacy file needs repair) but real.

### 5.2 Data safety and recovery (F5, F6, F7, F8, F18, F20)

The five JSON stores all use the correct temp-in-same-directory + `os.Rename`
pattern with 0600 modes, and two more (`internal/prefs/prefs.go`,
`internal/toolbox/board.go`) use their own copies of it. None of them flushes. Three
writers do not use the pattern at all:

| Writer | Pattern | Consequence |
| :--- | :--- | :--- |
| `internal/stats/loop.go:358` `saveSample` | `os.WriteFile(path, data, 0o600)`, **error discarded** | A torn file is silently ignored by `loadSample` (`:329-331`), so the interval since the last save is never charged — **undercounting, i.e. free traffic**, which `loop.go:241-243` itself calls the worse error. |
| `internal/cert/cert.go:303-311` `writeFile` (used by `installPair`, `acme.go:449` then `:452`) | `os.WriteFile` + `os.Chmod`, truncating in place | Key is written **before** the certificate. On a **renewal**, a crash between the two leaves a new key beside the *old* certificate. `pairIn`/`pair.ok()` (`cert.go:115, 121`) only test existence and non-zero size, so `Paths` still reports a pair and config.json still references it — the next core start is refused. `docs/pitfalls.md:223-227` claims the opposite guarantee, and that claim **only holds for a first issuance** (when no `fullchain.cer` exists yet). This is a genuine doc/behaviour divergence. |
| `internal/deploy/deploy.go:108-119` `writeConfigFile` | `os.WriteFile` + `os.Chmod`, **no validation** | See F8 below. |

**F8 specifically.** `WriteServerConfig` (`deploy.go:93`) → `writeConfigFile`
(`:108`) writes `/etc/sing-box/config.json` directly: no temp file, no
`os.Rename`, and critically **no `sbcore.Check`**. That is the exact sequence
`ApplyConfig` (`:190-224`) exists to avoid, and the file carries every account's
UUID and password. The audited code has **no production caller** — an exhaustive
search finds only `internal/deploy/deploy_test.go:182` and `:200` — and
`architecture.md` describes `ApplyConfig` as the deploy path. It is dead code that
reintroduces the failure mode the design eliminated, so it is a hazard waiting for
a future caller rather than a live bug. It must be fixed or removed, and removal
needs approval because the brief forbids unreviewed deletion.

**F5 — no recovery path.** `user.Load` (`internal/user/store.go:48-50`) and
`node.Load` return a hard error on a parse failure, with no `.bak`, no
`.corrupt-<ts>` quarantine, and no rebuild-from-`config.json` path. One truncated
`easysb-users.json` means the subscription endpoint answers **500 for every
client** (`internal/subd/server.go:221-226`) and the accounting loop errors out
every interval without advancing (`stats/loop.go:122-125`), so quota enforcement
stops. Worse, `state.Load` **cannot fail at all**: `sysinfo.ReadKeyValues`
(`internal/sysinfo/sysinfo.go:467-492`) skips malformed lines and returns an empty
map when the file cannot be opened, so a truncated `easysb.conf` silently becomes
`Default()` — `Domain == ""`, `NodeDeployed == false` — and the next apply takes
the first-deploy branch. Silent divergence is worse than an error here.

**F18/F20.** No state writer calls `f.Sync()`, and nothing ever removes the
`*.tmp-<rand>` siblings a crash leaves behind (they are 0600, so this is
housekeeping plus a slow disk leak, not a disclosure). The absence of `fsync` is a
real but low-frequency risk on power loss; the cheap fix is `Sync()` before
`Close()` in the four JSON stores.

### 5.3 Panel security (F10, F12, F13, F14, F15, F16)

The panel's security *implementation* is good — the framing that matters is that
its systemd unit has no `User=` (`internal/panel/service.go:19-34`) and it exposes
a PTY root shell (`internal/panel/terminal.go:87`), so **every authenticated
request is root-equivalent**, and only CSRF, session theft, brute force or a
second-order actor matter.

- **F10 (High).** `config.go:45 DefaultListen = "0.0.0.0"`, `config.go:78-84`
  leaves `TLS` false, and `run.go:45-49` serves plain HTTP in that case while
  `handlers_auth.go:10-18` accepts the admin password as a JSON body on it. No
  HSTS exists anywhere in the repository. `docs/panel-architecture.md:134` and
  `docs/panel-installation.md:61-64` acknowledge this and tell the operator to fix
  it, but **the shipped default is the insecure one**. Compounding it,
  `handlers_auth.go:37-38` passes `cfg.TLS` as the cookie's `Secure` flag
  (`auth.go:181`), so on the *documented* TLS-terminating reverse-proxy deployment
  (`docs/panel-installation.md:83-84`) the session cookie is not `Secure` at all.
  Both changes alter deployment behaviour, so both need approval.
- **F12 (Medium).** `handlers_panel.go:80-89` validates the port range then
  schedules a restart. `node.CheckSubPort` exists (`internal/node/node.go:256`) and
  the TUI calls it (`internal/tui/nodes.go:284, 356, 458`), but `internal/panel`
  never does — although `docs/panel-integration-audit.md:214-215` claims it does.
  Pointing the panel at a node's or the subscription service's port persists a
  config that cannot bind, making the panel unreachable and recoverable only from a
  shell. This is a documentation/implementation divergence *and* a real footgun.
- **F13 (Low).** `config.go:248-259` falls back to the fixed string
  `"change-me-on-first-login"` if `crypto/rand` fails. Practically unreachable, but
  it fails **open** where it should fail closed.
- **F14 (Low).** `server.go:245` logs `r.URL.Path`, which is the *decoded* path;
  `net/url` does not reject C0 control bytes, so `%0A` in an unauthenticated
  request forges a line in the panel log. Audit-trail integrity only.
- **F15 (Low).** `cert.go:128-140 legacyDirs` joins the **raw** domain
  (`filepath.Join(sysinfo.CertDir, domain)`, and `~/.acme.sh/<domain>_ecc`) for the
  legacy read fallbacks, whereas the primary lookup goes through `domainDir`, which
  rejects separators and `..`. Reads only, it never returns the path to a client,
  and the caller is already root — but the guard should not be bypassable on one
  branch.
- **F16 (Low).** `handlers_auth.go:94-105` changes the password without touching
  any session; `sessionStore` only has per-token `revoke` (`auth.go:106-111`), so a
  stolen session survives the operator's remediation for the full 12 h TTL.

### 5.4 Release pipeline (F19)

The workflow triggers on **every push to `master`** touching a watched path
(`.github/workflows/easysb-go-release.yml:14-29`) and tags the release
`v<VERSION>` (`:233`). `VERSION` is currently `6.0.0` and tag `v6.0.0` already
exists — and HEAD is 29 commits past it. So every push to `master` re-publishes
`v6.0.0`, with a release body that names no changes. The workflow then deletes
every *other* release and tag (`:285-296`), so the Release page is not merely
duplicated, the previous version's apt source is retired by design.

This is a **policy** question rather than a defect: the project's own
`docs/pitfalls.md:16-21` and `docs/conventions.md:37-40` document "only the newest
release is kept" as intentional. What is genuinely missing is a guard: nothing
fails the run when the tag already exists and the version did not move, so a
release can be cut that ships no change. Also worth noting from the same file: the
`release` job runs the `gh release delete` prune under the same `concurrency`
group with `cancel-in-progress: true` (`:45-47`), so a cancellation mid-prune can
leave a partially pruned release. Both are proposals for approval, not fixes to
apply unilaterally.

---

## 6. Existing test coverage

**Covered well.** Config rendering and the carried-core acceptance test
(`internal/deploy/deploy_test.go`, `deploy_release_test.go`); ACME and certificate
handling (`internal/cert/acme_test.go` 25 KB, `cert_test.go` 22 KB); subscription
generation for all three formats (`internal/subscribe/*`); accounting deltas and
quota transitions (`internal/stats/loop_test.go` 13 KB); the TUI at large
(`internal/tui/app_test.go` 56 KB, including a `--render` layout assertion); the
toolbox tools, which are dependency-injected specifically so they are testable
without network or root; the user store, including one genuine concurrency test.

**Not covered — the gaps that matter.**

1. `internal/node` has **no test file at all**, and `internal/filelock` has none.
   The node store's `Validate` (`internal/node/node.go:221-252`) and the lock
   primitive itself are therefore unverified, and the node store is the only
   source of what the host serves.
2. `deploy.ApplyStore` is untested, and `internal/panel/panel_test.go:32` injects a
   **no-op `Apply`**, so the panel's real write path (`server.go:466-510`) is never
   executed by any test. This is why F1, F2 and F4 were able to exist.
3. No test asserts cross-process behaviour. `TestLockedKeepsConcurrentWriters` is
   the only concurrency test and it exercises `user.Locked` alone — nothing pins
   the invariant that `provision` and `MigrateV2` currently violate.
4. The panel's `security_test.go` (94 lines, 2 tests) covers **only input
   validation**, narrowly. Nothing asserts cookie attributes, session expiry,
   logout invalidation, path traversal, or — the highest-value gap — that every
   route is behind `require`. A new route registered without `s.require` would pass
   CI today.
5. No fault-injection test exists for a failed `Apply` leaving the accounting
   baseline unadvanced, although `stats/loop.go:180-192` is written for exactly
   that case.
6. `internal/config` rendering of the tagged protocols (`hysteria2`, `tuic`,
   `vless-reality`) only runs under the build-tagged `deploy_release_test.go`, so
   `make test-plain` (no tags) does not exercise them.

---

## 7. Recommended order of work

The ordering follows the brief's §8.1 and the severity table. Rationale for each
step is in `docs/optimization-plan.md`.

1. **P0-a — stop losing data**: F3, then F1, then F9. These are ordering bugs with
   small, reviewable diffs and clear tests.
2. **P0-b — make the failure modes recoverable**: F5, F6, F7, F8, F4.
3. **P0-c — security items that change no behaviour**: F16, F14, F15, F13.
   The two that *do* change deployment behaviour (F10) are proposed separately for
   approval.
4. **P1 — tests first for the untested critical paths**: node store, filelock,
   `ApplyStore`, route-authorization enumeration.
5. **P2 — convergence and validation**: F11 (divergent cascade), F12, shared
   validation at the API boundary.
6. **P3 — release hardening (F19), housekeeping (F18, F20), and any
   evidence-backed performance work.** No performance work is proposed yet:
   nothing measured in this audit shows a bottleneck, and the brief forbids
   optimising without evidence.

## 8. Deliberately out of scope this round

- **No database migration.** The JSON stores have no transaction, query or
  multi-writer requirement that a file plus an advisory lock does not already
  meet. F1/F2/F3 are locking and ordering bugs; a database would not fix the
  absent lock on `easysb.conf`, it would only move it. Re-proposing SQLite needs a
  separate justification and an approved migration design.
- **No dependency upgrades.** `sing-box v1.14.2` and every other pin are
  untouched; the brief requires one upgrade per change with its own compatibility
  evidence, and this audit found no security or compatibility reason to move one.
- **No build-tag changes.** `release/TAGS` is not modified. Nothing measured here
  shows an unused tag; establishing that safely means building every subset on both
  release architectures, which is its own task.
- **No file deletions.** F8 (`writeConfigFile`) is the one removal candidate and it
  is presented for approval, not performed.
- **No UI/TUI redesign, no package restructuring.** The layering is already the one
  the brief asks for; the defects are inside it, not in its shape.
- **`--provision` is not measured against a live `systemd`.** WSL here has no
  systemd as PID 1, so `internal/service` integration paths cannot be exercised
  end to end on this host. Tests that need root, systemd, `apt` or a real network
  are marked "not executed" in every round report rather than claimed as passing.
