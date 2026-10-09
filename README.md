<div align="center">

<img src="assets/easysb-banner-en.webp" alt="EasySB" width="950">

**5-in-1 sing-box deployment script · readable config templates · the core compiled in**

[![sing-box](https://img.shields.io/badge/sing--box-compiled%20in-3B82F6?style=for-the-badge&logo=go&logoColor=white)](https://sing-box.sagernet.org/)
![License](https://img.shields.io/badge/License-GPL--3.0-22C55E?style=for-the-badge)
[![Protocols](https://img.shields.io/badge/Protocols-5-8B5CF6?style=for-the-badge)](#supported-protocols)
[![Platform](https://img.shields.io/badge/Platform-Linux-F59E0B?style=for-the-badge)](#quick-start)

[简体中文](README_ZH.md) | **English**

<sub>AnyTLS · Hysteria2 · TUIC v5 · VMess + WebSocket + TLS · VLESS + Vision + Reality · Certificates · Subscription · Port hopping</sub>

</div>

---

## Table of Contents

- [Introduction](#introduction)
- [Repository Layout](#repository-layout)
- [Supported Protocols](#supported-protocols)
- [Quick Start](#quick-start)
- [Debian / Ubuntu Packages](#debian--ubuntu-packages)
- [Releases](#releases)
- [Capabilities](#capabilities)
- [Interactive Menu](#interactive-menu)
- [Command Line](#command-line)
- [Non-interactive Install](#non-interactive-install)
- [Config Templates](#config-templates)
- [Subscription](#subscription)
- [Firewall and Port Hopping](#firewall-and-port-hopping)
- [Toolbox](#toolbox)
- [Developers: Build and Test](#developers-build-and-test)
- [Security Notes](#security-notes)
- [License](#license)

---

## Introduction

EasySB is a 5-in-1 sing-box deployment tool for Linux VPS. It brings protocol deployment, certificate issuance, service unlock checks and subscription generation into one interactive menu.

- **Go (primary implementation)**: a root Go module built with bubbletea / bubbles / lipgloss, compiled into a single static binary exposed as `sb`.
- **Templates**: the separate [EasySB-Examples](https://github.com/EasySBTeam/EasySB-Examples) repository ships readable JSONC samples for the five protocols plus the subscription template. Use them on their own, or let the tool deploy them.
- **Core**: sing-box is **compiled into the panel** — `github.com/sagernet/sing-box` is a `go.mod` requirement, so installing EasySB installs the core with it, and the node is `easysb core run`. There is no core binary to download, replace or switch, and the traffic counters come with the build (`with_v2ray_api`, see `release/TAGS`).
- **Certificates**: Let's Encrypt through `go-acme/lego`, in the panel's own process. No acme.sh, no socat, nothing downloaded to issue a certificate.

- Homepage: https://github.com/EasySBTeam/EasySB
- Core source (compiled in): https://github.com/SagerNet/sing-box
- Changelog: [CHANGELOG.md](CHANGELOG.md)
- Contributing: [CONTRIBUTING.md](CONTRIBUTING.md)
- Security: [SECURITY.md](SECURITY.md)

---

## Repository Layout

```text
.
├── main.go                       # Go entrypoint (TUI)
├── install.sh                    # Installer (one command: add the signed apt source and install)
├── packaging/                    # Package lifecycle scripts (deb/) and the apt source builder (repo/)
├── VERSION                       # Single source of truth for the release tag
├── AGENTS.md                     # Guide for AI agents and contributors
├── go.mod                        # Go module definition
├── internal/                     # Go packages, mapped in docs/architecture.md
├── assets/                       # README banners
├── docs/                         # Engineering docs for agents and contributors
└── .github/                      # CI workflows and community health files
```

The protocol samples in [EasySB-Examples](https://github.com/EasySBTeam/EasySB-Examples) are readable JSONC; strip the comments and they work as sing-box server / client configs as-is.

---

## Supported Protocols

| Protocol | Transport | Default port | Highlights |
| :--- | :--- | :--- | :--- |
| AnyTLS | TCP + TLS | 8000 | Multi-stage Padding Scheme against traffic fingerprinting |
| Hysteria2 | QUIC / UDP | 8001 | Excellent on lossy networks, supports port hopping |
| TUIC v5 | QUIC / UDP | 8002 | 0-RTT handshake, `native` UDP relay, low latency |
| VLESS + Vision + Reality | TCP | 8003 | Certificate-free disguise, borrows `apple.com` by default |
| VMess + WebSocket + TLS | WS over TLS | 8004 | CDN and reverse-proxy friendly, standard TLS |

Each node is one protocol inbound with its own port: Enter takes the protocol default, `r` picks a random port, a number sets it manually. A port already used by another enabled node, or by the subscription service, is rejected and re-prompted. Every protocol except VLESS + Reality needs a domain that already resolves to this host plus a valid certificate. An account is then granted access to the nodes it selects, and every node change re-renders the config and restarts the core, so there is no separate deploy step.

---

## Quick Start

One command, the same shape as Docker's `get.docker.com`: the script checks the
machine's `/etc/os-release` against the releases EasySB ships for, installs the
signing key and the apt source, and installs through apt. The source is the GitHub
Release itself and covers Debian 12 and 13 and Ubuntu 24.04, for amd64 and arm64.

```bash
curl -fsSL https://github.com/EasySBTeam/EasySB/releases/latest/download/install.sh | sudo bash
```

The script also takes `--repo-url URL` to point at a mirror and `--lang E` to switch
the output to English.

The shortcut then opens the dark dashboard:

```bash
sb
```

Preset the language before entering the menu:

```bash
# Simplified Chinese
sb --language C

# English
sb --language E
```

Supports Debian 12+ / Ubuntu 24.04+ (systemd); run as root.

---

## Debian / Ubuntu Packages

Debian and Ubuntu get an apt repository for `apt install` and `apt upgrade`, and a `.deb` for `dpkg -i`.

The package carries the panel and the core together — the core is compiled into the binary — so installing it is the whole installation:

| Path | Contents |
| :--- | :--- |
| `/usr/bin/easysb` | The panel, with the sing-box core compiled in |
| `/usr/bin/sb` | Shortcut for `easysb` |
| `/usr/lib/systemd/system/sing-box.service` | Node unit: `easysb core run -c /etc/sing-box/config.json` |
| `/usr/lib/systemd/system/easysb.service` | Subscription service unit: `easysb --serve` |
| `/usr/share/licenses/easysb/LICENSE` | License text |

The units come from the binary itself (`sb --print-unit node|sub`), which is the same code the panel writes a unit from at runtime, so the packaged copy and the runtime copy cannot drift. The package deliberately does not enable or start either service: a fresh install has no node configuration yet, so run `sb`, configure the node, and the panel enables and starts the service.

### dpkg

Download the `.deb` for this host's architecture and install it:

```bash
# architectures: amd64, arm64
sudo dpkg -i easysb_6.0.0-1_amd64.deb
```

### apt repository

The apt index and the `.deb` files are attached to the GitHub Release, at the fixed
address `https://github.com/EasySBTeam/EasySB/releases/latest/download`, so one
sources entry covers every later version (the workflow keeps only the newest release,
so `latest` always resolves). The setup is caddy's: the armored key is dearmored to
`/usr/share/keyrings/easysb-archive-keyring.gpg`, the entry is one line in
`/etc/apt/sources.list.d/easysb.list` carrying `signed-by`, and apt installs the
package.

```bash
curl -fsSL https://github.com/EasySBTeam/EasySB/releases/latest/download/install.sh | sudo bash
```

The entry the installer writes is:

```text
deb [signed-by=/usr/share/keyrings/easysb-archive-keyring.gpg] https://github.com/EasySBTeam/EasySB/releases/latest/download ./
```

After that `sudo apt upgrade` keeps the panel current. The source is a flat apt
repository (every file in one directory, the `./` distribution), so there is no
per-distribution index. One package serves every supported release: it depends on
nothing but `ca-certificates`, so the version string carries no distribution
(`6.0.0-1`) and upgrading the distribution does not change which package apt pulls.

The index is signed with one key. Add the armored private key as the repository secret
`GPG_PRIVATE_KEY` and its passphrase as `GPG_PASSPHRASE`; the release workflow imports
it and signs apt's `Release` (`InRelease` and `Release.gpg`). The passphrase is read
from a file, so it never reaches a process list. The public key is attached as
`easysb-archive-keyring.asc` alongside the index. A run without `GPG_PRIVATE_KEY` fails
rather than attaching an unsigned index.

---

## Releases

Each release is tagged and named `v<VERSION>` and carries one updatable asset per
architecture, `easysb_<version>-1_<arch>.deb`:

| Asset (amd64) | Architecture |
| :--- | :--- |
| `easysb_6.0.0-1_amd64.deb` | `amd64` |
| `easysb_6.0.0-1_arm64.deb` | `arm64` |

Only the newest release is kept; the workflow prunes the previous one and its tag
after every publish. Besides the `.deb` files, the release carries a signed, flat apt
repository built from the same packages, so the one-command installer and `apt
upgrade` always have a fixed address to work from. Two repository secrets drive it:
`GPG_PRIVATE_KEY` and, when the key has one, `GPG_PASSPHRASE`.

---

## Capabilities

| Capability | Description |
| :--- | :--- |
| Nodes | One node is one protocol inbound with its own port and its own parameters (Reality SNI / keypair / short id, Hysteria2 hop range); the node store is the only source of what the host serves, so adding, editing or removing a node re-renders the config and restarts the core. Deleting a node also drops it from every account that selected it, and the panel reports how many were affected |
| Accounts and traffic | Per-account, per-node credentials, traffic quota, expiry date, node selection, enable switch, usage reset and token rotation; disabled, expired and over-quota accounts drop out of the core automatically |
| Service unlock status | Probes what this IP can really use: Netflix (including the originals-only case), Disney+, YouTube Premium, Amazon Prime Video, DAZN, TVBAnywhere+, Spotify, Reddit, TikTok, ChatGPT, Gemini, Claude, Steam, BiliBili (mainland / HK-Macau-Taiwan / Taiwan) and 巴哈姆特動畫瘋 — 17 services, one to three HTTP requests each, classified as unlocked / partially unlocked / blocked / failed with a reason. A probe that cannot read the answer reports a failure instead of claiming the service works |
| Core inside the panel | sing-box is a `go.mod` requirement, so the node is `easysb core run -c /etc/sing-box/config.json` and the version line reads the compiled-in release. `easysb core check` validates a configuration with the same engine the node uses, and the per-account counters exist when the build carries `with_v2ray_api` (`release/TAGS`), which the panel reports rather than assumes |
| Version panel | Program version and the sing-box release inside it, with whether this build can count traffic |
| Device panel | Local IPv4/IPv6, swap, uptime, CPU cores and load, memory, disk, host, kernel, OS and timezone |
| System info | The runtime the panel is running on, and the one place the look changes from inside the interface: `↑`/`↓` + `Enter` or `A`-`D` picks a skin, `T` flips dark/light, `I` swaps Unicode markers for ASCII. Every choice lands on the next frame, and the glyph preview row shows before a card anywhere else does whether the terminal font can draw the markers. The status strip and the hints stay put while the body swaps |
| Copy links | Subscription and share-link results render as a card grid inside the same fixed panel as the main menu. Subscription cards show the subscription name (sing-box / mihomo / Base64) plus a format note, share-link cards show the protocol name, and neither draws the host or the full URL. Select with `↑`/`↓`/`←`/`→` (or a number key), `Enter` copies the card, `C` copies all, `Q` quits the program, `Esc` returns. A copied card turns green and copy-all reports in the header. Narrow or short windows reflow the grid and truncate content, never overflowing the panel. On log screens `C` copies the log (OSC52) |
| Certificates | Let's Encrypt issuance in process through lego with the HTTP-01 standalone challenge: issue, list, switch active and remove. The preflight check covers DNS before an attempt is spent, the core is stopped to free port 80 during the challenge, and nothing is downloaded to do any of it. Renewal is decided by expiry (30 days before it) and driven by the panel's own systemd timer, which also reloads sing-box and the subscription service |
| Subscription | One URL per account (`/sub/<token>`) served by the built-in service, which picks the format from the client (the sing-box or mihomo subscription template, or Base64 share links) and reports usage in `Subscription-Userinfo`; QR codes and one share link per selected node in the panel |
| Port hopping | Hysteria2 defaults to `2080:3000`, auto-applies iptables / nftables DNAT and a boot restore unit |
| Service control | Start, stop, restart, status and enable-on-boot |
| BBR acceleration | Shows the running kernel, congestion control, queue discipline and installed kernels; enabling BBR loads `tcp_bbr`, writes `net.core.default_qdisc` and `net.ipv4.tcp_congestion_control` and persists them in `/etc/sysctl.d/99-easysb-bbr.conf` and `/etc/modules-load.d/easysb-bbr.conf` so the choice survives a reboot; installs a prebuilt BBRv3 kernel published by [Linux-BBR-v3](https://github.com/MinimaxFlora/Linux-BBR-v3) (standard or Max, x86_64 and arm64, downloaded straight from the release), or lists every published version to pick one from; the kernel and its settings can be removed from the panel again. Versions come from the kernel project itself — its version stamp and release list — so a kernel published there shows up here without a release of this panel |
| Self-update | Checks the published version against the one compiled into the panel and upgrades the `easysb` package through apt, then asks for a restart |
| Bilingual | Language picked on first screen, consistent Chinese and English throughout |

---

## Interactive Menu

```text
Main menu (one card, two columns, ten entries)
├── Service unlock      Check what this IP can use: streaming, AI, game stores, mainland-China and Taiwan catalogues
├── Node management      Add, edit, enable / disable, set the port and protocol parameters, and delete a node (deleting one with accounts assigned warns how many are affected)
├── Domain management    Issue (with preflight checks), renew now, renewal timer, list, switch active and remove certificates
├── Subscription         One account's URL, QR code and share links (pick the account first, then the panel prints the endpoint prefix); install / restart / status of the subscription service
├── Accounts             List, create, rename, remark, quota, expiry, node selection, enable / disable, usage reset, token rotation, delete
├── Service management   Start, stop, restart, status, enable / disable and port-hopping rules
├── System info          Runtime, and the one place the look changes from inside the interface: skin / palette / markers / language, terminal and host details
├── BBR                  Status (kernel, congestion control, queue discipline, installed kernels), enable BBR with fq / fq_codel / fq_pie / cake, install the standard or Max BBRv3 kernel, pick any published version from a list, remove it, clear the settings
├── Update version       Pull the latest EasySB release
└── Uninstall script     Remove EasySB completely
```

Files: server config `/etc/sing-box/config.json`, state `/etc/sing-box/easysb.conf`, nodes `/etc/sing-box/easysb-nodes.json`, accounts `/etc/sing-box/easysb-users.json`, shortcut `/usr/bin/sb`.

---

## Command Line

| Flag | Description |
| :--- | :--- |
| `--language C\|E` | Preset the UI language, then open the menu |
| `--icons symbols\|ascii` | Marker set: Unicode symbols (default), or ASCII when the terminal cannot render them (also `on`/`off`; borders still follow the skin) |
| `--theme auto\|dark\|light` | Override the terminal background detection (default `auto`) |
| `--skin jade\|aurora\|ember\|graphite` | Pick the interface skin, also `a`-`d` (default `jade`, env `EASYSB_SKIN`) |
| `--apply-firewall` | Restore port-hopping rules only, used by the boot unit |
| `--renew-certs` | Renew every certificate, reloading sing-box and the subscription service only when one was actually renewed (called by the renewal timer) |
| `--install-renew-timer` | Install the renewal timer (systemd timer); the unit names this binary's own path |
| `--remove-renew-timer` | Remove the renewal timer |
| `--render --width N --height N` | Render the dashboard once and exit (debug; add `--screen system` to draw a subpage) |
| `--serve` | Run the subscription service and the usage accounting loop (backs `easysb.service`) |
| `--provision FILE` | Deploy from a JSON manifest and exit, without the menu (`-` reads the manifest from stdin) |
| `--print-unit node\|sub` | Print a service unit body to stdout; the `.deb` is assembled from this exact text |
| `--unit-exec PATH` | Executable path `--print-unit` writes into the unit (default `/usr/bin/easysb`) |
| `--version` | Print the version and build hash |
| `--help` | Print usage |

### Headless Provisioning

`--provision` deploys a host from a manifest instead of the menu, so a script can install and configure a machine with no terminal. It writes the certificate, the node store, the account store, the rendered core config and the service units, then starts the services, and prints each account's subscription URL.

```json
{
  "domain": "sb.example.com",
  "email": "ops@example.com",
  "server_ip": "203.0.113.10",
  "sub_port": 8443,
  "nodes": [
    { "protocol": "anytls" },
    { "protocol": "vless-reality" },
    { "protocol": "hysteria2", "port": 8001, "hop_range": "2080:3000" },
    { "protocol": "tuic" }
  ],
  "accounts": [
    { "name": "alice", "quota_gb": 100, "expire_days": 30 },
    { "name": "bob", "nodes": ["anytls"], "password": "a-shared-secret" }
  ]
}
```

```bash
sudo sb --provision deploy.json
```

`nodes` defaults to every protocol at its default port; `name` defaults to the protocol's label. `accounts[].nodes` matches a node by protocol key or node name and defaults to every node; `quota_gb` 0 is unlimited and `expire_days` 0 never expires. `password` and `uuid` are applied to the protocols that use them. The manifest is a desired state: re-running it reuses the nodes it already finds and keeps each account's token and credentials, so a subscription URL never changes under a client that already imported it. Unknown keys are rejected rather than ignored.

The full example lives at `docs/provision.example.json`.

---

## Config Templates

The samples live in the separate [EasySB-Examples](https://github.com/EasySBTeam/EasySB-Examples) repository, one directory per protocol.

| Directory | Protocol | Transport | Disguise / encryption | Highlights |
| :--- | :--- | :--- | :--- | :--- |
| `Hysteria2/` | Hysteria 2 | QUIC / UDP | TLS (ALPN `h3`) | Port hopping, strong on lossy links |
| `VLESS-Vision-REALITY/` | VLESS + Vision | TCP | REALITY (no cert) | `xtls-rprx-vision`, active-probing resistant |
| `TUIC/` | TUIC | QUIC / UDP | TLS (ALPN `h3`) | 0-RTT handshake, `native` UDP relay |
| `AnyTLS/` | AnyTLS | TCP | TLS | Multi-stage Padding Scheme |
| `VMess-WebSocket-TLS/` | VMess | WebSocket over TLS | TLS | CDN friendly, Early Data |
| `Config/tun-fakeip.json` | TUN + FakeIP | System-wide | — | Rule routing, DNS split, URLTest |
| `Config/mihomo.yaml` | mihomo / Clash Meta | System-wide | — | Full client profile: proxies, groups, DNS, rules |

UUIDs, passwords, REALITY private keys and certificate paths in the templates are samples. Replace them before deployment and keep server and client in sync. Validate syntax with the core:

```bash
sing-box check -c VLESS-Vision-REALITY/config_server.json
```

---

## Subscription

Every account has one subscription URL, and the document behind it is rendered from the built-in sing-box subscription template (TUN + FakeIP) or the mihomo / Clash Meta profile — or a Base64 share-link document for everything else. Delivery:

1. The built-in subscription service (`easysb --serve`, installed as `easysb.service` from the panel) answers `/sub/<token>` on `SUB_SERVE_PORT` (default `8443`).
2. A terminal QR code per client format, scannable once `qrencode` is installed.
3. One share link per node the account selected.

The format is negotiated from the User-Agent, so one URL works everywhere:

| Client | Response |
| :--- | :--- |
| sing-box (SFM / SFA / SFI) | JSON profile |
| mihomo / Clash Meta / luci-app-nikki | Complete YAML profile |
| v2rayN / passwall / passwall2 / homeproxy | Base64 share-link document |

The Base64 document is the universal format. v2rayN imports it directly, and the OpenWrt proxy clients `passwall`, `passwall2` and `homeproxy` base64-decode the same document before parsing it. `luci-app-nikki` uses the mihomo core and validates a top-level `proxies` key, which the mihomo profile carries. `?client=singbox|mihomo|v2ray` overrides the detection.

Every share link keeps the canonical hyphenated UUID. `homeproxy` validates the node UUID with the LuCI `uuid` check and rejects the 32-character hyphen-less form, so the compact form must not be emitted.

The account token in the URL is the access secret. The core user name is `<token>@<node id>`, so each account's traffic is counted per node; the token and a node id are ASCII by construction and survive a rename, while a display name may not. Revoke for one person by rotating that account's token or disabling it; nobody else is affected. Renaming an account leaves its token and client imports alone.

Each response carries `Subscription-Userinfo: upload=<bytes>; download=<bytes>; total=<bytes>; expire=<unix seconds>`, which Clash Verge Rev, Clash Orbit and v2rayN display as remaining traffic and days. An account that is disabled, expired or over quota gets `403` with a plain-text reason instead of a profile with no nodes in it, and the core stops accepting its credentials on the next accounting cycle. Traffic is sampled every `SUB_SYNC_SECONDS` (default `300`).

The service terminates TLS itself when a real certificate is installed for the domain; otherwise it serves plain HTTP and the panel warns, because a subscription carries credentials. The sing-box QR payload is wrapped as `sing-box://import-remote-profile?url=...` for one-scan import; mihomo and v2rayN QR payloads are the plain subscription URL, because Clash-family scanners fetch the scanned text directly as a profile URL (the `clash://install-config?url=...` deep link only works when clicked from a browser). sing-box listens for WebSocket directly; the subscription service never proxies traffic.

The mihomo profile mirrors a full desktop setup: `external-controller` on `0.0.0.0:9090` with `secret`, the Zashboard web UI via `external-ui-url`, fake-ip DNS with `fake-ip-filter`, `load-balance` / `url-test` / `select` proxy groups, and `GEOSITE` / `GEOIP` rules. Import it only on machines you trust on your LAN.

---

## Firewall and Port Hopping

Hysteria2 port hopping applies standard NAT rules to a UDP port range:

```bash
# iptables
iptables -t nat -A PREROUTING -p udp --dport 2080:3000 -j REDIRECT --to-ports 8001

# nftables
nft add table ip nat
nft 'add chain ip nat prerouting { type nat hook prerouting priority dstnat; }'
nft add rule ip nat prerouting udp dport 2080-3000 redirect to :8001
```

NAT rules do not survive a reboot, so the script creates a boot restore unit:

- systemd: `easysb-firewall.service` (oneshot, starts before `sing-box.service`).

The unit restores rules via `easysb --apply-firewall`. It is not created when Hysteria2 port hopping is disabled.

---

## Toolbox

The toolbox (the first entry of the main menu, where 服务解锁状态 used to be) is where a host
is measured: one entry per measurement, one report per entry, and a board that remembers
what the last run of each entry found — stored in `/etc/sing-box/easysb-toolbox.json`, so it
outlives the panel. **Opening the section runs nothing** — a speed test, a
return route or a benchmark is not something a navigation key should start.

| Group | Entries |
| :--- | :--- |
| Unlock checks | Streaming (Netflix, Disney+, YouTube Premium, Prime Video, DAZN, TVBAnywhere+, Spotify, Reddit, TikTok), AI (ChatGPT, Gemini, Claude), regional (Steam, the three Bilibili catalogues, Bahamut Anime) |
| Network | Return routes to the Chinese carriers, a nearby speed test, a three-network speed test |
| IP and ports | IP quality (several databases plus DNS blocklists), mail ports (could this host run mail?) |
| Hardware and performance | System information, disks, CPU benchmark, memory test, sequential and random 4K disk IO, every mounted disk |

The interface uses one fixed layout: every page draws the same two boxes in the same rows,
sized from the main page, and a page with more content than its box holds is clipped with a
count of what was left out — **nothing scrolls**. A running task and a report use both boxes as
one, and a run that can count its steps shows a real percentage. The keys are one rule too:
**Q leaves the panel from any page**, Esc goes back, Enter only enters or confirms.

A verdict is one of three words — **unlocked / blocked / unknown** — beside the region the
service itself reported, one service per row:

- **unlocked**: the service answered, and its own answer says this address is served.
- **blocked**: the service refused this address, or offered it only partly (half-working is
  not working).
- **unknown**: the answer could not be read — a Cloudflare challenge, a timeout, a page with
  no verdict in it. The table says so and the note underneath says what it saw; it is never
  guessed into an "unlocked".
- The region column is **the service's own answer**, not something the panel computes: the
  services sit in front of different geolocation databases, and one machine being seen as two
  countries is normal (measured on a Zenixcloud host: Cloudflare, Netflix and Gemini said
  `US` while TikTok and DAZN reported `SC`).

Run one entry from the panel (工具箱 → group → entry), or without a terminal:

```bash
sb --tool list          # every entry
sb --tool backtrace     # return routes
sb --tool unlock-media  # streaming unlock
sb --unlock             # all seventeen unlock checks in one report
```

What was taken from 融合怪 ecs and what was left out, what each tool measures and where its
numbers come from — including why there is no geekbench or fio — is in
[docs/toolbox.md](docs/toolbox.md).

## The core inside the panel

| Item | Description |
| :--- | :--- |
| Source | `github.com/sagernet/sing-box` as a `go.mod` requirement (currently `v1.14.2`); installing the panel installs the core |
| Node | `ExecStart=<panel> core run -c /etc/sing-box/config.json`, where `<panel>` is `/usr/bin/easysb` from the `.deb`; `/etc/sing-box/sing-box` no longer exists |
| Validation | `easysb core check -c <config>` builds the configuration with the same engine that would serve it, which is what the deploy path runs before restarting |
| Counters | `with_v2ray_api` (`release/TAGS`) is compiled in, and the deploy path writes `experimental.v2ray_api` only when `sbcore.StatsCapable()` says so, because a core without the API rejects the whole document |
| Release | `.github/workflows/easysb-go-release.yml` reads the architecture list and every build flag from the `Makefile` (`make release-matrix` / `make packages-asset`, which read `release/TAGS`) and publishes one release, tagged and named `v<VERSION>`, then prunes the previous one |
| Packages | `make deb` wraps the same `dist/` binaries and the same staged tree with fpm, reading the arch names and unit text from one place (`ARCHES` / `DEBARCH_MAP` and `sb --print-unit`); `pkg-stage` UPX-compresses the binary, so the release asset and the source put down the same bytes |
| Sources | `make repo` lays the `.deb` files out as a flat apt repository (`Packages`, signed `Release`/`InRelease`, the keyring and `install.sh` all in one directory); `packaging/repo/index.sh` writes the indexes and signs them, and the release workflow attaches that directory to the release at `https://github.com/EasySBTeam/EasySB/releases/latest/download` |

---

## Developers: Build and Test

Go implementation (primary, requires Go 1.27.1; `go.mod` declares `go 1.27.1`, and `GOTOOLCHAIN=auto` fetches that toolchain automatically). `internal/tui/` holds the TUI shell and interaction logic; the other packages under `internal/` cover the compiled-in core, certificates, service, subscription, unlock probes and firewall modules, mapped in `docs/architecture.md`:

```bash
# Build, run the full pre-commit gate, or cross-compile every release architecture.
# `make` alone builds ./easysb; `make help` lists every target.
make
make check
make dist

# Package the .deb files, then lay out the signed flat apt repository the release serves
make deb
make repo

# Render the dashboard once without interaction (preview / screenshot / debug)
make render
```

`make check` is `gofmt -l` + `go vet` + the tagged tests. `make test-plain` runs the
untagged tests too, which is the build without per-account counters.

The bare Go commands still work; only the tags and the commit stamp differ:

```bash
# The build tags are defined once, in release/TAGS
tags=$(tr -d '[:space:]' < release/TAGS)

# Build the binary (the core and its counters come with it)
go build -tags "$tags" -o easysb .

# What this binary carries, and whether it can count traffic
./easysb core version

# Validate a rendered node configuration with the compiled-in engine
./easysb core check -c /etc/sing-box/config.json

# Print the service unlock report without a terminal
./easysb --unlock

# Switch language, icon mode, theme and skin
./easysb --language E --icons ascii --theme dark --skin graphite
```

---

## Security Notes

> UUIDs, passwords, REALITY private keys and certificate paths in this repository are samples. Using them in production is equivalent to having no protection.

- Regenerate every key and UUID before deployment, and keep server and client strictly in sync.
- A REALITY private key belongs on the server only. Never commit it to a public repository.
- Use a real domain and a valid certificate for certificate-based protocols, and tighten certificate file permissions to `600`.
- Follow local laws and use this project only in network environments you are authorized to operate.

Report security issues privately as described in [SECURITY.md](SECURITY.md) instead of opening a public issue.

---

## License

This project is licensed under **GPL-3.0**. See [LICENSE](LICENSE) for the full text.

Copyright (C) 2026 MinimaxFlora. Redistribution and modification must continue to follow GPL-3.0.

<div align="center">

**Built for sing-box · 5-in-1 deployment, straight from the menu.**

GPL-3.0 License © [MinimaxFlora](https://github.com/MinimaxFlora)

</div>
