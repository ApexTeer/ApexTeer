package tui

import (
	"context"
	"errors"
	"strings"

	tea "charm.land/bubbletea/v2"

	"github.com/MinimaxFlora/EasySB/internal/cert"
	"github.com/MinimaxFlora/EasySB/internal/firewall"
	"github.com/MinimaxFlora/EasySB/internal/i18n"
	"github.com/MinimaxFlora/EasySB/internal/state"
	"github.com/MinimaxFlora/EasySB/internal/subd"
	"github.com/MinimaxFlora/EasySB/internal/subscribe"
	"github.com/MinimaxFlora/EasySB/internal/uninstall"
	"github.com/MinimaxFlora/EasySB/internal/update"
)

// clientLabel returns the localized menu label for a subscription client.
func clientLabel(lang i18n.Lang, client subscribe.Client) string {
	switch client {
	case subscribe.ClientMihomo:
		return lang.T("sub_client_mihomo")
	case subscribe.ClientV2Ray:
		return lang.T("sub_client_v2ray")
	default:
		return lang.T("sub_client_singbox")
	}
}

// clientDescription returns a localized note listing the clients a subscription
// format serves. The v2ray and mihomo documents are each shared by several
// OpenWrt plugins, so the note names them explicitly.
func clientDescription(lang i18n.Lang, client subscribe.Client) string {
	switch client {
	case subscribe.ClientV2Ray:
		return lang.T("sub_client_v2ray_desc")
	case subscribe.ClientMihomo:
		return lang.T("sub_client_mihomo_desc")
	default:
		return lang.T("sub_client_singbox_desc")
	}
}

// installSubscriptionService writes the unit of the built-in subscription service
// and starts it. v4 has no web server in front of the panel: this binary serves
// /sub/<token> itself, so there is no document to regenerate and no site to
// publish.
func installSubscriptionService() actionFunc {
	return func(a *App) tea.Cmd {
		lang := a.lang
		return a.startTask(lang.T("sub_svc_install"), func(ctx context.Context, r *taskReporter) error {
			cfg := state.Load()
			if !cfg.NodeDeployed {
				return errors.New(lang.T("sub_need_deploy"))
			}
			if err := subd.WriteUnit(); err != nil {
				return err
			}
			r.Log("write " + subd.UnitPath())
			if err := subd.Do(ctx, "enable"); err != nil {
				r.Log("enable: " + err.Error())
			}
			if err := subd.Do(ctx, "restart"); err != nil {
				return err
			}
			r.Log(lang.T("sub_svc_started"))
			logEndpoint(cfg, r.Log, lang)
			return nil
		})
	}
}

// restartSubscriptionService restarts the endpoint service, which is what an
// endpoint port change needs.
func restartSubscriptionService() actionFunc {
	return func(a *App) tea.Cmd {
		lang := a.lang
		return a.startTask(lang.T("sub_svc_restart"), func(ctx context.Context, r *taskReporter) error {
			if err := subd.Do(ctx, "restart"); err != nil {
				return err
			}
			r.Log(lang.T("sub_svc_restarted"))
			logEndpoint(state.Load(), r.Log, lang)
			return nil
		})
	}
}

// subscriptionServiceStatus reports whether the endpoint service is running and
// where it listens.
func subscriptionServiceStatus() actionFunc {
	return func(a *App) tea.Cmd {
		lang := a.lang
		return a.startTask(lang.T("sub_svc_status"), func(ctx context.Context, r *taskReporter) error {
			if subd.Active(ctx) {
				r.Log(lang.T("sub_svc_running"))
			} else {
				r.Log(lang.T("sub_svc_stopped"))
			}
			logEndpoint(state.Load(), r.Log, lang)
			return nil
		})
	}
}

// logEndpoint prints the endpoint base URL and explains what has to be appended
// to it, because the account token is what makes a subscription work.
func logEndpoint(cfg state.Config, log func(string), lang i18n.Lang) {
	if cfg.Host() == "" {
		log(lang.T("sub_need_domain"))
		return
	}
	// A subscription carries the account credentials, so an endpoint served over
	// plain HTTP is called out instead of being left for the operator to notice
	// from the scheme.
	if !cert.Usable(cfg.Domain) {
		log(lang.T("sub_plaintext_warning"))
	}
	log(lang.T("sub_endpoint") + ": " + subscribe.Endpoint(cfg))
	log(lang.T("sub_endpoint_hint"))
}

// firewallApply opens ports, installs the hopping redirect and enables boot
// persistence.
func firewallApply() actionFunc {
	return func(a *App) tea.Cmd {
		lang := a.lang
		return a.startTask(lang.T("fw_configuring"), func(ctx context.Context, r *taskReporter) error {
			cfg := state.Load()
			if !cfg.AnyEnabled() {
				return errors.New(lang.T("node_all_disabled"))
			}
			if err := firewall.Apply(ctx, cfg, r.Log); err != nil {
				return err
			}
			if err := firewall.WriteUnit(cfg); err != nil {
				return err
			}
			if err := firewall.UnitAction(ctx, "enable"); err != nil {
				r.Log("enable unit: " + err.Error())
			}
			r.Log(lang.T("fw_added"))
			return nil
		})
	}
}

// firewallRemove deletes the hopping redirect and its boot unit.
func firewallRemove() actionFunc {
	return func(a *App) tea.Cmd {
		lang := a.lang
		return a.startTask(lang.T("fw_remove"), func(ctx context.Context, r *taskReporter) error {
			cfg := state.Load()
			if err := firewall.Remove(ctx, cfg); err != nil {
				return err
			}
			if err := firewall.UnitAction(ctx, "disable"); err != nil {
				r.Log("disable unit: " + err.Error())
			}
			if err := firewall.RemoveUnit(); err != nil {
				return err
			}
			r.Log(lang.T("fw_removed"))
			return nil
		})
	}
}

// buildUpdatePage is the version section: what the panel and the core are on, above
// the one action that changes them. It is a page rather than an action run straight
// off the main menu, because a root entry that starts a task in place leaves the
// frame half-switched: the top box would show this section's 看板 over the main menu,
// and Esc would quit the program instead of walking back.
func buildUpdatePage() *menu {
	return &menu{
		id:    "script-update",
		title: tk("menu_script_update"),
		nodes: []*node{
			leaf("script-update-run", "script_update_run", "desc_script_update_run", scriptUpdate()),
		},
	}
}

// buildSelfPage is the uninstall section: what the script installed, above the action
// that removes it. The removal asks for confirmation of its own.
func buildSelfPage() *menu {
	return &menu{
		id:    "uninstall",
		title: tk("menu_uninstall"),
		nodes: []*node{
			leaf("uninstall-run", "uninstall_run", "desc_uninstall_run", uninstallAction()),
		},
	}
}

// scriptUpdate downloads and replaces the running binary.
func scriptUpdate() actionFunc {
	return func(a *App) tea.Cmd {
		lang := a.lang
		current := a.scriptVersion
		return a.startTask(lang.T("script_updating"), func(ctx context.Context, r *taskReporter) error {
			updated, remote, err := update.Apply(ctx, current, r.Log)
			if err != nil {
				return err
			}
			if !updated {
				version := remote
				if version == "" {
					version = current
				}
				r.Log(lang.T("script_uptodate") + ": " + version)
				return nil
			}
			r.Log(lang.T("script_updated") + ": " + remote)
			r.Log(lang.T("script_restart_hint"))
			return nil
		})
	}
}

// uninstallAction asks for confirmation before removing the deployment.
func uninstallAction() actionFunc {
	return func(a *App) tea.Cmd {
		lang := a.lang
		a.openForm(lang.T("uninstall_title"), lang.T("uninstall_confirm")+" (y/N)", "", "", func(a *App, value string) (tea.Cmd, error) {
			switch strings.ToLower(strings.TrimSpace(value)) {
			case "y", "yes":
				return a.startTask(lang.T("uninstall_title"), func(ctx context.Context, r *taskReporter) error {
					if err := uninstall.Run(ctx, r.Log); err != nil {
						return err
					}
					r.Log(lang.T("uninstall_done"))
					r.Log(lang.T("uninstall_keep_certs"))
					return nil
				}), nil
			default:
				return nil, errors.New(lang.T("cancelled"))
			}
		})
		return nil
	}
}
