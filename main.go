// Command easysb is the panel. One binary is four things: the TUI, which is what it does
// with no arguments; the node itself (`easysb core run`, which the sing-box service unit
// starts); the subscription service (`easysb --serve`); and a set of one-shot modes -
// `--render`, `--tool`, `--unlock`, `--renew-certs`, `--apply-firewall`, `--print-unit`,
// `--provision` - that exist so a host driven by a script needs no terminal at all.
package main

import (
	"context"
	_ "embed"
	"flag"
	"fmt"
	"io"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"

	"github.com/EasySBTeam/EasySB/internal/cert"
	"github.com/EasySBTeam/EasySB/internal/config"
	"github.com/EasySBTeam/EasySB/internal/deploy"
	"github.com/EasySBTeam/EasySB/internal/firewall"
	"github.com/EasySBTeam/EasySB/internal/i18n"
	"github.com/EasySBTeam/EasySB/internal/node"
	"github.com/EasySBTeam/EasySB/internal/prefs"
	"github.com/EasySBTeam/EasySB/internal/provision"
	"github.com/EasySBTeam/EasySB/internal/sbcore"
	"github.com/EasySBTeam/EasySB/internal/service"
	"github.com/EasySBTeam/EasySB/internal/state"
	"github.com/EasySBTeam/EasySB/internal/stats"
	"github.com/EasySBTeam/EasySB/internal/subd"
	"github.com/EasySBTeam/EasySB/internal/sysinfo"
	"github.com/EasySBTeam/EasySB/internal/toolbox"
	"github.com/EasySBTeam/EasySB/internal/toolbox/tools"
	"github.com/EasySBTeam/EasySB/internal/tui"
	"github.com/EasySBTeam/EasySB/internal/unlock"
	"github.com/EasySBTeam/EasySB/internal/user"
)

// version is compiled in from the repository's VERSION file, the single source of
// truth for the release number. A bare `go build` therefore reports the same
// version as a published release: there is no -ldflags -X to keep in step and no
// second constant to drift.
//
//go:embed VERSION
var version string

// commit is stamped at build time (-X main.commit=<sha>), empty in a bare build.
var commit = ""

// versionLine is the human-facing build string, including the short commit when
// the build stamped one.
func versionLine() string {
	v := resolveVersion()
	if len(commit) >= 7 {
		return v + " (" + commit[:7] + ")"
	}
	return v
}

func main() {
	// Node mode is a subcommand, not a flag: the service unit runs
	// `easysb core run -c /etc/sing-box/config.json`, so the same argument shape
	// has to work from a shell too.
	if len(os.Args) > 1 && os.Args[1] == "core" {
		runCoreCommand(os.Args[2:])
		return
	}

	langFlag := flag.String("language", "", "界面语言 / UI language: C (中文) or E (English)")
	iconsFlag := flag.String("icons", "", "图标方案 / icon set: symbols, ascii (or on, off)")
	themeFlag := flag.String("theme", "", "配色方案 / color theme: auto, dark, light")
	skinFlag := flag.String("skin", "", "界面皮肤 / UI skin: jade, aurora, ember, graphite (or a-d)")
	showVersion := flag.Bool("version", false, "显示版本 / show version")
	printUnit := flag.String("print-unit", "", "打印服务单元文本，供打包与脚本使用：node 或 sub / print a service unit body for packaging and scripts: node or sub")
	unitExec := flag.String("unit-exec", "/usr/bin/easysb", "配合 --print-unit 指定 ExecStart 的路径 / executable path that --print-unit writes into the unit")
	render := flag.Bool("render", false, "渲染一次仪表盘后退出 / render once and exit")
	screen := flag.String("screen", "", "配合 --render 渲染指定界面：栏目 id（toolbox/node/domain/bbr…）、system、task、toolbox-report 或 bbr-versions / with --render, draw this screen by section id, or system, task, toolbox-report, bbr-qdisc, bbr-versions")
	applyFirewall := flag.Bool("apply-firewall", false, "应用端口跳跃防火墙规则 / apply port-hopping firewall rules")
	renewCerts := flag.Bool("renew-certs", false, "续期证书并重载服务（供定时器调用）/ renew certificates and reload the services")
	installTimer := flag.Bool("install-renew-timer", false, "安装证书续期定时器 / install the certificate renewal timer")
	removeTimer := flag.Bool("remove-renew-timer", false, "移除证书续期定时器 / remove the certificate renewal timer")
	serve := flag.Bool("serve", false, "运行订阅服务 / run the subscription service")
	provisionFile := flag.String("provision", "", "按部署清单部署并退出，传 '-' 从标准输入读取 / deploy from a manifest and exit, '-' reads stdin")
	unlockCheck := flag.Bool("unlock", false, "一次跑完 17 项解锁检测并输出报告（同工具箱的三个解锁条目）/ run all seventeen unlock checks in one report (the same three entries as the toolbox)")
	toolFlag := flag.String("tool", "", "工具箱的某一项，list 列出全部 / one toolbox entry, or list")
	width := flag.Int("width", 100, "渲染宽度 / render width")
	height := flag.Int("height", 36, "渲染高度 / render height")
	flag.Parse()

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

	// Remembered interface choices fill in what the command line left open: a flag
	// or an exported variable still wins, and everything else keeps the choice made
	// from inside the panel.
	prefs.Load(prefs.Path()).Apply(os.Getenv, os.Setenv)

	applyIcons(*iconsFlag)
	applyTheme(*themeFlag)
	applySkin(*skinFlag)
	lang := i18n.Parse(firstNonEmpty(*langFlag, os.Getenv("EASYSB_LANG")))

	// A v5 deployment is upgraded before the panel reads either store, so the
	// render, the menus and the first apply all see the explicit node model.
	migrateStores()

	app := tui.New(resolveVersion(), lang)

	if *render {
		fmt.Println(app.SnapshotScreen(*screen, *width, *height))
		return
	}

	program := tea.NewProgram(app)
	if _, err := program.Run(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

// migrateStores upgrades a v5 single-node deployment to the explicit node model
// before anything reads the stores. It is idempotent, and on an already migrated
// host it only completes a pending account-file schema upgrade.
func migrateStores() {
	created, err := node.Migrate(state.Load(), sysinfo.NodesFile, sysinfo.UsersFile)
	if err != nil {
		fmt.Fprintln(os.Stderr, "migrate: "+err.Error())
		os.Exit(1)
	}
	if created {
		fmt.Fprintln(os.Stderr, "migrated the v5 deployment to the node model")
	}
}

// runPrintUnit writes a service unit body to stdout. The .deb is assembled from this
// output, so the unit shipped in the package is the same text the panel writes at
// runtime and there is no second copy to drift.
func runPrintUnit(kind, exe string) {
	switch kind {
	case "node":
		fmt.Print(service.UnitBody(exe))
	case "sub":
		fmt.Print(subd.UnitBody(exe))
	default:
		fmt.Fprintf(os.Stderr, "unknown unit %q: use node or sub\n", kind)
		os.Exit(2)
	}
}

// runCoreCommand is the core the panel carries, exposed the way a service unit
// needs it: `core run` is the node (`ExecStart=… core run -c <config>`), `core
// check` validates a configuration without starting anything, and `core version`
// answers what this build carries.
func runCoreCommand(args []string) {
	if len(args) == 0 {
		fmt.Fprintln(os.Stderr, "usage: easysb core run|check|version [-c <config>]")
		os.Exit(2)
	}
	command := args[0]
	flags := flag.NewFlagSet("core "+command, flag.ExitOnError)
	configPath := flags.String("c", sysinfo.ConfigJSON, "配置文件 / configuration file")
	switch command {
	case "run", "check":
		if err := flags.Parse(args[1:]); err != nil {
			os.Exit(2)
		}
	case "version":
		fmt.Printf("EasySB %s\nsing-box %s\nbuild: %s\n", versionLine(), sbcore.Version(), coreCapability())
		return
	default:
		fmt.Fprintf(os.Stderr, "unknown core command %q: use run, check or version\n", command)
		os.Exit(2)
	}

	if command == "check" {
		if err := sbcore.Check(context.Background(), *configPath); err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(1)
		}
		fmt.Println("config ok: " + *configPath)
		return
	}

	// The node runs until the service manager stops it: SIGTERM ends the context
	// and the engine closes its listeners on the way out.
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	logf := func(line string) { fmt.Printf("%s %s\n", time.Now().Format(time.RFC3339), line) }
	logf("EasySB " + versionLine() + " · sing-box " + sbcore.Version() + " · " + coreCapability())
	logf("config: " + *configPath)
	if err := sbcore.Run(ctx, *configPath); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

// coreCapability names the one build flag that changes what the panel can do.
func coreCapability() string {
	if sbcore.StatsCapable() {
		return "with_v2ray_api (per-account traffic counters)"
	}
	return "no v2ray api (usage cannot be counted)"
}

// runUnlockCheck probes every service in the catalogue and prints one line per
// verdict. It is the headless form of the 服务解锁状态 page, for a host where the
// panel is scripted rather than opened.
func runUnlockCheck() {
	ctx := context.Background()
	report := unlock.New(unlock.Options{}).Report(ctx)
	fmt.Printf("service unlock check · %d services · %s\n\n", len(report.Results), report.Elapsed.Round(time.Millisecond))
	group := ""
	for _, res := range report.Results {
		if res.Group != group {
			group = res.Group
			fmt.Println("[" + unlockGroupName(group) + "]")
		}
		line := fmt.Sprintf("  %-26s %-10s", res.Name, unlockStatusName(res.Status))
		if res.Region != "" {
			line += " " + res.Region
		}
		if res.Text != "" && res.Status != unlock.StatusUnlocked {
			line += "  " + res.Text
		}
		fmt.Println(line)
	}
	fmt.Printf("\nunlocked %d · partial %d · blocked %d · failed %d\n",
		report.Count(unlock.StatusUnlocked), report.Count(unlock.StatusPartial),
		report.Count(unlock.StatusBlocked), report.Count(unlock.StatusFailed))
}

// runToolboxTool prints one toolbox entry as a plain table and exits. It is the same run
// the panel starts from its menu, for a host that is scripted rather than opened, and it is
// how this project verifies a tool on a real machine.
func runToolboxTool(id string, lang i18n.Lang) {
	if id == "list" || id == "" {
		printToolboxList(lang)
		return
	}
	tool, ok := tools.Lookup(id)
	if !ok {
		fmt.Fprintf(os.Stderr, "unknown toolbox entry %q\n\n", id)
		printToolboxList(lang)
		os.Exit(2)
	}

	fmt.Printf("%s\n\n", lang.T("toolbox_"+id))
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	result, err := tool.Run(ctx, toolbox.Options{Log: func(line string) { fmt.Fprintln(os.Stderr, "  "+line) }})
	// The run is written down whatever it produced, so a result measured without a terminal
	// shows up in the panel's 看板 as well. A board that cannot be written is not a failed
	// run: the table below is what the caller asked for, and it is printed either way.
	if recordErr := tools.Record(id, result, err); recordErr != nil {
		fmt.Fprintf(os.Stderr, "board: %v\n", recordErr)
	}
	if err != nil {
		fmt.Fprintf(os.Stderr, "%s: %v\n", lang.T("toolbox_failed"), err)
		os.Exit(1)
	}
	printPlainTable(lang, result)
}

// printPlainTable writes a result without the panel's frame: this output is read from a log
// or piped into something else, so it stays plain text and keeps its columns aligned.
func printPlainTable(lang i18n.Lang, result toolbox.Result) {
	headers := result.Headers
	if len(headers) == 0 {
		headers = []string{"item", "value"}
	}
	rows := make([][]string, 0, len(result.Rows))
	for _, row := range result.Rows {
		localised := make([]string, 0, len(row))
		for _, cell := range row {
			localised = append(localised, cellText(lang, cell))
		}
		rows = append(rows, localised)
	}
	localisedHeaders := make([]string, 0, len(headers))
	for _, header := range headers {
		localisedHeaders = append(localisedHeaders, cellText(lang, header))
	}

	// Column widths are display widths, not rune counts: a Chinese verdict occupies two
	// columns per character, and counting runes would leave every table with a Chinese
	// cell ragged.
	width := make([]int, len(headers))
	for i, header := range localisedHeaders {
		width[i] = lipgloss.Width(header)
	}
	for _, row := range rows {
		for i, cell := range row {
			if i < len(width) && lipgloss.Width(cell) > width[i] {
				width[i] = lipgloss.Width(cell)
			}
		}
	}
	printRow := func(cells []string) {
		line := ""
		for i, w := range width {
			cell := ""
			if i < len(cells) {
				cell = cells[i]
			}
			line += cell + strings.Repeat(" ", w-lipgloss.Width(cell)+2)
		}
		fmt.Println(strings.TrimRight(line, " "))
	}
	printRow(localisedHeaders)
	for _, row := range rows {
		printRow(row)
	}

	for _, note := range result.Notes {
		fmt.Println("· " + note)
	}
	if result.Summary != "" {
		fmt.Println()
		fmt.Println(result.Summary)
	}
}

// cellText words the tokens the panel defined, so the command line reads like the panel's
// table: the verdicts first, then the column names the registry uses.
func cellText(lang i18n.Lang, cell string) string {
	if tools.IsVerdict(cell) {
		return tools.Verdict(lang, cell)
	}
	switch cell {
	case "service":
		return lang.T("toolbox_col_service")
	case "status":
		return lang.T("toolbox_col_status")
	case "region":
		return lang.T("toolbox_col_region")
	case "item":
		return lang.T("toolbox_col_item")
	case "value":
		return lang.T("toolbox_col_value")
	}
	return cell
}

// printToolboxList names every entry, so an unknown --tool argument is answered with the
// list instead of an error alone.
func printToolboxList(lang i18n.Lang) {
	for _, group := range tools.Groups() {
		fmt.Println("[" + lang.T("toolbox_group_"+group) + "]")
		for _, tool := range tools.InGroup(group) {
			fmt.Printf("  %-18s %s\n", tool.ID, lang.T("toolbox_"+tool.ID))
		}
	}
}

// unlockGroupName names a catalogue group in the report's own language, which is
// English: this output goes to a log or a pipe, not to the bilingual panel.
func unlockGroupName(group string) string {
	switch group {
	case unlock.GroupMultination:
		return "streaming"
	case unlock.GroupAI:
		return "ai"
	case unlock.GroupGame:
		return "game"
	case unlock.GroupChina:
		return "china"
	case unlock.GroupTaiwan:
		return "taiwan"
	}
	return group
}

// unlockStatusName words a verdict for the plain-text report.
func unlockStatusName(status unlock.Status) string {
	switch status {
	case unlock.StatusUnlocked:
		return "unlocked"
	case unlock.StatusPartial:
		return "partial"
	case unlock.StatusBlocked:
		return "blocked"
	}
	return "failed"
}

// runSubscribeService serves the subscription endpoint and enforces the account
// policy. It backs the easysb service unit and is the only long-running mode of
// this binary.
func runSubscribeService() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	logf := func(line string) { fmt.Printf("%s %s\n", time.Now().Format(time.RFC3339), line) }
	logf("EasySB subscription service " + versionLine())

	options := subd.Options{
		Version:      resolveVersion(),
		AccountsPath: sysinfo.UsersFile,
		NodesPath:    sysinfo.NodesFile,
		Dial:         func() (stats.Counter, error) { return stats.Dial(config.StatsListen) },
		Apply: func(ctx context.Context, cfg state.Config, nodes []node.Node, accounts []user.User) error {
			return deploy.Apply(ctx, cfg, nodes, accounts)
		},
		Log: logf,
	}
	if err := options.Run(ctx); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

// runApplyFirewall applies the port-hopping rules and exits. It backs the
// easysb-firewall boot unit.
func runApplyFirewall() {
	cfg := state.Load()
	log := func(line string) { fmt.Println(line) }
	nodes, err := node.Load(sysinfo.NodesFile)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	if err := firewall.Apply(context.Background(), cfg, nodes.Nodes(), log); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	if err := firewall.WriteUnit(nodes.Nodes()); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

// runRenewCerts is the entry point of the renewal timer: it renews the
// certificates the panel manages and then reloads the services that hold the old
// one open, so a renewal is actually served instead of only stored on disk.
// runRenewTimer installs or removes the renewal timer, the same work the domain
// screen offers. It is a command line mode because the unit names this binary's own
// path, so the binary has to be the thing that writes it: a headless or scripted
// setup has no panel to click, and a unit pointing at some other path renews nothing.
func runRenewTimer(install bool) {
	ctx := context.Background()
	log := func(line string) { fmt.Println(line) }
	if install {
		if err := cert.InstallTimer(ctx, log); err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(1)
		}
		if next := cert.TimerStatus(); next != "" {
			fmt.Println(next)
		}
		return
	}
	if err := cert.RemoveTimer(ctx, log); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

// runProvision deploys a host from a manifest, printing each step and a summary
// of what was produced. It is the headless counterpart of the panel's node,
// domain and account screens, meant for a script (or a deploy skill) that has a
// document of the desired state and no terminal.
func runProvision(path string) {
	var data []byte
	var err error
	if path == "-" {
		data, err = io.ReadAll(os.Stdin)
	} else {
		data, err = os.ReadFile(path)
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, "provision: "+err.Error())
		os.Exit(1)
	}
	spec, err := provision.Parse(data)
	if err != nil {
		fmt.Fprintln(os.Stderr, "provision: "+err.Error())
		os.Exit(1)
	}
	opt := provision.Default()
	opt.Log = func(line string) { fmt.Println(line) }
	res, err := provision.Run(context.Background(), spec, opt)
	if err != nil {
		fmt.Fprintln(os.Stderr, "provision: "+err.Error())
		os.Exit(1)
	}
	printProvisionResult(res)
}

// printProvisionResult writes the subscription URL of every account, which is
// what an operator needs to hand a client, plus the nodes that back them.
func printProvisionResult(res *provision.Result) {
	fmt.Println()
	fmt.Printf("deployed %d node(s), %d account(s)\n", len(res.Nodes), len(res.Accounts))
	for _, n := range res.Nodes {
		fmt.Printf("  node  %-18s %-14s :%d\n", n.Name, n.Protocol, n.Port)
	}
	for _, a := range res.Accounts {
		fmt.Printf("  user  %-18s %s\n", a.Name, a.URL)
	}
}

func runRenewCerts() {
	ctx := context.Background()
	log := func(line string) { fmt.Println(line) }
	renewed, err := cert.Renew(ctx, log)
	// A renewal that changed nothing needs no reload: the core keeps running with
	// the certificate it already has. This timer runs every night, so restarting
	// the services unconditionally would drop every connection once a day.
	if len(renewed) == 0 {
		if err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(1)
		}
		fmt.Println("no certificate needed renewal")
		return
	}
	// A renewal overwrites the certificate files in place, so nothing in the config
	// on disk changes: only the services that are actually running have to restart
	// to pick the new pair up. Following the running services rather than the
	// deployed flag keeps this correct on a host whose node was deployed outside the
	// panel, where that flag is not set and a renewed certificate would otherwise
	// never be served.
	if service.Active(ctx) {
		if err := service.Do(ctx, "restart"); err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(1)
		}
	}
	if subd.Active(ctx) {
		if err := subd.Do(ctx, "restart"); err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(1)
		}
	}
	// A domain whose renewal failed does not hold back the ones that succeeded:
	// they were just reloaded, and the failure is still reported so the timer shows
	// up as failed in the journal.
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

// applyIcons forwards the icon mode to the TUI. "on" selects the Unicode symbol
// palette, "off" the ASCII fallback; the legacy "nerd" value behaves like "on".
func applyIcons(mode string) {
	switch strings.ToLower(strings.TrimSpace(mode)) {
	case "on", "1", "true", "yes", "symbols", "unicode", "nerd":
		_ = os.Setenv("EASYSB_ICONS", "symbols")
	case "off", "0", "false", "no", "ascii", "plain":
		_ = os.Setenv("EASYSB_ICONS", "ascii")
	}
}

// applyTheme forwards the requested palette to the TUI. An empty or unknown
// value leaves EASYSB_THEME untouched so the TUI keeps auto-detecting.
func applyTheme(mode string) {
	switch strings.ToLower(strings.TrimSpace(mode)) {
	case "auto", "dark", "light":
		_ = os.Setenv("EASYSB_THEME", strings.ToLower(strings.TrimSpace(mode)))
	}
}

// applySkin passes the requested look on to the TUI through the environment,
// the same way --theme does. An unknown name is ignored, so the panel falls back
// to the default skin instead of refusing to start.
func applySkin(name string) {
	if strings.TrimSpace(name) == "" {
		return
	}
	_ = os.Setenv("EASYSB_SKIN", strings.TrimSpace(name))
}

func firstNonEmpty(values ...string) string {
	for _, v := range values {
		if strings.TrimSpace(v) != "" {
			return v
		}
	}
	return ""
}

func resolveVersion() string {
	return strings.TrimSpace(version)
}
