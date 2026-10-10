# EasySB Optimization — Final Acceptance

Closing report for the full optimization pass. It says what was done, what was
actually executed, what is still unverified and why, and what remains gated on
authorisation. Nothing here is claimed as passing that was not run.

- **Branch**: `master`, **HEAD** `931fc63` (see §1 for the exact chain)
- **Tags**: `v6.0.0` only — never created, moved or deleted
- **Pushed**: nothing. No release, asset, tag or package was published.

---

## 1. The commit chain

Linear, on top of the clone point `37dae5f`, with no reset, amend, rebase or history
rewrite at any point (`git reflog` shows only the clone plus one entry per commit).

| Commit | Subject |
| :--- | :--- |
| `94133de` | `docs: audit the tree, plan the work, and review the result` |
| `b17a6ee` | `feat(atomicfile): one crash-safe write path for every state file` |
| `510facd` | `docs: correct the panel and certificate notes the changes invalidated` |
| `7ca1c31` | `test(sysinfo,service): cover the config parser and the systemctl diagnostic` |
| `04dd7bb` | `docs: audit the release pipeline and its F19 duplicate-tag risk` |
| `c079d0b` | `ci(release): refuse to republish a version whose tag already exists` |
| `559e837` | `fix(atomicfile): create the parent directory, and write every unit through it` |
| `af3a739` | `test(user): prove the store lock excludes across real processes` |
| `e62c48b` | `fix(deps): bump the patched toolchain and x/net for reachable vulnerabilities` |
| `931fc63` | `docs: record the later sweep, its fixes, and the final acceptance` |

Every commit is independently reviewable: `559e837` is one class of writer plus its
tests, `af3a739` adds only a test, `e62c48b` is two version lines.

---

## 2. What each round fixed

### 2.1 The first audit's findings (F1-F22)

Covered by the round plan in `docs/optimization-plan.md`: the store locks and their
ordering (`ApplyStore` restructured into four ordered steps, the read-modify-write
cycles in `provision` locked, the accounting loop persisting before releasing),
credential durability (`internal/atomicfile`, quarantine and backup recovery in both
credential stores), the state file's `Locked`/`Modify`, the panel's route table and
error sanitisation, the loopback listen default, the certificate pair verification,
goroutine lifetimes in `subd`, and the test and documentation work. Status per
finding is in `docs/optimization-audit.md` §4 and §5.

### 2.2 The later sweep (F23-F27)

| Finding | Fix | Commit |
| :--- | :--- | :--- |
| **F23** seven unit/sysctl writers used a plain `os.WriteFile`, so a crash could leave a truncated unit or sysctl drop-in | all seven now go through `internal/atomicfile` | `559e837` |
| **F24** `atomicfile.Write` did not create the parent directory although `WriteKeepingBackup`'s callers assumed it did; the test pinned the wrong contract | `Write` creates the parent at 0755 and leaves an existing directory's mode alone | `559e837` |
| **F25** `govulncheck` had never been run: 12 reachable stdlib vulnerabilities fixed in `go1.27.2`, 4 reachable `x/net` ones fixed in `v0.60.0` | `go` directive → 1.27.2, `x/net` → v0.60.0 | `e62c48b` |
| **F26** the cross-process property of the store lock was measured by hand and pinned by no test | `TestLockedExcludesAcrossProcesses` starts six real processes | `af3a739` |
| **F27** two pre-existing actionlint/shellcheck findings and a stale runner-label database | recorded, deliberately not changed | — |

### 2.3 F19, the release-pipeline defect

The tag comes only from `VERSION`, nothing checked whether it existed, and the
package version is just `<VERSION>-1`, so it does not encode the commit. One additive
step in `prepare` now reads the tag ref and compares its target commit with
`GITHUB_SHA`: absent → first publish; at this commit → a retry; at another commit →
error and stop; API unreadable, unexpected object type, failed dereference or a
non-40-hex value → **fail closed**. Annotated tags are dereferenced through
`git/tags` so a tag object's sha is never compared with a commit.

---

## 3. Everything that was executed

Environment: Debian 13 (trixie), `go1.27.2 linux/amd64`, 16 cores, gcc 14.2.0.
`fpm 1.18.0`, `actionlint 1.7.12`, `shellcheck 0.10.0` and `govulncheck` were
installed into `$HOME` without root; no system change was made.

### 3.1 Gate results

| Check | Result |
| :--- | :--- |
| `gofmt -l .` | clean |
| `go vet` with `release/TAGS` / without | exit 0 / exit 0 |
| `go test -tags <TAGS> ./...` | **38/38 packages ok, 0 failures** |
| `go test ./...` (no tags) | **38/38 packages ok, 0 failures** |
| `go test -tags <TAGS> -race ./...` | **38/38 packages ok, 0 failures, no data race** |
| `make fmt-check` / `make release-matrix` | pass / `["amd64","arm64"]` |
| `scripts/layout_check.py` (16 screens × 6 sizes = **192 renders**) | `ok (0 problems)` |
| `govulncheck -tags <TAGS> ./...` | **No vulnerabilities found** |
| `go mod verify` | all modules verified |
| `go mod tidy` | no-op on the committed state |

### 3.2 Platform note, stated plainly

On the **Windows** host two `internal/update` tests fail:
`TestRunAptStreamsOutput` and `TestInstalledVersion`. They build `#!/bin/sh` stubs
and `exec` them, which Windows cannot do. They are **pre-existing and untouched** —
`git diff 37dae5f..HEAD -- internal/update/` is empty — and they **pass on Linux**
(`ok github.com/EasySBTeam/EasySB/internal/update`). Every gate above was therefore
run on Linux, the platform the `.deb` targets. This is an environment difference,
not a passing test that was skipped.

### 3.3 Release and packaging verification

| Check | Result |
| :--- | :--- |
| `make build` then `make packages-asset` for amd64 and arm64 | real `.deb` files produced (12 MB / 9.1 MB after UPX) |
| Payload extracted and inspected | `/usr/bin/easysb`, `/usr/bin/sb → easysb`, two units, licenses |
| The packaged binary runs | `--version` → `EasySB 6.0.0 (<commit>)` |
| Packaged units vs `--print-unit node\|sub` | **byte-identical** — the packaged unit cannot drift from the runtime one |
| `/etc/sing-box` in the package? | **no**, and neither maintainer script mentions it |
| F19 impact, measured | two same-version different-byte builds are `dpkg --compare-versions … eq` → `gt` is false → apt has nothing to upgrade to |

### 3.4 Read-only verification of the live release

| Fact | Value |
| :--- | :--- |
| remote `master` | `37dae5f…` — the clone point, unchanged |
| remote `v6.0.0` | `a343e41…` (lightweight) |
| release | not draft, not prerelease, published 2026-10-06, 9 assets |
| apt index consistency | every `.deb` SHA256/size and the signed `Packages`/`Packages.gz` digests in `InRelease` **recomputed and matched** |

So the source users' `apt` talks to is currently sound; F19 is a future corruption,
not a present one.

### 3.5 The cross-process lock, measured

Eight independent OS processes each performed the locked read-modify-write cycle:
**8/8 updates survived**. An independent `flock -x` holder on the sidecar made a
writer **block for 2511 ms** and proceed **16 ms** after release. The new permanent
test passes 3/3 with the lock and fails with `store holds 1 accounts after 6
cross-process writers: a change was lost` when `lockFile` is replaced by a no-op.

### 3.6 F19 guard, exercised

Eleven cases against a stubbed `gh`, no network, no tag and no release created: tag
absent; lightweight tag at this commit; at another commit; API 503; rate limited;
annotated tag at this commit; at another commit; annotated dereference failure;
unexpected object type; non-hex sha; and the structural checks (the guard is
`prepare`'s last step, immediately after the version is read, unconditional, not
`continue-on-error`, with no upload or publish step before it, and `release` depends
on `prepare`). **11 passed, 0 failed.** The guard's script is `shellcheck`-clean at
severity `info`.

---

## 4. Behaviour changes an operator should know about

1. **The panel's default listen address is `127.0.0.1`.** This affects **fresh
   installs only**: `PANEL_LISTEN` has always been persisted, `EnsureConfig` returns
   early when a password hash exists, `install.sh` never mentions the panel, the
   `.deb` does not own `/etc/sing-box`, and both maintainer scripts only run
   `daemon-reload`. Verified by running the new `EnsureConfig` against a config
   planted as a previous version would have left it: the file came back
   **byte-identical** and the effective listen stayed `0.0.0.0`.
   `EASYSB_PANEL_LISTEN` overrides the file.
2. **Internal errors return a generic message plus a reference** instead of raw OS
   text. Status codes and user-input wording are unchanged; the detail is logged as
   `panel: <ref> <summary>: <err>`.

Both are documented in `docs/panel-installation.md` and
`docs/panel-architecture.md`.

---

## 5. Still unverified, with the reason and the consequence

| Scenario | Why not | Consequence if it is wrong |
| :--- | :--- | :--- |
| The release workflow actually running | needs a push, which is not authorised | the guard is statically verified and case-tested against a stub, but never executed by GitHub's runner; `GITHUB_SHA`/`GH_TOKEN` semantics are reasoned from the Actions model |
| `action-gh-release` on an existing tag | its source is not fetchable from here | if it *failed* instead of updating, F19 would have been a failed job rather than a corrupted release; the guard stops the run either way |
| GPG signing of `Release`/`InRelease` | no key | the index layout is verified against the published one, but a signing failure or a wrong key is not |
| `make repo` end to end | needs the key | as above |
| Real `apt` upgrade on a configured host | needs such a host | F19's version comparison is measured; the end-to-end `apt update && apt upgrade` is not |
| Real systemd, real ACME issuance | no systemd as PID 1; no domain or port 80 | `internal/service` integration, ACME issuance and renewal are exercised only through seams and unit tests |
| The `service.Do`/`Active` deadline firing | with no systemd bus the call fails immediately rather than blocking | the 120 s ceiling is verified by inspection, not by a hang |
| The mixed-checksum upload window | needs a live upload | unchanged by this work; noted in the release audit |
| `dpkg --unpack` into a throwaway root | dpkg requires superuser even with `--root` | the payload is verified by extraction instead of a real unpack |

---

## 6. Final state

```
HEAD    : 931fc63 (the documentation commit; the code state verified below is e62c48b)
branch  : master
status  : clean — no modified, staged or untracked files
tags    : v6.0.0 (unchanged; never created, moved or deleted)
remote  : origin/master still at 37dae5f — nothing pushed
```

**Gated on authorisation, deliberately not done**: `git push`; creating, moving or
deleting any tag; creating or updating a GitHub Release; publishing a `.deb`;
modifying the live apt source; and any change to release policy beyond the F19 guard.
The F19 guard will, on the next qualifying push with `VERSION` still `6.0.0`, stop
the run and require an explicit version bump — which is the intended outcome, and the
first thing to confirm once pushing is authorised.
