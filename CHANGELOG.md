# 变更记录

本文件记录 EasySB 项目的重要变更。格式参考 [Keep a Changelog](https://keepachangelog.com/zh-CN/1.1.0/)。

程序的版本号与构建提交在编译期注入，内核版本独立于程序版本，由官方 `SagerNet/sing-box` Releases 提供。

## [Unreleased]

### 新增

- **Makefile**：常用命令从零散的 `go build` / `go test` / `gofmt` 收进一套目标——`make` 构建（读 `release/TAGS`、盖上提交短哈希）、`make check` 是提交前关卡（`gofmt -l` + `go vet` + 带标签测试）、`make dist` 交叉编译全部发布架构、`make render` / `make screens` 渲染并校验版式，另有 `run` / `test-plain` / `test-race` / `fmt` / `lint` / `install` / `tidy` / `version` / `clean`。`make help` 列出全部目标。标签与版本仍各读 `release/TAGS` / `VERSION`，Makefile 不另抄一份。
- **发布工作流改为走 Makefile**：架构清单、构建标签、链接参数不再在工作流里另写一份——`make release-matrix` 输出架构矩阵 JSON 供动态矩阵使用，`make dist-asset` 按单一映射（armv7 = `GOARCH=arm` + `GOARM=7`）逐架构交叉编译，`make test` 跑带标签测试。测试从「每个架构各跑一遍」收敛为 `prepare` 作业里跑一次，构建矩阵随后基于同一份清单展开。
- **发布压缩包**：`make tarball-asset` 把每个架构的二进制压成 `easysb-<版本>-linux-<架构>.tar.gz`（成员为 `easysb`、`LICENSE`、`README.md`）。发布与自更新都只发压缩包，`dist/easysb-linux-<架构>` 退回打包中间产物，不再作为资产单独出现。
- **资产命名统一为 sing-box 风格**：压缩包是 `easysb-<版本>-linux-<Go 架构>.tar.gz`，三种安装包是 `easysb_<版本>_linux_<架构>.<扩展名>`，`<架构>` 用各生态自己的拼写。release 的 tag 与名字统一成 `v<版本>`，取消原先承载 apt 源的 `debian` 标签。
- **`.deb` 安装包**：`make deb` 用 fpm 把 `dist/` 里的二进制打成 `easysb_<版本>_linux_<架构>.deb`，可直接 `dpkg -i`；包内含 `/usr/bin/easysb`（内核已编入）、快捷指令 `/usr/bin/sb`、`sing-box.service`、`easysb.service` 与许可证。单元文本由二进制自己打印（`sb --print-unit node|sub`），与面板运行时写下的是同一段代码，包内不再存第二份。Debian 架构名在 Makefile 里集中定义（armv7 → `armhf`、386 → `i386`）。安装时不自动 enable / start：新机器还没有节点配置，由面板在配置完成后启用。
- **`.rpm` 安装包**：`make rpm` 用 fpm 把同一棵暂存树打成 `easysb_<版本>_linux_<架构>.rpm`，供 Fedora / RHEL / openSUSE 使用。rpm 架构名集中在 `RPMARCH_*`（armv7 → `armv7hl`、386 → `i686`），生命周期脚本在 `packaging/rpm/`。
- **pacman 安装包**：`make pacman` 用 fpm 打 `easysb_<版本>_linux_<架构>.pkg.tar.zst`，供 Arch 使用。架构名集中在 `PACMANARCH_*`（armv7 → `armv7h`），Arch 没有 i386 / s390x，这两个架构不出包。
- **三种格式同源**：`.deb` / `.rpm` / pacman 都由 `make pkg-stage` 生成同一棵暂存树，再按架构由 `make packages-asset` 一次产出三包；二进制、systemd 单元与安装路径只有一处定义，三个包内容一致。发布工作流的打包作业因此按架构产出全部格式，Release 资产同时带上三种安装包。
- **四份软件源与发布服务器**：`make repo` 把同一批文件摊成四种客户端要的形态——apt 是扁平的 deb 源（`make apt-index` 跑 `apt-ftparchive` 并签名）、rpm 按架构成 rpm-md 目录（`createrepo_c`）、pacman 按架构成数据库（`repo-add`）、`bin/` 放发布压缩包。发布工作流用 FTP-Deploy-Action 以 FTPS 把这些同步到 `sb.kejizero.xyz`，安装脚本里写的就是这个固定地址。`packaging/server/` 放站点首页与一次性置备脚本：装 caddy、写站点配置、建站点目录、装 vsftpd 并建一个只能写站点根的账号，由 “Provision the release server” 工作流驱动。
- **`install.sh` 三种方式分开**：一键 / 软件源 / 手动安装包各有各的路径。源安装的 apt 写法用 `/etc/apt/keyrings` 加 `.sources`（`Signed-By`），rpm 区分 dnf5 的 `config-manager addrepo --from-repofile` 与 dnf4 直接读仓库文件，pacman 写 `pacman.conf` 段落；手动安装认得 `.deb` / `.rpm` / `.pkg.tar.zst` / `.tar.gz`。
- **三份源统一签名**：发布用 `GPG_PRIVATE_KEY` 与 `GPG_PASSPHRASE` 两个 secret，签名时口令从 0600 临时文件读入，不进进程列表。apt 签 `Release` / `InRelease`，rpm 用 `rpm --addsign` 签每个包并用 armored detached 签名签每个架构的 `repomd.xml`，pacman 签每个包与 `easysb.db`；公钥按各客户端的要求随源发布为 `apt/easysb.gpg`（二进制 keyring）、`rpm/RPM-GPG-KEY-easysb` 与 `pacman/easysb.asc`（armored）。

### 修复

- **rpm / pacman 源不再因未签名而被拒**：README 与站点页原先给出的 rpm 条目既没有公钥也没关掉校验，而 dnf5 对新仓库默认 `gpgcheck=1`，安装停在 `Package ... is not signed` / `GPG check FAILED`；pacman 段落则在未签名的情况下声明 `Required`。现在三份源一律签名：rpm 条目写 `gpgcheck=1`、`repo_gpgcheck=1` 与 `gpgkey=`，pacman 段落写 `SigLevel = Required DatabaseRequired` 并由 `install.sh` 导入并本地信任公钥；只有服务器上确实取不到公钥时才退回 `gpgcheck=0` / `Optional TrustAll` 并打印提示。
- **自签占位证书不再被当成已签发**：`Paths` 为了让内核在首次签发前有证书可服务，会解析自签占位对，但 `dueForRenewal` 曾把它的十年有效期当作真实证书，导致第一次签发 `Issue` 直接返回成功、其实什么都没签。现在自签占位一律视为到期，`Issue` / `Renew` 会真正走 ACME。
- **订阅服务的 TLS 判定与打印的 URL 一致**：监听端改用 `cert.Usable`（与 `subscribe.Endpoint`、面板告警同一个判断），自签占位存在时改为明文 HTTP，不再出现「用自签证书提供 TLS、却打印 `http://` 地址」的不可用订阅。
- **流量记账基线只在落盘后推进**：采样基线原先在 `Apply` / `Save` 之前前进，任何一次失败都会让那一轮增量在下一轮被减掉、永久丢失；现在只有 `Save` 成功后才推进。
- **账号文件并发写不再互相踩踏**：`Store.Save` 改用每次唯一的临时文件名（`os.CreateTemp`），面板进程与订阅服务的记账循环同时写 `easysb-users.json` 时不会再交错出截断的 JSON。
- **表单的字段提示重新显示**：`formModel.hint` 一直被赋值却从不渲染，账号配额 / 到期时间等输入框的「示例 50GB」「示例 2026-12-31 或 30d」提示因此丢失，现已在按键提示行前显示。
- **表单页面不再破坏固定版式**：`formScreen` 原先自绘整屏面板，丢掉状态条、把卡片从窗口首行拉到末行，任何输入框一打开面板就变形；该页面又无法用 `--render` 渲染，所以没有测试发现它。现在表单与运行 / 结果页同框——状态条、占正文两框位置的单张卡片、下方按键框——`--screen form` 可渲染，`make screens` 与 TUI 测试一并覆盖表单、运行、结果三个页面。
- **方向键只移动光标，不再执行目录项**：`menuColumns` 只按「主菜单 / 子菜单」判断列数，而子菜单在条目多到一行放不下时会回退成双列（`entryRows`），于是服务管理、节点参数、SNI、账号详情这些双列页面被当成单列——`→` 变成回车，会在服务管理里启动 / 重启服务、在账号详情里删除账号；`←` 变成返回。现在 `menuColumns` 与渲染同源（`itemCount() > boxRows(layout.menu)`），`←`/`→` 只换列，单列页面不做任何事；`Enter` 只进入与确认，`Esc` 只返回。系统信息页原先也让 `→` 漏到全局处理，会在看不见的菜单里进入一层，现在该页接管全部方向键。

### 移除

- 删除已无用的 sing-box 重编译链路：`.github/workflows/singbox-v2ray-api.yml`（含 `prune` job）、`scripts/build_singbox_v2ray_api.sh`、`scripts/verify_singbox_arches.sh`、`scripts/prune_release_assets.py`、`scripts/plan_check.py`，以及只验证已删除内核管理的 `scripts/vps/verify-kernel-*.sh` / `verify-source-switch.sh`。内核已编译进面板，这些脚本维护的 `singbox-stable` / `singbox-alpha` 通道不再被任何代码消费。

### 变更

- **版本号收敛到唯一来源**：`VERSION` 是唯一写下版本号的地方，现在用 `go:embed`
  在编译期读进二进制。删除 `main.go` 的 `version = "5.0.0"` 默认值，以及发布工作流
  与 `install.sh` 源码构建里的 `-ldflags -X main.version`：裸 `go build` 与发布版报告
  同一个号，不再有第二个常量可以漂移。
- **`install.sh` 不再写死版本号**：源码树内读根目录 `VERSION`，独立运行
  （`curl | bash`）时从默认分支读取同一个文件（与 `internal/update` 同址，避开 GitHub
  匿名 API 的限流），`--binary` 则读二进制自己报告的 `--version`；`RELEASE_TAG` 一律由
  解析出的版本派生。
- `install.sh` 的源码构建兜底 `DEFAULT_TAGS` 与 `release/TAGS` 对齐为 `with_quic,with_utls,with_v2ray_api`（离开源码树、读不到 `release/TAGS` 时的唯一兜底）。
- 清理死代码：TUI 不可达的 `q` 分支、未使用的 `menuDescColumn` / `menuRowParts`、`progressModel.afterLinks` 字段，以及主题里已随左侧导航移除的 `Metrics.Gutter` / `Metrics.NavWidth`。

## [5.0.0] - 2026-09-26

### 破坏性变更

- **sing-box 内核编译进面板**：`github.com/sagernet/sing-box v1.14.2` 成为 `go.mod` 的直接依赖，节点就是面板自己——`ExecStart=/usr/local/bin/easysb core run -c /etc/sing-box/config.json`，`/etc/sing-box/sing-box` 与 `/usr/local/bin/sing-box` 不再存在，面板装完即带内核，不需要再下载、安装或切换任何东西。新增 `internal/sbcore`（`Run` / `Check` / `Version` / 能力位）与 `internal/download`（面板自身更新与 BBR 内核包共用的下载层），`internal/core`（发布发现 / 下载 / 安装 / 切换）整体删除。
- **构建标签只有一处定义**：`release/TAGS`（当前 `with_quic,with_utls,with_v2ray_api`），发布工作流与 `install.sh` 的源码构建读同一个文件。`with_v2ray_api` 是账号流量统计的前提，而它是编译期的事实、无法在运行时探测，因此由 `internal/sbcore/stats_on.go` / `stats_off.go` 这对带标签的文件回答，部署前用 `StatsCapable()` 决定要不要写 `experimental.v2ray_api`（不带该标签的构建照常部署可用节点，只是不计流量，并在部署日志里说明）。这次同时确认了上游 `DEFAULT_BUILD_TAGS` 里的 `with_naive_outbound` 会把 cronet 拖进来、在 386/armv7/riscv64/s390x 上直接编译不过，而面板的服务端配置不用它，所以不再随包携带。
- **证书改为面板内置 lego**：`github.com/go-acme/lego/v5` 在面板进程内完成 ACME 开户与 HTTP-01 签发（自己监听 80 端口应答挑战），不再下载 acme.sh，也不再需要 socat / python。ACME 账户与证书放在 `/etc/sing-box/acme/`（可用 `EASYSB_ACME_DIR` 覆盖），续期定时器仍旧由面板安装（`--install-renew-timer` / `--renew-certs`），续期判定改为读本地叶证书的到期时间，`install.sh` 不再要求 socat。

### 新增

- **服务解锁状态**（占原来「内核管理」的位置）：新增 `internal/unlock`，用 Go 重新实现 RegionRestrictionCheck 那套探测——Netflix（含“仅原创”判定）、Disney+、YouTube Premium、Amazon Prime Video、DAZN、TVBAnywhere+、Spotify、Reddit、TikTok、ChatGPT、Gemini、Claude、Steam、Bilibili 中国大陆 / 港澳台 / 台湾、巴哈姆特動畫瘋，共 17 项。每项只发 1-3 个请求并读响应体，判断不出结论时返回「检测失败 + 原因」而不是猜「解锁」；结果按「解锁 / 部分解锁 / 屏蔽 / 检测失败」统计，看板显示上次检测时间与各项计数，单项也可单独检测。同一条探测能在没有终端的环境跑：`easysb --unlock` 输出纯文本报告。
- **检测结果页可以上下滚动**：结果表格比框高时不再用「…还有 N 行未显示」收尾，而是 ↑/↓ 逐行、PageUp/PageDown 翻页、Home/End 到两端，框下面那行写明当前是第几行／共几行；按键提示里出现「↑/↓ 滚动」。这是唯一会滚动的页面，因为检测结果必须能读全——系统信息、硬盘、跑分最后几行往往正是要用到的。
- **看板可以自己挑**：`工具箱 → 看板设置` 按分组勾选（也可以全部勾选／全部取消），选择写进 `easysb-ui.conf` 的 `BOARD=`，重启后保留。没被问过的面板显示默认那几项（三类解锁、三网回程、IP 质量、系统信息）。看板按菜单顺序固定排列、每项一行、没跑过就写「未检测」，抬头是「已检测 N / M 项」。
- **看板用的是一句话结论**：工具可以给 `Result.Board` 一个短形式（如磁盘「写 458 MB/s · 读 3045 MB/s」），比 Summary 更短；没给就用 Summary。完整表格永远在它自己的报告里。
- **说明行只在主菜单上面**：二级、三级页面的条目说明跟在条目后面，按键提示上面的那一行留空但保留（按键提示因此固定在同一个位置，不再随页面上下浮动）；系统信息页那一行仍是它自己的字体提示。
- **界面固定成一套框，条目与说明各归其位**：任何页面都是同一套上下两个框，尺寸与位置照主页面——上框是主页面看板的高度，下框按面板里「一行一条」最高的菜单（硬件组六项 + 返回，九行）定高，两者之间的空行只在能给看板换来一行时才让路。终端的空缺由看板吸收：欢迎卡先丢引号、再丢标语、最后丢字标，活的数据始终在最上面，所以短终端上主页面仍然能读。
- **主菜单回到原来的样子**：条目只有编号和名字（两列），说明不再挤在条目旁边；二级页面一行一条、编号 + 名字 + 说明排在名字右边，出框时回到面板的两列布局（账号详情 12 项就是这样），而不是把条目藏到「+N」后面——看不见的条目等于进不去。
- **说明行回到按键提示上面**：光标所在条目的完整说明单独一行，紧跟在菜单框下面、按键提示框上面，和以前一样。
- **界面改成固定布局**：任何页面都是同一套上下两个框，尺寸与位置都照主页面（上框=主页面看板的高度，下框=菜单框的高度），条目少的页面下面留白。放不下的内容在框内截断并在末行写明还剩多少行，**面板任何页面都不滚动**；工具箱看板因此改成概要（「已检测 3 项 · 还有 11 项未检测」+ 最近几项的结论），不再把十几项硬塞进框里。运行与结果页把上下两个框合成一个大框，位置行数不变，所以干活的时候界面也不会变形。
- **下框按面板里最长的菜单定高**（账号详情 12 项 + 返回），主菜单只占其中一部分：菜单框若按主菜单定高，条目多的页面会把最后几项挤到框外，而不滚动就意味着那几项再也进不去。
- **运行中给真进度**：能算出总量的项显示百分比（解锁 5/9、三网回程 9/12、测速 2/4），算不出的（跑分、大文件传输）显示已用时间与最后一行进度；跑完进度条补满并自动切到结果页，不必再按回车。
- **按键统一**：Q（大小写都算）在任何页面任何层级都直接退出面板，返回只有 Esc，Enter 只用于进入与确认（结果页上 Enter 不做事）。看板的结论行也和结果页用同一套措辞，不再出现 `unlocked 8 · blocked 1 (9)`。
- **工具箱**（原「服务解锁状态」）：新增 `internal/toolbox`（每个工具返回同一张表格：Headers/Rows/Notes/Summary，外加注入 Options）与 `internal/toolbox/tools` 注册表，共 14 个条目分四组——解锁检测（流媒体 / AI / 区域，就是原来的 17 项）、网络检测（三网回程、就近测速、三网测速）、IP 与端口（IP 质量、邮件端口）、硬件与性能（系统信息、硬盘信息、CPU 跑分、内存测试、磁盘 IO（顺序 + 4K 随机）、多盘 IO）。结果改成表格显示，结论只保留「解锁 / 不解锁 / 未知」三个词（半能用记为不解锁，读不出来记未知绝不猜成解锁），地区列照抄各服务自己的判定；看板记录每项上次的结论与时间（存在 `/etc/sing-box/easysb-toolbox.json`，面板重启后仍在，文件损坏就当空看板），打开分组不会自动跑任何东西。
- 工具箱的条目移植自融合怪 ecs：三网回程用 ICMP 探测到电信/联通/移动的路径并用 ip-api 批量查 ASN 判回程线路；IP 质量查多家免费库并做 DNS 黑名单与 FCrDNS；邮件端口连自己的公网 IP 判断能否搭邮局；系统信息与硬盘信息读 /proc、/sys（SMART 只在 smartctl 存在时读，否则如实说读不到）；CPU/内存/磁盘是面板自研负载，**不是** geekbench/sysbench/fio 的分数，面板不下载任何外部二进制（因此这三样与 geekbench/fio/mtr/nexttrace 那类条目按设计不做）；就近测速与三网测速用 speedtest-go 直连 speedtest.net，三网筛不到节点时如实报告。
- 无终端也能跑工具箱：`sb --tool list` 列出条目，`sb --tool <id>` 打印该项的纯文本表格（与面板同一次运行、同一套判定）。
- `.github/dependabot.yml`：gomod 每周自动更新，patch 与小版本合成一个 PR、大版本单独开一个，方便盯 sing-box / lego 的主版本升级。
- `internal/ui` 新增 `Table`：列宽按内容自适应、最后一列吃剩余宽度、每个单元格可带色调、永不换行（放不下就截断），所以一个工具想返回多少行都不会把面板撑破。
- `easysb core check -c <config>` / `core version`：前者用面板里那套引擎校验配置（部署路径用的就是它），后者打印本二进制携带的 sing-box 版本与能力位。

### 变更

- 版本行与系统卡片显示 `1.14.2 · 带流量统计`（内核版本 + 本构建能否计流量），不再显示「正式版/测试版」「作者源/官方源」这类已不存在的选项。
- 状态文件不再写 `CORE_CHANNEL` / `CORE_SOURCE` / `STATS_API`：它们描述的是可下载、可切换的内核。旧文件照常读入，下次保存时丢掉这三个键。
- 主菜单第一项由「内核管理」变为「服务解锁状态」，随后再改名为「工具箱」：内核管理的位置空出来后先放解锁检测，现在这一栏是整套测评；四套皮肤的导航分组同步调整（原「内核」分组改名「检测」，其余分组内位置不变）。
- 任务页示例输出与内核相关的文案改为节点/订阅语境。

### 修复

- 服务解锁状态的 Prime Video 不再把「可用」读成失败：店面前台有好几种形态，地区标记可能落在 1 MB 之后，也可能整页只有约 40 KB、不含任何地区信息（偶尔还返回 503）——同一台机器连着发同样的请求，来哪种并不固定，参考脚本自己的 `curl` 同样会碰到精简版（实测那边 1/3、这边 3/6）。现在会读到标记为止（上限 8 MB），并在报失败前重试，最多 3 次请求；失败文案写明读了多少 KB、用了几次。真机实测同一台机器由「检测失败」变为「解锁 US」。
- Claude 即使拿到 Cloudflare 挑战页也会给出地区（从 `claude.ai/cdn-cgi/trace` 读），结论仍如实记为「检测失败」——挑战不构成国家判定；参考脚本在这种情况下会因为 URL 未变而判「可用」，那是猜。
- YouTube Premium 补齐参考脚本的最后一道门槛（要求页面含 `ad-free` 才判可用，并读到该标记为止）：原来只要读到地区、没有否定标记就算可用，可能把「只是提到地区」的页面当成可用。真机复测 `unlocked US`，与参考脚本同款命令的判定一致（region=US、ad-free=1）。
- README（中英）新增「与参考脚本的差异」一览：17 项里 16 项与他同接口同字段，逐条列出刻意不同之处（Prime Video 重试、Claude 挑战页口径、ChatGPT 读 `unsupported_country` 的值、Disney+ 补 `location`、Steam 解析方式、TikTok 为他没有的项、IPv6 不分支、其余项取舍）。
- `.gitattributes` 固定 `*.sh` 与 `release/TAGS` 为 LF：CRLF 检出会让 `install.sh` 在 Linux 上直接跑不起来，也会把回车带进 `-tags` 值。

### 移除

- 「内核管理」整页（切换内核 / 更新内核 / 通道×来源四组合与相关状态键），「版本更新」保留。
- `internal/core` 的发布发现、下载、解包、安装、切换、UUID 与 Reality 密钥生成（后两者早就由 `internal/secret` 本地生成），以及 `deploy.ErrNoCore` 与「内核未安装」类提示——内核永远在。
- acme.sh 的下载与安装、`cert.EnsureACME` / `cert.ACMEInstalled` / `cert.ACMEDir` / `cert.CheckPort`（改名 `CheckPort80`），`install.sh` 的 socat 依赖。

### 说明（未发布历史）

- 下面 `[4.3.0 之前未发布]` 一节记录的是 5.0.0 之前、随本版本一并移除的「内核来源与通道」改动，保留在此仅供追溯；对应的代码与菜单已不在仓库里。

## [4.3.0 之前未发布]

### 新增

- **带流量统计的内核构建工作流**：`.github/workflows/singbox-v2ray-api.yml` 用上游同一份源码加一个 `with_v2ray_api` 标签，重建正式版与测试版两条通道（滚动 release `singbox-stable` / `singbox-alpha`），产物与官方发布**同名**（`sing-box-<版本>-linux-<架构>.tar.gz` + `.sha256` + `version.ini` 版本戳），因此面板可以按同一套命名取用。上游版本没变就不重复构建；支持手动 dispatch（可选 both/stable/alpha 与强制重建）与每日定时。为什么需要它、光环标签怎么来、加架构要动哪几处：见 `docs/core-builds.md`。
- **通道资产保留策略**：`prune` 任务每次运行都清掉通道不再提供的旧内核包（`scripts/prune_release_assets.py`）。固定通道 tag 让 release 永远只有两个，但包名带版本号，不清的话每个新版本都会在同一 release 里再堆 14 个文件；默认每个通道只留当前版本（15 个资产），dispatch 时可选 `keep_generations`（2=连上一代一起留，0=全留不清理）。

### 变更

- **内核管理的菜单收敛成三项**：`切换内核` / `更新内核` / `返回上一级`。切换内核点进去是**通道 × 来源**的四个组合——正式版·作者源、测试版·作者源、正式版·官方源、测试版·官方源，上下选择回车即装；当前已安装的那个组合在说明里标「当前」（读记录的状态，没记录就不标，以看板读内核本身的结果为准）。切换与更新走同一条安装路径，`更新内核` 会**保留当前来源**只刷新版本（选了官方源的人不会被悄悄换回作者源）；装完若内核能力与现有配置不符，依旧自动按新内核重新生成配置。`--render --screen kernel-switch` 可单独渲染这个页面核对版式。
- **二级页面与主菜单同版式**：去掉左侧导航栏，二级页面改为整幅两框——上框是该栏目自己的**看板**，下框是该页面的**菜单**（框标题就是页面名，例如「域名管理」）。返回上一级仍是 `Esc` / `←` / `[ n ] 返回上一级`，栏目之间靠主菜单切换。窗宽较窄时不再需要为导航让位，选项与说明拿到整行宽度。
- **下载显示进度条**：内核安装（稳定版 / 测试版 / 切换 / 更新）、脚本更新、BBR 内核包下载时，任务卡片首行显示实时进度条（文件名、进度条、百分比与已下载 / 总大小）；服务端不给长度时退化为只报字节数。进度读数由下载层（`internal/core` 的 `Progress`）上报，TUI 侧经 `taskReporter.Progress` 汇总到任务面板。
- **BBR 内核安装后明确提示需要重启**：安装完成的日志追加「新内核需重启服务器后生效：执行 reboot 后回来再看一次状态」，任务结束后菜单上以警告样式再提示一次；BBR 看板在「已安装内核」落后于运行内核时增加一行「新内核待重启生效」。
- `--render --screen task` 可预览任务页（含进度条样本），用于核对版式。
- **内核管理默认从作者源安装**：`core.FetchPreferred` 读 `singbox-stable` / `singbox-alpha` 的 `version.ini` 版本戳再拼下载地址，通道还没发布过就回落官方发行版；新增「切换到官方源内核」作为退路，安装前日志先说明官方源不含流量统计。装的来源会被记下来（状态键 `CORE_SOURCE`），面板在**每个页面**的版本行与内核页看板的「内核来源」行显示 **作者源 / 官方源**，并标明能否统计流量；没有记录时按内核自身的构建标签判断，因此老版本面板装过的机器也能显示对。
- **换内核来源会自动按新内核重新生成配置**：官方内核拒绝整份带 `v2ray_api` 的配置（节点起不来），而带统计能力的内核拿一份没有该块的配置则计不了流量。内核安装结束后比对「配置内容 vs 内核能力」（`configMatchesCore`），不符就复用部署流程重新生成配置并重启服务——切换来源不再把节点留在起不来的状态，切回作者源也立刻恢复计费。为此把部署正文抽成 `runDeploy`，节点菜单与内核安装共用同一条部署路径。

### 修复

- **从官方源切回作者源不再是无操作**：「安装正式版/测试版内核」原先只比通道，装的是官方内核时会判「当前已是该通道」直接返回——于是切到官方源之后菜单里没有任何一条能把作者源装回来（只能用「更新内核」）。现在判断把**来源**也算进去（同一通道且同一来源才跳过），`[1]/[2]` 会真的重新下载作者源内核，并按新内核重新生成配置、恢复流量统计。
- **订阅服务不再指向"运行中的那份副本"**：服务单元以前用 `os.Executable()` 写 `ExecStart`，于是从临时副本跑一次**部署**（测试构建、解开的目录、`/root/easysb-new` 这类）就会把订阅服务的启动路径改成那个副本——副本一删，订阅服务就再也起不来（真机复现：删掉副本后服务 `status=203/EXEC`、订阅 HTTP 000）。现在优先使用**已安装路径**（`sysinfo.PanelPaths`，与卸载脚本共用同一份列表），只有从未安装过时才退回指向自身；判定还要求目标确实是可执行文件，避免半截下载被当成面板。
- **订阅文件的下载名固定为 `EasySB`**：此前用的是账号令牌（`<token>.yaml` 等），等于把一条凭据放进下载目录，每个账号名字还不一样。现在固定为 `EasySB`，不带后缀，并同时下发 RFC 6266 的两种形式 `filename=EasySB; filename*=UTF-8''EasySB`——Clash 系客户端（Clash Verge Rev / Clash Orbit）把响应头当 Debug 字符串解析、只去掉最外层引号，带引号的 `filename="EasySB"` 到它们手里会变成 `\"EasySB\"`，而它们优先读的 `filename*` 不受影响。
- **「版本更新 / 卸载脚本」不再把页面顶坏**：这两个主菜单项以前是「就地执行」的动作（不打开新页），但仍然把 current section 设成了自己，于是回车后画面变成「上框 = 版本信息看板 + 下框 = 不复存在的主菜单」——选项丢了两列排版、按键提示变成「Esc 返回」而 Esc 实际会直接退出面板。现在它们与其它主菜单项一致：回车进入自己的页面（上框是本页看板、下框是本页动作 + 返回上一级），Esc 正常返回主菜单；并加了一条不变式（只有会开新页的条目才设置 section）与两个回归测试。
- **节点部署在官方内核上不再失败**：官方 `SagerNet/sing-box` 构建不含 `with_v2ray_api`（该标签上游默认关闭），配置里的 `experimental.v2ray_api` 会让 `sing-box check` 直接 FATAL（`v2ray api is not included in this build`），此前部署永远走不到「启动服务」那一步。现在部署前探测已装内核是否带该标签：不带就**不写**这一段，并在任务日志里明确说明「本次部署不含流量统计，节点与账号照常可用」，订阅服务的计费循环也随之跳过采样（不再每周期报一次连接错误）；装的是带该标签的内核时行为完全不变。新增状态键 `STATS_API`（`none` 表示当前内核不计流量，缺省即历史行为）。
- 任务结束后重新读取栏目看板：BBR 内核安装完成回到页面时，看板此前仍显示「已装内核 0」，现在任务结束会重新读取本机状态。

## [4.2.2] - 2026-09-24

### 变更

- **页面框架固定为两个框**：上框永远是当前页面的**看板**，下框永远是当前页面的**选项**；主菜单与二级菜单只是换掉这两个框里的内容，框架本身不变。
- 每个二级目录都配上自己的看板：内核信息、节点信息、域名信息、订阅信息、账号概况、服务信息、BBR 信息、版本信息、面板信息（不再只有节点管理有看板）。
- BBR 看板在进入栏目时读取本机状态，不再先显示「未读取」；「加速状态」按内核实际使用的拥塞算法判定（`net.ipv4.tcp_congestion_control`），不再把运行内核版本当成加速开关。
- 二级页面的选项框不再带栏目标题（左侧导航已经标明栏目）；仅在窗口过窄、导航收起时用栏目名作为该框标题。
- `--render --screen <栏目 id>` 可渲染任意带子菜单的栏目，方便逐页核对版式。

### 修复

- 订阅信息的「统计间隔」在未部署时显示「未设置」，不再显示 `0s`。

## [4.2.1] - 2026-09-24

### 变更

- 主菜单也是**一个框**：大字标记、副标题、格言、运行概况与主菜单选项共用同一个边框，中间用两条横线分隔（第二条带「主菜单」标签）。这样主菜单与二级页面的形状完全一致——上面看板、下面选项、一个框——主菜单不再比二级菜单多出一个框。

## [4.2.0] - 2026-09-24

### 变更

- 主菜单版面重排：最上面是「EasySB」看板——大字标记、副标题、格言与运行概况**合并在同一个框里**（中间一条横线分隔），下面是主菜单，再下面是当前选中项的说明与按键提示。按键提示不再钉在屏幕底部而是紧跟内容，高屏终端不会再出现「菜单与提示之间一大片空白」。
- 菜单选项改为数字编号 `[ 1 ] … [ 10 ]`，主菜单与二级菜单一致，「返回上一级」也参与编号；左侧导航保持图标，因为它同时承担分组与「你在哪」两件事。
- 二级菜单合并为一个框：上半是该栏目自己的看板（例如「节点信息」的订阅端口、统计间隔、端口跳跃范围、偷用域名与 short_id），中间一条横线，下半是选项。栏目名不再在卡片标题、面包屑与左侧导航里重复三遍；窗口过窄收起导航时，框内首行改显示面包屑，仍能看出所在位置。
- `--render --screen node` 可直接渲染节点页，便于核对版式。

## [4.1.1] - 2026-09-24

### 新增

- 新增「选择版本安装」（BBR 管理）：打开时实时拉取内核项目已发布的版本（标准版 / Max 版，按本机架构过滤），新版本排在最前，`↑`/`↓` 选中后 `Enter` 安装指定版本；每行标注所属版本线（标准版 / Max 版）、是否为该版本线的最新、以及是否已安装或正在运行。列表每次打开都重新拉取，对面仓库发了新内核，这里直接就能看到，本仓库不需要跟着发版。
- 「查看 BBR 状态」在已装内核落后于最新发布版本时，直接给出「有更新的内核：x.y.z」与去哪里装。

### 修复

- 修正「最新内核版本」的判定：Linux-BBR-v3 的 `version.ini` 每次构建**追加**一节 `[kernel]`（真机实测同时存在 `7.2.0` / `7.2.2` / `7.2.6` 三节），此前只读第一节，于是把该项目最旧的版本当成了最新——面板会给出 7.2.0，而仓库里实际最新是 7.2.6。现在读取全部 `[kernel]` 节取最高版本，并且**优先使用 release 列表**（按本机架构过滤）判定最新，版本戳只作为网络不可用时的兜底：最新版必须同时是「装得上」的版本。
- 最新版本按本机架构过滤（`NewestVersionFor`）：仅 arm64 发布的 7.3.0 不会再让 x86_64 的机器去下载一个不存在的包。
- 主菜单的方向键：主菜单是双列卡片，此前 `↑`/`↓` 按条目移动，光标走到左列底部时会跳到右列顶部。现在 `↑`/`↓` 在所在列内上下移动（到列尾环绕），`←`/`→` 在左右列之间切换并保持同一行；窗口过窄回到单列时，`←` 仍是返回、`→` 仍是进入。底部按键提示相应显示「`←`/`→` 换列」。

### 内部

- 内核版本列表的过滤与排序抽成纯函数 `releasesFromTags`，用任意版本号的 tag 列表单测：版本号全部来自 release 列表，仓库里不写死任何内核版本（`--render` 预览用的示例列表已更名为 `previewReleases` 并注明只用于离线渲染）。
- 新增 `bbr.LocalStatus`（只读本机、不联网）与 `Collected.Outdated`；`Status.CustomVersion` 从 `7.2.6-minimaxflora-bbrv3` 里取出 `7.2.6` 参与比较。

## [4.1.0] - 2026-09-24

### 新增

- 新增「BBR 管理」页（主菜单「系统」分组）：一页读取运行内核、拥塞算法、队列算法、内核支持的算法、已装的 BBRv3 内核与最新发布版本。`启用 BBR 加速` 支持 `fq` / `fq_codel` / `fq_pie` / `cake` 四种队列算法，自动加载 `tcp_bbr` 与对应的 `sch_*` 模块、写入 `net.core.default_qdisc` 与 `net.ipv4.tcp_congestion_control=bbr`，并落到 EasySB 自己的 `/etc/sysctl.d/99-easysb-bbr.conf` 与 `/etc/modules-load.d/easysb-bbr.conf`——不占用内核项目自己的 `99-minimaxflora.conf`，两个工具可以在同一台机器上共存。写盘前先记下原有取值，`清空 BBR 配置` 按记录还原，因此它是一次真实的撤销，而不是假定回到某个默认值。
- 新增 BBRv3 内核安装：直接消费 [Linux-BBR-v3](https://github.com/MinimaxFlora/Linux-BBR-v3) 发布的预编译内核（标准版 / Max 版，x86_64 / arm64）。版本号先读该项目的 `version.ini`（一次小文件请求），失败再翻 release 列表；下载用 release 自己的资产清单，先 `dpkg-deb -I` 验证可读、再清理旧版内核包、`dpkg -i` 安装并 `update-grub`。内核包单列 20 分钟下载预算，且直连 GitHub——安装到机器上的内核字节不经过第三方镜像。过旧系统（Debian < 12、Ubuntu < 24.04、非 Debian 系）直接拒绝安装：内核与用户态不匹配不会当场报错，而是重启时才炸，在 VPS 上这比安装被拒难收拾得多。安装与卸载都不自动重启，面板会明确提示重启后生效。

### 变更

- 主菜单改为整幅双列卡片：根界面收起左侧导航，主菜单 10 项分两列排布（左 5 项、右 5 项），选中项的说明紧贴在菜单卡片下方，不再固定在最底部；窗口变窄时自动回到单列。左侧导航仍在子页面保留，并在「系统」分组里标出当前栏目。
- 主菜单不再重复展示「设备信息」与「账号概况」两张卡片（前者在「系统信息」页，后者在「账号管理」里），腾出的高度留给「运行概况」。

### 内部

- 新增 `internal/bbr`：内核版本比较、release tag 与资产名解析、`/etc/os-release` 判定、`version.ini` 解析等纯逻辑全部单测覆盖；真机冒烟测试带 `EASYSB_BBR_WRITE=1` 才改机器，覆盖「启用 → 清空」往返并校验还原值。
- `internal/core` 新增 `DownloadWithin`：内核包这类远超内核 tar 包的下载可以给更长的预算，`Download` 保持原有五分钟不变。

## [4.0.0] - 2026-09-24

### 重大变更

- 界面重构：主界面改为左侧分组导航 + 右侧卡片内容的两栏布局，顶部固定一条状态条（主机、服务、节点、内核、内存、磁盘、负载），底部固定按键提示；左侧导航实时标出当前所在栏目，并始终显示当前焦点项的说明。卡片标题改为实心强调色标题栏，卡片底色渗透到每个字符（不只是首列）。面板宽度上限 100 列，宽度不足 76 列时自动收起导航，主菜单回到单栏卡片形态。
- 新增皮肤框架 `internal/theme`，四套皮肤与深/浅两套配色自由组合，由 `--skin` 或 `EASYSB_SKIN` 选择：`jade`（默认，圆角卡片 + 背景色底 + 标题实心玉色标题栏 + 黄铜第二强调色，最宽敞）、`aurora`（青紫渐变标题）、`ember`（双线边框、橙粉强调色）、`graphite`（无边框极简，方便复制文本、最紧凑）。窗口宽度不足 76 列时自动收起左侧导航。
- 新增「系统信息」页（主菜单 → 系统信息）：一页看全运行环境（终端类型与尺寸、字形预览、主机/系统/内核/时区/负载、脚本与内核版本、服务与节点状态），并能在界面内即时换外观——`↑`/`↓` 加 `Enter` 或 `A`-`D` 选皮肤、`T` 切深浅、`I` 切符号/ASCII、`L` 切语言，改完下一帧生效且此后配色不再跟随终端；页面沿用同一固定框架，顶部状态条与底部按键提示不动。调试用 `--render --screen system` 可直接渲染该页。
- 账号化：移除节点级 `UUID` 与 `密码`，改为 `/etc/sing-box/easysb-users.json` 中的多账号模型。每个账号拥有各协议独立凭据、流量限额、有效期、可用协议与启用开关；停用、过期、超额账号会自动从内核配置中移除，恢复后自动加回。客户端需要按账号重新导入订阅。
- 订阅改为内置服务：删除 `internal/nginx` 与静态订阅目录 `/etc/sing-box/subscribe/`，由 `easysb --serve`（`easysb.service`）在 `SUB_SERVE_PORT`（默认 `8443`）上提供唯一端点 `/sub/<令牌>`，按 User-Agent 返回 sing-box JSON、mihomo YAML 或 Base64 分享链接文档。旧端点 `/subscribe`、`/singbox/<uuid>`、`/mihomo/<uuid>`、`/v2ray/<uuid>` 不再提供。
- 流量统计与自动处置：订阅服务每 `SUB_SYNC_SECONDS`（默认 `300`）秒读取内核 `StatsService` 计数并累加到账号，跨过限额或到期时重启内核生效；响应头 `Subscription-Userinfo` 向客户端上报已用流量、限额与到期时间。
- 状态键变更：新增 `SUB_SERVE_PORT`、`SUB_SYNC_SECONDS`，移除 `SUB_PORT`、`SUB_PATH`；菜单新增「账号与流量」，「订阅管理」改为订阅端点与服务管理。
- 订阅地址的 TLS 判定统一走 `cert.Usable`：只有存在真实证书时才以 HTTPS 提供服务并输出 `https://` 地址，否则明文 HTTP 并在面板提示，避免客户端拿到与监听协议不符的地址。

### 新增

- 图标方案重做：不再依赖 Nerd Font，默认换成一套单宽 Unicode 符号（宿主 ⌂、内核 ⬢、节点 ▴、域名 ◈、账号 ◉、订阅 ⇅、服务 ⚙、更新 ↻、卸载 ⌦、运行 ●/○、启用 ✓/✗、信息 ⓘ），终端自带字体即可对齐显示；`--icons ascii` / `EASYSB_ICONS=ascii` 提供纯 ASCII 回退（`on`/`off`/`nerd`/`1`/`0` 仍被接受），每个字形都有单列宽度单测守护。
- 安装脚本不再下载安装 Nerd Font，减少一次网络下载与一项系统依赖；`--no-font`、`--font-only` 仍被接受但不再做任何事。
- 设备信息面板扩充：本机 IPv4 与 IPv6 分列显示，新增运行时间、CPU 型号与核心数、系统负载、内存与磁盘占用。
- 任务/二维码页新增复制与鼠标控制：按 `C` 通过 OSC52 将整段日志复制到系统剪贴板，按 `M` 释放鼠标以便拖拽选择文本。
- 新增 `--theme auto|dark|light`（环境变量 `EASYSB_THEME`）：启动时自动探测终端背景色，亮色背景自动切换为浅色配色，也可手动强制指定，避免在白色终端下界面几乎不可读。
- 界面偏好可记忆：主题皮肤、亮/暗配色、符号方案与语言选择会写入 `/etc/sing-box/easysb-ui.conf`，下次启动自动恢复；命令行参数与已导出的环境变量优先级更高，可用 `EASYSB_UI_CONF` 指定其他路径。
- 域名管理新增「立即续期」与「续期定时器」两项：前者手动跑一次 acme.sh 续期并重载 sing-box 与订阅服务，后者安装或移除续期定时器（systemd timer / OpenRC），并显示下次执行时间。
- 新增 `--renew-certs`：续期全部证书后重载节点配置、sing-box 与订阅服务，供续期定时器调用。现在只在确实续下新证书时才重载，详见下方修复。
- 新增 `--install-renew-timer` 与 `--remove-renew-timer`：在命令行安装或移除续期定时器，与域名管理里的同名菜单等价。定时器单元里写的是当前二进制的真实路径，所以必须由那个二进制自己写入——无终端、用脚本部署的场景也能装上正确的定时器。
- 新增 `EASYSB_ACME_STAGING=1`：改用 Let's Encrypt 测试端点申请证书，便于在不消耗正式配额的前提下验证域名解析与端口是否可用（签发的证书不受信任）。
- 申请证书前新增预检：检查 socat / python 是否存在、域名当前解析到哪些地址、本机公网地址是什么，解析到别处时提示 CDN / Cloudflare 代理需要先关闭；80 端口被占用时直接报出占用者，不再让 acme.sh 的超时来报错。
- 订阅支持多客户端：mihomo / Clash Meta 完整配置（`mihomo.yaml`）与 v2rayN 分享链接文档由同一个 `/sub/<令牌>` 端点按 User-Agent 提供，可用 `?client=` 强制指定格式。
- 适配 OpenWrt 客户端：`passwall`、`passwall2`、`homeproxy` 使用 Base64 分享链接文档；`luci-app-nikki` 使用 mihomo 内核，订阅需要含顶层 `proxies`，因此返回 mihomo YAML 配置。
- 订阅二维码按客户端分别生成导入链接：sing-box 使用 `sing-box://import-remote-profile?url=`，mihomo 与 v2rayN 使用纯订阅地址（Clash 系客户端的扫码导入会把二维码内容直接当作订阅 URL 抓取，`clash://install-config?url=` 仅适用于系统级深链点击）。
- 新增 `templates/config/mihomo.yaml` 可读样例，与内嵌模板 `internal/subscribe/mihomo.yaml` 保持同步。
- mihomo 配置对齐完整桌面方案：新增 `external-controller`（`0.0.0.0:9090`）、`secret`、`external-ui`、`external-ui-url`（Zashboard）、`unified-delay`，补全 fake-ip DNS 与 `fake-ip-filter`，策略组改为 `负载均衡` / `自动选择` / `🌍选择代理节点`，规则新增 `GEOIP,LAN,DIRECT` 与 `GEOSITE,CN,DIRECT`。

### 修复

- 修复在最小化系统镜像上无法安装 acme.sh、因而完全无法申请证书的问题：旧实现调用的是 `get.acme.sh` 包装脚本，它无论是否需要都会先要求系统存在 cron，缺 cron 时直接以 `Pre-check failed, cannot install` 失败，并在末尾附上中国区安装说明链接，把真正原因埋掉。现在改为直接下载 acme.sh 官方脚本并带 `--nocron --noprofile --home` 安装，不再需要 cron；自动续期改由面板自己的 systemd timer / OpenRC 脚本驱动，续期后自动重载 sing-box 与订阅服务。已在 Debian 13（trixie）最小化镜像（systemd，无 cron / crontab，有 socat 与 python3）上验证安装与 `--version` 均成功。
- 修复签发机构不确定的问题：acme.sh 的默认 CA 已从 Let's Encrypt 换成 ZeroSSL，旧实现只在启用 staging 时才传 `--server`，因此正式证书的签发者取决于服务器上装的是哪个版本的 acme.sh，而且与 staging（Let's Encrypt 测试端点）不是同一家。现在固定 `--server letsencrypt`（staging 时 `letsencrypt_test`），同一份代码在任何 acme.sh 版本下都得到 Let's Encrypt 证书。已在真实域名上验证：链为 leaf ← `CN=YE2` ← `Root YE` ← `ISRG Root X1`，`Le_API=https://acme-v02.api.letsencrypt.org/directory`。
- 修复对仍有效的证书重复申请时报错的问题：acme.sh 会以非 0 退出并输出 `Domains not changed. | Skipping. Next renewal time is: …`，旧实现把它当成签发失败并把这句话当作错误提示。现在识别为“证书仍有效”，记录下次续期时间并正常返回。
- 申请证书的预检现在会识别“域名有多条 A 记录、其中一条不属于本机”的情况：Let's Encrypt 会校验每一条 A 记录，一条失效（旧 IP、已下线的服务器）就会让整个申请失败，而报错里的 `During secondary validation: <ip>: … Connection refused` 很容易被误读成服务器故障。预检在申请前就列出这些地址，失败时也会点名该地址并直接给出“删除多余记录”的建议。
- 修复证书命令失败时错误信息不可读的问题：旧实现只取输出最后一行，而 acme.sh 的最后一行往往是中国区 wiki 链接或 `Install error`。现在会挑出含原因的行（`Pre-check failed`、`Please install socat`、`Domain not verified` 等）并去掉时间戳。
- 修复 acme.sh 目录依赖 `$HOME` 的问题：安装脚本、sudo 与 systemd 单元传入的 `HOME` 并不一致，导致面板按 `$HOME/.acme.sh` 找证书却什么也找不到。现在先探测哪个目录真的有 acme.sh，找不到再回退；所有 acme.sh 调用都显式带 `--home`，保证写入位置与读取位置一致。
- 修复同一域名同时存在 RSA 与 ECC 证书时切换菜单出现重复项的问题。
- 修复任务页（申请证书、安装内核、部署节点等所有动作的落地页）与主面板观感割裂的问题：现在与主面板同一套框架——顶部实时状态条、中间带标题栏与状态徽标的卡片、底部同一处按键提示。
- 修复续期在没有任何证书需要更新时也会重载服务的问题：`--renew-certs` 与面板的「立即续期」现在只在确实续下了新证书时才重载 sing-box 与订阅服务。续期定时器每天都会跑一次，旧行为等于每天凌晨断一次连接。另外 systemd 安装 `Persistent=true` 的定时器时会立即触发一次首次运行（弹出确认后瞬间就会执行），同样的逻辑让这次首次运行也变成空操作。
- 修复 `internal/cert` 两个测试依赖真实 `$HOME` 而在开发机上必失败的问题：改为通过 `EASYSB_ACME_HOME` 指向临时目录，并新增续期输出解析、错误摘要、域名去重等用例。

- 修复生成的 sing-box 订阅 JSON 被编码为字母序的问题：现在保留模板中的顶层分区顺序与节点内参数顺序，与 `templates/config/tun-fakeip.json` 可读样例一致。
- 修复任务页启用鼠标捕获后无法用鼠标选中并复制订阅链接的问题：现可用 `C` 直接复制，或按 `M` 释放鼠标后原生选择。
- 修复公网 IP 探测在双栈主机上返回 IPv6 的问题：探测端点改为优先使用仅 IPv4 的接口。
- 修复 OpenWrt 客户端订阅后节点丢失密码：homeproxy 会丢弃含百分号转义的 userinfo，标准 Base64 密码（`+` `/` `=`）会静默丢失密码。生成密码现改为纯字母数字（22 字符），v2rayN、passwall、passwall2 与 homeproxy 均可正常读取。
- 修复部分解析器读取 VMess 节点加密方式为空的问题：分享链接在 `scy` 之外同时写出 `security`，兼容只读 `security` 的解析器。
- 修复 homeproxy 报「请输入有效 uuid」：VLESS 分享链接保持带连字符的标准 UUID，不再输出 32 位紧凑形式（homeproxy 会用 LuCI 的 `uuid` 校验节点）。
- 订阅界面按插件列出兼容客户端：mihomo YAML 面向 mihomo / Clash Meta / luci-app-nikki，Base64 文档面向 v2rayN / passwall / passwall2 / homeproxy，并分别说明两种格式。
- 修复主菜单选中行光标长度随行内描述长短变化的问题：选中条现在统一填充到面板内宽。
- 修复分享链接生成失败：AnyTLS 与 Hysteria2 URI 在查询串前缺少 `/`，且密码未做百分号编码，导致客户端拒绝导入。
- 修复 mihomo 二维码无法被 FlClash 等 Clash 系客户端识别：二维码改为纯订阅地址，不再包装 `clash://install-config?url=`。
- 修复任务/二维码页无法用鼠标滚轮滚动：仅在任务页开启鼠标上报，并将滚轮与 ↑/↓ 的滚动步长统一为 3 行，同时支持 PgUp/PgDn 翻页。
- 修复主菜单选中态只高亮标签、描述仍为暗色的问题：选中行现在整行高亮（含描述），子菜单与返回行同样处理。
- 修复二级菜单中按 `q` 只返回上一级的问题：`q` 现在任意层级都直接退出，返回上一级使用 `Esc` / 左方向键 / 返回行。

### 变更

- 设备面板用「交换空间」替换「公网 IP」，CPU 仅显示核心数，系统仅显示发行版名称，面板更精简。
- 运行概况首行改为「服务 / 节点」，版本与内核下移到第二行，常用状态更靠前。
- 二级菜单每项后补上功能概述，与主菜单的展示风格保持一致。
- 任务页按键提示去掉 `PgUp/PgDn 翻页` 文案，翻页快捷键仍可用。
- 生成密码由标准 Base64（24 字符，可能含 `+` `/` `=`）改为 URL-safe Base64，再改为纯字母数字（22 字符），兼容 v2rayN、passwall、passwall2 与 homeproxy。旧实例的密码若含 `+` `/` `=` `-` `_`，部分客户端仍会丢失密码，需重新生成密码或重新部署后生效。
- 目录名统一小写：`Templates/` → `templates/`，子目录改为 `anytls`、`hysteria2`、`tuic`、`vmess-websocket-tls`、`vless-vision-reality`、`config`。
- README 主文档改为英文 `README.md`，中文版迁移到 `README_ZH.md`。
- 新增 `docs/` 面向其他 Agent 与协作者的工程文档，并在根目录提供 `AGENTS.md` 索引。

## [v3.0.0] - 2026-09-20

### 新增

- 全量 Go 重写：移除 bash 实现，基于 bubbletea / bubbles / lipgloss 的深色全屏仪表盘 TUI，编译为单一静态二进制，以 `sb` 呼出。
- 目录重组：五个协议样例与订阅模板统一归入 `Templates/`，订阅模板移至 `Templates/Config/tun-fakeip.json`；内核管理改为安装 stable / 安装 alpha / 通道切换 / 更新当前通道。
- 命令参数改为 Go flag：`--language`、`--icons`、`--apply-firewall`、`--render`、`--version`、`--help`。
- 仪表盘改为多卡片布局：新增设备信息、节点信息卡片，按键提示独立成框，菜单项以图标展示。
- 发行流程改为 `.github/workflows/easysb-go-release.yml` 交叉编译多平台二进制，以版本 tag `v3.0.0` 发布，并在 `--version` 中输出构建短哈希以便核验。

### 变更

- 版本号与构建提交由编译期注入（`main.version` / `main.commit`），取代 `EasySB/VERSION` 文件。

### 移除

- 移除 `legacy/EasySB/` bash 源码、`tests/`、`build.sh`、`config.conf` 非交互安装模板与旧 bash 发布工作流。

## [v2.1.0] - 2026-09-20

### 新增

- 主菜单改为七项：内核管理、节点管理、域名管理、订阅管理、服务管理、脚本更新、卸载脚本。
- 节点管理：面板展示域名 / 密码 / UUID / 端口跳跃 / Reality 密钥与 short_id / 端口；一键部署支持五协议多选（回车全选）；参数设置含 Reality 偷用域名预设。
- 内核管理：正式版 / alpha 切换保留配置，「更新内核」只更新当前通道。
- 本地一言库（中英随语言），Banner 改为左侧竖线开框（右侧不闭合）。

### 变更

- 脚本版本改为读取 `EasySB/VERSION`，修复 `/etc/os-release` 的 `VERSION` 覆盖脚本版本的问题。
- 证书申请增加 `--force`，重复申请 / 续期不再因已有域名密钥失败；激活证书后自动应用到节点配置。
- 卸载保留 acme 证书，配置备份到 `/root`。
- 订阅改为手动触发生成。

## [v2.0.0] - 2026-09-20

### 新增

- 脚本整体重写为五合一部署器：AnyTLS、Hysteria2、TUIC v5、VMess + WebSocket + TLS、VLESS + Vision + Reality。
- 内核来源切换为官方 `SagerNet/sing-box` Releases，正式版取 latest release，内测版取 prerelease，支持安装、卸载、替换（保留配置）。
- 菜单顶部常驻版本面板：脚本版本、本地内核、正式版、alpha 版，并标注「可更新 / 已是最新」。
- 证书管理：基于 acme.sh `--standalone` 申请与续期，支持列出证书、切换激活证书、删除证书；申请前检测 80 / 443 占用并可临时停止占用服务。
- 协议参数：五协议共享一个 UUID 与一个密码，均支持回车自动生成；Reality 密钥对自动生成，偷用域名默认 `apple.com`。
- Hysteria2 端口跳跃：默认范围 `2080:3000`，自动下发 iptables / nftables DNAT 规则，并生成开机恢复单元（systemd `easysb-firewall.service` / OpenRC）。
- 订阅管理：基于 `Templates/tun-fakeip.json` 渲染，输出本地订阅文件、终端二维码与五类分享链接；由 nginx 以静态站点形式提供 `https://域名:端口/subscribe`。
- 非交互安装：`EasySB/config.conf` KV 模板配合 `--config`，另支持 `--install`、`--replace`、`--uninstall`、`--apply-firewall` 等参数。
- Alpine / OpenRC 与 Debian / Ubuntu / systemd 双平台支持。

### 变更

- `EasySB/lib/` 模块重组为 12 个文件（`00-header.sh` 到 `11-entry.sh`），职责按内核、证书、协议、防火墙、服务、订阅、菜单划分。
- 语言选择前置为启动首屏，选定后进入主菜单；中英双语全流程一致。
- 订阅模板全部取自本仓库 `Templates/`，远端只依赖本仓库与官方内核仓库。
- 包安装、下载、端口提示统一为非交互与可默认执行的方式。

### 移除

- 不再支持 Argo 隧道、WARP、ShadowTLS、Shadowsocks、Trojan、NaiveProxy 等旧协议与旧菜单项。
- 不再自行编译内核，内核统一取自官方 Releases。

## [v1.3.25] - 2026-09-18

### 新增

- `Templates/config-rule.yaml`：Clash / Mihomo 订阅模板，内嵌节点与分流规则，不依赖 `proxy-providers`。
- `EasySB/lib/` 模块化源码，`EasySB/build.sh` 按编号合成单文件发行版。
- `EasySB/tests/` 测试套件，覆盖构建、文案、静态断言与 lint；`.github/workflows/easysb-release.yml` 在 CI 中执行。

### 变更

- 订阅模板来源改为本仓库 `Templates/`。
  - `Templates/config.yaml`：Clash / Mihomo 订阅，使用 `proxy-providers`。
  - `Templates/config-rule.yaml`：Clash / Mihomo 订阅，自包含形式。
  - `Templates/config.json`：sing-box SFM / SFA / SFI 订阅。
- 内核版本解析只接受 `x.y.z` 正式版标签，过滤预发布标签与脚本自身的 `easysb` 发布标签。
