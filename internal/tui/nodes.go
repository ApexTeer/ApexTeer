package tui

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"

	tea "charm.land/bubbletea/v2"

	"github.com/EasySBTeam/EasySB/internal/i18n"
	sbnode "github.com/EasySBTeam/EasySB/internal/node"
	"github.com/EasySBTeam/EasySB/internal/secret"
	"github.com/EasySBTeam/EasySB/internal/state"
	"github.com/EasySBTeam/EasySB/internal/sysinfo"
	"github.com/EasySBTeam/EasySB/internal/user"
)

// v6 makes the host's listeners explicit nodes: one node is one protocol
// inbound, and an account selects nodes rather than protocols. Node management
// creates, edits, enables and deletes them; every change is rendered into the
// core configuration and pushed to the running core in the same task.

// loadNodes refreshes the snapshot the node menus render from.
func (a *App) loadNodes() {
	nodes, err := loadNodes()
	if err != nil {
		a.setToast(err.Error(), true)
		return
	}
	a.nodes = nodes
}

// loadNodes returns every node from the store.
func loadNodes() ([]sbnode.Node, error) {
	store, err := loadNodeStore()
	if err != nil {
		return nil, err
	}
	return store.Nodes(), nil
}

// anyNodeEnabled reports whether at least one node may be rendered.
func anyNodeEnabled(nodes []sbnode.Node) bool {
	for _, n := range nodes {
		if n.Enabled {
			return true
		}
	}
	return false
}

// loadNodeStore opens the node file. The panel and the subscription service
// share it, so every action reloads it instead of caching a store.
func loadNodeStore() (*sbnode.Store, error) {
	return sbnode.Load(sysinfo.NodesFile)
}

// nodeByID returns the current state of one node.
func nodeByID(id string) (sbnode.Node, bool) {
	store, err := loadNodeStore()
	if err != nil {
		return sbnode.Node{}, false
	}
	return store.ByID(id)
}

// node returns one node from the rendered snapshot.
func (a *App) node(id string) (sbnode.Node, bool) {
	for _, n := range a.nodes {
		if n.ID == id {
			return n, true
		}
	}
	return sbnode.Node{}, false
}

// nodeLabel renders one node as "name · protocol · :port · state".
func (a *App) nodeLabel(l i18n.Lang, id string) string {
	n, ok := a.node(id)
	if !ok {
		return id
	}
	parts := []string{n.Name, protocolLabel(n.Protocol), fmt.Sprintf(":%d", n.Port)}
	if n.Enabled {
		parts = append(parts, l.T("state_enabled"))
	} else {
		parts = append(parts, l.T("state_disabled"))
	}
	return strings.Join(parts, " · ")
}

// protocolLabel is the human readable name of a protocol key.
func protocolLabel(key string) string {
	if label, ok := state.Labels[key]; ok {
		return label
	}
	return key
}

// refreshNodeMenus reloads the snapshot and rebuilds the node menus, so a node
// created or deleted in a task is visible without leaving the section.
func (a *App) refreshNodeMenus() {
	a.loadNodes()
	onList := a.current().id == "node-list"
	for i, m := range a.stack {
		if m.id == "node" {
			a.stack[i] = a.nodeMenu()
			break
		}
	}
	if onList {
		a.stack[len(a.stack)-1] = a.nodeListMenu()
	}
}

// enterNode refreshes the snapshot and pushes the node section.
func enterNode() actionFunc {
	return func(a *App) tea.Cmd {
		a.loadNodes()
		a.push(a.nodeMenu())
		return nil
	}
}

func (a *App) nodeMenu() *menu {
	return &menu{id: "node", title: tk("node_title"), nodes: []*node{
		{id: "node-list", label: tk("node_list"), desc: tk("desc_node_list"), sub: a.nodeListMenu()},
		leaf("node-new", "node_new", "desc_node_new", newNodeAction()),
		leaf("node-params", "node_params", "desc_node_params", func(a *App) tea.Cmd {
			a.push(buildParams())
			return nil
		}),
	}}
}

// nodeListMenu lists every node; opening one pushes its own menu.
func (a *App) nodeListMenu() *menu {
	nodes := make([]*node, 0, len(a.nodes)+1)
	for _, n := range a.nodes {
		id := n.ID
		nodes = append(nodes, &node{
			id:     "node-" + id,
			label:  func(l i18n.Lang) string { return a.nodeLabel(l, id) },
			desc:   tk("desc_node_open"),
			action: openNode(id),
		})
	}
	if len(nodes) == 0 {
		nodes = append(nodes, &node{
			id:     "node-none",
			label:  tk("node_list_empty"),
			desc:   tk("desc_node_new"),
			action: newNodeAction(),
		})
	}
	return &menu{id: "node-list", title: tk("node_list"), nodes: nodes}
}

// openNode pushes one node's menu.
func openNode(id string) actionFunc {
	return func(a *App) tea.Cmd {
		a.push(a.nodeDetailMenu(id))
		return nil
	}
}

func (a *App) nodeDetailMenu(id string) *menu {
	n, ok := a.node(id)
	if !ok {
		return a.nodeMenu()
	}
	field := func(key string, value func(sbnode.Node, i18n.Lang) string) func(i18n.Lang) string {
		return func(l i18n.Lang) string {
			label := l.T(key)
			if current, ok := a.node(id); ok {
				if extra := value(current, l); extra != "" {
					return label + " · " + extra
				}
			}
			return label
		}
	}
	title := func(l i18n.Lang) string {
		if current, ok := a.node(id); ok {
			return l.T("node_detail") + " · " + current.Name
		}
		return l.T("node_detail")
	}
	entries := []*node{
		{id: "node-toggle", label: field("node_toggle", enabledText), desc: tk("desc_node_toggle"), action: toggleNode(id)},
		leaf("node-rename", "node_rename", "desc_node_rename", renameNode(id)),
		leaf("node-port", "node_port", "desc_node_port", editNodePort(id)),
	}
	switch n.Protocol {
	case state.ProtoVLESSReality:
		entries = append(entries,
			&node{id: "node-sni", label: field("param_sni", sniText), desc: tk("desc_param_sni"), sub: a.nodeSNIMenu(id)},
			leaf("node-privkey", "param_privkey", "desc_param_privkey", regenNodeRealityKeys(id)),
			leaf("node-shortid", "param_shortid", "desc_param_shortid", regenNodeShortID(id)),
		)
	case state.ProtoHysteria2:
		entries = append(entries, leaf("node-hop", "param_hop", "desc_param_hop", editNodeHop(id)))
	}
	entries = append(entries, leaf("node-delete", "node_delete", "desc_node_delete", deleteNode(id)))
	return &menu{id: "node-detail", title: title, nodes: entries}
}

func enabledText(n sbnode.Node, l i18n.Lang) string {
	if n.Enabled {
		return l.T("state_enabled")
	}
	return l.T("state_disabled")
}

func sniText(n sbnode.Node, l i18n.Lang) string {
	if sni := n.Param(sbnode.ParamRealitySNI); sni != "" {
		return sni
	}
	return l.T("not_set")
}

func hopText(n sbnode.Node, l i18n.Lang) string {
	if hop := n.Param(sbnode.ParamHopRange); hop != "" {
		return hop
	}
	return l.T("not_set")
}

// nodeSNIMenu picks the Reality handshake domain, a preset or a custom value.
func (a *App) nodeSNIMenu(id string) *menu {
	presets := []string{"academy.nvidia.com", "apple.com", "bing.com", "microsoft.com", "cloudflare.com"}
	nodes := make([]*node, 0, len(presets)+1)
	for _, preset := range presets {
		preset := preset
		nodes = append(nodes, &node{
			id:     "node-sni-" + preset,
			label:  func(i18n.Lang) string { return preset },
			desc:   tk("desc_sni_use"),
			action: setNodeSNI(id, preset),
		})
	}
	nodes = append(nodes, leaf("node-sni-custom", "param_sni_custom", "desc_sni_custom", editNodeSNI(id)))
	return &menu{id: "node-sni", title: tk("param_sni"), nodes: nodes}
}

// newNodeAction creates a node: name, protocol, port. The protocol and port
// steps are pushed as menus and forms over the same frame.
func newNodeAction() actionFunc {
	return func(a *App) tea.Cmd {
		lang := a.lang
		a.openForm(lang.T("node_new"), lang.T("node_name_prompt"), "", "", func(a *App, value string) (tea.Cmd, error) {
			name := strings.TrimSpace(value)
			if name == "" {
				return nil, errors.New(lang.T("node_name_required"))
			}
			a.push(a.nodeProtocolMenu(name))
			return nil, nil
		})
		return nil
	}
}

func (a *App) nodeProtocolMenu(name string) *menu {
	nodes := make([]*node, 0, len(state.Keys))
	for _, key := range state.Keys {
		key := key
		nodes = append(nodes, &node{
			id:    "new-proto-" + key,
			label: func(i18n.Lang) string { return protocolLabel(key) },
			desc:  tk("desc_node_proto_pick"),
			action: func(a *App) tea.Cmd {
				lang := a.lang
				def := state.DefaultPorts[key]
				a.openForm(lang.T("node_port"), fmt.Sprintf(lang.T("node_port_prompt"), def), def, "", func(a *App, value string) (tea.Cmd, error) {
					port, err := strconv.Atoi(strings.TrimSpace(value))
					if err != nil || port < 1 || port > 65535 {
						return nil, errors.New(lang.T("port_invalid"))
					}
					n := sbnode.New(key, name, port, nil)
					return a.startTask(lang.T("node_new"), nodeChange(lang, func(store *sbnode.Store, _ time.Time) error {
						if err := sbnode.CheckSubPort(n, state.Load().SubPort()); err != nil {
							return err
						}
						return store.Add(n)
					})), nil
				})
				return nil
			},
		})
	}
	return &menu{id: "node-new-proto", title: tk("node_new"), nodes: nodes}
}

// nodeChange builds the body of a node task: load the file under its lock, apply
// the change, save it and then push the result to the core.
func nodeChange(lang i18n.Lang, change func(*sbnode.Store, time.Time) error) taskFunc {
	return func(ctx context.Context, r *taskReporter) error {
		store, lock, err := sbnode.Locked(sysinfo.NodesFile)
		if err != nil {
			return err
		}
		if err := change(store, time.Now()); err != nil {
			lock.Unlock()
			return err
		}
		lock.Unlock()
		return applyDeployment(ctx, lang, r.Log)
	}
}

// nodeForm opens a prompt that edits one node through store.Update and then
// pushes the result to the core.
func nodeForm(a *App, id, titleKey, prompt, initial, hint string, apply func(*sbnode.Node, string) error) tea.Cmd {
	lang := a.lang
	a.openForm(lang.T(titleKey), prompt, initial, hint, func(a *App, value string) (tea.Cmd, error) {
		value = strings.TrimSpace(value)
		return a.startTask(lang.T(titleKey), nodeChange(lang, func(store *sbnode.Store, _ time.Time) error {
			return store.Update(id, func(n *sbnode.Node) error { return apply(n, value) })
		})), nil
	})
	return nil
}

func renameNode(id string) actionFunc {
	return func(a *App) tea.Cmd {
		n, ok := nodeByID(id)
		if !ok {
			a.setToast(a.lang.T("node_missing"), true)
			return nil
		}
		return nodeForm(a, id, "node_rename", a.lang.T("node_name_prompt"), n.Name, "", func(n *sbnode.Node, value string) error {
			if value == "" {
				return errors.New(a.lang.T("node_name_required"))
			}
			n.Name = value
			return nil
		})
	}
}

func editNodePort(id string) actionFunc {
	return func(a *App) tea.Cmd {
		n, ok := nodeByID(id)
		if !ok {
			a.setToast(a.lang.T("node_missing"), true)
			return nil
		}
		return nodeForm(a, id, "node_port", fmt.Sprintf(a.lang.T("node_port_prompt"), fmt.Sprint(n.Port)), fmt.Sprint(n.Port), "", func(n *sbnode.Node, value string) error {
			port, err := strconv.Atoi(value)
			if err != nil || port < 1 || port > 65535 {
				return errors.New(a.lang.T("port_invalid"))
			}
			if err := sbnode.CheckSubPort(*n, state.Load().SubPort()); err != nil {
				return err
			}
			n.Port = port
			return nil
		})
	}
}

func setNodeSNI(id, sni string) actionFunc {
	return func(a *App) tea.Cmd {
		lang := a.lang
		return a.startTask(lang.T("param_sni"), nodeChange(lang, func(store *sbnode.Store, _ time.Time) error {
			return store.Update(id, func(n *sbnode.Node) error {
				n.Params[sbnode.ParamRealitySNI] = sni
				return nil
			})
		}))
	}
}

func editNodeSNI(id string) actionFunc {
	return func(a *App) tea.Cmd {
		n, ok := nodeByID(id)
		if !ok {
			a.setToast(a.lang.T("node_missing"), true)
			return nil
		}
		current := n.Param(sbnode.ParamRealitySNI)
		if current == "" {
			current = state.DefaultSNI
		}
		return nodeForm(a, id, "param_sni", fmt.Sprintf(a.lang.T("param_sni_prompt"), current), current, "", func(n *sbnode.Node, value string) error {
			if value == "" {
				value = state.DefaultSNI
			}
			n.Params[sbnode.ParamRealitySNI] = value
			return nil
		})
	}
}

func editNodeHop(id string) actionFunc {
	return func(a *App) tea.Cmd {
		n, ok := nodeByID(id)
		if !ok {
			a.setToast(a.lang.T("node_missing"), true)
			return nil
		}
		lang := a.lang
		hop := n.Param(sbnode.ParamHopRange)
		if hop == "" {
			hop = state.DefaultHopRange
		}
		return nodeForm(a, id, "param_hop", fmt.Sprintf(lang.T("param_hop_prompt"), state.DefaultHopRange), hop, "", func(n *sbnode.Node, value string) error {
			if value == "" {
				value = state.DefaultHopRange
			}
			if !validHopRange(value) {
				return errors.New(lang.T("param_invalid_range"))
			}
			n.Params[sbnode.ParamHopRange] = value
			return nil
		})
	}
}

func regenNodeRealityKeys(id string) actionFunc {
	return func(a *App) tea.Cmd {
		lang := a.lang
		return a.startTask(lang.T("param_privkey"), nodeChange(lang, func(store *sbnode.Store, _ time.Time) error {
			priv, pub := secret.RealityKeypair()
			if priv == "" || pub == "" {
				return errors.New(lang.T("param_key_fail"))
			}
			return store.Update(id, func(n *sbnode.Node) error {
				n.Params[sbnode.ParamRealityPrivate] = priv
				n.Params[sbnode.ParamRealityPublic] = pub
				return nil
			})
		}))
	}
}

func regenNodeShortID(id string) actionFunc {
	return func(a *App) tea.Cmd {
		lang := a.lang
		return a.startTask(lang.T("param_shortid"), nodeChange(lang, func(store *sbnode.Store, _ time.Time) error {
			return store.Update(id, func(n *sbnode.Node) error {
				n.Params[sbnode.ParamRealityShortID] = secret.ShortID()
				return nil
			})
		}))
	}
}

func toggleNode(id string) actionFunc {
	return func(a *App) tea.Cmd {
		lang := a.lang
		return a.startTask(lang.T("node_toggle"), nodeChange(lang, func(store *sbnode.Store, _ time.Time) error {
			return store.Update(id, func(n *sbnode.Node) error {
				n.Enabled = !n.Enabled
				if err := sbnode.CheckSubPort(*n, state.Load().SubPort()); err != nil {
					return err
				}
				return nil
			})
		}))
	}
}

// deleteNode removes a node and, in the same change, drops its id from every
// account that selected it, so no selection is left dangling.
func deleteNode(id string) actionFunc {
	return func(a *App) tea.Cmd {
		lang := a.lang
		n, ok := nodeByID(id)
		if !ok {
			a.setToast(lang.T("node_missing"), true)
			return nil
		}
		affected := accountsForNode(id)
		prompt := fmt.Sprintf(lang.T("node_delete_confirm"), n.Name, affected)
		a.openForm(lang.T("node_delete"), prompt, "", "y/N", func(a *App, value string) (tea.Cmd, error) {
			switch strings.ToLower(strings.TrimSpace(value)) {
			case "y", "yes":
				return a.startTask(lang.T("node_delete"), func(ctx context.Context, r *taskReporter) error {
					store, lock, err := sbnode.Locked(sysinfo.NodesFile)
					if err != nil {
						return err
					}
					if err := store.Remove(id); err != nil {
						lock.Unlock()
						return err
					}
					lock.Unlock()
					// The cascade is the same one the panel runs: user.Store.ForgetNode
					// is the single implementation, so deleting a node leaves one state
					// whichever interface performed it. It runs even when no account
					// selected the node, because a credential or usage entry can survive
					// a deselection.
					users, ulock, err := user.Locked(sysinfo.UsersFile)
					if err != nil {
						return err
					}
					users.ForgetNode(id)
					if err := users.Save(); err != nil {
						ulock.Unlock()
						return err
					}
					ulock.Unlock()
					return applyDeployment(ctx, lang, r.Log)
				}), nil
			default:
				return nil, errors.New(lang.T("cancelled"))
			}
		})
		return nil
	}
}

// accountsForNode counts the accounts that selected a node, which is what a
// delete warning reports.
func accountsForNode(id string) int {
	store, err := loadUsers()
	if err != nil {
		return 0
	}
	count := 0
	for _, u := range store.Users() {
		if u.Selects(id) {
			count++
		}
	}
	return count
}
