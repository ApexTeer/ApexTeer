package tui

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"strings"
	"time"

	tea "charm.land/bubbletea/v2"

	"github.com/MinimaxFlora/EasySB/internal/config"
	"github.com/MinimaxFlora/EasySB/internal/deploy"
	"github.com/MinimaxFlora/EasySB/internal/firewall"
	"github.com/MinimaxFlora/EasySB/internal/i18n"
	"github.com/MinimaxFlora/EasySB/internal/netutil"
	"github.com/MinimaxFlora/EasySB/internal/sbcore"
	"github.com/MinimaxFlora/EasySB/internal/secret"
	"github.com/MinimaxFlora/EasySB/internal/service"
	"github.com/MinimaxFlora/EasySB/internal/state"
	"github.com/MinimaxFlora/EasySB/internal/subd"
	"github.com/MinimaxFlora/EasySB/internal/sysinfo"
	"github.com/MinimaxFlora/EasySB/internal/user"
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

// deployNode generates config.json, installs the service unit and starts the
// node, auto-filling any missing credentials.
func deployNode() actionFunc {
	return func(a *App) tea.Cmd {
		lang := a.lang
		return a.startTask(lang.T("node_deploying"), func(ctx context.Context, r *taskReporter) error {
			return runDeploy(ctx, r, lang)
		})
	}
}

// runDeploy is the one deploy path: it renders the config for the accounts in use,
// starts the service and refreshes everything derived from it.
func runDeploy(ctx context.Context, r *taskReporter, lang i18n.Lang) error {
	cfg := state.Load()
	if !cfg.AnyEnabled() {
		return errors.New(lang.T("node_all_disabled"))
	}

	// The lock is held for the whole deploy: the accounts the core is rendered from
	// are the accounts recorded as applied at the end, so a change committed in
	// between cannot leave the file and the running core disagreeing.
	store, lock, err := user.Locked(sysinfo.UsersFile)
	if err != nil {
		return err
	}
	defer lock.Unlock()
	if store.Len() == 0 {
		// A node without accounts is legal and starts, but nobody can
		// connect, so the operator is told rather than blocked.
		r.Log(lang.T("node_no_users"))
	}

	// The node keeps only the material no account owns: the Reality
	// keypair and short id, which the server side needs to complete a
	// handshake.
	if cfg.Enabled[state.ProtoVLESSReality] {
		if cfg.RealityPriv == "" || cfg.RealityPub == "" {
			priv, pub := secret.RealityKeypair()
			if priv == "" || pub == "" {
				return errors.New(lang.T("param_key_fail"))
			}
			cfg.RealityPriv, cfg.RealityPub = priv, pub
			r.Log(lang.T("param_key_gen"))
		}
		if cfg.RealitySID == "" {
			cfg.RealitySID = secret.ShortID()
		}
	}

	if cfg.Domain == "" && cfg.CertDomain != "" {
		cfg.Domain = cfg.CertDomain
	}
	if cfg.Domain == "" && config.ParamsFromState(cfg).NeedsCert() {
		r.Log(lang.T("node_need_domain"))
		r.Log("→ self-signed placeholder certificate")
	}

	if cfg.ServerIP == "" && cfg.Domain == "" {
		if ip, err := netutil.PublicIP(ctx); err == nil {
			cfg.ServerIP = ip
			r.Log("server ip: " + ip)
		}
	}

	// Whether the node can count usage is a property of this build, so it is read
	// instead of assumed: a panel compiled without the with_v2ray_api tag carries
	// no counters and must leave the block out, because the core it carries
	// rejects a configuration naming an API it does not have. Such a build still
	// deploys a working node, only the byte columns stay empty.
	if !sbcore.StatsCapable() {
		r.Log(lang.T("node_stats_unavailable"))
	}

	// The configuration is rendered for the accounts that are usable
	// right now, so a deploy also revokes whatever expired meanwhile.
	now := time.Now()
	// The core is shown the document before it replaces the live one, so a
	// deployment that fails validation leaves the running node's configuration in
	// place rather than writing one the core will not load.
	if _, err := deploy.ApplyConfig(ctx, cfg, store.Routable(now)); err != nil {
		r.Log(err.Error())
		if errors.Is(err, deploy.ErrRejected) {
			return errors.New(lang.T("node_config_fail"))
		}
		return err
	}
	r.Log("write " + sysinfo.ConfigJSON)
	r.Log(lang.T("node_config_ok"))

	cfg.NodeDeployed = true
	if err := cfg.Save(); err != nil {
		return err
	}
	if err := service.WriteUnit(); err != nil {
		return err
	}
	r.Log("write " + service.UnitPath())
	if err := service.Do(ctx, "enable"); err != nil {
		r.Log("enable: " + err.Error())
	}
	if err := service.Do(ctx, "restart"); err != nil {
		return err
	}
	// The accounts in the core match the file again, which is what the
	// accounting loop compares against.
	store.MarkApplied(now)
	if err := store.Save(); err != nil {
		return err
	}

	if err := firewall.Apply(ctx, cfg, r.Log); err != nil {
		r.Log("firewall: " + err.Error())
	} else if err := firewall.WriteUnit(cfg); err == nil {
		_ = firewall.UnitAction(ctx, "enable")
	}

	r.Log(lang.T("node_deploy_done"))
	// Every account's subscription URL points at this service, so it is
	// installed together with the node.
	if err := installEndpoint(ctx, cfg, r.Log, lang); err != nil {
		r.Log(lang.T("sub_svc_failed") + ": " + err.Error())
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
