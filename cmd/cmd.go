// Package cmd is the easysb command line. One binary is five things: the TUI,
// which is what it does with no arguments; the node itself (`easysb core run`,
// which the sing-box service unit starts); the subscription service (`easysb
// --serve`); the Web management panel (`easysb panel`, its own systemd unit); and
// a set of one-shot modes - `--render`, `--tool`, `--unlock`, `--renew-certs`,
// `--apply-firewall`, `--print-unit`, `--provision` - that exist so a host driven
// by a script needs no terminal at all.
//
// The entry point is the same shape the service units and install.sh already call,
// so the split from a single main.go into this package changed no argument: the
// root package only embeds VERSION and calls Execute.
package cmd

import (
	"flag"
	"fmt"
	"os"

	"github.com/EasySBTeam/EasySB/internal/i18n"
)

// Execute runs the command line and exits the process on a mode that ends by
// itself. version is the release number embedded from the repository's VERSION
// file; commit is the build stamp, empty in a bare build. The root package owns
// both, so the embed and the -X main.commit target stay in one place.
func Execute(version, commit string) {
	buildVersion = version
	buildCommit = commit
	run(os.Args[1:])
}

// run is the dispatcher. Subcommands are matched first, the way a shell and the
// systemd units spell them; everything else is a flag of the default mode.
func run(args []string) {
	// Node mode is a subcommand, not a flag: the service unit runs
	// `easysb core run -c /etc/sing-box/config.json`, so the same argument shape
	// has to work from a shell too.
	if len(args) > 0 && args[0] == "core" {
		runCoreCommand(args[1:])
		return
	}
	// Panel mode is the Web management panel, also a subcommand: its own systemd
	// unit runs `easysb panel`, so the panel can be started, stopped and upgraded
	// independently of the node and the subscription service.
	if len(args) > 0 && args[0] == "panel" {
		runPanel()
		return
	}

	flags := flag.NewFlagSet("easysb", flag.ExitOnError)
	langFlag := flags.String("language", "", "界面语言 / UI language: C (中文) or E (English)")
	iconsFlag := flags.String("icons", "", "图标方案 / icon set: symbols, ascii (or on, off)")
	themeFlag := flags.String("theme", "", "配色方案 / color theme: auto, dark, light")
	skinFlag := flags.String("skin", "", "界面皮肤 / UI skin: jade, aurora, ember, graphite (or a-d)")
	showVersion := flags.Bool("version", false, "显示版本 / show version")
	printUnit := flags.String("print-unit", "", "打印服务单元文本，供打包与脚本使用：node 或 sub / print a service unit body for packaging and scripts: node or sub")
	unitExec := flags.String("unit-exec", "/usr/bin/easysb", "配合 --print-unit 指定 ExecStart 的路径 / executable path that --print-unit writes into the unit")
	render := flags.Bool("render", false, "渲染一次仪表盘后退出 / render once and exit")
	screen := flags.String("screen", "", "配合 --render 渲染指定界面：栏目 id（toolbox/node/domain/bbr…）、system、task、toolbox-report 或 bbr-versions / with --render, draw this screen by section id, or system, task, toolbox-report, bbr-qdisc, bbr-versions")
	applyFirewall := flags.Bool("apply-firewall", false, "应用端口跳跃防火墙规则 / apply port-hopping firewall rules")
	renewCerts := flags.Bool("renew-certs", false, "续期证书并重载服务（供定时器调用）/ renew certificates and reload the services")
	installTimer := flags.Bool("install-renew-timer", false, "安装证书续期定时器 / install the certificate renewal timer")
	removeTimer := flags.Bool("remove-renew-timer", false, "移除证书续期定时器 / remove the certificate renewal timer")
	serve := flags.Bool("serve", false, "运行订阅服务 / run the subscription service")
	provisionFile := flags.String("provision", "", "按部署清单部署并退出，传 '-' 从标准输入读取 / deploy from a manifest and exit, '-' reads stdin")
	unlockCheck := flags.Bool("unlock", false, "一次跑完 17 项解锁检测并输出报告（同工具箱的三个解锁条目）/ run all seventeen unlock checks in one report (the same three entries as the toolbox)")
	toolFlag := flags.String("tool", "", "工具箱的某一项，list 列出全部 / one toolbox entry, or list")
	width := flags.Int("width", 100, "渲染宽度 / render width")
	height := flags.Int("height", 36, "渲染高度 / render height")
	_ = flags.Parse(args)

	if *showVersion {
		fmt.Printf("EasySB %s\n", versionLine())
		return
	}

	if *printUnit != "" {
		runPrintUnit(*printUnit, *unitExec)
		return
	}

	if *applyFirewall {
		runApplyFirewall()
		return
	}

	if *unlockCheck {
		runUnlockCheck()
		return
	}

	if *toolFlag != "" {
		runToolboxTool(*toolFlag, i18n.Parse(*langFlag))
		return
	}
	if *renewCerts {
		runRenewCerts()
		return
	}

	if *installTimer || *removeTimer {
		runRenewTimer(*installTimer)
		return
	}

	if *serve {
		migrateStores()
		runSubscribeService()
		return
	}

	if *provisionFile != "" {
		migrateStores()
		runProvision(*provisionFile)
		return
	}

	runTUI(*langFlag, *iconsFlag, *themeFlag, *skinFlag, *render, *screen, *width, *height)
}
