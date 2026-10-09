package panel

import (
	"errors"
	"net/http"
	"sort"
	"strings"
	"time"

	"github.com/EasySBTeam/EasySB/internal/deploy"
	"github.com/EasySBTeam/EasySB/internal/node"
	"github.com/EasySBTeam/EasySB/internal/state"
	"github.com/EasySBTeam/EasySB/internal/subd"
	"github.com/EasySBTeam/EasySB/internal/subscribe"
	"github.com/EasySBTeam/EasySB/internal/user"
)

// userView is one account as the panel lists it. It carries the subscription
// token, which is the account's subscription credential and is shown to the
// administrator on purpose; it never carries the per-node-uuid or password fields.
type userView struct {
	Name             string    `json:"name"`
	Remark           string    `json:"remark,omitempty"`
	Token            string    `json:"token"`
	Enabled          bool      `json:"enabled"`
	Status           string    `json:"status"`
	Nodes            []string  `json:"nodes"`
	QuotaBytes       int64     `json:"quotaBytes"`
	UsedBytes        int64     `json:"usedBytes"`
	UploadBytes      int64     `json:"uploadBytes"`
	DownloadBytes    int64     `json:"downloadBytes"`
	Remaining        int64     `json:"remaining"`
	Percent          int       `json:"percent"`
	Unlimited        bool      `json:"unlimited"`
	CredentialsReady bool      `json:"credentialsReady"`
	CreatedAt        time.Time `json:"createdAt"`
	ExpireAt         time.Time `json:"expireAt"`
	LastReset        time.Time `json:"lastReset"`
	SubscriptionURL  string    `json:"subscriptionUrl,omitempty"`
}

func newUserView(cfg state.Config, u user.User, now time.Time) userView {
	url := ""
	if cfg.Host() != "" {
		url = subscribe.SubscriptionURL(cfg, u.Token)
	}
	nodes := append([]string(nil), u.Nodes...)
	sort.Strings(nodes)
	return userView{
		Name:             u.Name,
		Remark:           u.Remark,
		Token:            u.Token,
		Enabled:          u.Enabled,
		Status:           string(u.Status(now)),
		Nodes:            nodes,
		QuotaBytes:       u.QuotaBytes,
		UsedBytes:        u.UsedBytes,
		UploadBytes:      u.UploadBytes,
		DownloadBytes:    u.DownloadBytes,
		Remaining:        u.Remaining(),
		Percent:          u.Percent(),
		Unlimited:        u.Unlimited(),
		CredentialsReady: u.CredentialsReady(),
		CreatedAt:        u.CreatedAt,
		ExpireAt:         u.ExpireAt,
		LastReset:        u.LastReset,
		SubscriptionURL:  url,
	}
}

// handleListUsers returns every account with its derived readings.
func (s *Service) handleListUsers(w http.ResponseWriter, r *http.Request) {
	store, err := s.loadUsers()
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	cfg := s.stateConfig()
	now := s.now()
	users := store.Users()
	views := make([]userView, 0, len(users))
	for _, u := range users {
		views = append(views, newUserView(cfg, u, now))
	}
	writeJSON(w, http.StatusOK, map[string]any{"users": views})
}

// userRequest is the create/update payload. Pointer fields distinguish "leave
// unchanged" from "set to zero" so an explicit 0 quota means unlimited rather
// than being ignored.
type userRequest struct {
	Name       string   `json:"name"`
	Remark     *string  `json:"remark"`
	QuotaBytes *int64   `json:"quotaBytes"`
	ExpireAt   *string  `json:"expireAt"`
	Nodes      []string `json:"nodes"`
	Enabled    *bool    `json:"enabled"`
}

// handleCreateUser adds an account. With no node list it selects every enabled
// node, the same default the TUI uses.
func (s *Service) handleCreateUser(w http.ResponseWriter, r *http.Request) {
	var body userRequest
	if err := decodeJSON(r, &body); err != nil {
		badRequest(w, "invalid request body: "+err.Error())
		return
	}
	name := strings.TrimSpace(body.Name)
	if name == "" {
		badRequest(w, "user name is required")
		return
	}
	nodes, err := s.loadNodes()
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	selections, err := selectionsFor(nodes, body.Nodes)
	if err != nil {
		badRequest(w, err.Error())
		return
	}
	account := user.New(name, selections, s.now())
	if body.Remark != nil {
		account.Remark = strings.TrimSpace(*body.Remark)
	}
	if body.QuotaBytes != nil {
		if *body.QuotaBytes < 0 {
			badRequest(w, "quota cannot be negative")
			return
		}
		account.QuotaBytes = *body.QuotaBytes
	}
	if body.ExpireAt != nil {
		expiry, perr := parseExpireAt(*body.ExpireAt)
		if perr != nil {
			badRequest(w, perr.Error())
			return
		}
		account.ExpireAt = expiry
	}
	if body.Enabled != nil {
		account.Enabled = *body.Enabled
	}
	if err := s.withUserLock(func(store *user.Store) error {
		return store.Add(account)
	}); err != nil {
		s.storeError(w, err)
		return
	}
	if err := s.apply(r.Context()); err != nil {
		s.applyError(w, err)
		return
	}
	writeJSON(w, http.StatusCreated, map[string]any{"ok": true, "name": name})
}

// handleGetUser returns one account.
func (s *Service) handleGetUser(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("name")
	store, err := s.loadUsers()
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	account, ok := store.Find(name)
	if !ok {
		writeError(w, http.StatusNotFound, "user not found")
		return
	}
	writeJSON(w, http.StatusOK, newUserView(s.stateConfig(), account, s.now()))
}

// handleUpdateUser edits remark, quota, expiry, enabled or the selected node set.
func (s *Service) handleUpdateUser(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("name")
	var body userRequest
	if err := decodeJSON(r, &body); err != nil {
		badRequest(w, "invalid request body: "+err.Error())
		return
	}
	err := s.withUserLock(func(store *user.Store) error {
		nodes, nerr := s.loadNodes()
		if nerr != nil {
			return nerr
		}
		return store.Update(name, func(u *user.User) error {
			return applyUserEdit(u, body, nodes)
		})
	})
	if err != nil {
		s.storeError(w, err)
		return
	}
	if err := s.apply(r.Context()); err != nil {
		s.applyError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

// applyUserEdit mutates one account from an update payload.
func applyUserEdit(u *user.User, body userRequest, nodes *node.Store) error {
	if body.Remark != nil {
		u.Remark = strings.TrimSpace(*body.Remark)
	}
	if body.QuotaBytes != nil {
		if *body.QuotaBytes < 0 {
			return errors.New("quota cannot be negative")
		}
		u.QuotaBytes = *body.QuotaBytes
	}
	if body.ExpireAt != nil {
		expiry, err := parseExpireAt(*body.ExpireAt)
		if err != nil {
			return err
		}
		u.ExpireAt = expiry
	}
	if body.Enabled != nil {
		u.Enabled = *body.Enabled
	}
	if body.Nodes != nil {
		proto := nodeProtocols(nodes.Nodes())
		want := map[string]bool{}
		for _, id := range body.Nodes {
			if _, ok := proto[id]; !ok {
				return errors.New("unknown node " + id)
			}
			want[id] = true
		}
		for _, id := range append([]string(nil), u.Nodes...) {
			if !want[id] {
				u.Deselect(id)
			}
		}
		for id := range want {
			u.Select(id, proto[id])
		}
	}
	return nil
}

// handleDeleteUser removes an account and applies the change.
func (s *Service) handleDeleteUser(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("name")
	err := s.withUserLock(func(store *user.Store) error {
		return store.Remove(name)
	})
	if err != nil {
		s.storeError(w, err)
		return
	}
	if err := s.apply(r.Context()); err != nil {
		s.applyError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

// handleSetUserEnabled switches an account on or off.
func (s *Service) handleSetUserEnabled(enabled bool) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		name := r.PathValue("name")
		err := s.withUserLock(func(store *user.Store) error {
			return store.Update(name, func(u *user.User) error {
				u.Enabled = enabled
				return nil
			})
		})
		if err != nil {
			s.storeError(w, err)
			return
		}
		if err := s.apply(r.Context()); err != nil {
			s.applyError(w, err)
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"ok": true, "enabled": enabled})
	}
}

// handleResetUser zeroes an account's traffic counters.
func (s *Service) handleResetUser(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("name")
	err := s.withUserLock(func(store *user.Store) error {
		return store.Update(name, func(u *user.User) error {
			u.ResetCounters(s.now())
			return nil
		})
	})
	if err != nil {
		s.storeError(w, err)
		return
	}
	if err := s.apply(r.Context()); err != nil {
		s.applyError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

// handleUserSubscriptions renders one account's subscription endpoints and share
// links. The links carry the account credentials, so this stays behind auth.
func (s *Service) handleUserSubscriptions(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("name")
	store, err := s.loadUsers()
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	account, ok := store.Find(name)
	if !ok {
		writeError(w, http.StatusNotFound, "user not found")
		return
	}
	cfg := s.stateConfig()
	if cfg.Host() == "" {
		writeError(w, http.StatusUnprocessableEntity, "set a domain or server address before sharing a subscription")
		return
	}
	nodes, err := deploy.LoadNodes(s.opts.NodesPath)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	clients := make([]map[string]string, 0, len(subscribe.Clients))
	for _, client := range subscribe.Clients {
		clients = append(clients, map[string]string{
			"client": string(client),
			"url":    subscribe.SubscriptionURL(cfg, account.Token),
			"link":   subscribe.ClientLink(cfg, account.Token, client),
		})
	}
	links := subscribe.ShareLinks(cfg, nodes, account)
	shareLinks := make([]map[string]string, 0, len(links))
	for _, l := range links {
		shareLinks = append(shareLinks, map[string]string{"key": l.Key, "name": l.Name, "uri": l.URI})
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"name":       account.Name,
		"token":      account.Token,
		"endpoint":   subscribe.Endpoint(cfg),
		"clients":    clients,
		"shareLinks": shareLinks,
	})
}

// handleUserDocument renders one client format for one account.
func (s *Service) handleUserDocument(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("name")
	client := subscribe.Client(strings.TrimSpace(r.URL.Query().Get("client")))
	switch client {
	case subscribe.ClientSingBox, subscribe.ClientMihomo, subscribe.ClientV2Ray:
	default:
		client = subscribe.ClientSingBox
	}
	store, err := s.loadUsers()
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	account, ok := store.Find(name)
	if !ok {
		writeError(w, http.StatusNotFound, "user not found")
		return
	}
	nodes, err := deploy.LoadNodes(s.opts.NodesPath)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	doc, err := subscribe.Document(s.stateConfig(), nodes, account, client)
	if err != nil {
		writeError(w, http.StatusUnprocessableEntity, err.Error())
		return
	}
	w.Header().Set("Content-Type", subscribe.ContentType(client))
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(doc)
}

// handleSubscriptions reports the subscription endpoint shared by every account:
// the host it is published under, the client formats it serves and whether the
// subscription service is up. The endpoint carries no token, so this overview
// lists no credentials.
func (s *Service) handleSubscriptions(w http.ResponseWriter, r *http.Request) {
	cfg := s.stateConfig()
	clients := make([]string, 0, len(subscribe.Clients))
	for _, c := range subscribe.Clients {
		clients = append(clients, string(c))
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"host":     cfg.Host(),
		"endpoint": subscribe.Endpoint(cfg),
		"path":     subd.SubPath,
		"clients":  clients,
		"active":   subd.Active(r.Context()),
	})
}

// selectionsFor resolves a node id list to account selections. An empty list
// selects every enabled node.
func selectionsFor(nodes *node.Store, ids []string) ([]user.Selection, error) {
	proto := nodeProtocols(nodes.Nodes())
	if len(ids) == 0 {
		var out []user.Selection
		for _, n := range nodes.Enabled() {
			out = append(out, user.Selection{Node: n.ID, Protocol: n.Protocol})
		}
		return out, nil
	}
	out := make([]user.Selection, 0, len(ids))
	for _, id := range ids {
		protocol, ok := proto[id]
		if !ok {
			return nil, errors.New("unknown node " + id)
		}
		out = append(out, user.Selection{Node: id, Protocol: protocol})
	}
	return out, nil
}

// parseExpireAt reads an expiry date as a calendar day or an RFC3339 instant. An
// empty value clears the expiry.
func parseExpireAt(value string) (time.Time, error) {
	value = strings.TrimSpace(value)
	if value == "" {
		return time.Time{}, nil
	}
	if day, err := time.ParseInLocation("2006-01-02", value, time.Local); err == nil {
		return day.AddDate(0, 0, 1).Add(-time.Second), nil
	}
	instant, err := time.Parse(time.RFC3339, value)
	if err != nil {
		return time.Time{}, errors.New("expiry must be a YYYY-MM-DD date or an RFC3339 timestamp")
	}
	return instant, nil
}
