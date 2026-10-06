# The core inside the panel

`github.com/sagernet/sing-box` is a requirement of this module. The core is not a file
any more: the version is a line in `go.mod`, the node is this binary started in node mode,
and nothing downloads, installs, unpacks or switches a core binary on the host.

```
go.mod                       github.com/sagernet/sing-box v1.14.2
internal/sbcore              the engine: run, check, version, build capability
/etc/systemd/system/sing-box.service
                             ExecStart=/usr/local/bin/easysb core run -c /etc/sing-box/config.json
easysb core run -c <config>  the node
easysb core check -c <config>  the same engine, building and closing the config
easysb core version          the release this binary carries, and its capability
```

`internal/sbcore.Run` builds the configuration with the library that would serve it,
starts the instance and blocks until the service manager ends the process; `Check` is the
same build without `Start`, which is what the deploy path runs before restarting the node.
Both go through `include.Context`, the registry set the upstream command line uses, so a
configuration the panel accepts is one `sing-box` itself accepts.

## The tag set, defined once

Build tags decide what the binary can express, so they are part of the product, not a
build detail. They live in `release/TAGS` — one line, read by the Makefile and by
`.github/workflows/easysb-go-release.yml`:

```
with_clash_api,with_quic,with_utls,with_v2ray_api,with_wireguard
```

The set is deliberately narrower than upstream's `release/DEFAULT_BUILD_TAGS`: the panel
carries what its node and the profiles it hands out actually name.

- `with_quic` for the Hysteria2 and TUIC inbounds and `with_utls` for the Reality
  inbound: without these the core refuses the whole node configuration.
- `with_v2ray_api` for the per-account byte counters. It is the one entry upstream's
  default does not carry either, and the panel cannot count traffic without it.
- `with_clash_api` for the Clash-compatible API block the subscription document names,
  and `with_wireguard` for a profile that names a WireGuard endpoint.

Upstream carries four more that the panel leaves out, because the node it runs names none
of them and they only cost binary size: `with_acme` (the panel's certificates come from
`internal/cert`, which talks to the CA itself), `with_dhcp`, `with_gvisor` and
`with_tailscale`. One consequence is worth knowing: the WireGuard userspace stack is
gVisor-backed, so `with_wireguard` alone keeps the endpoint type and the system stack,
while a userspace-stack endpoint reports `gVisor is not included in this build`. The
panel's own node never names WireGuard, so this only affects a hand-written profile.

`with_naive_outbound` stays out as well: it drags in the cronet/Chromium libraries, which
have no build for every release architecture (see `docs/pitfalls.md`). Every tag that is
carried was verified against the two release builds, `linux/amd64` and `linux/arm64`.

**`with_v2ray_api` is the tag that matters.** It is the only way sing-box counts bytes per
account, which is what the traffic columns of 账号与流量 read. A tag cannot be probed at run
time — it is compiled in or it is not — so the capability is a file pair rather than a
runtime check:

| File | Tag | `sbcore.StatsCapable()` |
| :-- | :-- | :-- |
| `internal/sbcore/stats_on.go` | `with_v2ray_api` | `true` |
| `internal/sbcore/stats_off.go` | `!with_v2ray_api` | `false` |

The deploy path asks it before writing `experimental.v2ray_api`: the core refuses a whole
configuration that names an API it does not have (`v2ray api is not included in this
build`), so a build without the tag must leave the block out. Such a build still deploys a
working node — the panel says so in the deploy log and the accounting loop skips sampling
instead of reporting a dead socket every interval. The release builds always carry the tag;
only a developer build without `-tags` does not.

**`with_quic` is the other tag that shows up as a sentence from the core.** Hysteria2 and
TUIC are QUIC protocols, so a build without that tag refuses any configuration carrying
one (`QUIC is not included in this build, rebuild with -tags with_quic`) — the deploy path
logs the core's own words and stops, which is the honest failure but not a working node.
`release/TAGS` carries it, so this only affects a hand-rolled build; `internal/deploy`'s
tests are split the same way, with the five-protocol document behind `with_quic` and a
document that every build accepts (`deploy_test.go`), alongside the tagged acceptance test
(`deploy_release_test.go`).

## What an operator sees

There is no 内核管理 section: a panel cannot manage a core that ships inside it. The
version lives where the panel's own version lives (the system card and the version line on
every page), and it reads

```
1.14.2 · 带流量统计
```

where `带流量统计` / `无流量统计` is `StatsCapable()` — the same fact the deploy path uses,
so the label cannot disagree with what the node does. The menu entry that used to open
内核管理 now opens 工具箱, the measurement toolbox, which answers a question a VPS operator
actually has: which services this IP can use, how the routes look, and how fast the box is.

`internal/sbcore.Version` prefers the release stamp
(`-X github.com/sagernet/sing-box/constant.Version=…`) and falls back to the module
version of the requirement, which is why a local build reports the same number as the
released one.
