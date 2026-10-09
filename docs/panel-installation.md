# Web 面板安装与升级 / Panel Installation and Upgrade

面板是 EasySB 二进制的一个子命令，因此安装 EasySB 就同时安装了面板软件。这里说的是
如何把面板作为常驻服务启用、第一次登录、加固与升级、以及卸载。

The panel is a subcommand of the EasySB binary, so installing EasySB installs the
panel software. This document covers enabling it as a service, first login,
hardening, upgrade and uninstall.

## 1. 前置条件 / Prerequisites

- Debian 12/13 或 Ubuntu 24.04+，systemd，`amd64` 或 `arm64`。
- root 权限（写 systemd 单元、读 `/etc/sing-box`）。
- 已按 `README.md` 的 Quick Start 安装 `easysb` 包。

## 2. 首次安装面板服务 / Enable the panel service

面板服务由 EasySB 自己安装，有两种等价入口：

方式一：TUI。运行 `sb`，进入“Web 面板”菜单，选择安装服务。

方式二：直接运行面板并登录，从“设置”页安装服务：

```bash
# 前台运行面板（用于首次配置）
sudo easysb panel
```

面板启动时会读取 `/etc/sing-box/easysb-panel.conf`。文件不存在（或口令哈希为空）时，
面板会生成一个随机管理员口令，把 bcrypt 哈希写入该文件，并**在首次启动的控制台输出里
打印一次明文口令**。查看输出与配置：

```bash
# 首次启动的明文口令只出现在控制台 / journald，不写入面板日志文件
sudo journalctl -u easysb-panel | grep "initial password"

# 配置文件里只有哈希，没有明文
sudo cat /etc/sing-box/easysb-panel.conf
```

默认监听 `0.0.0.0:2095`，默认管理员用户名 `admin`。口令是 bcrypt 哈希，无法从文件
还原；若首次生成的口令未记录，删除 `PANEL_PASSWORD_HASH` 行后重启面板会重新生成并再次
打印，或直接用 `POST /api/v1/auth/password` 修改。明文口令只打印到控制台（`journalctl`
或前台终端），不会写入面板日志文件。

从 TUI 或“设置”页安装服务后，面板以 systemd 单元 `easysb-panel.service` 常驻，并在
开机自启：

```bash
# 查看状态
sudo systemctl status easysb-panel
```

单元文本与 `easysb --print-unit panel` 完全一致，`.deb` 也用同一文本。

## 3. 访问与首次配置 / Access and first setup

浏览器打开 `http://<主机>:2095`，用 `admin` 与口令登录，然后立即在“设置”页修改口令。

面板服务的默认端口 `2095` 位于协议默认端口（8000-8004）与订阅默认端口（8443）之外，
避免与节点/订阅冲突。默认监听所有接口且为明文 HTTP，生产环境应做以下其一：

- 在面板配置中启用 TLS（见下节）；或
- 用反向代理/防火墙限制来源，仅允许管理网络访问 2095。

## 4. 启用 HTTPS / Enable TLS

在 `/etc/sing-box/easysb-panel.conf` 中设置证书对，然后重启面板：

```text
PANEL_TLS="yes"
PANEL_CERT_FILE="/etc/sing-box/acme/<domain>/fullchain.cer"
PANEL_KEY_FILE="/etc/sing-box/acme/<domain>/private.key"
PANEL_LISTEN="0.0.0.0"
PANEL_PORT="2095"
```

```bash
sudo systemctl restart easysb-panel
```

面板直接以 `https://` 提供服务；`AccessURL()` 会跟随实际协议，日志与“设置”页显示的
地址不会在明文监听上谎称 HTTPS。若让反向代理终止 TLS，保持 `PANEL_TLS="no"` 并仅让
代理访问即可。

## 5. 升级 / Upgrade

面板与核心在同一个包里，升级 EasySB 即升级面板：

```bash
sudo apt update
sudo apt upgrade easysb
```

面板自身随 EasySB 包一起升级；升级后重启面板以载入新前端与后端：

```bash
sudo systemctl restart easysb-panel
```

前端是构建产物，随二进制嵌入；若发行版在 `/usr/share/easysb/panel` 放了更新的前端，
运行时会优先使用磁盘副本。

## 6. 卸载面板 / Uninstall the panel

从 TUI 的“Web 面板”菜单或“设置”页卸载，服务与单元被移除：

```bash
# 等价的命令行操作
sudo systemctl disable --now easysb-panel
sudo rm /etc/systemd/system/easysb-panel.service
sudo systemctl daemon-reload
```

卸载面板只移除面板单元与进程，**不会**删除节点、账号、证书、`config.json` 或订阅
服务；EasySB 与节点继续正常运行。

## 7. 从命令行/脚本使用 / Headless use

脚本客户端用 Bearer 令牌调用 API，不需要浏览器：

```bash
TOKEN=$(curl -s http://127.0.0.1:2095/api/v1/auth/login \
  -H 'Content-Type: application/json' \
  -d '{"username":"admin","password":"'"$PANEL_PASSWORD"'"}' \
  | sed -n 's/.*"token":"\([^"]*\)".*/\1/p')

curl -s http://127.0.0.1:2095/api/v1/nodes -H "Authorization: Bearer $TOKEN"
```

完整接口见 `docs/panel-api.md`。
