# Web 面板 API / Panel API

面板后端暴露 `/api/v1`。所有响应为 JSON（订阅文档接口除外），未知 `/api/` 路径返回
JSON `404`，其余路径回退到前端 SPA。API 版本在响应中以 `apiVersion` 报告，当前为
`1`（`internal/panel.APIVersion`）。

The panel backend exposes `/api/v1`. Every response is JSON except the subscription
document endpoint. Unknown `/api/` paths answer JSON `404`; other paths fall back to
the front-end SPA. The API version is reported as `apiVersion`, currently `1`.

## 认证 / Authentication

除 `POST /auth/login` 与 `POST /auth/logout` 外，所有接口都需要会话。

| 方式 | 用途 | 说明 |
| :--- | :--- | :--- |
| Cookie `easysb_panel_session` | 浏览器 | HttpOnly、`SameSite=Strict`；写请求需带 `X-EasySB-Panel: 1` |
| `Authorization: Bearer <token>` | 脚本/API 客户端 | 不依赖浏览器 Cookie，写请求免 CSRF 头 |

失败状态码：`401` 未认证，`403` 缺少 `X-EasySB-Panel` 头，`429` 登录限流。

```bash
# 登录，取出令牌
curl -s https://panel.example.com/api/v1/auth/login \
  -H 'Content-Type: application/json' \
  -d '{"username":"admin","password":"..."}'

# 用令牌调用一个受保护接口
curl -s https://panel.example.com/api/v1/nodes \
  -H "Authorization: Bearer $TOKEN"
```

## 端点总览 / Endpoint map

| 方法 | 路径 | 说明 |
| :--- | :--- | :--- |
| POST | `/api/v1/auth/login` | 登录，返回会话令牌 |
| POST | `/api/v1/auth/logout` | 退出，注销会话 |
| GET | `/api/v1/auth/session` | 当前登录者信息 |
| POST | `/api/v1/auth/password` | 修改管理员口令 |
| GET | `/api/v1/dashboard` | 落地视图：主机快照、服务状态、部署计数 |
| GET | `/api/v1/nodes` | 节点列表 + 协议元数据 |
| POST | `/api/v1/nodes` | 新建节点 |
| GET | `/api/v1/nodes/{id}` | 单个节点 |
| PUT | `/api/v1/nodes/{id}` | 编辑节点 |
| DELETE | `/api/v1/nodes/{id}` | 删除节点并清理账号选择 |
| POST | `/api/v1/nodes/{id}/enable` | 启用节点 |
| POST | `/api/v1/nodes/{id}/disable` | 停用节点 |
| GET | `/api/v1/nodes/{id}/config` | 该节点渲染出的 inbound |
| GET | `/api/v1/users` | 账号列表 |
| POST | `/api/v1/users` | 新建账号 |
| GET | `/api/v1/users/{name}` | 单个账号 |
| PUT | `/api/v1/users/{name}` | 编辑账号（备注/配额/到期/节点选择/启停） |
| DELETE | `/api/v1/users/{name}` | 删除账号 |
| POST | `/api/v1/users/{name}/enable` | 启用账号 |
| POST | `/api/v1/users/{name}/disable` | 停用账号 |
| POST | `/api/v1/users/{name}/reset` | 清零流量计数 |
| GET | `/api/v1/users/{name}/subscriptions` | 订阅地址、客户端链接、分享链接 |
| GET | `/api/v1/users/{name}/document?client=` | 渲染订阅文档（singbox/mihomo/v2ray） |
| GET | `/api/v1/subscriptions` | 订阅端点汇总（不含 token） |
| GET | `/api/v1/domains` | 证书列表、活动证书、续期定时器 |
| POST | `/api/v1/domains/issue` | 签发证书 |
| POST | `/api/v1/domains/renew` | 续期一遍 |
| POST | `/api/v1/domains/remove` | 移除证书 |
| POST | `/api/v1/domains/activate` | 切换活动证书 |
| GET | `/api/v1/domains/timer` | 续期定时器状态 |
| POST | `/api/v1/domains/timer` | 安装/移除续期定时器 |
| GET | `/api/v1/core` | 核心版本、能力位、服务状态、部署计数 |
| GET | `/api/v1/core/config` | 生效的 `config.json`（含凭据，仅管理员） |
| POST | `/api/v1/core/apply` | 应用当前存储（渲染+校验+重启） |
| POST | `/api/v1/core/check` | 用真实核心校验渲染结果，不改线上 |
| POST | `/api/v1/core/{action}` | start/stop/restart/enable/disable/status |
| GET | `/api/v1/system` | 主机快照 + 三个服务状态 |
| GET | `/api/v1/system/network` | 累计收发与磁盘读写字节（`rxBytes/txBytes/readBytes/writeBytes`），供面板轮询算实时速率 |
| POST | `/api/v1/system/subscription/{action}` | 订阅服务 install/uninstall/start/stop/restart/enable/disable |
| GET | `/api/v1/logs?service=&lines=` | 服务日志尾部（panel/core/subscription） |
| GET | `/api/v1/panel` | 面板自身状态与配置（不含口令哈希） |
| POST | `/api/v1/panel/{action}` | 面板服务 install/uninstall/start/stop/restart/enable/disable |
| GET | `/api/v1/security` | 面板 TLS 设置、已签发域名证书、防火墙后端/单元/放行端口 |
| POST | `/api/v1/security/tls` | 保存面板 TLS 设置（启用时校验证书与私钥可解析） |
| POST | `/api/v1/security/firewall/{apply\|remove}` | 应用/移除节点端口防火墙规则 |
| GET | `/api/v1/bbr` | BBR 状态：拥塞算法、队列、平台、支持性与可用 qdisc |
| POST | `/api/v1/bbr/enable` | 以指定 qdisc 启用 BBR（写入 sysctl/模块 drop-in） |
| POST | `/api/v1/bbr/clear` | 清除 EasySB 写入的 BBR drop-in |
| GET | `/api/v1/toolbox` | 工具箱注册表（分组 + 条目）与最近一次的看板 |
| GET | `/api/v1/toolbox/board` | 只返回看板（每次运行后刷新） |
| POST | `/api/v1/toolbox/{id}/run` | 运行一个工具并记录结果 |
| GET | `/api/v1/terminal/ws` | WebSocket 终端：会话 Cookie 认证，桥接到 PTY |

## 通用约定 / Conventions

- 错误的统一形状为 `{"error":"..."}`。
- 请求体为单个 JSON 对象，未知字段被拒绝（`decodeJSON` + `DisallowUnknownFields`），
  大小上限 1 MiB。
- 变更接口成功后返回 `{"ok":true}`（新建返回 `201`，含 `{"ok":true,"name":...}`）。
- 写操作成功后自动走 `deploy.ApplyStore`；核心拒绝配置返回 `422`。
- 节点创建/编辑校验失败返回 `400`；资源不存在返回 `404`。

## 关键接口细节 / Selected details

### 登录 / Login

```json
// 请求
{ "username": "admin", "password": "secret" }

// 200
{
  "username": "admin",
  "expiresAt": "2026-10-09T15:00:00Z",
  "token": "…64 hex…",
  "version": "6.0.0",
  "apiVersion": "1"
}
```

### 节点 / Nodes

`GET /nodes` 返回 `{"nodes":[{"…node.Node…","usedBy":N}],"protocols":[…]}`，其中
`protocols` 描述每个协议的可设置参数（Reality SNI、Hysteria2 跳端口），生成的密钥
（Reality keypair、short id）不可由客户端设置。

```json
// POST /nodes 或 PUT /nodes/{id}
{
  "name": "hk-anytls",
  "protocol": "anytls",
  "port": 8000,
  "enabled": true,
  "params": {}
}
```

`params` 只会保留协议允许的键；其余键被忽略，因此请求无法注入 Reality 私钥。删除节点
会同时清理所有账号对该节点 id 的选择、凭据与用量条目。

### 账号 / Users

`GET /users` 返回 `{"users":[userView…]}`。`userView` 含订阅 token 与派生读数
（`status`、`remaining`、`percent`、`unlimited`、`subscriptionUrl` 等），但不含逐节点
uuid/password。

```json
// POST /users（nodes 省略则选择全部启用节点）
{
  "name": "alice",
  "remark": "team",
  "quotaBytes": 107374182400,
  "expireAt": "2026-12-31",
  "nodes": ["<node-id>"],
  "enabled": true
}
```

`quotaBytes: 0` 表示不限量；`expireAt` 接受 `YYYY-MM-DD`（当日 23:59:59 到期）或
RFC3339，空串清除到期。`PUT` 使用指针字段区分“保持不变”与“设为 0”。

### 订阅 / Subscription

```json
// GET /users/{name}/subscriptions
{
  "name": "alice",
  "token": "…",
  "endpoint": "https://sb.example.com/sub",
  "clients": [ { "client": "sing-box", "url": "…", "link": "sing-box://…" } ],
  "shareLinks": [ { "key": "anytls", "name": "AnyTLS", "uri": "anytls://…" } ]
}
```

该接口携带账号凭据，仅管理员可见。`GET /subscriptions` 是不含任何 token 的汇总：
`{"host","endpoint","path","clients","active"}`。

### 核心 / Core

`POST /core/apply` 触发渲染+核心校验+重启；`POST /core/check` 只校验不落地；两者都返回
`{"ok":true}` 或错误。`GET /core/config` 返回 `{"path","config"}`，`config` 是配置文本，
含凭据，仅管理员可见。无落盘文件时从存储渲染返回但不保存。

### 日志 / Logs

`GET /logs?service=panel|core|subscription&lines=N`（`lines` 上限 2000，默认 200）返回
`{"service","path","lines":[…]} `；文件不可读时 `lines` 为空并附 `note`。

### 工具箱 / Toolbox

`GET /toolbox` 返回注册表与看板：

```json
{
  "groups": ["unlock", "network", "ip", "hardware"],
  "tools": [ { "id": "backtrace", "group": "network" } ],
  "board": {
    "backtrace": {
      "id": "backtrace",
      "when": "2026-10-09T07:00:00Z",
      "result": { "headers": ["…"], "rows": [["…"]], "summary": "…" }
    }
  }
}
```

条目只报告稳定 id，界面负责把它翻译成两种语言。`POST /toolbox/{id}/run` 同步运行一个工具
（受 `toolbox.DefaultTimeout` 约束），成功后返回该工具的 `Result`，失败返回 `502` 与错误；
无论成败都写入看板，因此「从未运行」与「运行失败」在界面上是两件事。未知 id 返回 `404`。

### 终端 / Terminal

`GET /terminal/ws` 是一次 WebSocket 升级，浏览器无法为握手附加 Bearer 头，因此它只认会话
Cookie（`SameSite=Strict` 已阻止跨站发起）。握手成功后，面板在 PTY 上启动登录 Shell，并按
帧类型分流：

| 帧 | 方向 | 内容 |
| :--- | :--- | :--- |
| binary | 双向 | 终端读写字节，原样透传 |
| text | 浏览器 → 面板 | `{"type":"resize","cols":N,"rows":N}`，调整 PTY 窗口 |

升级时面板会清除该连接上的读写超时（否则长会话会被服务器的 `ReadTimeout`/`WriteTimeout`
中断）。终端始终要求会话，即使测试用的 `AllowAnonymous` 也不会放开。

### BBR / Congestion control

`GET /bbr` 只读取内核状态，不触碰网络；它报告当前拥塞算法、队列算法、运行内核、平台、
是否为上游 BBRv3 内核以及是否需要重启，并列出可选的 qdisc（`bbr.Qdiscs`）：

```json
{
  "enabled": true,
  "congestion": "bbr",
  "available": "bbr cubic reno",
  "qdisc": "fq",
  "running": "6.8.0-31-generic",
  "arch": "amd64",
  "supported": true,
  "kernels": [],
  "customKernel": "",
  "needsReboot": false,
  "qdiscs": ["fq", "fq_codel", "fq_pie", "cake"]
}
```

`POST /bbr/enable` 的请求体为 `{"qdisc":"fq"}`，qdisc 会先对照 `bbr.Qdiscs` 校验，非法值
返回 `400`；写入的是 `internal/bbr` 管理的 drop-in，与 TUI 完全一致。`POST /bbr/clear`
只移除 EasySB 写入的 drop-in 并恢复被覆盖的值，不触碰主机上手写的配置。

### 安全 / Security

`GET /security` 汇总面板自身的 TLS 设置、`internal/cert` 已签发的域名证书，以及
`internal/firewall` 检测到的后端、服务单元和启用节点对应的放行端口：

```json
{
  "tls": { "enabled": false, "certFile": "", "keyFile": "" },
  "domains": [
    { "domain": "example.com", "certFile": "/etc/sing-box/acme/example.com/fullchain.cer",
      "keyFile": "/etc/sing-box/acme/example.com/private.key", "usable": true }
  ],
  "firewall": {
    "backend": "ufw",
    "unit": "easysb-firewall",
    "ports": [ { "protocol": "vless", "port": 443, "network": "tcp" } ]
  }
}
```

`POST /security/tls` 接受 `{enabled, certFile, keyFile, domain}`：给了 `domain` 时由
`cert.Paths` 解析出证书与私钥路径；启用时必须能通过 `tls.LoadX509KeyPair` 解析成对文件，
否则返回 `422`，避免保存一个无法启动的配置。`POST /security/firewall/{apply|remove}`
复用 `internal/firewall`，与 TUI 和首次部署写入的规则完全相同。

## 尚未纳入首版 / Deferred

工具箱已通过上面的接口开放；面板只镜像 `internal/toolbox/tools` 的注册表，不新增任何
检测逻辑。面板自身升级未纳入，升级随 EasySB 包由 `apt upgrade` 完成，面板不伪造一个
升级动作。理由见 `docs/panel-feature-matrix.md`。
