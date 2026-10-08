package tui

import (
	"charm.land/bubbletea/v2"

	"github.com/EasySBTeam/EasySB/internal/i18n"
	"github.com/EasySBTeam/EasySB/internal/icons"
)

type actionFunc func(a *App) tea.Cmd

type node struct {
	id     string
	label  func(i18n.Lang) string
	desc   func(i18n.Lang) string
	icon   func(icons.Set) string
	sub    *menu
	action actionFunc
}

type menu struct {
	id    string
	title func(i18n.Lang) string
	nodes []*node
}

func tk(key string) func(i18n.Lang) string {
	return func(l i18n.Lang) string { return l.T(key) }
}

func leaf(id, labelKey, descKey string, fn actionFunc) *node {
	return &node{id: id, label: tk(labelKey), desc: tk(descKey), action: fn}
}

func iconLeaf(id, labelKey, descKey string, ic func(icons.Set) string, fn actionFunc) *node {
	n := leaf(id, labelKey, descKey, fn)
	n.icon = ic
	return n
}

func buildRoot() *menu {
	return &menu{
		id:    "root",
		title: tk("menu_main"),
		nodes: []*node{
			{id: "toolbox", label: tk("toolbox_title"), desc: tk("menu_toolbox"), icon: func(s icons.Set) string { return s.Globe }, sub: buildToolbox()},
			{id: "node", label: tk("node_title"), desc: tk("menu_node"), icon: func(s icons.Set) string { return s.Rocket }, action: enterNode()},
			{id: "domain", label: tk("domain_title"), desc: tk("menu_domain"), icon: func(s icons.Set) string { return s.Globe }, sub: buildDomain()},
			{id: "subscribe", label: tk("sub_title"), desc: tk("menu_subscribe"), icon: func(s icons.Set) string { return s.Subscribe }, sub: buildSubscribe()},
			{id: "users", label: tk("users_title"), desc: tk("menu_users"), icon: func(s icons.Set) string { return s.Account }, action: enterUsers()},
			{id: "service", label: tk("svc_title"), desc: tk("menu_service"), icon: func(s icons.Set) string { return s.Service }, sub: buildService()},
			iconLeaf("system", "menu_system", "menu_system_desc", func(s icons.Set) string { return s.System }, func(a *App) tea.Cmd {
				a.openSystem()
				return nil
			}),
			{id: "bbr", label: tk("bbr_title"), desc: tk("menu_bbr"), icon: func(s icons.Set) string { return s.Speed }, sub: buildBBR()},
			{id: "script-update", label: tk("menu_script_update"), desc: tk("menu_script_update_desc"), icon: func(s icons.Set) string { return s.Refresh }, sub: buildUpdatePage()},
			{id: "uninstall", label: tk("menu_uninstall"), desc: tk("menu_uninstall_desc"), icon: func(s icons.Set) string { return s.Trash }, sub: buildSelfPage()},
		},
	}
}

// buildParams renders the global parameters: the ones that describe the host
// rather than one node. Per-node ports and protocol parameters live on the
// node's own page.
func buildParams() *menu {
	return &menu{
		id:    "params",
		title: tk("node_params"),
		nodes: []*node{
			leaf("param-sub-port", "param_sub_port", "desc_param_sub_port", editSubPort()),
			leaf("param-sub-sync", "param_sub_sync", "desc_param_sub_sync", editSubSync()),
		},
	}
}

func buildDomain() *menu {
	return &menu{
		id:    "domain",
		title: tk("domain_title"),
		nodes: []*node{
			leaf("domain-issue", "domain_issue", "desc_domain_issue", issueCertAction()),
			leaf("domain-renew", "domain_renew", "desc_domain_renew", renewCertAction()),
			leaf("domain-timer", "domain_timer", "desc_domain_timer", renewTimerAction()),
			leaf("domain-list", "domain_list", "desc_domain_list", listCerts()),
			leaf("domain-switch", "domain_switch", "desc_domain_switch", switchCertAction()),
			leaf("domain-remove", "domain_remove", "desc_domain_remove", removeCertAction()),
		},
	}
}

func buildSubscribe() *menu {
	return &menu{
		id:    "subscribe",
		title: tk("sub_title"),
		nodes: []*node{
			leaf("sub-url", "sub_url", "desc_sub_url", pickAccount("sub_url", showUserSubscription)),
			leaf("sub-qr", "sub_qr", "desc_sub_qr", pickAccount("sub_qr", showUserQR)),
			leaf("sub-links", "sub_links", "desc_sub_links", pickAccount("sub_links", showUserLinks)),
			leaf("sub-svc-install", "sub_svc_install", "desc_sub_svc_install", installSubscriptionService()),
			leaf("sub-svc-restart", "sub_svc_restart", "desc_sub_svc_restart", restartSubscriptionService()),
			leaf("sub-svc-status", "sub_svc_status", "desc_sub_svc_status", subscriptionServiceStatus()),
		},
	}
}

func buildService() *menu {
	return &menu{
		id:    "service",
		title: tk("svc_title"),
		nodes: []*node{
			leaf("svc-start", "svc_start", "desc_svc_start", serviceAction("start")),
			leaf("svc-stop", "svc_stop", "desc_svc_stop", serviceAction("stop")),
			leaf("svc-restart", "svc_restart", "desc_svc_restart", serviceAction("restart")),
			leaf("svc-status", "svc_status", "desc_svc_status", serviceAction("status")),
			leaf("svc-enable", "svc_enable", "desc_svc_enable", serviceAction("enable")),
			leaf("svc-disable", "svc_disable", "desc_svc_disable", serviceAction("disable")),
			leaf("svc-fw-apply", "fw_apply", "desc_svc_fw_apply", firewallApply()),
			leaf("svc-fw-remove", "fw_remove", "desc_svc_fw_remove", firewallRemove()),
		},
	}
}
