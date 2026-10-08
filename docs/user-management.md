# User Management and the Subscription Service

This document records the v4 design that replaces the node-wide credential and
the static nginx subscription site with a per-user model served by a built-in
HTTP service. It is the reference for the implementation branch
`feature/user-management`.

v6, recorded here too, replaces the implicit single node with an explicit node
list: one node is one protocol inbound with its own port and protocol
parameters, and an account selects nodes rather than protocols. The account
store becomes version 2 and the legacy one is migrated in place; the sections
below carry the v6 shape.

## Problem

Until v3 every deployment had exactly one identity: a single node `UUID` and
`PASSWORD`, written into the `users` array of every enabled inbound
(`internal/config/config.go`). Subscriptions were three files rendered once
(`subscribe.json`, `mihomo.yaml`, `v2ray.txt`) and published by nginx through
exact-match locations keyed by the node UUID
(`internal/nginx/nginx.go`). That model cannot express:

- per-person traffic quotas or expiry dates,
- revoking one person without rotating the credential for everybody,
- traffic information in the subscription, the way an airport (机场) link
  carries it,
- one subscription URL that adapts to the requesting client instead of three
  addresses the user has to choose between.

## Scope

In scope: per-user credentials, quotas, expiry, protocol selection, one
adaptive subscription URL, a built-in subscription service, usage accounting
with automatic suspension, and the TUI that manages all of it.

Out of scope, deliberately:

- **Per-user speed limits and device/IP limits.** sing-box has no per-user rate
  control for these protocols, and `common/trafficcontrol` is a connection
  tracker for the Clash API, not a limiter. hysteria2 and tuic expose only
  node-wide `up_mbps` / `down_mbps`.
- **A web panel.** The bubbletea TUI stays the management surface; the new HTTP
  service serves subscriptions only.
- **Migration from a v3 deployment.** A fresh deployment is assumed; leftover
  nginx fragments or static subscription files are not removed automatically.

## Decisions

| # | Decision | Rationale | Rejected |
| :-- | :--- | :--- | :--- |
| D1 | Every user owns **independent credentials per node**. | Isolation is real: revoking one user does not touch the others, and two nodes that run the same protocol still hold different secrets. | One credential set reused across nodes (simpler UI, but one node's leak exposes the rest); a node-wide credential (no isolation at all). |
| D2 | Subscriptions are served by a **built-in HTTP service**, and `internal/nginx` is deleted. | The endpoint must branch on User-Agent, emit per-user response headers, and count traffic; static files cannot. Removing nginx also removes a package install, a config fragment and its reload path. | nginx statics plus a sync timer (a reload per cycle, files per user per format, no real UA negotiation); keeping nginx in front of the service (a second moving part for no benefit). |
| D3 | The service **terminates TLS itself**, reusing `cert.ResolveActive`. | The certificate paths the deploy path already resolves are enough; no proxy layer. | nginx terminating TLS; a loopback-only listener that expects an external reverse proxy (no longer one-click). |
| D4 | One subscription URL per user: `/sub/<token>`, content negotiated from the User-Agent. | Users cannot pick the wrong link; the same URL works in sing-box, mihomo, Clash Orbit and v2rayN. | Per-client paths (`/sub/<token>/mihomo`) as the primary form. |
| D5 | The URL token is a **random short id**, unrelated to the user's UUIDs. | Rotation and revocation are independent of the credentials inside the document. | Reusing the VLESS UUID as the token. |
| D6 | Quota and expiry are enforced by **rewriting `config.json` and restarting the core**. | sing-box exposes `StatsService` only; there is no runtime handler to add or remove users. | Leaving exceeded users in the config (they keep working). |
| D7 | Usage is read from **`experimental.v2ray_api` StatsService over gRPC**. | The only supported per-user counter source; `stats.users` is the documented whitelist. | Scraping core logs; Clash API connection tracking (no cumulative totals). |
| D8 | Quota resets on the **natural month**; expiry **disables** the user. | Matches the monthly plan habit; data is kept so renewing restores access. | Rolling N-day windows; deleting expired users. |
| D9 | A disabled, expired or over-quota user gets **403 with a plain-text reason**, and still receives the `Subscription-Userinfo` header. | The client can show "used up" or "expired" while never receiving a half-valid profile (an empty `proxies` list is rejected by Clash-family parsers and an empty sing-box profile fails to parse). | Serving an empty node list; serving the full profile and letting connections time out. |
| D10 | Client node names are `EasySB-<user name>-<node name>`. | Distinguishes users and nodes inside a client now that one account may carry several inbounds. | A fixed `EasySB` name; `EasySB-<user name>-<protocol>` (two nodes of one protocol would collide). |
| D11 | `订阅管理` keeps the client-facing side (endpoint prefix, service unit, "show one account's subscription") and a new top-level entry `账号与流量` owns the account lifecycle. | The old entry was about subscription files, but the new feature is two things: a service the operator restarts and a per-account lifecycle. One merged screen would carry fifteen rows and mix node-level with account-level actions. | Renaming `订阅管理` to `用户管理` and moving everything into it (one overloaded screen, and the endpoint actions lose their obvious home). |
| D12 | New state keys replace the old ones: `SUB_SERVE_PORT`, `SUB_SYNC_SECONDS` are added, `SUB_PORT` and `SUB_PATH` are removed. | The old keys described an nginx static site; keeping unread keys in the file is dead weight. | Keeping them for legacy readers. |

## Data model

### State keys

`/etc/sing-box/easysb.conf` carries only the host-wide settings:

| Key | Default | Meaning |
| :--- | :--- | :--- |
| `SUB_SERVE_PORT` | `8443` | Port the built-in subscription service listens on. |
| `SUB_SYNC_SECONDS` | `300` | Usage accounting interval, in seconds. |

`SUB_PORT` and `SUB_PATH` are dropped. `DOMAIN`, `CERT_DOMAIN`, `SERVER_IP` and
`NODE_DEPLOYED` keep their meaning. The per-protocol keys (`IS_*`, `PORT_*`,
`HY2_HOP_RANGE`, `REALITY_*`) were the v5 single-node model; v6 reads them once
to migrate the deployment into the node store and then drops them on the next
save, because `internal/node` owns what the host serves.

### Node store

`/etc/sing-box/easysb-nodes.json`, mode `0600`, written atomically like the other
stores:

```json
{
  "version": 1,
  "nodes": [
    {
      "id": "k7m2p9q4rt3xz8",
      "name": "Tokyo",
      "protocol": "vless-reality",
      "port": 8003,
      "enabled": true,
      "params": {
        "reality_sni": "apple.com",
        "reality_private": "…",
        "reality_public": "…",
        "reality_short_id": "abcd1234"
      },
      "created_at": "2026-10-08T11:00:00Z"
    }
  ]
}
```

- One node is exactly one protocol inbound; selecting several protocols for one
  account creates one node each.
- `id` is random and stable, so renaming a node never breaks an account selection
  or a stored credential.
- Protocol parameters live on the node (`reality_sni`, `reality_private`,
  `reality_public`, `reality_short_id`, `hop_range`); the domain, certificate and
  subscription port stay host-wide.
- Node names are unique and enabled node ports may not collide with each other or
  with `SUB_SERVE_PORT`.

### User store

`/etc/sing-box/easysb-users.json`, mode `0600`, written atomically
(`*.tmp` plus rename) like the state file:

```json
{
  "version": 2,
  "users": [
    {
      "name": "alice",
      "remark": "phone",
      "token": "t9x2m4k8q1w7",
      "enabled": true,
      "nodes": ["k7m2p9q4rt3xz8", "9wq3zv5t2n8m4r"],
      "credentials": {
        "k7m2p9q4rt3xz8": { "protocol": "vless-reality", "uuid": "…" },
        "9wq3zv5t2n8m4r": { "protocol": "hysteria2", "password": "…" }
      },
      "usage": {
        "k7m2p9q4rt3xz8": { "upload_bytes": 0, "download_bytes": 0 },
        "9wq3zv5t2n8m4r": { "upload_bytes": 0, "download_bytes": 0 }
      },
      "quota_bytes": 107374182400,
      "used_bytes": 0,
      "upload_bytes": 0,
      "download_bytes": 0,
      "created_at": "2026-09-24T11:00:00Z",
      "expire_at": "2026-10-24T00:00:00Z",
      "last_reset": "2026-09-01T00:00:00Z",
      "applied": true
    }
  ]
}
```

- `quota_bytes: 0` means unlimited, `expire_at` zero means never.
- `nodes` is the selection set: the node ids this account may use.
- `credentials` is keyed by **node id**, not protocol, because two nodes may run
  the same protocol with independent secrets. Each entry holds only the fields
  its protocol needs: `uuid` for `vless-reality` and `vmess-ws-tls`, `password`
  for `anytls` and `hysteria2`, both for `tuic`.
- `usage` is keyed by node id too, so traffic is attributed to the node it was
  spent on. The top-level `upload_bytes` / `download_bytes` / `used_bytes` are
  the sums and are what the quota and the `Subscription-Userinfo` header use.
- Credentials are generated when a node is selected and are **kept** when it is
  deselected, so re-selecting never invalidates a client import.
- `applied` records whether the user is currently present in the rendered core
  config. The accounting loop only rewrites the config and restarts the core on
  a transition, never on every cycle.
- Status is derived, never stored: `disabled` (admin switch off), `expired`
  (`expire_at` passed), `over-quota` (`used_bytes >= quota_bytes`), else
  `active`.

### Mapping a user onto sing-box

- The **core user name is `<token>@<node id>`**, not the display name. The V2Ray
  counter key is `user>>><name>>>traffic>>>…`, so the name is both a per-node
  aggregation key and a value that must survive being used as a `QueryStats`
  regex pattern. A random ASCII token and a random ASCII node id are unique by
  construction and stable across renames, while a display name may be Chinese or
  contain regex metacharacters. Neither part contains `@`, so the split is
  unambiguous.
- Every enabled node receives a `users` entry for each account that selected it,
  is enabled, is not expired and is not over quota. A different `@` suffix per
  node keeps the counters separate, which is what lets `usage` be per node.
- `experimental.v2ray_api` is added automatically when **this build** carries
  `with_v2ray_api` (`release/TAGS`; `easysb core version` prints what the binary
  has). The capability is a build tag rather than something on the host, so the
  deploy path asks `sbcore.StatsCapable()`, and a build without the tag must leave
  the block out — sing-box rejects the whole config for an API it was not built
  with. Such a deployment carries no stats block and the panel's status board
  says so: the node and its accounts work, usage is just not counted. The
  accounting loop asks the same question and skips its sampling instead of
  failing on a socket nothing listens on:

  ```json
  {
    "listen": "127.0.0.1:10085",
    "stats": { "enabled": true, "users": ["<token>@<node id>", "…"] }
  }
  ```

   Only names listed in `stats.users` are counted, so the list is rebuilt with
   the same predicate as the inbound `users` arrays and carries every
   `<token>@<node id>` pair that appears in one.

## Subscription service

`easysb --serve` runs the HTTP service and the accounting loop in one process,
installed as `easysb.service` (systemd) through `internal/service`.

| Request | Behaviour |
| :--- | :--- |
| `/sub/<token>` | Content selected by User-Agent, plus the response headers below. |
| unknown token | `404` |
| disabled / expired / over-quota token | `403` with a plain-text reason, headers still present |
| any other path | `404` |

User-Agent mapping (normalised to lowercase, first match wins):

| Match | Format | Content type |
| :--- | :--- | :--- |
| `sing-box`, `sfa`, `sfm`, `sfi` | sing-box JSON profile | `application/json` |
| `clash`, `mihomo`, `stash`, `meta` | mihomo YAML profile | `text/yaml; charset=utf-8` |
| anything else (`v2rayN`, `passwall`, `passwall2`, `homeproxy`, `quantumult x`, `loon`, browsers, `curl`, empty) | Base64 share-link document | `text/plain; charset=utf-8` |

The Clash row is deliberately short: every Clash-derived client sends a
User-Agent that already contains `clash` or `mihomo` (`Clash Verge Rev`,
`clash-verge`, `FlClash`, `ClashMeta`, `Clash Orbit`, `luci-app-nikki`), so the
extra markers v3 needed are not carried, and the Base64 fallback covers the
rest. `?client=singbox|mihomo|v2ray` overrides the guess.

Response headers on every `/sub/<token>` response:

```
Subscription-Userinfo: upload=<bytes>; download=<bytes>; total=<bytes>; expire=<unix seconds>
Content-Disposition: attachment; filename=EasySB; filename*=UTF-8''EasySB
Cache-Control: no-store
```

`total=0` means unlimited and `expire=0` never, which is what Clash Orbit,
Clash Verge Rev and v2rayN already understand. The document body stays
byte-compatible with what the v3 templates produced, so no client needs a new
parser. The download name is the product name, fixed for every account, and carries
no extension: a Clash client shows the file name as the profile's name, so it is the
one string a user reads, and the account token it used to carry is a credential that
has no reason to sit in a download folder. It is sent twice, as RFC 6266 allows,
because the Clash family (Clash Verge Rev, Clash Orbit) reads the header through a
Debug-formatted string and strips the surrounding quotes only: a quoted
`filename="EasySB"` reaches them as `\"EasySB\"` and becomes the profile's name, while
the `filename*=UTF-8''EasySB` they look at first survives intact.

`expire` is omitted entirely when the account never expires, because a client
reads `expire=0` as "already expired".

TLS: when `cert.Usable(DOMAIN)` finds a certificate the panel issued, the service listens
with that certificate; otherwise it serves plaintext HTTP and the panel warns,
because a plaintext document carries the user's credentials. `cert.Usable` is the
single predicate behind both the URL the panel prints and the certificate the
listener loads, so a client is never handed an `https://` address for a listener
that speaks HTTP. A self-signed pair does not count: clients reject it.

## Accounting and enforcement

1. Every `SUB_SYNC_SECONDS`, the loop calls
   `v2ray.core.app.stats.command.StatsService/QueryStats` with a pattern
   covering all `<token>@<node id>` names and `reset=true`, then adds each delta
   to the stored per-node counter. The pattern must quote the names, because a
   regular expression would split on `@` only by accident.
2. The counters live in the running core and **reset to zero when the core
   restarts**, so deltas — never absolute values — are persisted.
3. `last_reset` moving to a new month zeroes the per-node counters and the
   top-level `used_bytes`, `upload_bytes` and `download_bytes` with them.
4. A user that becomes disabled, expired or over quota is removed from the
   rendered config (`applied: false`) and the core is restarted, after
   `sing-box check` accepts the new file. Recovery — renewal, quota reset,
   re-enabling — applies in reverse.
5. "Reset traffic" in the panel only zeroes the stored counters; the core
   counters are reset by the next `QueryStats(reset=true)` call.

The visible cost of D6 is a sub-second core restart at the moment a user crosses
a threshold, not on every cycle.

## TUI

Two root entries share the work instead of one renamed entry (this narrows D11):

```
订阅管理                  账号与流量
├── 订阅链接              ├── 账号列表        # 一行一个账号：名称 · 状态 · 用量/配额
├── 订阅二维码            │   └── <账号>      # 订阅地址 / 订阅二维码 / 分享链接
├── 各节点分享链接        │                   # 账号名称 / 备注 / 流量限额 / 有效期
├── 安装订阅服务          │                   # 勾选节点 / 启用状态 / 重置已用流量
├── 重启订阅服务          │                   # 重置订阅令牌 / 删除账号
└── 订阅服务状态          └── 新建账号
```

The first three entries of `订阅管理` ask which account first
(`accountPicker`), then print the endpoint prefix plus that account's token, so
the URL the panel shows and the URL a client uses are the same string.

`订阅管理` keeps everything client-facing — the endpoint prefix, the service
unit and the "show one account's subscription" entry points — while `账号与流量`
owns the account lifecycle. Merging both into one screen would put fifteen rows
in a single menu and mix a device-level action (restart the service) with
per-account ones. Accounts are listed as ordinary menu rows rather than a
bubbles table: the list reuses the menu renderer the rest of the panel already
uses, so it inherits the fixed frame, the numeric shortcuts and the layout test,
and every action lives in the account's own submenu instead of behind
single-letter hotkeys.

The account detail screen shows the subscription URL and QR code per client
format and the per-node share links for that account only.

`节点参数` keeps only the host-wide `订阅端口` and `流量统计间隔`; the
protocol parameters moved to the node screens. `节点管理` owns the node list and
the create form, and `账号与流量` owns the per-account node selection. There is
no one-click deploy: every node or account change re-renders the config and
restarts the core through one path, so the panel and the core cannot drift.

## Verified sing-box behaviour

Facts checked against the sing-box `testing` branch source, not assumed:

| Fact | Evidence |
| :--- | :--- |
| All five protocols mark the connection with the user | `metadata.User` is set in `protocol/{vless,vmess,anytls,hysteria2,tuic}/inbound.go` |
| User counters are named and aggregated per name | `experimental/v2rayapi/stats.go` — `user>>>` + user + `>>>traffic>>>uplink/downlink` |
| Only whitelisted users are counted | `StatsService` keeps `s.users` from `stats.users` and skips other names |
| The gRPC service name | `StatsService_ServiceDesc.ServiceName = "v2ray.core.app.stats.command.StatsService"` |
| Counters are in-process atomics | `bufio.NewInt64CounterConn` around each connection; a restart clears them |
| VLESS and VMess fall back to the user index when `name` is empty | `F.ToString(userIndex)` — hence every user must carry a name |
| No per-user rate limiting exists | `common/trafficcontrol` only tracks connections for the Clash API |

## Packages

| Package | Change |
| :--- | :--- |
| `internal/node` | new: node model, store, protocol parameters, migration from the legacy per-protocol state keys |
| `internal/user` | new: user model, store, per-node credential generation, node selection, quota/expiry evaluation |
| `internal/subd` | new: subscription HTTP service, TLS, User-Agent negotiation, response headers |
| `internal/stats` | new: minimal gRPC client for StatsService, accounting loop, enforcement |
| `internal/nginx` | deleted |
| `internal/subscribe` | keeps share links and profile rendering, now parameterised by a user and a node |
| `internal/config` | renders one inbound per enabled node, the multi-user arrays and the `v2ray_api` block |
| `internal/state` | adds `SUB_SERVE_PORT`, `SUB_SYNC_SECONDS`, drops `SUB_PORT`, `SUB_PATH`; the per-protocol keys are read once for migration and then dropped |
| `internal/tui` | node list and create form, account list, per-account node selection, per-account links and QR, single apply path |
| `internal/service` | installs and controls `easysb.service` in addition to `sing-box.service` |
| `internal/uninstall` | removes the subscription service, user store and unit |
| `internal/i18n` | new strings in both languages |

## Risks

- **A restart is the only enforcement tool.** A threshold crossing disconnects
  every user of the node for a moment. The loop batches transitions per cycle to
  keep this rare.
- **A missing name is a silent zero.** If a user is not in `stats.users`, or an
  inbound omits `metadata.User`, the counter simply never moves. Tests must
  assert the two lists are built from one predicate.
- **The User-Agent table drifts.** A new client that sends an unexpected string
  lands in the Base64 fallback, which most clients still accept.
- **Port collision.** A machine that still runs the old nginx on
  `SUB_SERVE_PORT` will fail to start the service; the error message names the
  port.
- **`SUB_PORT` removal contradicts `docs/design.md`.** The "legacy-compatible
  state" rule was written for the shell tool handover and is narrowed by this
  change; `docs/design.md`, `AGENTS.md` and `docs/pitfalls.md` are updated in
  the same branch.

## Follow-ups

- Reordering the node list, or grouping it by protocol.
- A `profile-web-page-url` header once the project has a page to point at.
- Optional web panel on top of the same service.
