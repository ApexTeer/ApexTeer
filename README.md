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
- [RPM and pacman Packages](#rpm-and-pacman-packages)
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
- **Templates**: `templates/` ships readable JSONC samples for the five protocols plus the subscription template. Use them on their own, or let the tool deploy them.
- **Core**: sing-box is **compiled into the panel** — `github.com/sagernet/sing-box` is a `go.mod` requirement, so installing EasySB installs the core with it, and the node is `easysb core run`. There is no core binary to download, replace or switch, and the traffic counters come with the build (`with_v2ray_api`, see `release/TAGS`).
- **Certificates**: Let's Encrypt through `go-acme/lego`, in the panel's own process. No acme.sh, no socat, nothing downloaded to issue a certificate.

- Homepage: https://github.com/MinimaxFlora/EasySB
- Core source (compiled in): https://github.com/SagerNet/sing-box
- Changelog: [CHANGELOG.md](CHANGELOG.md)
- Contributing: [CONTRIBUTING.md](CONTRIBUTING.md)
- Security: [SECURITY.md](SECURITY.md)

---

## Repository Layout

```text
.
├── main.go                       # Go entrypoint (TUI)
├── install.sh                    # Installer (one-click / package source / manual package)
├── packaging/                    # Package lifecycle scripts (deb/, rpm/) and server/
├── VERSION                       # Single source of truth for the release tag
├── AGENTS.md                     # Guide for AI agents and contributors
├── go.mod                        # Go module definition
├── internal/                     # Go packages, mapped in docs/architecture.md
├── templates/                    # Subscription and protocol config templates
│   ├── config/
│   │   ├── tun-fakeip.json       # sing-box TUN + FakeIP subscription template
│   │   └── mihomo.yaml           # mihomo / Clash Meta profile (readable mirror)
│   ├── anytls/                   # AnyTLS client / server samples
│   ├── hysteria2/                # Hysteria2 client / server samples
│   ├── tuic/                     # TUIC client / server samples
│   ├── vmess-websocket-tls/      # VMess + WebSocket + TLS samples
│   └── vless-vision-reality/     # VLESS + Vision + Reality samples
├── assets/                       # README banners
├── docs/                         # Engineering docs for agents and contributors
└── .github/                      # CI workflows and community health files
```

The protocol samples under `templates/` are readable JSONC; strip the comments and they work as sing-box server / client configs as-is.

---

## Supported Protocols

| Protocol | Transport | Default port | Highlights |
| :--- | :--- | :--- | :--- |
| AnyTLS | TCP + TLS | 8000 | Multi-stage Padding Scheme against traffic fingerprinting |
| Hysteria2 | QUIC / UDP | 8001 | Excellent on lossy networks, supports port hopping |
| TUIC v5 | QUIC / UDP | 8002 | 0-RTT handshake, `native` UDP relay, low latency |
| VLESS + Vision + Reality | TCP | 8003 | Certificate-free disguise, borrows `apple.com` by default |
| VMess + WebSocket + TLS | WS over TLS | 8004 | CDN and reverse-proxy friendly, standard TLS |

Ports are prompted one by one: Enter takes the default, `r` picks a random port, a number sets it manually. Conflicts with another protocol are rejected and re-prompted. Every protocol except VLESS + Reality requires a domain that already resolves to this host plus a valid certificate.

---

## Quick Start

Three ways in, all from the same `install.sh`.

One-click: detects the system and architecture, fills in the runtime dependencies and prefers the release tarball, falling back to a source build.

```bash
bash <(curl -fsSL https://raw.githubusercontent.com/MinimaxFlora/EasySB/master/install.sh)
```

From the package source: adds the apt / rpm / pacman entry for this system and installs through the OS package manager.

```bash
bash <(curl -fsSL https://raw.githubusercontent.com/MinimaxFlora/EasySB/master/install.sh) --method repo
```

From a package file you downloaded yourself:

```bash
bash install.sh --method package --package ./easysb_5.0.0_linux_amd64.deb
```

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

Supports Debian / Ubuntu (systemd) and Alpine (OpenRC); run as root.

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
# architectures: amd64, arm64, armhf, i386, riscv64, s390x
sudo dpkg -i easysb_5.0.0_linux_amd64.deb

# or let the installer fetch and install the same file
bash install.sh --method package --package ./easysb_5.0.0_linux_amd64.deb
```

### apt repository

The apt index and the `.deb` files are served from `https://sb.kejizero.xyz/apt`, a fixed address, so one sources entry covers every later version. The index is signed, and the public key is served next to it as `easysb.gpg`:

```bash
# Trust the key the repository is signed with
sudo install -d -m 0755 /etc/apt/keyrings
curl -fsSL https://sb.kejizero.xyz/apt/easysb.gpg | sudo tee /etc/apt/keyrings/easysb.gpg >/dev/null

sudo tee /etc/apt/sources.list.d/easysb.sources >/dev/null <<'EOF'
Types: deb
URIs: https://sb.kejizero.xyz/apt
Suites: ./
Signed-By: /etc/apt/keyrings/easysb.gpg
EOF

sudo apt-get update
sudo apt-get install easysb
```

`install.sh --method repo` writes exactly those two files for you and runs the install. If the index is ever published unsigned, the entry to use is `Trusted: yes` in place of `Signed-By:`.

To publish a signed index, add an armored private key as the repository secret `GPG_PRIVATE_KEY` and its passphrase as `GPG_PASSPHRASE`. The release workflow imports the key, signs `Release` with it and publishes `InRelease`, `Release.gpg` and the public key as `easysb.gpg`. The passphrase is read from a file, so it never reaches a process list. Without the secret the index is published unsigned, and the `Trusted: yes` form is the one to use.

---

## RPM and pacman Packages

The same release also carries an `.rpm` for Fedora, RHEL and openSUSE, and a pacman package for Arch. Both wrap the identical binary, the identical units and the same staged tree as the `.deb`, so all three formats agree with each other and with the runtime.

Both also come from the release server as real repositories, an rpm-md tree and a pacman database, so `install.sh --method repo` adds the source for this system and installs through it:

```bash
# Fedora / RHEL / openSUSE
sudo tee /etc/yum.repos.d/easysb.repo >/dev/null <<'EOF'
[easysb]
name=EasySB
baseurl=https://sb.kejizero.xyz/rpm/$basearch
enabled=1
type=rpm-md
EOF
sudo dnf install easysb

# Arch
sudo tee -a /etc/pacman.conf >/dev/null <<'EOF'

[easysb]
SigLevel = Optional TrustAll
Server = https://sb.kejizero.xyz/pacman/$arch
EOF
sudo pacman -Sy && sudo pacman -S easysb
```

Single files are still on the release page if you prefer to install by hand:

```bash
# Fedora / RHEL (dnf installs the dependencies too)
sudo dnf install https://github.com/MinimaxFlora/EasySB/releases/download/v5.0.0/easysb_5.0.0_linux_x86_64.rpm

# openSUSE
sudo zypper install https://github.com/MinimaxFlora/EasySB/releases/download/v5.0.0/easysb_5.0.0_linux_x86_64.rpm

# Arch
sudo pacman -U https://github.com/MinimaxFlora/EasySB/releases/download/v5.0.0/easysb_5.0.0_linux_x86_64.pkg.tar.zst
```

Every asset carries the release version and its architecture, in the shape sing-box uses:

| Format | Asset (amd64) |
| :--- | :--- |
| Release tarball | `easysb-5.0.0-linux-amd64.tar.gz` |
| Debian | `easysb_5.0.0_linux_amd64.deb` |
| RPM | `easysb_5.0.0_linux_x86_64.rpm` |
| pacman | `easysb_5.0.0_linux_x86_64.pkg.tar.zst` |

The architecture names follow each ecosystem's own spelling, not the Go ones:

| Go `GOARCH` | `.deb` (`DEBARCH_*`) | `.rpm` (`RPMARCH_*`) | pacman (`PACMANARCH_*`) |
| :--- | :--- | :--- | :--- |
| `amd64` | `amd64` | `x86_64` | `x86_64` |
| `arm64` | `arm64` | `aarch64` | `aarch64` |
| `armv7` | `armhf` | `armv7hl` | `armv7h` |
| `386` | `i386` | `i686` | — (Arch has no i386) |
| `riscv64` | `riscv64` | `riscv64` | `riscv64` |
| `s390x` | `s390x` | `s390x` | — (Arch has no s390x) |

Like the `.deb`, these packages install the files and refresh the systemd unit cache, and leave enabling and starting to the panel once a node is configured.

### Release server

The fixed URLs apt, rpm and pacman need are served by one host, `sb.kejizero.xyz`. Prepare it once from the Actions tab: run the **Provision the release server** workflow, which installs caddy for HTTPS, creates the site tree, and installs vsftpd with one account confined to that tree. After that, every release run syncs `dist/repo` there with FTP-Deploy-Action, over FTPS, and the sources stay current.

The setup and the release runs need six repository secrets:

| Secret | Used by | What it is |
| :--- | :--- | :--- |
| `GPG_PRIVATE_KEY` | release | the armored signing key, optional |
| `GPG_PASSPHRASE` | release | that key's passphrase, only when it has one |
| `FTP_PASSWORD` | release | the password of the server's upload account |
| `SERVER_SSH_PASSWORD` | provision | the server's root password, only for the one-time setup |
| `SERVER_HOST` | release + provision | the release server's address, shared by both workflows |
| `SERVER_USER` | provision | the account the provisioning run logs in as |

The server is prepared once, so `SERVER_SSH_PASSWORD` is only needed for the provisioning run; the release runs use `FTP_PASSWORD` alone.

`SERVER_HOST` and `SERVER_USER` keep that address and login out of the repository, so moving
to another host is a settings change rather than a commit. Both are required: a missing value
fails the run instead of falling back to a default host.

---

## Capabilities

| Capability | Description |
| :--- | :--- |
| 5-in-1 deployment | Ports allocated one by one; the node keeps only what no account owns (the Reality keypair), because credentials belong to accounts |
| Accounts and traffic | Per-account credentials for every protocol, traffic quota, expiry date, protocol selection, enable switch, usage reset and token rotation; disabled, expired and over-quota accounts drop out of the core automatically |
| Service unlock status | Probes what this IP can really use: Netflix (including the originals-only case), Disney+, YouTube Premium, Amazon Prime Video, DAZN, TVBAnywhere+, Spotify, Reddit, TikTok, ChatGPT, Gemini, Claude, Steam, BiliBili (mainland / HK-Macau-Taiwan / Taiwan) and 巴哈姆特動畫瘋 — 17 services, one to three HTTP requests each, classified as unlocked / partially unlocked / blocked / failed with a reason. A probe that cannot read the answer reports a failure instead of claiming the service works |
| Core inside the panel | sing-box is a `go.mod` requirement, so the node is `easysb core run -c /etc/sing-box/config.json` and the version line reads the compiled-in release. `easysb core check` validates a configuration with the same engine the node uses, and the per-account counters exist when the build carries `with_v2ray_api` (`release/TAGS`), which the panel reports rather than assumes |
| Version panel | Program version and the sing-box release inside it, with whether this build can count traffic |
| Device panel | Local IPv4/IPv6, swap, uptime, CPU cores and load, memory, disk, host, kernel, OS and timezone |
| System info | The runtime the panel is running on, and the one place the look changes from inside the interface: `↑`/`↓` + `Enter` or `A`-`D` picks a skin, `T` flips dark/light, `I` swaps Unicode markers for ASCII. Every choice lands on the next frame, and the glyph preview row shows before a card anywhere else does whether the terminal font can draw the markers. The status strip and the hints stay put while the body swaps |
| Copy links | Subscription and share-link results render as a card grid inside the same fixed panel as the main menu. Subscription cards show the subscription name (sing-box / mihomo / Base64) plus a format note, share-link cards show the protocol name, and neither draws the host or the full URL. Select with `↑`/`↓`/`←`/`→` (or a number key), `Enter` copies the card, `C` copies all, `Q` quits the program, `Esc` returns. A copied card turns green and copy-all reports in the header. Narrow or short windows reflow the grid and truncate content, never overflowing the panel. On log screens `C` copies the log (OSC52) |
| Certificates | Let's Encrypt issuance in process through lego with the HTTP-01 standalone challenge: issue, list, switch active and remove. The preflight check covers DNS before an attempt is spent, the core is stopped to free port 80 during the challenge, and nothing is downloaded to do any of it. Renewal is decided by expiry (30 days before it) and driven by the panel's own systemd timer (or OpenRC script), which also reloads sing-box and the subscription service |
| Subscription | One URL per account (`/sub/<token>`) served by the built-in service, which picks the format from the client (`templates/config/tun-fakeip.json`, `templates/config/mihomo.yaml` or Base64 share links) and reports usage in `Subscription-Userinfo`; QR codes and per-protocol share links in the panel |
| Port hopping | Hysteria2 defaults to `2080:3000`, auto-applies iptables / nftables DNAT and a boot restore unit |
| Service control | Start, stop, restart, status and enable-on-boot |
| BBR acceleration | Shows the running kernel, congestion control, queue discipline and installed kernels; enabling BBR loads `tcp_bbr`, writes `net.core.default_qdisc` and `net.ipv4.tcp_congestion_control` and persists them in `/etc/sysctl.d/99-easysb-bbr.conf` and `/etc/modules-load.d/easysb-bbr.conf` so the choice survives a reboot; installs a prebuilt BBRv3 kernel published by [Linux-BBR-v3](https://github.com/MinimaxFlora/Linux-BBR-v3) (standard or Max, x86_64 and arm64, downloaded straight from the release), or lists every published version to pick one from; the kernel and its settings can be removed from the panel again. Versions come from the kernel project itself — its version stamp and release list — so a kernel published there shows up here without a release of this panel |
| Self-update | Pulls the latest script from this repository and replaces it after validation |
| Bilingual | Language picked on first screen, consistent Chinese and English throughout |

---

## Interactive Menu

```text
Main menu (one card, two columns, ten entries)
├── Service unlock      Check what this IP can use: streaming, AI, game stores, mainland-China and Taiwan catalogues
├── Node management      One-click deploy, enable protocols, parameters (UUID / password / hop / ports / SNI / Reality keys)
├── Domain management    Issue (with preflight checks), renew now, renewal timer, list, switch active and remove certificates
├── Subscription         One account's URL, QR code and share links (pick the account first, then the panel prints the endpoint prefix); install / restart / status of the subscription service
├── Accounts             List, create, rename, remark, quota, expiry, protocol selection, enable / disable, usage reset, token rotation, delete
├── Service management   Start, stop, restart, status, enable / disable and port-hopping rules
├── System info          Runtime, and the one place the look changes from inside the interface: skin / palette / markers / language, terminal and host details
├── BBR                  Status (kernel, congestion control, queue discipline, installed kernels), enable BBR with fq / fq_codel / fq_pie / cake, install the standard or Max BBRv3 kernel, pick any published version from a list, remove it, clear the settings
├── Update version       Pull the latest EasySB release
└── Uninstall script     Remove EasySB completely
```

Files: server config `/etc/sing-box/config.json`, state `/etc/sing-box/easysb.conf`, accounts `/etc/sing-box/easysb-users.json`, shortcut `/usr/local/bin/sb`.

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
| `--install-renew-timer` | Install the renewal timer (systemd timer / OpenRC); the unit names this binary's own path |
| `--remove-renew-timer` | Remove the renewal timer |
| `--render --width N --height N` | Render the dashboard once and exit (debug; add `--screen system` to draw a subpage) |
| `--serve` | Run the subscription service and the usage accounting loop (backs `easysb.service`) |
| `--print-unit node\|sub` | Print a service unit body to stdout; the `.deb` is assembled from this exact text |
| `--unit-exec PATH` | Executable path `--print-unit` writes into the unit (default `/usr/bin/easysb`) |
| `--version` | Print the version and build hash |
| `--help` | Print usage |

---

## Config Templates

| Directory | Protocol | Transport | Disguise / encryption | Highlights |
| :--- | :--- | :--- | :--- | :--- |
| `templates/anytls/` | AnyTLS | TCP | TLS | Multi-stage Padding Scheme |
| `templates/hysteria2/` | Hysteria 2 | QUIC / UDP | TLS (ALPN `h3`) | Port hopping, strong on lossy links |
| `templates/tuic/` | TUIC | QUIC / UDP | TLS (ALPN `h3`) | 0-RTT handshake, `native` UDP relay |
| `templates/vmess-websocket-tls/` | VMess | WebSocket over TLS | TLS | CDN friendly, Early Data |
| `templates/vless-vision-reality/` | VLESS + Vision | TCP | REALITY (no cert) | `xtls-rprx-vision`, active-probing resistant |
| `templates/config/tun-fakeip.json` | TUN + FakeIP | System-wide | — | Rule routing, DNS split, URLTest |
| `templates/config/mihomo.yaml` | mihomo / Clash Meta | System-wide | — | Full client profile: proxies, groups, DNS, rules |

UUIDs, passwords, REALITY private keys and certificate paths in the templates are samples. Replace them before deployment and keep server and client in sync. Validate syntax with the core:

```bash
sing-box check -c templates/vless-vision-reality/config_server.json
```

---

## Subscription

Every account has one subscription URL, and the document behind it is rendered from `templates/config/tun-fakeip.json` (sing-box) or `templates/config/mihomo.yaml` (mihomo / Clash Meta) — or a Base64 share-link document for everything else. Delivery:

1. The built-in subscription service (`easysb --serve`, installed as `easysb.service` from the panel) answers `/sub/<token>` on `SUB_SERVE_PORT` (default `8443`).
2. A terminal QR code per client format, scannable once `qrencode` is installed.
3. Five per-protocol share links per account.

The format is negotiated from the User-Agent, so one URL works everywhere:

| Client | Response |
| :--- | :--- |
| sing-box (SFM / SFA / SFI) | JSON profile |
| mihomo / Clash Meta / luci-app-nikki | Complete YAML profile |
| v2rayN / passwall / passwall2 / homeproxy | Base64 share-link document |

The Base64 document is the universal format. v2rayN imports it directly, and the OpenWrt proxy clients `passwall`, `passwall2` and `homeproxy` base64-decode the same document before parsing it. `luci-app-nikki` uses the mihomo core and validates a top-level `proxies` key, which the mihomo profile carries. `?client=singbox|mihomo|v2ray` overrides the detection.

Every share link keeps the canonical hyphenated UUID. `homeproxy` validates the node UUID with the LuCI `uuid` check and rejects the 32-character hyphen-less form, so the compact form must not be emitted.

The account token in the URL is the access secret, and it is also the core user name the usage counters are keyed by. Revoke for one person by rotating that account's token or disabling it; nobody else is affected. Renaming an account leaves its token and client imports alone.

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
- OpenRC: `/etc/init.d/easysb-firewall`.

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
| Node | `ExecStart=<panel> core run -c /etc/sing-box/config.json`, where `<panel>` is `/usr/local/bin/easysb` from `install.sh` or `/usr/bin/easysb` from the `.deb`; `/etc/sing-box/sing-box` no longer exists |
| Validation | `easysb core check -c <config>` builds the configuration with the same engine that would serve it, which is what the deploy path runs before restarting |
| Counters | `with_v2ray_api` (`release/TAGS`) is compiled in, and the deploy path writes `experimental.v2ray_api` only when `sbcore.StatsCapable()` says so, because a core without the API rejects the whole document |
| Release | `.github/workflows/easysb-go-release.yml` reads the architecture list and every build flag from the `Makefile` (`make release-matrix` / `make tarball-asset`, which read `release/TAGS`) and publishes one release, tagged and named `v<VERSION>` |
| Packages | `make deb`, `make rpm` and `make pacman` wrap the same `dist/` binaries and the same staged tree with fpm, reading the arch names and unit text from one place (`DEBARCH_*` / `RPMARCH_*` / `PACMANARCH_*` and `sb --print-unit`) |
| Sources | `make repo` lays the same files out as apt / rpm / pacman / bin trees and the release workflow syncs them to the release server with FTP-Deploy-Action; `packaging/server/` holds the one-shot provisioning script and the Caddy browse template the landing page is rendered from, so the site root is the directory listing plus the install commands |

---

## Developers: Build and Test

Go implementation (primary, requires Go 1.27.1; `go.mod` declares `go 1.27.1`, and `GOTOOLCHAIN=auto` fetches that toolchain automatically). `internal/tui/` holds the TUI shell and interaction logic; the other packages under `internal/` cover the compiled-in core, certificates, service, subscription, unlock probes and firewall modules, mapped in `docs/architecture.md`:

```bash
# Build, run the full pre-commit gate, or cross-compile every release architecture.
# `make` alone builds ./easysb; `make help` lists every target.
make
make check
make dist

# Package the .deb / .rpm / pacman files, then lay out the apt / rpm / pacman / bin
# sources the release server serves
make deb
make rpm
make pacman
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
