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
- Release tags are `v<VERSION>`, and the release name is that same string. The
  workflow, `install.sh` and `internal/update` all derive the tag from the
  version; do not create a second naming scheme.
- Release assets are named after the version and the architecture, in the shape
  sing-box uses: `easysb-<version>-linux-<goarch>.tar.gz` for the tarball, and
  `easysb_<version>_linux_<arch>.<ext>` for the `.deb` / `.rpm` / pacman package,
  where `<arch>` is that ecosystem's own spelling. `dist/easysb-linux-<asset>` is
  an intermediate and is never published by itself.
- The package sources live on the release server, not on a second release tag, so
  `install.sh --method repo` has one fixed address (`https://sb.kejizero.xyz`)
  to point at. Its four subtrees are `apt/`, `rpm/<arch>/`, `pacman/<arch>/` and
  `bin/`; `make repo` builds them and the workflow syncs them there. The three
  index subtrees are signed with one key when `GPG_KEY_ID` is set, and each
  publishes that key inside its own tree: `apt/easysb.gpg`, `rpm/RPM-GPG-KEY-easysb`
  and `pacman/easysb.asc`. The rpm tree root also carries `easysb.repo`, so
  registering the rpm source is one command per dnf generation: dnf5's
  `config-manager addrepo --from-repofile` and dnf4's `config-manager --add-repo`
  (which `dnf-plugins-core` provides). `install.sh --method repo` writes the
  strict entry only when it finds that key on the server, and the permissive
  form otherwise.

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
  runs on push to `master` for changes under the watched paths, and publishes one
  release, tagged and named `v<VERSION>`, carrying a tarball per architecture and the
  three package formats.
- The same binaries are wrapped into `.deb` (`make deb`), `.rpm` (`make rpm`) and
  pacman (`make pacman`) packages by fpm, all from one staged tree, and laid out as
  apt / rpm / pacman / bin sources by `make repo` (`apt-ftparchive`, `createrepo_c`,
  `repo-add`). The per-ecosystem arch names live in the Makefile's `DEBARCH_*` /
  `RPMARCH_*` / `PACMANARCH_*`, and the packaged units come from `easysb --print-unit`;
  do not hand-write a unit under `packaging/`. The release workflow syncs `dist/repo`
  to the release server with FTP-Deploy-Action; `packaging/server/` holds the one-shot
  provisioning script, the Caddy browse template the landing page is rendered from, and
  the favicon. The template and the favicon travel as `dist/repo/.easysb/browse.html`
  and `dist/repo/.easysb/favicon.svg`, so `make repo` is the only place that decides
  where they land.
- The apt index, the rpm-md trees and the pacman database are signed with one
  passphrase-protected key: the secrets are `GPG_PRIVATE_KEY` and `GPG_PASSPHRASE`,
  and signing reads the passphrase from a 0600 file so it never reaches a process
  list. Run the "Provision the release server" workflow once; `FTP_PASSWORD` and
  `SERVER_SSH_PASSWORD` are what it needs.
  The server's address and the account the provisioning run logs in as are secrets as
  well (`SERVER_HOST`, `SERVER_USER`), so neither is written into the repository and
  moving to another host is a settings change, not a commit.
- After a force push, trigger the workflow with a normal push; force pushes do
  not reliably raise a `push` event for Actions.

## Documentation hygiene

- Update `CHANGELOG.md` under `## [Unreleased]` for structural or behavioral
  changes, and move entries under the version heading at release time.
- Keep `docs/` current when the layout or a core decision changes.
