# Web 管理面板集成审计 / Panel Integration Audit

本文档是 EasySB 原生 Web 管理面板（EasySB-Panel）集成工作的第一阶段交付物。
它记录了动手之前对两个仓库的真实审计结果，是后续架构、API 与页面设计的依据。

This document is the Phase 0 deliverable for the native Web panel integration. It
records the audit of both repositories that was performed before any code was
written, and is the basis for the architecture, API and page design that follows.

## 0. 审计范围与方法 / Scope and method

- 核心仓库：`github.com/EasySBTeam/EasySB`，审计时版本 `VERSION = 6.0.0`，模块
  `github.com/EasySBTeam/EasySB`，Go 1.27.1。
- 面板仓库：`github.com/EasySBTeam/EasySB-Panel`，审计时为空仓库（没有 commit）。
- 方法：直接阅读源码，而不是只读 README。功能是否存在、如何实现、数据放在哪里，
  均以包源码为准。下表每个条目都能追到具体文件与符号。

结论先行：EasySB 的业务逻辑已经集中在若干可复用的 Go 包中（`internal/node`、
`internal/user`、`internal/subscribe`、`internal/deploy`、`internal/cert`、
`internal/state`、`internal/service`、`internal/subd`、`internal/sysinfo`），
配置持久化全部是 `/etc/sing-box` 下的文件而不是数据库。因此“Web 面板复用核心业务
逻辑”是可行的，不需要另写一套节点/用户/订阅逻辑。面板后端应当作为 EasySB 二进制
的一个新的运行模式存在，直接调用这些包。

## 1. EasySB 现有功能清单 / Existing feature inventory

### 1.1 运行时数据 / Runtime data

| 路径 | 拥有者 | 内容 | 权限 |
| :--- | :--- | :--- | :--- |
| `/etc/sing-box/easysb.conf` | `internal/state` | 主机状态 KV：`DOMAIN`、`CERT_DOMAIN`、`ACME_EMAIL`、`NODE_DEPLOYED`、`SUB_SERVE_PORT`、`SUB_SYNC_SECONDS`、`SERVER_IP` | 0600 |
| `/etc/sing-box/easysb-nodes.json` | `internal/node` | 节点数组，一个节点一个协议 inbound（`id`、`name`、`protocol`、`port`、`enabled`、`params`、`created_at`） | 0600 |
| `/etc/sing-box/easysb-users.json` | `internal/user` | 账号数组：`token`、`enabled`、`nodes[]`、`credentials{}`、`usage{}`、`quota_bytes`、`used_bytes`、`expire_at`、`applied` | 0600 |
| `/etc/sing-box/config.json` | `internal/config` + `internal/deploy` | 渲染后的 sing-box 服务配置 | 0600 |
| `/etc/sing-box/acme/` | `internal/cert` | ACME 账号与每域名证书（`EASYSB_ACME_DIR` 可覆盖） | 目录 0700 |
| `/etc/sing-box/easysb.log`、`easysb-sub.log` | `internal/sysinfo` | 日志路径常量 | - |
| `/etc/sing-box/easysb-ui.conf` | `internal/prefs` | TUI 皮肤/配色/标记/语言 | 0644 |
| `/etc/systemd/system/sing-box.service` | `internal/service` | 核心服务单元，运行 `easysb core run -c ...` | 0644 |
| `/etc/systemd/system/easysb.service` | `internal/subd` | 订阅服务单元，运行 `easysb --serve` | 0644 |
| `/etc/systemd/system/easysb-acme.timer` | `internal/cert` | 证书续期定时器 | 0644 |

配置文件格式是 JSON（节点、账号）与 `KEY="value"` KV（状态）。所有写入都走
`os.CreateTemp` + `os.Rename` 的原子替换，并在写入前加锁（`internal/node/lock.go`、
`internal/user/lock.go`、`internal/filelock`）。

### 1.2 协议 / Protocols

`internal/state` 定义五个协议键，`internal/config` 为每个启用的节点渲染一个 inbound。

| 协议键 | 名称 | 凭据字段 | 每节点参数 |
| :--- | :--- | :--- | :--- |
| `anytls` | AnyTLS | `password` | - |
| `hysteria2` | Hysteria2 | `password` | `hop_range`（端口跳跃） |
| `tuic` | TUIC v5 | `uuid` + `password` | - |
| `vless-reality` | VLESS-Vision-Reality | `uuid` | `reality_sni`、`reality_private`、`reality_public`、`reality_short_id` |
| `vmess-ws-tls` | VMess-WebSocket-TLS | `uuid` | - |

### 1.3 账号模型 / Account model

- 账号 = 名称 + 一个订阅 token + 一组选择的节点 id + 每个节点一份凭据。
- 派生状态：`active` / `disabled` / `expired` / `over-quota`（`user.User.Status`）。
- 流量配额与到期时间；计数器由核心的 V2Ray StatsService 提供
  (`internal/stats`)，核心用户名是 `<token>@<node id>`（`node.CoreName`）。
- 只有 `Store.Routable` 的账号会进入核心配置与统计白名单。
- 月度自动清零：`ResetIfNewMonth`（按本地时区）。

### 1.4 订阅 / Subscription

- 单端点 `/sub/<token>`，由 `internal/subd` 提供；格式按 User-Agent 协商：
  sing-box JSON、mihomo YAML、v2rayN Base64 分享链接文档。
- 响应头 `Subscription-Userinfo` 报告用量；停用/过期/超额的账号返回 403 纯文本。
- `internal/subscribe` 负责渲染三种文档、分享链接、订阅 URL 与 sing-box 深链二维码。

### 1.5 证书与域名 / Certificates and domains

- `internal/cert` 用内置的 lego 直接在进程内做 ACME HTTP-01 签发/续期。
- `cert.Domains()` 列出已签发域名；`Issue`/`Renew`/`Remove`/`InstallTimer`/`RemoveTimer`。
- 自签占位证书在首次签发前保持核心配置可渲染。

### 1.6 服务与部署 / Services and deploy

- `internal/deploy` 是唯一的写入路径：渲染 → 核心校验 → 原子落盘 → 首次安装并启动
  核心单元 / 之后重启。
- `internal/service`（sing-box 核心）与 `internal/subd`（订阅服务）各自管理系统单元。
- 订阅服务内还跑统计循环，按配额/到期变化重启核心。

### 1.7 工具箱与系统 / Toolbox and system

- `internal/toolbox` + `internal/toolbox/tools`：解锁检测、回程、IP 质量、邮件端口、
  性能测试、测速、硬件信息。
- `internal/sysinfo`：主机/设备/核心/服务状态与路径常量。
- `internal/unlock`：服务解锁探测。
- `internal/bbr`：BBR 状态与内核安装。
- `internal/update`：读取远端 `VERSION` 并通过 apt 升级。

## 2. EasySB-Panel 当前实现情况 / Panel current state

审计时 `EasySBTeam/EasySB-Panel` 是一个**空仓库**：`git clone` 返回
"warning: You appear to have cloned an empty repository"，没有任何 commit 或文件。

因此面板没有既有架构、页面、路由、认证或 API 需要保留。按 SKILL 的要求，本阶段不是
"在现有面板上增量"，而是从零建立面板，同时确保它调用 EasySB 的真实业务逻辑。本审计
据此把设计重点放在“核心复用”而不是“迁移旧面板代码”。

## 3. 各功能对应的源码位置 / Source map

| 功能 | 源码位置 |
| :--- | :--- |
| 节点模型与存储、参数补齐、校验 | `internal/node/node.go`、`internal/node/lock.go` |
| v5 → v6 节点迁移 | `internal/node/migrate.go` |
| 账号模型、状态、配额、凭据 | `internal/user/user.go`、`internal/user/store.go`、`internal/user/migrate.go` |
| 配置渲染 | `internal/config/config.go` |
| 部署写入路径（渲染+校验+重启） | `internal/deploy/deploy.go` |
| 订阅文档、分享链接、二维码负载 | `internal/subscribe/subscribe.go`、`generate.go`、`mihomo.go` |
| 订阅 HTTP 服务与统计循环 | `internal/subd/server.go`、`internal/stats/loop.go` |
| 证书签发/续期/移除/定时器 | `internal/cert/cert.go`、`acme.go`、`unit.go`、`preflight.go` |
| 主机状态 KV | `internal/state/state.go` |
| 核心服务控制 | `internal/service/service.go` |
| 订阅服务控制 | `internal/subd/unit.go` |
| 主机/设备状态快照与路径 | `internal/sysinfo/sysinfo.go` |
| 核心版本与能力位 | `internal/sbcore/sbcore.go` |
| 账号面板入口（TUI） | `internal/tui/menu.go`、`nodes.go`、`users.go`、`domain.go`、`actions.go` |
| 版本与发布检查 | `internal/update/update.go`、`VERSION`、`release/TAGS` |

## 4. 可直接复用的业务逻辑 / Directly reusable logic

以下逻辑无需修改即可被 Web API 调用：

- `node.Load` / `Store.Add` / `Update` / `Remove` / `Validate` / `New` / `EnsureParams`。
- `user.Load` / `Store.Add` / `Update` / `Remove` / `Find` / `Routable` / `Repair`。
- `user.New` / `User.Select` / `Deselect` / `Status` / `Remaining` / `Percent` / `ResetCounters`。
- `subscribe.Document` / `ShareLinks` / `SubscriptionURL` / `ClientLink` / `Endpoint` / `QRCode`。
- `deploy.ApplyStore` / `Apply` / `ApplyConfig` / `ServerConfig` / `LoadNodes`。
- `cert.Domains` / `Issue` / `Renew` / `Remove` / `TimerInstalled` / `InstallTimer` / `RemoveTimer` / `TimerStatus`。
- `state.Load` / `Config.Save`。
- `service.Detect` / `Active` / `Do` / `WriteUnit` / `RemoveUnit` / `UnitBody`。
- `subd.Active` / `Do` / `WriteUnit` / `RemoveUnit` / `UnitBody`。
- `sysinfo.Collect` / `Status` / 路径常量。
- `sbcore.Version` / `StatsCapable`。
- `update.RemoteVersion` / `Apply`。

这些是可被 Web API 直接调用的“业务服务层”雏形。EasySB 没有把业务逻辑写在 TUI 里，
TUI 只是这些包的调用者之一，这正是可以低成本增加一个 HTTP 入口的原因。

## 5. 需要抽取为服务层的业务逻辑 / Logic to wrap

需要新增一层（不是重写）来服务 Web API：

- 统一的“变更 + 应用”包装：每次节点/账号写操作后调用 `deploy.ApplyStore`，并把
  `ErrNoNodes`（合法的空节点状态）与 `ErrRejected`（核心拒绝）区分报告。
- 删除节点时清理账号里对该节点 id 的选择（当前 TUI 未做，Web API 应做）。
- 账号凭据/令牌的返回脱敏策略：面板需要在“订阅管理”页展示链接与凭据，但任何列表
  接口都应避免在日志和错误里泄露 token、uuid、password。
- 面板自身的运行配置（监听地址、端口、管理员账号、密码哈希）需要一个新的持久化点，
  与 EasySB 主机状态分开，避免污染 `easysb.conf` 的 legacy 兼容语义。
- 面板服务的 systemd 单元管理（安装/升级/启动/停止/重启/日志/卸载），TUI 与 Web
  共用同一套函数。

这些都要调用现有包，不复制它们的逻辑。

## 6. 需要新增的 API / New surface

面板后端作为 EasySB 二进制的新运行模式，暴露 `/api/v1`。资源划分：

```text
/api/v1/auth/*          登录、登出、会话、改密
/api/v1/dashboard       核心/面板版本、服务状态、节点/账号统计、主机要点
/api/v1/nodes/*         节点 CRUD、启停、配置预览、应用
/api/v1/users/*         账号 CRUD、启停、配额/到期、重置计数、订阅链接与文档
/api/v1/subscriptions   订阅端点与账号订阅汇总
/api/v1/domains/*       证书列表、签发、续期、移除、定时器
/api/v1/core/*          核心版本、状态、配置、校验、应用、启停
/api/v1/system/*        主机信息、sing-box 与订阅服务控制
/api/v1/logs            服务日志
/api/v1/panel/*         面板自身状态、升级、重启、卸载
```

详见 `docs/panel-api.md`。

## 7. 尚未实现或存在缺陷 / Gaps found

- 没有 Web/HTTP 管理 API；唯一的对外入口是订阅端点。
- 没有面板账号/认证体系。
- 面板（Web）不存在，需要从零建立。
- 删除节点不会清理账号中悬空的选择（当前无害但会留下垃圾数据）。
- 没有面向面板的“应用变更”并发控制；文件锁已经存在，但 Web 需要避免两次并发写
  覆盖彼此。
- 面板尚无版本与 API 兼容性检查机制。

## 8. 集成方案 / Integration plan

采用“核心内嵌面板服务”的方案，理由：

- EasySB 已经是单静态二进制，sing-box 也编译在内；面板后端作为同一二进制的新模式
  (`easysb panel`) 运行，可 100% 复用业务包，且不存在两份节点/账号数据。
- 独立后端进程会引入“两份数据 + 同步问题”，与 SKILL 的“避免两个服务各自维护一份
  冲突数据”直接冲突。
- React 前端构建产物作为静态资源由面板服务提供；面板服务与订阅服务、核心服务是三
  个相互独立的 systemd 单元，面板故障不会停核心。

数据所有权：节点、账号、状态、证书全部仍归 EasySB 现有包所有；面板只读写这些文件，
不引入第二份存储。面板自身配置单独放在 `/etc/sing-box/easysb-panel.conf`。

## 9. 风险清单与测试方案 / Risks and test plan

风险：

1. 并发写入：面板与订阅服务/CLI 可能同时写账号文件。缓解：沿用文件锁 + 原子替换，
   面板写操作串行化。
2. 凭据泄露：订阅链接、uuid、password 出现在 API 响应与日志中。缓解：列表接口不返回
   完整凭据，日志不打印敏感字段，错误信息脱敏。
3. 误删核心数据：卸载面板不得删除节点/账号/证书或核心配置。缓解：面板卸载只移除
   面板单元与面板配置。
4. 端口冲突：面板默认端口可能与节点或订阅端口冲突。缓解：安装时检测端口占用与
   `CheckSubPort`。
5. 配置一致性：写盘点多处。缓解：所有业务写操作统一经过 `deploy.ApplyStore`。

测试方案：

- Go 单元测试：认证（密码哈希、会话、登录限流）、节点/账号 CRUD、删除节点的选择清理、
  订阅文档渲染、核心/系统只读接口、面板服务单元文本。
- 编译与静态检查：`go build ./...` + `go vet` + `gofmt -l`。
- 前端：`tsc --noEmit` 与 `vite build` 必须通过。
- 端到端（干净环境）：安装面板 → 登录 → 建节点 → 建账号 → 取订阅 → 改节点启用 →
  应用 → 停/重启核心 → 卸载面板后 EasySB 仍可用。
- 回归：`internal/tui` 与其余包测试保持通过；未安装面板时 CLI/TUI 行为不变。

已知限制在 `docs/panel-troubleshooting.md` 与 `docs/panel-feature-matrix.md` 中持续
维护；未经验证的功能不得标记为完成。
