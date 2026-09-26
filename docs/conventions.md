# Conventions and Preferences

## Naming

- Directories under the repository are lowercase and ASCII: `templates/`,
  `docs/`, `assets/`. This includes template subdirectories
  (`anytls`, `hysteria2`, `tuic`, `vmess-websocket-tls`,
  `vless-vision-reality`, `config`).
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
  docs are English. User-facing UI strings are bilingual through `internal/i18n`.

## Versioning

- `VERSION` holds the program version, currently in `X.Y.Z` form.
- `VERSION` is the only place the number is written: it is embedded with
  `go:embed`, and `install.sh` reads it in a checkout or detects the latest
  release otherwise. Do not add a `main.version` default or a script constant.
- The program version is independent of the sing-box core version.
- Release tags are `v<VERSION>`. The workflow, `install.sh`, and
  `internal/update` all derive the tag from the version; do not create a second
  naming scheme.
- The apt repository is the one exception: it publishes to the fixed tag
  `debian`, because apt needs a URI that never changes, and it carries only the
  generated index and the `.deb` files. Binaries stay on `v<VERSION>`.

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

- `.github/workflows/easysb-go-release.yml` cross-compiles `linux/{amd64,arm64,armv7,386,riscv64,s390x}`,
  runs on push to `master` for changes under the watched paths, and publishes
  all assets to the `v<VERSION>` release.
- The same binaries are wrapped into `.deb` (`make deb`), `.rpm` (`make rpm`) and
  pacman (`make pacman`) packages by fpm, all from one staged tree, and into the apt
  index by `make apt-index` (`apt-ftparchive`). The per-ecosystem arch names live in
  the Makefile's `DEBARCH_*` / `RPMARCH_*` / `PACMANARCH_*`, and the packaged units
  come from `easysb --print-unit`; do not hand-write a unit under `packaging/`.
  Only the `.deb` has a repository (the `debian` tag); the `.rpm` and the pacman
  package are plain release assets.
- After a force push, trigger the workflow with a normal push; force pushes do
  not reliably raise a `push` event for Actions.

## Documentation hygiene

- Update `CHANGELOG.md` under `## [Unreleased]` for structural or behavioral
  changes, and move entries under the version heading at release time.
- Keep `docs/` current when the layout or a core decision changes.
