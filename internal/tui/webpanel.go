package tui

import (
	"context"

	tea "charm.land/bubbletea/v2"

	"github.com/EasySBTeam/EasySB/internal/i18n"
	"github.com/EasySBTeam/EasySB/internal/panel"
)

// buildPanel is the menu for the native Web management panel. The panel is a mode
// of this same binary under its own unit, so installing it never touches the node
// or the subscription service.
func buildPanel() *menu {
	return &menu{
		id:    "panel",
		title: tk("webpanel_title"),
		nodes: []*node{
			leaf("panel-install", "webpanel_install", "desc_webpanel_install", installPanelService("webpanel_install")),
			leaf("panel-upgrade", "webpanel_upgrade", "desc_webpanel_upgrade", installPanelService("webpanel_upgrade")),
			leaf("panel-uninstall", "webpanel_uninstall", "desc_webpanel_uninstall", uninstallPanelService()),
			leaf("panel-start", "webpanel_start", "desc_webpanel_start", panelAction("webpanel_start", "start")),
			leaf("panel-stop", "webpanel_stop", "desc_webpanel_stop", panelAction("webpanel_stop", "stop")),
			leaf("panel-restart", "webpanel_restart", "desc_webpanel_restart", panelAction("webpanel_restart", "restart")),
			leaf("panel-status", "webpanel_status", "desc_webpanel_status", panelStatus()),
		},
	}
}

// installPanelService writes the unit, enables it and restarts the service. It is
// idempotent: running it on an installed panel rewrites the same unit and restarts
// it, which is how a panel upgrade takes effect.
func installPanelService(titleKey string) actionFunc {
	return func(a *App) tea.Cmd {
		lang := a.lang
		return a.startTask(lang.T(titleKey), func(ctx context.Context, r *taskReporter) error {
			if err := panel.WriteUnit(); err != nil {
				return err
			}
			r.Log("write " + panel.UnitPath)
			if err := panel.Do(ctx, "enable"); err != nil {
				r.Log("enable: " + err.Error())
			}
			if err := panel.Do(ctx, "restart"); err != nil {
				return err
			}
			r.Log(lang.T("webpanel_installed"))
			logPanelAddress(r.Log, lang)
			return nil
		})
	}
}

// uninstallPanelService disables and removes the panel unit. It deliberately
// leaves the node store, the account store, the certificates and the panel
// configuration in place.
func uninstallPanelService() actionFunc {
	return func(a *App) tea.Cmd {
		lang := a.lang
		return a.startTask(lang.T("webpanel_uninstall"), func(ctx context.Context, r *taskReporter) error {
			if err := panel.Do(ctx, "disable"); err != nil {
				r.Log("disable: " + err.Error())
			}
			if err := panel.Do(ctx, "stop"); err != nil {
				r.Log("stop: " + err.Error())
			}
			if err := panel.RemoveUnit(); err != nil {
				return err
			}
			r.Log(lang.T("webpanel_uninstalled"))
			return nil
		})
	}
}

// panelAction runs one lifecycle action on the panel service.
func panelAction(titleKey, verb string) actionFunc {
	return func(a *App) tea.Cmd {
		lang := a.lang
		return a.startTask(lang.T(titleKey), func(ctx context.Context, r *taskReporter) error {
			if err := panel.Do(ctx, verb); err != nil {
				return err
			}
			r.Log(lang.T("webpanel_" + verbResult(verb)))
			return nil
		})
	}
}

// panelStatus reports whether the panel unit is installed and running and prints
// the address an operator opens.
func panelStatus() actionFunc {
	return func(a *App) tea.Cmd {
		lang := a.lang
		return a.startTask(lang.T("webpanel_status"), func(ctx context.Context, r *taskReporter) error {
			if !panel.Installed() {
				r.Log(lang.T("webpanel_not_installed"))
			} else if panel.Active(ctx) {
				r.Log(lang.T("webpanel_running"))
			} else {
				r.Log(lang.T("webpanel_not_running"))
			}
			logPanelAddress(r.Log, lang)
			return nil
		})
	}
}

// logPanelAddress prints the panel URL and its configuration file path.
func logPanelAddress(log func(string), lang i18n.Lang) {
	cfg, err := panel.LoadConfig(panel.ConfigPath)
	if err != nil {
		return
	}
	log(lang.T("webpanel_url") + ": " + cfg.AccessURL())
	log(lang.T("webpanel_config") + ": " + panel.ConfigPath)
}

// verbResult maps a systemctl verb to the i18n suffix of its past-tense message.
func verbResult(verb string) string {
	switch verb {
	case "start":
		return "started"
	case "stop":
		return "stopped"
	case "restart":
		return "restarted"
	default:
		return verb
	}
}
