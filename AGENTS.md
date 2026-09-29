# AGENTS.md

Guidance for AI agents working in this repository.

## What this is

EasySB is a single static Go binary that deploys and operates a five-protocol
sing-box server on Linux, with a full-screen bubbletea TUI. It replaces a
legacy bash implementation. The entry point is `main.go`; the module is
`github.com/MinimaxFlora/EasySB` and requires Go 1.27.1.

Read `docs/` first, then the package you need:

- `docs/architecture.md` - layout, runtime paths, package responsibilities
- `docs/design.md` - the decisions behind the shape of the code
- `docs/user-management.md` - accounts, the subscription service and usage accounting
- `docs/conventions.md` - naming, language, versioning, commit and release rules
- `docs/pitfalls.md` - known traps and how to avoid them

## Commands

```bash
make            # build ./easysb with the tags from release/TAGS
make check      # gofmt -l + go vet + go test, the pre-commit gate
make dist       # cross-compile every release architecture into dist/
make deb        # package the dist/ binaries into .deb files with fpm
make rpm        # package them into .rpm files for the Fedora / RHEL family
make pacman     # package them into pacman packages for Arch
make repo       # build the per-distribution packages and lay out the linux/ source tree
make repo-index # lay out and index the sources when the packages already exist
```

`make help` lists every target. The bare Go commands still work; `make build` only
adds `-trimpath`, the tags from `release/TAGS` and the commit stamp.

Render one TUI frame without a TTY (good for layout checks):

```bash
make render     # or: ./easysb --render --width 100 --height 40
make screens    # render every screen and assert the layout (python3)
```

## Rules that are easy to get wrong

- The release tag is always `v<VERSION>`. Derive it; never hardcode it in a
  second place. `install.sh`, the workflow, and `internal/update` share it.
  `VERSION` is embedded into the binary with `go:embed`; do not reintroduce a
  `main.version` default or a version constant in `install.sh`.
- The `.deb`, the `.rpm`, the pacman package and the apt repository share the
  release's single sources: the arch names come from the Makefile (`ARCHES` plus
  the `DEBARCH_MAP` / `RPMARCH_MAP` / `PACMANARCH_*` mappings, because Debian spells
  armv7 `armhf`, rpm spells it `armv7hl` and Arch `armv7h`), and the packaged
  systemd units are printed by the binary (`easysb --print-unit node|sub`) rather
  than copied into `packaging/`. A hand-written unit or a second arch list in the
  workflow drifts. The three formats come from one staged tree (`make pkg-stage`,
  driven per architecture by `make packages-asset`), so installing the `.deb` and
  installing the `.rpm` put down the same bytes.
- The sources are laid out in Docker's official shape, so a distribution is a
  directory: apt and rpm share the `linux/<distro>/...` tree (`DEB_SUITES` and
  `RPM_TREES` in the Makefile, both keyed on asset names), and `packaging/repo/`
  holds the two scripts that build and index it (`make repo` is `repo-packages`
  then `repo-index`). Distro directories, suites and release numbers must stay in
  step with `install.sh`'s `repo_coords`, which is what decides whether a machine
  gets a source or the tarball.
- Keep `/etc/sing-box/easysb.conf` compatible with the legacy shell tool. Add
  keys, do not rename or repurpose them. The one exception is a key that
  described a component which no longer exists (v4 dropped `SUB_PORT` and
  `SUB_PATH` with the nginx site); removing such a key is part of the same
  change that removes the component, and the docs change with it.
- The account store `/etc/sing-box/easysb-users.json` is the only source of
  credentials. The core user name is the account token, and the inbound `users`
  arrays and `stats.users` must both come from `user.Store.Routable`, or an
  account is authenticated but never counted.
- Every user-facing string goes through `internal/i18n` for both `C` and `E`.
- Directories and paths are lowercase ASCII. `templates/` subdirectories are
  lowercase.
- The runtime subscription templates are embedded from `internal/subscribe/`
  (`tun-fakeip.json`, `mihomo.yaml`); `templates/config/` holds the readable
  mirrors. Keep each pair in sync.
- `README.md` is English; `README_ZH.md` is Chinese. Update both.
- Use conventional commit subjects (`type(scope): subject`) and no co-author
  trailers.
