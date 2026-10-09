# Web 面板故障排查 / Panel Troubleshooting

按“先看面板服务 → 再看配置 → 再看业务存储 → 最后看核心”的顺序定位。每一步都给出
可判断的证据，而不是猜测。

Work from the panel service, to its configuration, to the business stores, and last
to the core. Each step names the evidence that decides the question.

## 面板打不开 / The panel is not reachable

1. 服务是否在跑：

```bash
sudo systemctl status easysb-panel
```

2. 是否在监听预期地址：

```bash
sudo ss -ltnp | grep 2095
```

3. 读面板日志：

```bash
sudo journalctl -u easysb-panel -n 100 --no-pager
# 或者面板自己的日志文件
sudo tail -n 100 /etc/sing-box/easysb-panel.log
```

常见原因与处理：

- **端口被占用**：`panel listen 0.0.0.0:2095: bind: address already in use`。改
  `PANEL_PORT` 后重启，或释放占用端口的进程。
- **配置端口非法**：`PANEL_PORT` 非 1-65535 时回退到默认 `2095`；检查文件是否被手改。
- **服务未安装/未启用**：`systemctl is-enabled easysb-panel` 返回 `disabled`，从 TUI
  “Web 面板”菜单或“设置”页安装。
- **防火墙**：本机正常但外部打不开，检查云安全组与 `ufw`/`nftables`。面板默认明文
  HTTP，不要把它直接暴露到公网。

## 登录失败 / Cannot log in

- **401 用户名或口令错误**：确认用户名（默认 `admin`）与口令。口令是 bcrypt 哈希，
  无法从 `/etc/sing-box/easysb-panel.conf` 还原。
- **429 too many failed attempts**：同一来源连续 5 次失败，锁定 15 分钟；等待后重试。
- **忘记口令**：删除 `PANEL_PASSWORD_HASH` 行后重启面板，会重新生成一个口令并把明文
  打印到首次启动的控制台（`journalctl -u easysb-panel`）；也可在能登录时用“设置”页修改。
  明文只出现在控制台，不会写入面板日志文件。
- **写请求 403 `missing X-EasySB-Panel header`**：浏览器前端会自动带该头；手写脚本用
  Cookie 认证时需要补上 `-H 'X-EasySB-Panel: 1'`，或改用 Bearer 令牌。

## 面板启动报配置错误 / Configuration errors

`LoadConfig` 对缺失文件返回默认值，因此面板总能启动；出现异常时按字段排查：

- `PANEL_LISTEN` 为空 → 使用 `0.0.0.0`。
- `PANEL_PORT` 非数字或越界 → 使用 `2095`。
- `PANEL_PASSWORD_HASH` 为空 → 启动时生成新口令并在控制台打印一次。

确认没有多处写入同一文件造成格式错乱；面板写配置使用临时文件 + 原子重命名，并置
`0600`。

## 变更没有生效 / A change did not take effect

1. 写操作是否返回 `ok`。节点/账号创建返回 `201`，编辑返回 `200`。
2. **422 核心拒绝**：`{"error":"the core rejected the configuration: …"}`。这是真实
   核心校验失败，线上配置保持不变；按错误信息修正端口、参数或证书。
3. 查看 `config.json` 的修改时间与内容，确认渲染已落盘：

```bash
sudo ls -l /etc/sing-box/config.json
```

4. 查看核心服务是否重启成功：

```bash
sudo systemctl status sing-box
sudo journalctl -u sing-box -n 100 --no-pager
```

端口冲突（与另一启用节点或订阅端口相同）在写入前被拒绝，返回 `400`。

## 订阅打不开 / The subscription URL does not work

- 账号状态可能是 `disabled`/`expired`/`over-quota`，订阅端点会返回 `403` 纯文本原因。
  在“用户管理”页查看 `status`，解除停用、延长到期或提高配额。
- 未设置域或服务器地址时，`GET /users/{name}/subscriptions` 返回 `422`，因为无法拼出
  订阅地址；先在域名/主机状态里设置。
- 订阅服务未安装或未运行：检查 `easysb.service`

```bash
sudo systemctl status easysb
```

## 证书签发失败 / Certificate issuance fails

签发流程会逐步记录，响应中的 `steps` 数组说明走到哪一步：

1. **DNS 预检**：`dns: …` 或 `dns lookup failed: …`；域必须解析到本机。
2. **端口 80 占用**：`port 80 is not free: …`。HTTP-01 standalone 需要独占 80；面板会
   先停核心再尝试，失败时会重新拉起核心。
3. **ACME 账号未注册**：续期接口在校验阶段返回 `422 no ACME account is registered yet`。
4. **签发成功但证书缺失**：`issuance reported success but no certificate is on disk`，
   检查 `/etc/sing-box/acme/<domain>/` 的权限与磁盘空间。

## 面板与核心“状态不一致” / Panel and core disagree

面板的状态来自 systemd 与磁盘文件，核心的状态来自进程。若面板显示某节点启用但核心没
有对应 inbound，说明变更尚未成功应用；在“核心管理”页执行“应用”，或查看 `config.json`
与 `sing-box.service` 日志。`deploy.ApplyStore` 在核心接受配置前不会替换线上配置，因此
“文件是旧的但服务在跑”属于正常保护状态，而不是数据损坏。

## 无 systemd 的环境 / Environments without systemd

在容器或非 systemd 主机上，`systemctl` 不可用：面板的服务状态查询会返回未知/失败，
安装服务会返回 `422 systemctl not available`。此时可直接前台运行 `easysb panel` 管理
业务，但节点的自动重启与单元管理不可用。这是已知限制，不是缺陷。

## 卸载面板后 EasySB 是否仍可用 / Does EasySB survive a panel uninstall

是。卸载面板只移除 `easysb-panel.service`，节点、账号、证书、`config.json` 与订阅服务
不受影响。若卸载后节点未随开机启动，检查 `sing-box.service` 是否仍 enabled。
