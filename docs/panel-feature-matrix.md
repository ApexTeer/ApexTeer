# Web 面板功能覆盖矩阵 / Panel Feature Matrix

本文件逐项登记 EasySB 现有功能到面板的覆盖情况。字段含义：

- 功能名称：EasySB 中现有的功能。
- 源码位置：实现该功能的包/文件。
- CLI 状态：是否已有命令行/TUI 入口。
- Web API：对应的 HTTP 接口。
- 面板页面：对应的 React 页面。
- 数据来源：真实数据的存储位置。
- 测试情况：验证方式与结果。
- 完成状态：已完成 / 进行中 / 未开始 / 受阻。

状态在每次迭代结束时更新。未经验证的功能不得标记为已完成。

| 功能名称 | 源码位置 | CLI 状态 | Web API | 面板页面 | 数据来源 | 测试情况 | 完成状态 |
| :--- | :--- | :--- | :--- | :--- | :--- | :--- | :--- |
| 节点列表 | `internal/node/node.go` | TUI 节点页 | `GET /api/v1/nodes` | 节点管理 | `easysb-nodes.json` | `internal/panel` 单测 + `go vet` | 已完成 |
| 创建节点 | `internal/node` `node.New` | TUI 表单 | `POST /api/v1/nodes` | 节点管理 | `easysb-nodes.json` | 单测（含端口冲突） | 已完成 |
| 编辑节点 | `internal/node` `Store.Update` | TUI 表单 | `PUT /api/v1/nodes/{id}` | 节点管理 | `easysb-nodes.json` | 单测 | 已完成 |
| 删除节点 | `internal/node` `Store.Remove` | TUI | `DELETE /api/v1/nodes/{id}` | 节点管理 | `easysb-nodes.json` | 单测（含账号选择清理） | 已完成 |
| 启用/停用节点 | `internal/node` | TUI | `POST /api/v1/nodes/{id}/enable|disable` | 节点管理 | `easysb-nodes.json` | 单测 | 已完成 |
| 节点配置预览 | `internal/config` `Build` | TUI 详情 | `GET /api/v1/nodes/{id}/config` | 节点管理 | 渲染结果 | 单测 | 已完成 |
| 应用节点变更 | `internal/deploy` `ApplyStore` | TUI 自动 | `POST /api/v1/core/apply` | 节点/仪表盘 | `config.json` + 核心 | 真机：创建/删除账号触发 apply，`config.json` 重写、核心重启后仍 active | 已完成 |
| 账号列表 | `internal/user/store.go` | TUI 账号页 | `GET /api/v1/users` | 用户管理 | `easysb-users.json` | 单测 | 已完成 |
| 创建账号 | `internal/user` `user.New` | TUI 表单 | `POST /api/v1/users` | 用户管理 | `easysb-users.json` | 单测 | 已完成 |
| 编辑账号 | `internal/user` `Store.Update` | TUI 表单 | `PUT /api/v1/users/{name}` | 用户管理 | `easysb-users.json` | 单测 | 已完成 |
| 删除账号 | `internal/user` `Store.Remove` | TUI | `DELETE /api/v1/users/{name}` | 用户管理 | `easysb-users.json` | 单测 | 已完成 |
| 账号启停 | `internal/user` | TUI | `POST /api/v1/users/{name}/enable|disable` | 用户管理 | `easysb-users.json` | 单测 | 已完成 |
| 配额/到期设置 | `internal/user/user.go` | TUI 表单 | `PUT /api/v1/users/{name}` | 用户管理 | `easysb-users.json` | 单测 | 已完成 |
| 重置流量计数 | `internal/user` `ResetCounters` | TUI | `POST /api/v1/users/{name}/reset` | 用户管理 | `easysb-users.json` | 单测 | 已完成 |
| 订阅 URL / 链接 / 二维码 | `internal/subscribe` | TUI 订阅页 | `GET /api/v1/users/{name}/subscriptions` | 订阅管理 | 渲染结果 | 单测 | 已完成 |
| 订阅文档预览/下载 | `internal/subscribe` `Document` | TUI | `GET /api/v1/users/{name}/document` | 订阅管理 | 渲染结果 | 单测（三种格式） | 已完成 |
| 订阅端点信息 | `internal/subscribe` `Endpoint` | TUI | `GET /api/v1/subscriptions` | 订阅管理 | `easysb.conf` | 单测 | 已完成 |
| 证书列表 | `internal/cert` `Domains` | TUI 域名页 | `GET /api/v1/domains` | 域名管理 | `acme/` | 单测（临时目录） | 已完成 |
| 证书签发 | `internal/cert` `Issue` | TUI | `POST /api/v1/domains/issue` | 域名管理 | `acme/` | 单测（流程）；真机未重新签发（避免 ACME 限流），账号注册路径已由 renew 覆盖 | 已完成（受限：未重新签发） |
| 证书续期 | `internal/cert` `Renew` | TUI/定时器 | `POST /api/v1/domains/renew` | 域名管理 | `acme/` | 真机：真实 ACME 账号执行续期，返回 `renewed:[]`（未到期） | 已完成 |
| 证书移除 | `internal/cert` `Remove` | TUI | `POST /api/v1/domains/remove` | 域名管理 | `acme/` | 单测（临时目录） | 已完成 |
| 续期定时器 | `internal/cert` `InstallTimer` | TUI + CLI | `GET/POST /api/v1/domains/timer` | 域名管理 | systemd | 真机：安装后 timer 已排程（22h 后），移除后恢复未安装 | 已完成 |
| 核心版本/能力 | `internal/sbcore` | `core version` | `GET /api/v1/core` | 核心管理 | 二进制内置 | 单测 | 已完成 |
| 核心服务状态 | `internal/service` | TUI 服务页 | `GET /api/v1/core` | 核心管理 | systemd | 真机：`active=true enabled=true`、`core status` 返回真实 `systemctl status` | 已完成 |
| 核心启停/重启 | `internal/service` | TUI/CLI | `POST /api/v1/core/start|stop|restart` | 核心管理 | systemd | 真机：`core status/enable` 通过；核心 apply 触发真实重启后仍 active | 已完成 |
| 生效配置查看 | `config.json` | 无 | `GET /api/v1/core/config` | 核心管理 | `config.json` | 单测 | 已完成 |
| 配置校验 | `internal/sbcore.Check` | `core check` | `POST /api/v1/core/check` | 核心管理 | 渲染结果 | 真机：真实核心对渲染配置返回 `{"ok":true}` | 已完成 |
| 主机信息 | `internal/sysinfo` | TUI 系统页 | `GET /api/v1/system` | 系统信息 | /proc、/sys | 单测 | 已完成 |
| 概览与状态 | `internal/sysinfo` | TUI | `GET /api/v1/dashboard` | 概览 / 状态 | `/proc`、`/sys`、节点与账号库 | 单测 + jsdom 渲染（概览 4 计数、4 环形） | 已完成 |
| 实时流量与磁盘 IO 监控 | `internal/sysinfo` `NetworkTotals`/`DiskTotals` | - | `GET /api/v1/system/network` | 监控（概览标签页） | `/proc/net/dev`、`/proc/diskstats` | 单测（`parseNetDev` 排除 lo、`parseDiskStats` 只取整盘）+ jsdom 渲染；真机返回真实累计字节 | 已完成 |
| 订阅服务启停 | `internal/subd` | TUI 订阅页 | `POST /api/v1/system/subscription/...` | 系统信息 | systemd | 真机：`subscription/start` 返回 `active=true installed=true` | 已完成 |
| 服务日志 | journald | TUI | `GET /api/v1/logs` | 系统信息 | journalctl | 单测（文件尾部读取） | 已完成 |
| 面板状态/启停/卸载 | 新增 `internal/panel` | 新增 TUI 面板页 | `/api/v1/panel/*` | 设置 | `easysb-panel.conf` + systemd | 单测（单元文本、配置读写） | 已完成 |
| 登录认证 | 新增 `internal/panel` | - | `/api/v1/auth/*` | 登录页 | `easysb-panel.conf` | 单测（哈希、会话、CSRF、限流、首次口令引导） | 已完成 |
| 界面外壳与偏好 | `easysb-panel`（React + Arco） | - | - | 布局/顶栏 | `localStorage` | jsdom 冒烟：登录页与登录后外壳（Sider/Menu/Header/概览卡片）真实渲染；真机资源 200 | 已完成 |
| 中英文切换 | `easysb-panel` `src/i18n.tsx` | - | - | 顶栏切换 | `easysb_panel_lang` | 类型化字典（缺键即编译失败）；jsdom 断言中文串渲染 | 已完成 |
| 明暗主题切换 | `easysb-panel` `src/theme.tsx` | - | - | 顶栏切换 | `easysb_panel_theme` | jsdom 断言 `body[arco-theme='dark']` 生效 | 已完成 |
| 工具箱（解锁/回程/测速等） | `internal/toolbox` | TUI | `GET /api/v1/toolbox`、`GET /api/v1/toolbox/board`、`POST /api/v1/toolbox/{id}/run` | 工具箱 | `easysb-toolbox.json`（看板） | 单测（注册表/看板/未知 id）；真机运行中运行一次工具核对真实结果 | 已完成 |
| 交互式终端 | 新增 `internal/panel/terminal.go` | - | `GET /api/v1/terminal/ws`（WebSocket + PTY） | 终端 | 本机 Shell | 单测（未认证 401、匿名模式 403）；真机发起 WS 会话 | 已完成 |
| BBR 管理 | `internal/bbr` | TUI | `GET /api/v1/bbr`、`POST /api/v1/bbr/enable`、`POST /api/v1/bbr/clear` | BBR 加速 | `internal/bbr` drop-in（`/etc/sysctl.d`、`/etc/modules-load.d`） | 单测（状态查询、非法 qdisc 400）；真机启停核对真实 `sysctl` | 已完成 |
| 面板 TLS 设置 | `internal/cert` + 面板配置 | TUI 域名页 | `GET /api/v1/security`、`POST /api/v1/security/tls` | 面板设置 · 安全 | `easysb-panel.conf` + `acme/` | 单测（缺成对文件 400、不可解析 422、自签成对 200 并落盘） | 已完成 |
| 防火墙规则应用 | `internal/firewall` | TUI/首次部署 | `POST /api/v1/security/firewall/{apply\|remove}` | 面板设置 · 安全 | ufw/firewalld/nftables | 单测（security 视图含防火墙信息）+ 复用原包 | 已完成 |
| 面板自身升级 | `internal/update` | TUI「版本更新」 | 未纳入首版（升级随 EasySB 包走 `apt`） | 设置（只读展示版本） | apt | - | 未开始 |

## 未纳入首版的功能与理由 / Deferred

- **面板自身升级**：Web 面板首版只安装/启停/卸载面板单元，自身升级随 EasySB 包由
  `apt upgrade` 完成；TUI 的「版本更新」仍可用。面板不伪造一个升级动作。

工具箱已接入：面板只镜像 `internal/toolbox/tools` 的注册表，运行仍由原工具完成，结果
与 TUI 一致。这些功能在面板中不显示假页面，也不伪造数据。

## 真机验证记录 / Real-host verification

已在真实主机（Ubuntu 24.04、systemd、真实域名与 sing-box 核心）完成以下端到端验证：

- **登录与首次口令**：首次启动打印一次性明文口令并写入 bcrypt 哈希；面板重启后同一口令仍
  可登录（不会二次生成）。
- **真实数据读取**：节点、账号、订阅、域名、核心、系统信息均返回线上真实数据（5 节点 /
  1 账号 / 域名 `new.kejizero.xyz` / 核心 v1.14.2）。
- **写入与落地**：创建/删除账号触发 `deploy.ApplyStore`，`config.json` 被重写、核心重启后
  仍 `active`；核心标志位 `NODE_DEPLOYED` 保持。
- **配置校验**：真实核心对渲染配置返回 `{"ok":true}`。
- **systemd 操作**：面板单元 install/enable/restart、核心 status/enable、订阅服务 start、
  续期定时器 install/remove 均通过真实 systemd 验证。
- **CSRF**：Cookie 写请求缺少 `X-EasySB-Panel` 头返回 403。
- **前端 UI 重做**：面板前端从 Ant Design 迁移到 Arco Design React，界面严格参照
  1Panel：侧栏为一块一块的独立圆角菜单卡片（选中态浅蓝填充 + 左侧蓝条）、顶栏为当前页
  胶囊标题。概览是单一滚动页：核心与系统作为页内区块顺序排列，不再有标签页，主机信息只在
  「系统信息」卡片出现一次；流量与磁盘 IO 合并为一张监控卡片，用分段开关切换。核心与系统
  不是顶级导航；导航新增 BBR 加速，安全作为面板设置的标签页。侧栏底部的「主节点」控件集中了
  退出登录。侧栏折叠后仅显示图标并保留展开按钮，图标尺寸大于 Arco 默认值；外壳、页脚与 favicon
  使用 EasySB 自有标识。登录页为紧凑分栏卡片，品牌标识使用 EasySB 图标，并加入 GPL-3.0 许可
  协议勾选与弹窗。登录页用 `box-sizing: border-box` 配合 `min-height: 100dvh`，使页面正好一屏高、
  内容可容纳时不出现滚动条（与 1Panel 一致）。
- **面板设置对齐 1Panel**：设置页拆为「面板」「安全」两个标签页。面板页承载主题颜色、系统语言、
  面板信息、生命周期与修改密码；安全页承载监听地址、面板端口、安全入口、TLS 与防火墙。安全入口
  是 `PANEL_SECURITY_ENTRY`（单个 URL-safe 片段）：设置后服务端把所有路由挂到 `/<entry>/` 前缀下，
  前缀之外的请求按 404 处理，注入的 `<base href>` 同步指向该前缀；新前端用相对资源路径与
  `apiBase()`/`routerBasename()` 读取 `<base>`，因此在带前缀挂载下仍可正常工作。监听地址与端口
  变更需重启，保存后前端自动跳转；安全入口即时生效、无需重启。
- **新前端已部署验证**：二进制 `sha256=027a3174ffb83728612e61a8fe5a306d461968b13f9d86f0237ee826ba8263ec`
  安装到 `/usr/bin/easysb`（旧版备份为 `/usr/bin/easysb.bak-*`），`easysb-panel` 单元重启后
  `active`，`*:2095` 在监听；线上返回的页面引用 `assets/index-BvrH33hU.css`、
  `assets/index-C4OFHUfJ.js` 与 `/logo.png`（`image/png`）。`/api/v1/bbr`、`/api/v1/security`、
  `/api/v1/system/network` 登录后均返回真实数据，未登录为 401。面板已启用 HTTPS：
  `PANEL_TLS="yes"`，证书为 `/etc/sing-box/acme/new.kejizero.xyz/{fullchain.cer,private.key}`，
  经 `https://new.kejizero.xyz:2095` 访问证书校验通过（`ssl_verify_result=0`），明文 HTTP 被拒；
  终端 WebSocket 带会话 Cookie 握手返回 `101 Switching Protocols`，匿名握手返回 `401`。
  安全入口已用临时片段在线验证：设置后根路径与根 API 均 404，`/<entry>/` 下的页面与 API 均 200
  且 `<base>` 改写为 `/<entry>/`；清除片段后根路径恢复 200。
- **脚本化端到端验证**：`/tmp/opencode/panel_e2e.py` 对线上面板 API 做全量扫描，62 项检查全部通过
  （认证/CSRF、dashboard、system/network、logs、core+apply+status、nodes 增删改启停、users 增删改
  启停与订阅文档、subscriptions、domains、panel/security、bbr、toolbox、终端 WS `101`）。
  `/tmp/opencode/panel_entry_test.py` 在线验证安全入口 7 项全通过。双向一致性用 `/tmp/opencode/panel_verify.py`
  与 `--provision` 验证：面板创建的节点/账号能在 TUI `--render --screen node-list|user-list` 中显示，
  CLI `--provision` 创建的节点/账号也能在面板 `GET /nodes`、`GET /users` 中显示。
- **两处端到端暴露的缺陷已修复**：其一，`POST /api/v1/nodes` 原先只回 `{"ok":true}`，脚本客户端拿不到
  新节点 id；现回 `{"ok":true,"id":…,"name":…}`。其二，连续快速写入时旧实例仍占用 v2ray API 端口
  `127.0.0.1:10085`，叠加 systemd 启动限制会让单元进入 `start request repeated too quickly` 的失败态，
  后续 apply 一起失败；`deploy.Apply` 现于重启/启动前 `systemctl reset-failed`，并在重启失败后清理失败态
  重试一次，`restartAfterChange` 承载该重试。修复后重跑 e2e 由 60/2 变为 62/0。

仍为"受限"的项：

- **证书签发**：未在真机重新走完整 ACME 签发（避免触发签发限流）。账号注册与续期路径已由
  `domains/renew` 覆盖；如需，可在域名页对该域名重新签发一次以补全记录。
