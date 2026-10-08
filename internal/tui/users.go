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
	"github.com/EasySBTeam/EasySB/internal/secret"
	"github.com/EasySBTeam/EasySB/internal/state"
	"github.com/EasySBTeam/EasySB/internal/subscribe"
	"github.com/EasySBTeam/EasySB/internal/sysinfo"
	"github.com/EasySBTeam/EasySB/internal/user"
)

// v4 replaces the node-wide credential with one account per subscriber, so the
// panel needs a place to create, edit, disable and delete accounts and to hand
// out each account's subscription endpoint and QR code.
//
// The list the menus render from is a snapshot taken on entering the section and
// refreshed after every change, because menu labels are drawn on every frame and
// reading the account file per frame would be wasteful.

// loadAccounts refreshes the snapshot the account menus render from.
func (a *App) loadAccounts() {
	store, err := loadUsers()
	if err != nil {
		a.setToast(err.Error(), true)
		return
	}
	a.accounts = store.Users()
}

// refreshAccountMenus reloads the snapshot and rebuilds the account menus. The
// submenus are built when the section is entered, so without this a newly
// created account stayed invisible under the account list until the operator
// left the section and came back.
func (a *App) refreshAccountMenus() {
	a.loadAccounts()
	onList := a.current().id == "user-list"
	for i, m := range a.stack {
		if m.id == "users" {
			a.stack[i] = a.usersMenu()
			break
		}
	}
	if onList {
		a.stack[len(a.stack)-1] = a.userListMenu()
	}
}

// loadUsers opens the account file. The panel and the subscription service share
// it, so every action reloads it instead of caching a store.
func loadUsers() (*user.Store, error) {
	return user.Load(sysinfo.UsersFile)
}

// accountByToken returns the current state of one account.
func accountByToken(token string) (user.User, bool) {
	store, err := loadUsers()
	if err != nil {
		return user.User{}, false
	}
	return store.ByToken(token)
}

// accountsChange builds the body of an account task: load the file, apply the
// change, save it and then push the result to the core.
func accountsChange(lang i18n.Lang, change func(*user.Store, time.Time) error) taskFunc {
	return func(ctx context.Context, r *taskReporter) error {
		now := time.Now()
		// The change is a read-modify-write of a file the accounting service writes
		// too, so it runs under the account lock: a change made here is seen by that
		// service instead of being overwritten by a cycle that read the file before it.
		store, lock, err := user.Locked(sysinfo.UsersFile)
		if err != nil {
			return err
		}
		if err := change(store, now); err != nil {
			lock.Unlock()
			return err
		}
		if err := store.Save(); err != nil {
			lock.Unlock()
			return err
		}
		// The lock is released here so this write path is not the one holding it.
		// ApplyStore takes it again around reload-apply-record, because the set that is
		// applied and the set recorded as applied have to be the same one -- which does
		// mean it is held across the core restart on purpose.
		lock.Unlock()
		return applyAccounts(ctx, lang, r.Log)
	}
}

// applyAccounts pushes the account list to the core. A node change and an
// account change take the same path, so a new account is live without a
// separate deploy step.
func applyAccounts(ctx context.Context, lang i18n.Lang, log func(string)) error {
	return applyDeployment(ctx, lang, log)
}

// accountsAction turns an account change into a menu action.
func accountsAction(titleKey string, change func(*user.Store, time.Time) error) actionFunc {
	return func(a *App) tea.Cmd {
		return a.startTask(a.lang.T(titleKey), accountsChange(a.lang, change))
	}
}

// enterUsers refreshes the snapshot and pushes the account section.
func enterUsers() actionFunc {
	return func(a *App) tea.Cmd {
		a.loadAccounts()
		a.loadNodes()
		a.push(a.usersMenu())
		return nil
	}
}

func (a *App) usersMenu() *menu {
	return &menu{id: "users", title: tk("users_title"), nodes: []*node{
		{id: "users-list", label: tk("users_list"), desc: tk("desc_users_list"), sub: a.userListMenu()},
		leaf("users-new", "users_new", "desc_users_new", newUserAction()),
	}}
}

// userListMenu lists every account; opening one pushes its own menu.
func (a *App) userListMenu() *menu {
	nodes := make([]*node, 0, len(a.accounts)+1)
	for _, account := range a.accounts {
		token := account.Token
		nodes = append(nodes, &node{
			id:     "user-" + token,
			label:  func(l i18n.Lang) string { return a.accountLabel(l, token) },
			desc:   tk("desc_user_open"),
			action: openUser(token),
		})
	}
	if len(nodes) == 0 {
		nodes = append(nodes, &node{
			id:     "user-none",
			label:  tk("users_empty"),
			desc:   tk("desc_users_new"),
			action: newUserAction(),
		})
	}
	return &menu{id: "user-list", title: tk("users_list"), nodes: nodes}
}

// accountLabel renders one account as "name · status · traffic".
func (a *App) accountLabel(l i18n.Lang, token string) string {
	account, ok := a.account(token)
	if !ok {
		return token
	}
	used := formatSize(account.UploadBytes + account.DownloadBytes)
	if account.QuotaBytes > 0 {
		used += " / " + formatSize(account.QuotaBytes)
	} else {
		used += " / " + l.T("user_quota_unlimited")
	}
	parts := []string{account.Name, l.T(statusKey(account.Status(time.Now()))), used}
	return strings.Join(parts, " · ")
}

// account returns one account from the rendered snapshot.
func (a *App) account(token string) (user.User, bool) {
	for _, account := range a.accounts {
		if account.Token == token {
			return account, true
		}
	}
	return user.User{}, false
}

// openUser pushes one account's menu. Every action inside looks the account up by
// token, so a rename cannot leave the menu pointing at the wrong account.
func openUser(token string) actionFunc {
	return func(a *App) tea.Cmd {
		a.push(a.userMenu(token))
		return nil
	}
}

// pickAccount pushes a menu of every account and runs act on the one the operator
// chooses. The subscription, QR and share-link screens all start this way, because
// each of them belongs to exactly one account.
func pickAccount(titleKey string, act func(token string) actionFunc) actionFunc {
	return func(a *App) tea.Cmd {
		a.loadAccounts()
		a.push(a.accountPicker(titleKey, act))
		return nil
	}
}

func (a *App) accountPicker(titleKey string, act func(token string) actionFunc) *menu {
	nodes := make([]*node, 0, len(a.accounts)+1)
	for _, account := range a.accounts {
		token := account.Token
		nodes = append(nodes, &node{
			id:     "pick-" + token,
			label:  func(l i18n.Lang) string { return a.accountLabel(l, token) },
			desc:   tk("desc_user_pick"),
			action: act(token),
		})
	}
	if len(nodes) == 0 {
		nodes = append(nodes, &node{
			id: "pick-none",
			// Subscriptions are issued per account, so the empty state has to say why and
			// what to do, not only that there is nothing here.
			label:  tk("sub_pick_empty"),
			desc:   tk("desc_sub_pick_empty"),
			action: newUserAction(),
		})
	}
	return &menu{id: "account-picker", title: tk(titleKey), nodes: nodes}
}

func (a *App) userMenu(token string) *menu {
	field := func(key string, value func(user.User, i18n.Lang) string) func(i18n.Lang) string {
		return func(l i18n.Lang) string {
			label := l.T(key)
			if account, ok := a.account(token); ok {
				if extra := value(account, l); extra != "" {
					return label + " · " + extra
				}
			}
			return label
		}
	}
	// The title names the account, so an operator who opened several detail
	// screens in a row always knows whose quotas they are editing.
	title := func(l i18n.Lang) string {
		if account, ok := a.account(token); ok {
			return l.T("user_detail") + " · " + account.Name
		}
		return l.T("user_detail")
	}
	return &menu{id: "user-detail", title: title, nodes: []*node{
		leaf("user-sub", "user_sub", "desc_user_sub", showUserSubscription(token)),
		leaf("user-qr", "user_qr", "desc_user_qr", showUserQR(token)),
		leaf("user-links", "user_links", "desc_user_links", showUserLinks(token)),
		leaf("user-name", "user_name", "desc_user_name", renameUser(token)),
		leaf("user-remark", "user_remark", "desc_user_remark", editUserRemark(token)),
		{id: "user-quota", label: field("user_quota", quotaText), desc: tk("desc_user_quota"), action: editUserQuota(token)},
		{id: "user-expiry", label: field("user_expiry", expiryText), desc: tk("desc_user_expiry"), action: editUserExpiry(token)},
		{id: "user-nodes", label: field("user_nodes", a.nodesText), desc: tk("desc_user_nodes"), sub: a.userNodesMenu(token)},
		{id: "user-toggle", label: field("user_toggle", statusText), desc: tk("desc_user_toggle"), action: toggleUser(token)},
		leaf("user-reset", "user_reset", "desc_user_reset", resetUserUsage(token)),
		leaf("user-rotate", "user_rotate", "desc_user_rotate", rotateUserToken(token)),
		leaf("user-delete", "user_delete", "desc_user_delete", deleteUser(token)),
	}}
}

func quotaText(account user.User, l i18n.Lang) string {
	used := formatSize(account.UploadBytes + account.DownloadBytes)
	if account.QuotaBytes <= 0 {
		// 0 means unlimited; printing "0 B" here read as "no allowance.`n		return used + " / " + l.T("user_quota_unlimited")
	}
	return used + " / " + formatSize(account.QuotaBytes)
}

func expiryText(account user.User, _ i18n.Lang) string {
	if account.ExpireAt.IsZero() {
		return ""
	}
	return account.ExpireAt.Local().Format("2006-01-02")
}

func statusText(account user.User, l i18n.Lang) string {
	return l.T(statusKey(account.Status(time.Now())))
}

// nodesText names the nodes an account may use.
func (a *App) nodesText(account user.User, l i18n.Lang) string {
	names := make([]string, 0, len(account.Nodes))
	for _, id := range account.Nodes {
		if n, ok := a.node(id); ok {
			names = append(names, n.Name)
		}
	}
	if len(names) == 0 {
		return l.T("user_nodes_none")
	}
	return strings.Join(names, ", ")
}

// userNodesMenu chooses which nodes one account may use. The selection is stored
// per account and the core configuration is rebuilt from it, so a node switched
// off stops accepting that account's credentials.
func (a *App) userNodesMenu(token string) *menu {
	nodes := make([]*node, 0, len(a.nodes))
	for _, candidate := range a.nodes {
		id := candidate.ID
		nodes = append(nodes, &node{
			id: "user-node-" + id,
			label: func(i18n.Lang) string {
				mark := "[ ]"
				if account, ok := a.account(token); ok && account.Selects(id) {
					mark = "[x]"
				}
				return mark + " " + a.nodeDisplayName(id)
			},
			desc:   tk("desc_user_node_toggle"),
			action: toggleUserNode(token, id),
		})
	}
	if len(nodes) == 0 {
		nodes = append(nodes, &node{
			id:     "user-node-none",
			label:  tk("user_nodes_none"),
			desc:   tk("desc_node_new"),
			action: newNodeAction(),
		})
	}
	return &menu{id: "user-nodes", title: tk("user_nodes"), nodes: nodes}
}

// nodeDisplayName is the name a node is listed under, falling back to its id for
// a selection that outlived the node.
func (a *App) nodeDisplayName(id string) string {
	if n, ok := a.node(id); ok {
		return n.Name
	}
	return id
}

// newUserAction creates an account with every enabled node selected, no quota
// and no expiry: the operator narrows it afterwards.
func newUserAction() actionFunc {
	return func(a *App) tea.Cmd {
		lang := a.lang
		a.openForm(lang.T("users_new"), lang.T("users_new_prompt"), "", "", func(a *App, value string) (tea.Cmd, error) {
			name := strings.TrimSpace(value)
			if name == "" {
				return nil, errors.New(lang.T("users_name_required"))
			}
			return a.startTask(lang.T("users_new"), accountsChange(lang, func(store *user.Store, now time.Time) error {
				return store.Add(user.New(name, enabledSelections(), now))
			})), nil
		})
		return nil
	}
}

// enabledSelections is the default node set of a new account: every enabled node.
func enabledSelections() []user.Selection {
	store, err := loadNodeStore()
	if err != nil {
		return nil
	}
	nodes := store.Enabled()
	out := make([]user.Selection, 0, len(nodes))
	for _, n := range nodes {
		out = append(out, user.Selection{Node: n.ID, Protocol: n.Protocol})
	}
	return out
}

// accountForm opens a one-field prompt for one account. The current value is
// pre-filled and apply decides what the text means for that field.
func accountForm(a *App, token, titleKey, promptKey, hintKey string, current func(user.User) string, apply func(*user.User, string, time.Time) error) tea.Cmd {
	lang := a.lang
	account, ok := accountByToken(token)
	if !ok {
		a.setToast(lang.T("users_missing"), true)
		return nil
	}
	name := account.Name
	a.openForm(lang.T(titleKey), lang.T(promptKey), current(account), lang.T(hintKey), func(a *App, value string) (tea.Cmd, error) {
		change := func(store *user.Store, now time.Time) error {
			return store.Update(name, func(u *user.User) error {
				return apply(u, strings.TrimSpace(value), now)
			})
		}
		return a.startTask(lang.T(titleKey), accountsChange(lang, change)), nil
	})
	return nil
}

func renameUser(token string) actionFunc {
	return func(a *App) tea.Cmd {
		return accountForm(a, token, "user_name", "user_name_prompt", "", func(account user.User) string {
			return account.Name
		}, func(u *user.User, value string, _ time.Time) error {
			if value == "" {
				return errors.New(a.lang.T("users_name_required"))
			}
			u.Name = value
			return nil
		})
	}
}

func editUserRemark(token string) actionFunc {
	return func(a *App) tea.Cmd {
		return accountForm(a, token, "user_remark", "user_remark_prompt", "", func(account user.User) string {
			return account.Remark
		}, func(u *user.User, value string, _ time.Time) error {
			u.Remark = value
			return nil
		})
	}
}

// editUserQuota prompts for the quota as two fields -- a GB box and an MB box --
// so the value is exact and the operator never has to spell out a unit.
func editUserQuota(token string) actionFunc {
	return func(a *App) tea.Cmd {
		lang := a.lang
		account, ok := accountByToken(token)
		if !ok {
			a.setToast(lang.T("users_missing"), true)
			return nil
		}
		name := account.Name
		gb, mb := splitSize(account.QuotaBytes)
		a.openDualForm(lang.T("user_quota"), lang.T("user_quota_prompt"), gb, mb, lang.T("user_quota_hint"), func(a *App, gbText, mbText string) (tea.Cmd, error) {
			size, err := parseSizeFields(gbText, mbText, lang)
			if err != nil {
				return nil, err
			}
			change := func(store *user.Store, now time.Time) error {
				return store.Update(name, func(u *user.User) error {
					u.QuotaBytes = size
					return nil
				})
			}
			return a.startTask(lang.T("user_quota"), accountsChange(lang, change)), nil
		})
		return nil
	}
}

// splitSize renders a byte count as the GB and MB halves the quota prompt edits.
// The value is binary, so the two fields reconstruct it exactly.
func splitSize(bytes int64) (string, string) {
	if bytes <= 0 {
		return "0", "0"
	}
	return strconv.FormatInt(bytes>>30, 10), strconv.FormatInt((bytes>>20)&1023, 10)
}

// parseSizeFields reads the two boxes of the quota prompt. MB is capped at 1023 so
// the pair is unambiguous, and 0 GB 0 MB means unlimited, which the prompt states.
func parseSizeFields(gb, mb string, lang i18n.Lang) (int64, error) {
	g, err := strconv.ParseInt(strings.TrimSpace(gb), 10, 64)
	if err != nil || g < 0 || g > 1024 {
		return 0, errors.New(lang.T("user_quota_invalid"))
	}
	m, err := strconv.ParseInt(strings.TrimSpace(mb), 10, 64)
	if err != nil || m < 0 || m > 1023 {
		return 0, errors.New(lang.T("user_quota_invalid"))
	}
	return g<<30 + m<<20, nil
}

func editUserExpiry(token string) actionFunc {
	return func(a *App) tea.Cmd {
		return accountForm(a, token, "user_expiry", "user_expiry_prompt", "user_expiry_hint", func(account user.User) string {
			if account.ExpireAt.IsZero() {
				return ""
			}
			return account.ExpireAt.Local().Format("2006-01-02")
		}, func(u *user.User, value string, now time.Time) error {
			expiry, err := parseExpiry(value, now)
			if err != nil {
				return err
			}
			u.ExpireAt = expiry
			return nil
		})
	}
}

// toggleUser switches an account on or off. A disabled account keeps its
// counters and its credentials, so switching it back on restores access.
func toggleUser(token string) actionFunc {
	return accountsAction("user_toggle", func(store *user.Store, _ time.Time) error {
		account, ok := store.ByToken(token)
		if !ok {
			return errors.New("account not found")
		}
		return store.Update(account.Name, func(u *user.User) error {
			u.Enabled = !u.Enabled
			return nil
		})
	})
}

// resetUserUsage zeroes an account's counters, which also lifts a quota
// suspension and moves the monthly reset to today.
func resetUserUsage(token string) actionFunc {
	return accountsAction("user_reset", func(store *user.Store, now time.Time) error {
		account, ok := store.ByToken(token)
		if !ok {
			return errors.New("account not found")
		}
		return store.Update(account.Name, func(u *user.User) error {
			u.ResetCounters(now)
			return nil
		})
	})
}

// rotateUserToken invalidates the account's subscription URL and issues a new
// one. The credentials stay, so an existing client keeps working until its
// profile is refreshed from the old URL, which by then is dead.
func rotateUserToken(token string) actionFunc {
	return accountsAction("user_rotate", func(store *user.Store, _ time.Time) error {
		account, ok := store.ByToken(token)
		if !ok {
			return errors.New("account not found")
		}
		return store.Update(account.Name, func(u *user.User) error {
			u.Token = secret.Token()
			return nil
		})
	})
}

func toggleUserNode(token, id string) actionFunc {
	return accountsAction("user_protocols", func(store *user.Store, _ time.Time) error {
		account, ok := store.ByToken(token)
		if !ok {
			return errors.New("account not found")
		}
		n, ok := nodeByID(id)
		if !ok {
			return errors.New("node not found")
		}
		return store.Update(account.Name, func(u *user.User) error {
			if u.Selects(id) {
				u.Deselect(id)
			} else {
				u.Select(id, n.Protocol)
			}
			return nil
		})
	})
}

// deleteUser removes an account after a yes/no confirmation typed as a form.
func deleteUser(token string) actionFunc {
	return func(a *App) tea.Cmd {
		lang := a.lang
		account, ok := accountByToken(token)
		if !ok {
			a.setToast(lang.T("users_missing"), true)
			return nil
		}
		name := account.Name
		a.openForm(lang.T("user_delete"), fmt.Sprintf(lang.T("user_delete_confirm"), name), "", "y/N", func(a *App, value string) (tea.Cmd, error) {
			switch strings.ToLower(strings.TrimSpace(value)) {
			case "y", "yes":
				return a.startTask(lang.T("user_delete"), accountsChange(lang, func(store *user.Store, _ time.Time) error {
					return store.Remove(name)
				})), nil
			default:
				return nil, errors.New(lang.T("cancelled"))
			}
		})
		return nil
	}
}

// showUserSubscription opens the card grid with one account's client endpoints.
// One URL serves every client, so the cards differ only in the client they are
// meant for.
func showUserSubscription(token string) actionFunc {
	return func(a *App) tea.Cmd {
		lang := a.lang
		account, ok := accountByToken(token)
		if !ok {
			a.setToast(lang.T("users_missing"), true)
			return nil
		}
		cfg := state.Load()
		if cfg.Host() == "" {
			a.setToast(lang.T("sub_need_domain"), true)
			return nil
		}
		items := make([]linkItem, 0, len(subscribe.Clients))
		for _, client := range subscribe.Clients {
			items = append(items, linkItem{
				label: subscriptionTitle(lang, client),
				desc:  account.Name + " · " + clientDescription(lang, client),
				value: subscribe.ClientLink(cfg, account.Token, client),
			})
		}
		a.links = newLinksModel(lang.T("user_sub")+" · "+account.Name, items)
		return nil
	}
}

// showUserQR renders the account's import links, and the QR code of each, in the
// task panel: a QR code is a picture, so it cannot live in the card grid.
func showUserQR(token string) actionFunc {
	return func(a *App) tea.Cmd {
		lang := a.lang
		return a.startTaskQR(lang.T("user_qr"), func(_ context.Context, r *taskReporter) error {
			cfg := state.Load()
			if cfg.Host() == "" {
				r.Log(lang.T("sub_need_domain"))
				return nil
			}
			account, ok := accountByToken(token)
			if !ok {
				return errors.New(lang.T("users_missing"))
			}
			for _, client := range subscribe.Clients {
				payload := subscribe.ClientLink(cfg, account.Token, client)
				r.Log(clientLabel(lang, client) + " · " + lang.T("sub_import_link") + ":")
				r.Log(payload)
				r.Log("")
				qr, err := subscribe.QRCode(payload)
				if err != nil {
					r.Log(lang.T("sub_no_qrencode"))
					r.Log("")
					continue
				}
				for _, line := range strings.Split(qr, "\n") {
					r.Log(line)
				}
				r.Log("")
			}
			return nil
		})
	}
}

// showUserLinks opens the card grid with the account's share links, one card per
// protocol, for clients that import a single node instead of a subscription.
func showUserLinks(token string) actionFunc {
	return func(a *App) tea.Cmd {
		lang := a.lang
		account, ok := accountByToken(token)
		if !ok {
			a.setToast(lang.T("users_missing"), true)
			return nil
		}
		cfg := state.Load()
		if cfg.Host() == "" {
			a.setToast(lang.T("sub_need_domain"), true)
			return nil
		}
		nodes, err := loadNodes()
		if err != nil {
			a.setToast(err.Error(), true)
			return nil
		}
		links := subscribe.ShareLinks(cfg, nodes, account)
		if len(links) == 0 {
			a.setToast(lang.T("users_link_empty"), true)
			return nil
		}
		items := make([]linkItem, 0, len(links))
		for _, link := range links {
			items = append(items, linkItem{label: link.Name, desc: account.Name + " · " + link.Name, value: link.URI})
		}
		a.links = newLinksModel(lang.T("user_links")+" · "+account.Name, items)
		return nil
	}
}

// statusKey maps an account status to its translation key.
func statusKey(status user.Status) string {
	switch status {
	case user.StatusDisabled:
		return "user_status_disabled"
	case user.StatusExpired:
		return "user_status_expired"
	case user.StatusQuota:
		return "user_status_quota"
	default:
		return "user_status_active"
	}
}

// parseSize reads a quota in bytes. A bare number counts bytes; a suffix may be
// KB, MB, GB or TB and is binary, and 0 means unlimited.
func parseSize(s string) (int64, error) {
	text := strings.ToUpper(strings.TrimSpace(s))
	if text == "" {
		return 0, errors.New("empty quota")
	}
	multiplier := int64(1)
	for _, unit := range []struct {
		suffix string
		factor int64
	}{
		{"TB", 1 << 40},
		{"GB", 1 << 30},
		{"MB", 1 << 20},
		{"KB", 1 << 10},
	} {
		if strings.HasSuffix(text, unit.suffix) {
			multiplier = unit.factor
			text = strings.TrimSpace(strings.TrimSuffix(text, unit.suffix))
			break
		}
	}
	value, err := strconv.ParseFloat(strings.TrimSpace(text), 64)
	if err != nil || value < 0 {
		return 0, errors.New("invalid quota")
	}
	return int64(value * float64(multiplier)), nil
}

// parseExpiry reads an expiry. It accepts a date, a day offset such as "30d", or
// "0" and "never" for a permanent account. A date lasts to the end of that day,
// so the operator does not lose a day to the timezone.
func parseExpiry(s string, now time.Time) (time.Time, error) {
	text := strings.ToLower(strings.TrimSpace(s))
	switch text {
	case "", "0", "never", "none":
		return time.Time{}, nil
	}
	if strings.HasSuffix(text, "d") {
		days, err := strconv.Atoi(strings.TrimSuffix(text, "d"))
		if err != nil || days < 0 {
			return time.Time{}, errors.New("invalid expiry")
		}
		return now.AddDate(0, 0, days), nil
	}
	date, err := time.ParseInLocation("2006-01-02", text, time.Local)
	if err != nil {
		return time.Time{}, errors.New("invalid expiry")
	}
	return date.AddDate(0, 0, 1).Add(-time.Second), nil
}

// formatSize renders a byte count the way the panel shows quotas.
func formatSize(bytes int64) string {
	switch {
	case bytes >= 1<<40:
		return fmt.Sprintf("%.1f TB", float64(bytes)/(1<<40))
	case bytes >= 1<<30:
		return fmt.Sprintf("%.1f GB", float64(bytes)/(1<<30))
	case bytes >= 1<<20:
		return fmt.Sprintf("%.0f MB", float64(bytes)/(1<<20))
	case bytes >= 1<<10:
		return fmt.Sprintf("%.0f KB", float64(bytes)/(1<<10))
	default:
		return fmt.Sprintf("%d B", bytes)
	}
}
