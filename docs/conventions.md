# Conventions and Preferences

## Naming

- Directories under the repository are lowercase and ASCII: `docs/`, `assets/`
  and the Go package tree. The readable config samples moved to the separate
  EasySB-Examples repository, where the protocol directories keep their display
  casing (`Hysteria2`, `VLESS-Vision-REALITY`, `TUIC`, `AnyTLS`,
  `VMess-WebSocket-TLS`, `Config`).
- Go packages stay lowercase single words (`state`, `subscribe`, `sysinfo`).
  Interfaces and stores are named after what they model, not after the UI
  screen: the account model is `internal/user` with a `Store`, while the panel
  labels it 「账号与流量」.
- Protocol display names keep their brand casing in prose (`AnyTLS`,
  `Hysteria2`, `TUIC v5`), while protocol keys and paths are lowercase.

## Language

- `README.md` is English and is the default document.
- `README_ZH.md` is the Chinese translation. Keep both in step; a feature is
  not done until both describe it.
- Code identifiers, comments, commit subjects for non-trivial code, and these
  docs are English. User-facing UI strings are bilingual through `internal/i18n`,
  with one deliberate exception: the toolbox's own report bodies. A tool's row
  labels, summaries and notes are the tool's own words, because a translation
  layer between a measurement and its label is somewhere a number ends up under
  the wrong word. See `docs/toolbox.md` for what the panel words and what the
  tools word.

## Versioning

- `VERSION` holds the program version, currently in `X.Y.Z` form.
- `VERSION` is the only place the number is written: it is embedded with
  `go:embed`, and `install.sh` never needs it, because apt resolves the package
  itself. Do not add a `main.version` default or a script constant.
- The program version is independent of the sing-box core version.
- Release tags are `v<VERSION>`, and the release name is that same string. The
  workflow, `internal/update` and the release notes all derive the tag from the
  version; do not create a second naming scheme. Only the newest release is kept:
  the workflow prunes older releases and their tags after every publish.
- Release assets are one `.deb` per architecture, `easysb_<version>-1_<arch>.deb`,
  where `<arch>` is Debian's spelling (`amd64`, `arm64`) and `-1` is the package's
  own revision. `dist/easysb-linux-<asset>` is an intermediate and is never
  published by itself.
- The apt source is the GitHub Release itself, so the one-command `install.sh` has one
  fixed address (`https://github.com/EasySBTeam/EasySB/releases/latest/download`) to
  point at. That directory carries `install.sh` itself, so the one command (`curl
  -fsSL https://github.com/EasySBTeam/EasySB/releases/latest/download/install.sh |
  sudo bash`) needs no second address. It is a flat ("trivial") apt repository: every
  file sits in one directory (`Packages` and the signed `Release` / `InRelease` /
  `Release.gpg`, the armored key `easysb-archive-keyring.asc`, `install.sh`, and one
  `.deb` per architecture), so there is no `dists/<suite>` split. `make repo` builds and
  signs it (`apt-ftparchive`) and the release job attaches it to the release. One
  package serves every distribution; `install.sh` still maps a machine to a supported
  Debian or Ubuntu release only to reject one we do not ship for.

## Commits

- Use conventional commits: `type(scope): subject`, for example
  `fix(tui): 收束底部空行` or `test(update): 覆盖发布 tag 推导`.
- Keep the subject one line. Explain the why in the body when it is not obvious.
- Do not add co-author trailers.

## Code

- Run `gofmt` before committing; the tree must be gofmt clean.
- Run `go vet ./...` and `go test ./...` before pushing.
- Prefer small packages with a single responsibility and doc comments on the
  package and exported identifiers.
- Avoid comments that restate the code; keep comments for intent and gotchas.

## Tests

- Unit tests live next to the code as `*_test.go`.
- The TUI has a render smoke path: `--render --width W --height H` prints one
  frame, which makes overflow and alignment regressions testable without a TTY.
- Prefer table-driven tests for parsing and mapping logic.

## Release

- `.github/workflows/easysb-go-release.yml` cross-compiles `linux/{amd64,arm64}`
  (the two architectures the BBR kernels cover), runs on push to `master` for
  changes under the watched paths, and publishes one release, tagged and named
  `v<VERSION>`, carrying one `.deb` per architecture. The release job then prunes the
  older releases and their tags, so the Release page only shows the current version.
- Publishing is decided by a `gate` job, not by the release job itself. `gate` runs
  `scripts/guard-duplicate-release.sh`, which reports `publish=true` for a first
  publish (or a retry of the exact commit the tag points at) and `publish=false` when
  `v<VERSION>` already points at another commit. A `publish=false` run leaves build and
  test to complete and skips only the release, so an un-bumped merge to `master` stays
  green while `gate` records "bump VERSION" in the run summary. Only a genuine error
  (the API cannot confirm the tag, or the response shape is unexpected) fails the run.
- The `.deb` (`make deb`) is built by fpm from one staged tree; the arch names live in
  the Makefile's `DEBARCH_MAP`, keyed on an asset name so one table serves packaging
  and layout. `pkg-stage` UPX-compresses the binary on the way into the tree, so the
  release asset and the apt source carry the same compressed bytes. The packaged units
  come from `easysb --print-unit`; do not hand-write a unit under `packaging/`.
- `make repo` (`packaging/repo/index.sh`, via `apt-ftparchive`) lays the `.deb` files
  out as a flat apt repository and signs it. The release job attaches `dist/repo/*` to
  the release, which serves as the source root; `packaging/repo/` is the only place
  that decides the layout.
- The apt index is signed with one passphrase-protected key: the secrets are
  `GPG_PRIVATE_KEY` and `GPG_PASSPHRASE`, and signing reads the passphrase from a 0600
  file so it never reaches a process list. The release job requires `GPG_PRIVATE_KEY`
  and fails without it, because an unsigned source is not something `install.sh`
  should ever point a machine at.
- After a force push, trigger the workflow with a normal push; force pushes do
  not reliably raise a `push` event for Actions.

## Documentation hygiene

- Update `CHANGELOG.md` under `## [Unreleased]` for structural or behavioral
  changes, and move entries under the version heading at release time.
- Keep `docs/` current when the layout or a core decision changes.
