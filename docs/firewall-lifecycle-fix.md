# Firewall Lifecycle Fix and Post-Deployment Audit

Second production round. It fixes the firewall rule-lifecycle defect found after the
deployment round, verifies the boot unit, and reports what was found while auditing the
panel and the package. Nothing was taken down: no reboot, no rule flush, no config edit.

- **Branch/HEAD before this round**: `93713d4`; **after**: `7bcea91`
- **Deployed binary**: `EasySB 6.0.0 (7bcea91)`, md5 `8233a36adf68`
- **Config integrity**: every `/etc/sing-box` file is byte-identical to before the round
  (`easysb.conf ad1d9dff…`, `nodes 213408d5…`, `users 1ec45687…`, `panel f4240602…`,
  `config.json addb7fc3…`)

## 1. Task 1 — the firewall rule lifecycle

### Root causes, both proven on the host

**A. `Remove()` was keyed on the live node list.** `internal/firewall/firewall.go` walked
`nodes` and deleted `hopRange(n) → n.Port` for each Hysteria2 entry. A node deleted
earlier contributes nothing to that walk, so its redirect could never be deleted. The
host had exactly that: `3100:3200 → 8101`, with **no listener on 8101**, **no node on
8101**, and **no s-ui inbound on 8101**. There was also **no firewall state file at all**,
so nothing recorded what had been applied.

**B. The nft delete path could not see a chain named `PREROUTING`.** `nftList()` hardcoded
the lowercase `prerouting`. Proved in an isolated namespace: this program's own
`nftEnsure()` creates `prerouting` (where listing works), while `iptables-nft` creates
`PREROUTING`. nft is case sensitive, so on a host whose chain came from `iptables-nft` the
listing returned 0 bytes, `nftHandles()` returned nothing, and `nftDelete` removed nothing —
the nft path was add-only.

**Live impact was A only**: `Detect()` prefers iptables, it is present, and its delete is
an exact range+target match (`-C 3100:3200 → 8101` = EXISTS; `-C 9999:9998 → 7777` = absent).

### The fix

A ledger at `/etc/sing-box/easysb-firewall.json` (0600, `atomicfile`) records every rule
this program installs. Deletion is driven by that record, not by the nodes, so a rule is
always reachable by whatever asks for its removal.

- `Apply` converges the host on the enabled nodes: ensure and record each wanted rule,
  then delete and forget each recorded rule no longer wanted. Applying twice adds nothing.
- `Remove` (and the new `RemoveAll`) delete every **recorded** rule regardless of the node
  list — what "turn port hopping off" and uninstall both mean.
- `PruneOrphans`, behind `--prune-firewall`, clears a rule left by a version with no
  ledger.
- The chain is resolved by reading the table (`nft list tables`), never by guessing a name.
- `execOutput` and `detectBackend` are seams, so the whole lifecycle is testable.

### Rule identification basis

| Mechanism | Identification | Safety |
| :--- | :--- | :--- |
| iptables check/delete | full spec `-t nat -C PREROUTING -p udp --dport S:E -j REDIRECT --to-ports T` | removes only a rule whose **range and target both match** |
| nft delete | `nft -a list table ip nat`, exact spec match, delete **by handle** | touches exactly the matched handle |
| ledger | every rule this program added | deletion no longer depends on nodes |
| orphan prune | shape is a UDP **range** redirect, target is not an enabled node's port, **and nothing is listening** | a single-port redirect is never considered; a rule with a live target is left alone |

**Documented boundary**: a range redirect to an unserved port is indistinguishable by shape
from an orphan and is treated as one. A redirect whose target is alive, or that is a
single-port rule, is never removed. This is why the prune is a separate `--prune-firewall`
operator action rather than an unattended boot step.

### Three further defects found while making this testable

1. **`iptables -A -t nat …` is rejected** by real iptables ("Bad argument `nat'"). The verb
   must follow the table selection. The old code only ever "worked" because no test had run
   it against a real iptables. Caught by the isolated test.
2. **`--prune-firewall`'s listener check dialled over UDP**, and a UDP dial always succeeds
   because a UDP connection is only a recorded peer. Every port looked served and the prune
   removed nothing. It now reads `/proc/net/{tcp,udp,tcp6,udp6}`. Caught by running the real
   binary.
3. **`PruneOrphans` compared rules without the backend field**, so every wanted rule looked
   like an orphan. Caught by a test.

### Firewall rules: before and after

| | Rule | Verdict |
| :--- | :--- | :--- |
| Before | `PREROUTING -p udp --dport 3100:3200 -j REDIRECT --to-ports 8101` | **orphan**, removed |
| Before | `PREROUTING -p udp --dport 2080:3000 -j REDIRECT --to-ports 8001` | live hop, kept |
| After | `PREROUTING -p udp --dport 2080:3000 -j REDIRECT --to-ports 8001` | unchanged, counters advancing |

Removal was a single exact-match `iptables -D`. **No `-F`, no `flush ruleset`.**

### Tests added, and their results

`internal/firewall/lifecycle_test.go` (20 functions) and `cmd/firewall_test.go` (2).
**24 tests pass.** Coverage: add, idempotence across four syncs, deleted-node cleanup,
target change, range change, foreign rules surviving, full removal, duplicate cleanup,
orphan pruning, the live remnant's exact shape, a served target being kept, single-port
rules being invisible, both backends, the `PREROUTING` case, a repeated nft add, deleting an
absent rule, no-backend behaviour, ledger reload, corrupt ledger, and `portServing`.

**Two were proven load-bearing** by reintroducing each defect and watching them fail:
- defect A → 3 failures, including
  `rules = "2080:3000->8001", want none: a deleted node's redirect must be cleaned up`
- defect B → `add: nft: no nat prerouting chain could be prepared`

### Isolated end-to-end proof (real iptables, private namespace)

```
seeded: 5353->5353 (foreign, single port) and 3100:3200->8101 (orphan)
A. --apply-firewall        -> port hopping 2080:3000 -> 8001 ; exit 0
B. again                   -> copies of the live range: 1 (idempotent)
C. --prune-firewall        -> port hopping 3100:3200 -> 8101 (orphan) removed
   orphan removed?            YES  OK
   foreign single-port kept?  YES  OK
   live hop kept?             YES  OK
   FAILURES: 0
production rules before and after: identical
```

## 2. Task 2 — easysb-firewall

**Why it was inactive**: the unit file was created **2026-10-08 04:21** but the machine
booted **2026-10-06 16:43**, so `multi-user.target` never had a chance to pull it in. It had
never run (`journalctl` → "No entries"). The rules present were leftovers.

**Started and verified**:

```
before: inactive / enabled      rules: 1
after : active   ExecMainStatus=0   rules: 1   (did not grow: idempotent)
journal: Finished easysb-firewall.service
hop counters still advancing, udp/8001 bound, all five services active
```

Starting it did **not** change the traffic path: the rule already existed and `iptablesAdd`
short-circuits on `-C`. On a reboot it now runs at boot and converges rather than duplicating.

## 3. Task 3 — panel audit (findings; no code change yet)

### `/api/v1/core/config` and the REALITY private key

`handleCoreConfig` returns the file verbatim, and `config.json` contains
`reality.private_key` (length 43, confirmed present). An authenticated admin can read it.

**The save path is safe**, which I verified in code rather than assumed: `handleApply`
re-renders from the node store (`deploy.ServerConfig`) and never accepts a submitted
document, so no save can blank the server-side key.

**Front-end usage could not be traced**: the SPA source lives in the separate
`EasySB-Panel` repository, and the bundle embedded in this build is the placeholder, so
there is no front-end code on the host to inspect. **Whether the sign-in UI actually reads
`reality_private` remains unverified.** Given the save path is safe, the risk of stripping
it is low, but I did not change it without that evidence — as agreed, tracing comes first.

### Security headers

Currently **none** are sent. The SPA blob is served with `cache-control: no-cache` only.
`X-Content-Type-Options: nosniff`, `X-Frame-Options`, `Referrer-Policy` and any CSP are all
absent. **Not implemented this round** — no SPA is being served to test a CSP against, so
adding one now would be unreviewable.

### CSRF

The guard header is `X-EasySB-Panel`, not `X-Requested-With`; a state-changing POST without
it is refused with `403 missing X-EasySB-Panel header`. Auth is checked first, so an
anonymous caller learns nothing. Logout is a public route but only calls
`revoke(requestToken(r))`, not `revokeAll()`, so an anonymous POST cannot log a real session
out — a deviation, not a vulnerability.

## 4. The deployed panel had no front end — now fixed

### 4.1 What was found

The running panel served a **568-byte placeholder page**:

```
<title>EasySB 控制台（占位页）</title>
面板前端占位页：运行 make panel 从 EasySB-Panel 的 Release 取前端，再重新构建二进制。
```

The binary had been built **without** running `make panel`, so `//go:embed all:dist`
embedded the placeholder committed in `public/dist/`. The API was complete and correct —
53/53 routes audited — but a browser saw the placeholder, not a console.

### 4.2 Why `make panel` still worked

`scripts/fetch-panel.sh` defaulted to `EasySBTeam/EasySB-Panel`, which GitHub now
**redirects** to `EasySBTeam/EasySB-Frontend`. The fetch therefore succeeded — one hop and
one rename away from a 404 in CI. The default is now the current name.

### 4.3 Why a placeholder could reach a release at all

`fetch-panel.sh` only failed when `index.html` was *missing*, and the placeholder
`index.html` is committed, so a fetch that produced no real bundle still passed. Two guards
were added:

- `make panel-check` fails when `public/dist/index.html` still contains `占位`, and
  **`pkg-stage` depends on it**, so a package cannot be built from a placeholder.
- `fetch-panel.sh` applies the same check after extraction.

Verified both ways: placeholder → `exit 2` ("public/dist/index.html is still the
placeholder: the release would ship a panel with no console"); real bundle → passes.

### 4.4 The fix, deployed and verified

`make panel` fetched `easysb-panel-dist-v0.1.0.tar.gz` (459,610 bytes) from
`EasySB-Frontend`; the panel was rebuilt, installed, and restarted. What the live panel now
serves:

| Request | Result |
| :--- | :--- |
| `GET /` | 535 bytes, `<title>EasySB 控制台</title>`, no placeholder text |
| `GET /assets/index-C4OFHUfJ.js` | **200, 1,311,116 bytes**, `text/javascript; charset=utf-8` |
| `GET /assets/index-BvrH33hU.css` | **200, 575,327 bytes**, `text/css; charset=utf-8` |
| `GET /logo.png` | 200, 34,323 bytes, `image/png` |
| `GET /nodes` (deep link) | 200, falls back to the shell |
| public IP `https://103.116.247.139:2095/` | 200 |

The JS is real minified application code (contains `createRoot` and `api/v1` paths), not an
error page. The core was **not** restarted: its PID was identical before and after.

### 4.5 What was verified about execution, and what was not

**Verified.** A real browser engine (Chrome for Testing 155) fetched the shell, the JS, the
CSS and `logo.png`, all 200. The panel log then shows the in-sequence API traffic a running
SPA produces: `auth/session` → `dashboard` → `nodes` → `users` → `domains` → `core` →
`system` → `security` → `bbr` → `toolbox` → `logs` → `panel` → `subscriptions`, then
`POST /auth/login`. A `POST /api/v1/core/apply` without the CSRF header was correctly
refused with **403**, and every call made without a session returned **401**.

**Not verified — the honest gap.** I could not confirm the painted UI in a browser.
`chrome-headless-shell` needs X/NSS libraries the server does not ship; I built a portable
bundle and that attempt **failed harmfully**: the bundle included **glibc**, and putting it
in `LD_LIBRARY_PATH` poisoned the shell (coreutils aborted with "stack smashing detected").
The bundle was removed immediately and the host was confirmed unaffected — all five
services active, 0 failed units, panel 200, your files untouched. I did not repeat it.

So the asset graph, MIME types, byte counts, API contract and request sequence are verified
against the live host. **That the React UI paints correctly is unverified.** Opening
`https://103.116.247.139:2095/` settles it in seconds, and it is the one check I cannot make
from here.

**Unverified (earlier question, now moot for the running host)**: whether the *published*
`.deb` in the release carries a real SPA. Binary-safe search found neither asset names nor
placeholder text in it, and serving it on a spare port failed to bind because it reads the
production config's port. I am not claiming either way; the guard added in 4.3 makes the
question answerable for the next release.


## 5. Task 4 — package status (investigation)

| Artifact | Version | Size | sha256 |
| :--- | :--- | :--- | :--- |
| Published `.deb` binary | `37dae5f` | 11,961,832 | `955fa822…` |
| Running binary | `7bcea91` | 44,294,304 | — (md5 `8233a36adf68`) |
| dpkg record | `6.0.0-1`, md5 `6abaaf11…` | — | does not match either |

The published `.deb` checksum still matches the APT index (`2778f3c5…`), so the source is
sound. `dpkg` believes `6.0.0-1` is installed, so **the next `apt upgrade` will overwrite
the running binary without warning**.

Recommendation, unevaluated for security impact: `apt-mark hold easysb` to stop the silent
overwrite, understanding that it also stops security updates from the source. The honest
long-term answer is a reproducible build published as a package whose version actually
changes; until then the choice is between a silent overwrite and a frozen package.

## 6. Regression results

| Check | Result |
| :--- | :--- |
| `gofmt -l .` | clean |
| `go vet` (tags / plain) | exit 0 / exit 0 |
| `go test` tags / plain / `-race` | **39/39 packages ok, 0 failures, 0 data races** |
| `internal/firewall` | 24 tests pass |
| `cmd` (new) | 2 `portServing` tests pass on Linux |
| TUI layout (16 × 6 = 96 frames) | `ok (0 problems)` |
| amd64 / arm64 build | 44,421,383 / 41,025,696 bytes |
| Host services | sing-box, easysb, easysb-panel, easysb-acme.timer, easysb-firewall all **active/enabled**; **0 failed units** |
| `/etc/sing-box` | **byte-identical to before the round** |

## 7. Rollback

**Firewall** (the only production change this round):
```bash
# restore the removed orphan (only if you want it back — nothing needs it)
iptables -t nat -A PREROUTING -p udp --dport 3100:3200 -j REDIRECT --to-ports 8101
# and forget the ledger so a later prune does not re-judge it
rm -f /etc/sing-box/easysb-firewall.json   # regenerated on the next apply
```
The exact pre-change rule set is saved at
`/root/firewall-backup-20261010T014433/iptables-nat-PREROUTING.rules`, with the nft ruleset,
sysctl and the ledger alongside it.

**Binary**: the previous binary is at
`/root/easysb-backup-20261010T010309/easysb.bin` (md5 `25e2c3e1…`, the `0ddd015` build).
Note: the firewall fix does not depend on the binary — it is the rule and the ledger.

**Service**: `systemctl stop easysb-firewall` returns it to the state it was in.

## 8. Open items

1. **The panel has no front end** (section 4). `make panel` needs network access at build
   time; this is the highest-impact finding of the round.
2. **Panel security headers** not added — needs a real SPA to validate a CSP against.
3. **`reality_private` in `/core/config`** — save path verified safe; front-end usage
   unverifiable from here.
4. **`apt upgrade` will overwrite the binary**; `apt-mark hold` proposed, not applied.
5. **Not verified**: reboot recovery. The firewall unit is enabled and idempotent and would
   run at boot, but I did not reboot the VPS (no authorisation) and am not claiming it.
