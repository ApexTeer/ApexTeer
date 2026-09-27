# Architecture

EasySB is one Go module rooted at the repository root. Everything the running
program needs is compiled into a single static binary: the sing-box core is a
module requirement (`internal/sbcore`) and certificates are issued in process
through `github.com/go-acme/lego/v5`. A running panel therefore fetches nothing at
all — no core archive, no `acme.sh`, no socat. The only downloads left are the
panel's own release (`internal/update`) and the optional BBR kernel packages
(`internal/bbr`).

## Repository layout

```
.
├── main.go                         # entry point, flags, version resolution, `core run`, `--tool`
├── VERSION                         # program version, single source of truth
├── release/TAGS                    # the one definition of the build tag set
├── install.sh                      # installer: one-click, package source, or a local package
├── Makefile                        # build / test / dist entry points (see `make help`)
├── packaging/                      # package lifecycle scripts (deb/, rpm/) and server/
├── go.mod / go.sum                 # module github.com/MinimaxFlora/EasySB, Go 1.27.1
├── templates/                      # readable JSONC samples and subscription template
│   ├── anytls/
│   ├── hysteria2/
│   ├── tuic/
│   ├── vmess-websocket-tls/
│   ├── vless-vision-reality/
│   └── config/
│       ├── tun-fakeip.json          # TUN + FakeIP sing-box subscription template
│       └── mihomo.yaml              # mihomo / Clash Meta subscription template
├── internal/                       # all implementation packages
├── assets/                         # README banners
├── docs/                           # these engineering docs
├── AGENTS.md                       # agent entry point
├── README.md                       # English (default)
└── README_ZH.md                    # Chinese
```

`templates/` is documentation and reference material. The subscription templates
actually used at runtime are embedded from `internal/subscribe/tun-fakeip.json`
and `internal/subscribe/mihomo.yaml` via `//go:embed`; keep each pair in sync when
editing.

## Runtime data

| Path | Owner | Purpose |
| :--- | :--- | :--- |
| `/etc/sing-box/easysb.conf` | `internal/state` | persisted node state, legacy-compatible KV |
| `/etc/sing-box/config.json` | `internal/config` | rendered server config |
| `/etc/sing-box/cert/` | `internal/cert` | the self-signed placeholder pair, used until a real certificate is issued |
| `/etc/sing-box/easysb-users.json` | `internal/user` | accounts: credentials, quotas, expiry and counters (`0600`) |
| `/etc/systemd/system/easysb.service` or `/etc/init.d/easysb` | `internal/service` | subscription service unit (`easysb --serve`) |
| `/etc/systemd/system/sing-box.service` or `/etc/init.d/sing-box` | `internal/service` | core service unit |
| `/etc/sing-box/acme/` | `internal/cert` | ACME state, overridable with `EASYSB_ACME_DIR`: `account.key` and `account.json` (`0600`), then one directory per domain holding `fullchain.cer` (`0644`) and `private.key` (`0600`) |
| `/etc/sysctl.d/99-easysb-bbr.conf`, `/etc/modules-load.d/easysb-bbr.conf` | `internal/bbr` | BBR settings EasySB writes itself, so they never collide with the kernel project's own drop-in; the sysctl file carries a comment recording the values it replaced, which is what the clear action restores. The installed kernel packages (`minimaxflora-bbrv3`) belong to dpkg and are removed through apt |
| `/etc/sing-box/easysb-ui.conf` | `internal/prefs` | interface choices (skin, palette, marker set, language), `0644`, overridable with `EASYSB_UI_CONF` |
| `/etc/systemd/system/easysb-acme.timer` or `/etc/init.d/easysb-acme` | `internal/cert` | certificate renewal: the panel issues and renews through lego in process, so this unit is the only thing that renews, and `--renew-certs` reloads the services afterwards. The unit names the path of the binary that wrote it, so it is installed from inside the panel (or with `--install-renew-timer`) rather than copied between hosts |

## Packaging

One release carries everything, tagged and named `v<VERSION>`: a release tarball per
architecture and the three package formats. The `.deb`, the `.rpm`, the pacman package
and the package sources are built from the same `dist/` binaries as the release, so
nothing is compiled twice and no arch list is repeated. All three formats come from one
staged tree (`make pkg-stage`), which one architecture's job drives end to end with
`make packages-asset`:

| Piece | Where it comes from |
| :--- | :--- |
| Binary and shortcut | `dist/easysb-linux-<asset>` → `/usr/bin/easysb`, symlinked as `/usr/bin/sb` |
| Release tarball | `make tarball-asset` wraps that binary as `dist/easysb-<version>-linux-<asset>.tar.gz`, with the member `easysb` at the root, which is what `install.sh` and `internal/update` unpack |
| `sing-box.service` | `easysb --print-unit node --unit-exec /usr/bin/easysb`, the same `internal/service.UnitBody` the panel writes at runtime |
| `easysb.service` | `easysb --print-unit sub --unit-exec /usr/bin/easysb`, the same `internal/subd.UnitBody` |
| Package architecture | `DEBARCH_*`, `RPMARCH_*` and `PACMANARCH_*` in the `Makefile` (armv7 → `armhf` / `armv7hl` / `armv7h`; rpm spells 386 `i686`; Arch has no i386 or s390x, so no pacman package is made for them) |
| Source trees | `make repo` lays the same files out as four sources: `make apt-index` runs `apt-ftparchive` over `dist/*.deb` and signs when `GPG_KEY_ID` is set, `make rpm-index` runs `createrepo_c` per rpm architecture, `make pacman-index` runs `repo-add` per pacman architecture, and `bin/` takes the tarballs |

`dist/easysb-linux-<asset>` is only an intermediate: `pkg-stage` copies it into the
staged tree and `tarball-asset` wraps it, and neither the release nor the sources ever
publish it on its own.

The package ships the units but does not enable or start them: a fresh host has no
node configuration, so the panel enables and starts the service once the user has
configured it. Because the packaged unit lives in `/usr/lib/systemd/system` and the
panel writes its own to `/etc/systemd/system`, the panel's copy wins while it exists
and the packaged one is the fallback — the two never fight over one path.

The fixed URLs apt, rpm and pacman need live on the release server
(`sb.kejizero.xyz`), not on a second release tag: the release workflow syncs
`dist/repo` there with FTP-Deploy-Action, so `install.sh --method repo` can write a
source entry that never changes. The server itself is prepared once by
`packaging/server/provision.sh` (caddy for HTTPS, vsftpd for the upload account,
`packaging/server/index.html` for the landing page), driven by the "Provision the
release server" workflow.

## Packages

| Package | Responsibility |
| :--- | :--- |
| `internal/tui` | bubbletea model, full-screen dashboard, menu tree, forms, panels, progress |
| `internal/state` | read/write `easysb.conf`; protocol keys, default ports, default parameters |
| `internal/config` | render the sing-box server configuration from state |
| `internal/sbcore` | the core compiled in: `Run` (the node, `easysb core run`), `Check` (config acceptance by the real engine), `Version`, and the `with_v2ray_api` capability as a tagged file pair |
| `internal/download` | the one HTTP-to-file path left: the panel's own release and the BBR kernel packages, with progress readings |
| `internal/cert` | ACME issuance in process through lego (HTTP-01 standalone): the account, issue/renew/remove certificates, expiry decisions, the renewal timer unit, the self-signed fallback |
| `internal/prefs` | remember and re-apply the interface choices: skin, palette, marker set, language |
| `internal/firewall` | Hysteria2 port-hopping DNAT rules and the boot restore unit |
| `internal/bbr` | BBR: read the running kernel's congestion control state, enable it through sysctl drop-ins (recording what they replaced so clearing can undo them), and install the prebuilt BBRv3 kernels published by Linux-BBR-v3 (release/tag discovery, mirror fallback, dpkg) |
| `internal/user` | account model and store: per-protocol credentials, quota/expiry evaluation, subscription tokens |
| `internal/subd` | subscription HTTP service: TLS, User-Agent negotiation, response headers, accounting loop |
| `internal/stats` | gRPC client for the core's `StatsService`, usage accounting, quota enforcement |
| `internal/subscribe` | subscription URLs, per-protocol share links, QR payloads, and the sing-box JSON, mihomo YAML and v2rayN base64 documents for one account |
| `internal/secret` | random UUID / password / Reality keypair generation |
| `internal/service` | systemd and OpenRC detection, install, start/stop, status |
| `internal/sysinfo` | host/device/core/service status for the dashboard: local IPv4/IPv6, CPU cores, load, memory, swap, disk and uptime |
| `internal/netutil` | small network helpers (public IPv4-first IP detection, host resolution) |
| `internal/uninstall` | remove the deployment while keeping the issued certificates |
| `internal/update` | self-update from the GitHub release tag `v<version>` |
| `internal/toolbox` | what every toolbox entry returns and what the panel hands it: one `Result` shaped as a table, one `Options` carrying every outside dependency |
| `internal/toolbox/tools` | the toolbox registry: the one list the menu, the board and `--tool` read |
| `internal/toolbox/backtrace` | 三网回程: ICMP path probing and the carrier that carries the return traffic |
| `internal/toolbox/ipquality` | IP 质量: several keyless databases, IP type, DNS blocklists |
| `internal/toolbox/portcheck` | 邮件端口: mail ports against the public address, PTR and FCrDNS |
| `internal/toolbox/bench` | CPU, memory and disk workloads, measured with the standard library |
| `internal/toolbox/speed` | speedtest.net runs: nearby servers, and only Chinese carrier servers for 三网测速 |
| `internal/toolbox/hw` | system and disk information read from /proc, /sys and df |
| `internal/unlock` | service-unlock probes: whether this IP can use ChatGPT, Netflix, Disney+, YouTube Premium, Prime Video, TikTok, Spotify, Reddit, Steam, 巴哈姆特動畫瘋 and the Bilibili catalogues, each verdict from a small HTTP request and never an optimistic guess |
| `internal/i18n` | `C` / `E` bilingual string table |
| `internal/icons` | single-column Unicode symbol palette, `EASYSB_ICONS=ascii` falls back to ASCII |
| `internal/theme` | dark / light color palettes and frame/column layout helpers |

## The fixed layout

Every page draws the same two boxes, in the same rows, at the same sizes. The rows come from
the main page, which is the page an operator sees first:

```
┌ 看板 ┐   the section's summary, or the welcome board on the main page
          one blank row
┌ 菜单 ┐   the entries of the page the operator is standing in
· 说明    the description of the hovered entry
┌ 提示 ┐   the keys that work on this page
```

`internal/tui/layout.go` owns the slots. `layoutFor` sizes them in one order and every page
uses the result, so the frame never moves under the cursor:

- the tail (the hovered entry's description, then the key hints) is reserved first, because it
  is part of every page;
- the entries box is the tallest menu the panel lists one entry per line — the hardware group's
  six tools plus the way back — which is nine rows at a hundred columns;
- the 看板 takes what is left, up to the height of the main page's own welcome card. The blank
  row between the boxes goes before the card loses a row, and on a terminal too short for
  everything it is the 看板 that shrinks: it is the one slot whose content can say
  "…还有 N 行未显示" and still be useful.

The 看板's content adapts to the rows it was given rather than deciding them: the welcome card
drops the quote, then the tagline, then the wordmark itself, and keeps its live vitals; every
other page's board is written into the same rows. `boxAt` pads a box with blank rows rather than
letting it shrink, so a page with four entries and a page with nine line up.

The line above the key hints belongs to the main menu, whose rows carry only their number and
name: that is where the hovered entry is explained in full. Every page under it puts the
description in the row itself, so the line is blank there — and still a row, because the hints
are pinned to the same place on every page. One screen scrolls: a finished report is read with
↑/↓, PageUp/PageDown and Home/End inside its box, with the top row number on the line under it,
because a measurement is read in full rather than counted.

`dashboard.go` lays the entries out: the main menu keeps the two columns and the bare
number-and-name rows it has always had, and every other page lists one entry per line with the
description beside the label — that is what the line after the box repeats in full, so a
description too wide for the row can still be read to the end. A page that has outgrown one
entry per line (the account detail page's twelve operations) falls back to the panel's columns
instead of hiding half of itself behind a "+N" row: an entry nobody can see is an entry nobody
can reach. Nothing scrolls — `clipRows` cuts content to the rows a box has and ends on a line
that says how many rows were left out, and the toolbox 看板 is written as a summary, a count of
what has been measured followed by the newest results it can show.

A running task and a finished report draw **one** box over both slots — the same rows in the
same place, so the screen does not change shape when work starts or ends. Destination screens
(the system screen, the link panel, the forms) keep the whole body: their content is the page,
and they take over the frame instead of listing entries in it.

## Program flow

```mermaid
graph TD
    A["main.go: parse argv"] --> N["core run|check|version: sbcore"]
    A --> B["i18n.Parse language"]
    B --> C["tui.New(version, lang)"]
    C --> D["tea.NewProgram alt-screen"]
    A --> E["--apply-firewall: firewall.Apply + WriteUnit"]
    A --> F["--render: print Snapshot then exit"]
    A --> G["--version: print version line"]
    A --> H["--serve: subd.Options.Run (HTTP + accounting)"]
    A --> I["--unlock: unlock report on stdout"]
```

`core` is the only subcommand: `core run -c <config>` is the node the service
unit starts, `core check` validates a configuration with the same engine, and
`core version` prints the sing-box release this binary carries.

The TUI is a tree of `menu` and `node` values (`internal/tui/menu.go`). Leaves
carry an `actionFunc`; branches carry a `sub *menu`. Actions call the domain
packages and report back through the app's log/progress channel.

## Deploy path

1. `internal/user` loads the accounts and generates the credentials every enabled
   protocol needs.
2. `internal/cert` resolves or issues a certificate.
3. `internal/config` renders `/etc/sing-box/config.json` from the node state, the
   accounts that may be live and the templates. The `experimental.v2ray_api` block
   is included only when `sbcore.StatsCapable()` says this build carries the API.
4. `internal/sbcore` accepts or refuses the rendered document: the same engine that
   would serve it builds it and closes it again, so a config the node cannot start
   never reaches the service.
5. `internal/service` installs and starts the `sing-box.service` unit, which runs
   this panel in node mode (`easysb core run -c …`).
6. The operator installs `easysb.service` from `订阅管理`; `easysb --serve`
   answers subscriptions and accounts traffic.

State is written after each successful step, so a partial deployment can be
resumed.

## Subscription endpoints

`internal/subd` serves one endpoint, `/sub/<token>`, from the built-in service
(`easysb --serve`); `internal/nginx` is gone. The token belongs to one account
and the response format is negotiated from the User-Agent, so a client never has
to choose between three addresses.

| Path | Body | Content type | Client |
| :--- | :--- | :--- | :--- |
| `/sub/<token>` | sing-box JSON profile | `application/json` | sing-box (SFM / SFA / SFI) |
| `/sub/<token>` | mihomo YAML profile | `text/yaml` | mihomo / Clash Meta, luci-app-nikki |
| `/sub/<token>` | Base64 share-link document | `text/plain` | v2rayN, passwall, passwall2, homeproxy |

The Base64 document is the universal format: every client above either reads the
Base64 share links directly or base64-decodes the document first.
`luci-app-nikki` runs the mihomo core and validates the subscription for a
top-level `proxies` key, so it consumes the mihomo profile.
Share links keep the canonical hyphenated UUID because homeproxy rejects the
32-character hyphen-less form through its LuCI `uuid` validation.

The service also reports usage in `Subscription-Userinfo`, so a client can show
"used up" or "expired" without parsing the profile, and refuses an account that
is disabled, expired or over quota with `403` and a plain-text reason instead of
serving a profile with no nodes in it.

`subscribe.ClientLink` builds the QR payload. sing-box wraps the URL in its
deep link (`sing-box://import-remote-profile?url=`) because that is what its
scanner expects. mihomo / Clash Meta and v2rayN get the plain URL: Clash-family
scanners pass the scanned text straight to their HTTP client, so a
`clash://install-config?url=` deep link would fail to fetch.
