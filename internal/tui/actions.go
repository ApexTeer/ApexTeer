package tui

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"strings"

	tea "charm.land/bubbletea/v2"

	"github.com/EasySBTeam/EasySB/internal/deploy"
	"github.com/EasySBTeam/EasySB/internal/firewall"
	"github.com/EasySBTeam/EasySB/internal/i18n"
	"github.com/EasySBTeam/EasySB/internal/state"
	"github.com/EasySBTeam/EasySB/internal/subd"
	"github.com/EasySBTeam/EasySB/internal/sysinfo"
)

func serviceAction(verb string) actionFunc {
	return func(a *App) tea.Cmd {
		title := a.lang.T("svc_title") + " · " + verb
		lang := a.lang
		fn := func(ctx context.Context, r *taskReporter) error {
			switch verb {
			case "status":
				r.Log("$ systemctl status " + sysinfo.ServiceName + " --no-pager")
				out, _ := runCmd(ctx, "systemctl", "status", sysinfo.ServiceName, "--no-pager")
				emit(r.Log, out)
				out2, _ := runCmd(ctx, "systemctl", "is-enabled", sysinfo.ServiceName)
				emit(r.Log, out2)
				return nil
			default:
				r.Log("$ systemctl " + verb + " " + sysinfo.ServiceName)
				out, err := runCmd(ctx, "systemctl", verb, sysinfo.ServiceName)
				emit(r.Log, out)
				if err != nil {
					return err
				}
				r.Log(lang.T("ok"))
				return nil
			}
		}
		return a.startTask(title, fn)
	}
}

// hasServerConfig reports whether a rendered node configuration is on disk, which is
// what makes a re-render worth announcing.
func hasServerConfig() bool {
	info, err := os.Stat(sysinfo.ConfigJSON)
	return err == nil && info.Size() > 0
}

// applyDeployment is the one write path for a change made in the panel. It
// renders the node and account stores into config.json, has the core accept it,
// then restarts the core (or installs and starts it the first time). The
// firewall rules and the subscription service are set up on that first deploy
// only, because they change with the host rather than with a single node.
func applyDeployment(ctx context.Context, lang i18n.Lang, log func(string)) error {
	cfg := state.Load()
	first := !cfg.NodeDeployed
	if err := deploy.ApplyStore(ctx, cfg, sysinfo.NodesFile, sysinfo.UsersFile); err != nil {
		switch {
		case errors.Is(err, deploy.ErrNoNodes):
			// The host has never had an enabled node, so there is no running
			// configuration to retire. Emptying an existing deployment is handled
			// inside deploy.Apply and comes back as success, not as this.
			log(lang.T("node_all_disabled"))
			return nil
		case errors.Is(err, deploy.ErrRejected):
			return errors.New(lang.T("node_config_fail"))
		}
		return err
	}
	log(lang.T("node_applied"))
	// Only the deployed flag is written, as a locked read-modify-write: cfg was read
	// before a deploy that can take seconds, and saving it whole would discard
	// anything another actor - the panel, the accounting loop - changed meanwhile.
	if err := state.UpdateNodeDeployed(true); err != nil {
		return err
	}
	if !first {
		return nil
	}
	nodes, err := loadNodes()
	if err != nil {
		return err
	}
	if err := firewall.Apply(ctx, cfg, nodes, log); err != nil {
		log("firewall: " + err.Error())
	} else if err := firewall.WriteUnit(nodes); err == nil {
		_ = firewall.UnitAction(ctx, "enable")
	}
	// Every account's subscription URL points at this service, so it is
	// installed together with the first node.
	if err := installEndpoint(ctx, cfg, log, lang); err != nil {
		log(lang.T("sub_svc_failed") + ": " + err.Error())
	}
	return nil
}

// installEndpoint writes and starts the subscription service without wrapping it
// in a task, so node deployment can report its failure as a warning instead of
// failing the deploy.
func installEndpoint(ctx context.Context, cfg state.Config, log func(string), lang i18n.Lang) error {
	if err := subd.WriteUnit(); err != nil {
		return err
	}
	log("write " + subd.UnitPath())
	if err := subd.Do(ctx, "enable"); err != nil {
		log("enable: " + err.Error())
	}
	if err := subd.Do(ctx, "restart"); err != nil {
		return err
	}
	log(lang.T("sub_svc_started"))
	logEndpoint(cfg, log, lang)
	return nil
}

func runCmd(ctx context.Context, name string, args ...string) (string, error) {
	cmd := exec.CommandContext(ctx, name, args...)
	cmd.Env = append(os.Environ(), "LC_ALL=C")
	out, err := cmd.CombinedOutput()
	return string(out), err
}

func emit(log func(string), out string) {
	out = strings.TrimRight(out, "\n")
	if out == "" {
		return
	}
	for _, line := range strings.Split(out, "\n") {
		log(line)
	}
}
