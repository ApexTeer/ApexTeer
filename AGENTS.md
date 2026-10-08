# AGENTS.md

Guidance for AI agents working in this repository.

## What this is

EasySB is a single static Go binary that deploys and operates a five-protocol
sing-box server on Linux, with a full-screen bubbletea TUI. It replaces a
legacy bash implementation. The entry point is `main.go`; the module is
`github.com/EasySBTeam/EasySB` and requires Go 1.27.1.

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
make deb        # package the dist/ binaries into .deb files with fpm (UPX-compressed)
make repo       # lay the .deb files out as a signed apt tree in dist/repo
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
  second place. The workflow, `internal/update`, and the release notes share it.
  `VERSION` is embedded into the binary with `go:embed`; do not reintroduce a
  `main.version` default or a version constant in `install.sh`.
- EasySB ships as a single `.deb`: the only architectures are `amd64` and `arm64`
  (the two the BBR kernels cover), the only platform is Debian and Ubuntu, and the
  only package is the `.deb`. The arch names come from the Makefile's `ARCHES` /
  `DEBARCH_MAP`, and the packaged systemd units are printed by the binary
  (`easysb --print-unit node|sub`) rather than copied into `packaging/`. A
  hand-written unit or a second arch list in the workflow drifts. The `.deb` comes
  from one staged tree (`make pkg-stage`, driven per architecture by `make
  packages-asset`), and `pkg-stage` UPX-compresses the binary there, so the release
  asset and the apt source put down the same bytes.
- The apt source is the GitHub Release itself, a flat ("trivial") apt repository
  rooted at `https://github.com/EasySBTeam/EasySB/releases/latest/download`: every
  file sits in one directory (`Packages`, `Release`, `InRelease`, `Release.gpg`, the
  armored key, `install.sh`, and one `.deb` per architecture), so no `dists/` split
  exists. `packaging/repo/index.sh` lays it out and signs it (`make repo`), and the
  release job attaches the tree to the release. The single `.deb` serves every
  distribution; `install.sh` still maps a machine's `/etc/os-release` to a supported
  release, purely to reject one we do not ship for.
- Keep `/etc/sing-box/easysb.conf` compatible with the legacy shell tool. Add
  keys, do not rename or repurpose them. A key that describes state a package now
  owns is the exception: v4 dropped `SUB_PORT` and `SUB_PATH` with the nginx site,
  and v6 dropped the per-protocol keys (`IS_*`, `PORT_*`, `HY2_HOP_RANGE`,
  `REALITY_*`) once `internal/node` took over what the host serves. The migration
  reads those keys, the next save drops them, and the docs change in the same
  commit.
- The node store `/etc/sing-box/easysb-nodes.json` is the only source of what the
  host serves: one node is one protocol inbound with its own port and parameters.
  The rendered config's inbounds come from the enabled nodes and nowhere else.
- The account store `/etc/sing-box/easysb-users.json` is the only source of
  credentials. The core user name is `<token>@<node id>`, and the inbound `users`
  arrays and `stats.users` must both come from the account store's routable set,
  or an account is authenticated but never counted.
- Every user-facing string goes through `internal/i18n` for both `C` and `E`,
  except the toolbox's own report bodies: a tool's row labels, summaries and notes
  are the tool's own words on purpose (see `docs/toolbox.md`).
- Directories and paths are lowercase ASCII. `templates/` subdirectories are
  lowercase.
- The runtime subscription templates are embedded from `internal/subscribe/`
  (`tun-fakeip.json`, `mihomo.yaml`); `templates/config/` holds the readable
  mirrors. Keep each pair in sync.
- `README.md` is English; `README_ZH.md` is Chinese. Update both.
- Use conventional commit subjects (`type(scope): subject`) and no co-author
  trailers.
