package panel

import (
	"encoding/json"
	"errors"
	"net/http"
	"strconv"
	"strings"

	"github.com/EasySBTeam/EasySB/internal/deploy"
	"github.com/EasySBTeam/EasySB/internal/node"
	"github.com/EasySBTeam/EasySB/internal/state"
	"github.com/EasySBTeam/EasySB/internal/user"
)

// paramInfo describes one protocol parameter a form may show.
type paramInfo struct {
	Key     string `json:"key"`
	Label   string `json:"label"`
	Default string `json:"default"`
}

// protocolInfo describes one protocol for the node form.
type protocolInfo struct {
	Key         string      `json:"key"`
	Label       string      `json:"label"`
	DefaultPort int         `json:"defaultPort"`
	Params      []paramInfo `json:"params"`
}

// protocolInfos lists the protocols this build renders, with the parameters a
// client may set. Generated secrets (Reality keypair, short id) are never
// client-settable.
func protocolInfos() []protocolInfo {
	var out []protocolInfo
	for _, key := range state.Keys {
		port, _ := strconv.Atoi(state.DefaultPorts[key])
		info := protocolInfo{Key: key, Label: state.Labels[key], DefaultPort: port}
		switch key {
		case state.ProtoVLESSReality:
			info.Params = []paramInfo{{Key: node.ParamRealitySNI, Label: "SNI", Default: state.DefaultSNI}}
		case state.ProtoHysteria2:
			info.Params = []paramInfo{{Key: node.ParamHopRange, Label: "Port hopping", Default: state.DefaultHopRange}}
		}
		out = append(out, info)
	}
	return out
}

// settableParams filters a client-supplied parameter map to the keys the protocol
// actually allows, so a request cannot inject a Reality private key.
func settableParams(protocol string, params map[string]string) map[string]string {
	allowed := map[string]bool{}
	for _, info := range protocolInfos() {
		if info.Key != protocol {
			continue
		}
		for _, p := range info.Params {
			allowed[p.Key] = true
		}
	}
	out := map[string]string{}
	for k, v := range params {
		if allowed[k] {
			out[k] = strings.TrimSpace(v)
		}
	}
	return out
}

// nodeView is a node plus the panel's derived readings.
type nodeView struct {
	node.Node
	UsedBy int `json:"usedBy"`
}

// handleListNodes returns every node with how many accounts select it.
func (s *Service) handleListNodes(w http.ResponseWriter, r *http.Request) {
	store, err := s.loadNodes()
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	users, err := s.loadUsers()
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	nodes := store.Nodes()
	counts := map[string]int{}
	for _, u := range users.Users() {
		for _, id := range u.Nodes {
			counts[id]++
		}
	}
	views := make([]nodeView, 0, len(nodes))
	for _, n := range nodes {
		views = append(views, nodeView{Node: n, UsedBy: counts[n.ID]})
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"nodes":     views,
		"protocols": protocolInfos(),
	})
}

// nodeRequest is the create/update payload.
type nodeRequest struct {
	Name     string            `json:"name"`
	Protocol string            `json:"protocol"`
	Port     int               `json:"port"`
	Enabled  *bool             `json:"enabled"`
	Params   map[string]string `json:"params"`
}

// handleCreateNode stores a new node and applies the change.
func (s *Service) handleCreateNode(w http.ResponseWriter, r *http.Request) {
	var body nodeRequest
	if err := decodeJSON(r, &body); err != nil {
		badRequest(w, "invalid request body: "+err.Error())
		return
	}
	var created node.Node
	err := s.withNodeLock(func(store *node.Store) error {
		n := node.New(body.Protocol, body.Name, body.Port, settableParams(body.Protocol, body.Params))
		if body.Enabled != nil {
			n.Enabled = *body.Enabled
		}
		if err := store.Add(n); err != nil {
			return err
		}
		created = n
		return nil
	})
	if err != nil {
		badRequest(w, err.Error())
		return
	}
	if err := s.apply(r.Context()); err != nil {
		s.applyError(w, err)
		return
	}
	// Report the created node's id and name, the way the account create path does:
	// a scripted client should not have to re-list the store to learn the id it
	// needs for the follow-up calls.
	writeJSON(w, http.StatusCreated, map[string]any{"ok": true, "id": created.ID, "name": created.Name})
}

// handleGetNode returns one node.
func (s *Service) handleGetNode(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	store, err := s.loadNodes()
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	n, ok := store.ByID(id)
	if !ok {
		writeError(w, http.StatusNotFound, "node not found")
		return
	}
	writeJSON(w, http.StatusOK, nodeView{Node: n})
}

// handleUpdateNode edits a node and applies the change.
func (s *Service) handleUpdateNode(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	var body nodeRequest
	if err := decodeJSON(r, &body); err != nil {
		badRequest(w, "invalid request body: "+err.Error())
		return
	}
	err := s.withNodeLock(func(store *node.Store) error {
		return store.Update(id, func(n *node.Node) error {
			if strings.TrimSpace(body.Name) != "" {
				n.Name = strings.TrimSpace(body.Name)
			}
			if body.Protocol != "" {
				n.Protocol = body.Protocol
			}
			if body.Port != 0 {
				n.Port = body.Port
			}
			if body.Enabled != nil {
				n.Enabled = *body.Enabled
			}
			for k, v := range settableParams(n.Protocol, body.Params) {
				n.Params[k] = v
			}
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

// handleDeleteNode removes a node, clears account selections of it and applies.
func (s *Service) handleDeleteNode(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	err := s.withNodeLock(func(store *node.Store) error {
		return store.Remove(id)
	})
	if err == nil {
		// A deleted node must not linger as a dangling selection in any account,
		// or the subscription would keep advertising a node the host no longer
		// serves.
		err = s.withUserLock(func(users *user.Store) error {
			users.Mutate(func(u *user.User) {
				u.Deselect(id)
				delete(u.Credentials, id)
				delete(u.Usage, id)
			})
			return users.Save()
		})
	}
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

// handleSetNodeEnabled enables or disables a node.
func (s *Service) handleSetNodeEnabled(enabled bool) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		id := r.PathValue("id")
		err := s.withNodeLock(func(store *node.Store) error {
			return store.Update(id, func(n *node.Node) error {
				n.Enabled = enabled
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

// handleNodeConfig returns the inbound the core would render for one node.
func (s *Service) handleNodeConfig(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	store, err := s.loadNodes()
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	if _, ok := store.ByID(id); !ok {
		writeError(w, http.StatusNotFound, "node not found")
		return
	}
	users, err := s.loadUsers()
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	data, err := deploy.ServerConfig(s.stateConfig(), store.Nodes(), users.Routable(s.now()))
	if err != nil {
		writeError(w, http.StatusUnprocessableEntity, err.Error())
		return
	}
	var doc struct {
		Inbounds []json.RawMessage `json:"inbounds"`
	}
	if err := json.Unmarshal(data, &doc); err != nil {
		writeError(w, http.StatusInternalServerError, "cannot parse the rendered configuration")
		return
	}
	for _, raw := range doc.Inbounds {
		var tag struct {
			Tag string `json:"tag"`
		}
		if err := json.Unmarshal(raw, &tag); err == nil && tag.Tag == id {
			writeJSON(w, http.StatusOK, map[string]any{"inbound": json.RawMessage(raw)})
			return
		}
	}
	writeError(w, http.StatusNotFound, "the node has no rendered inbound (disabled?)")
}

// applyError maps a deploy failure to an HTTP status.
func (s *Service) applyError(w http.ResponseWriter, err error) {
	if errors.Is(err, deploy.ErrRejected) {
		writeError(w, http.StatusUnprocessableEntity, "the core rejected the configuration: "+err.Error())
		return
	}
	writeError(w, http.StatusInternalServerError, "apply failed: "+err.Error())
}

// storeError maps a node/user store failure to an HTTP status.
func (s *Service) storeError(w http.ResponseWriter, err error) {
	if strings.Contains(err.Error(), "not found") {
		writeError(w, http.StatusNotFound, err.Error())
		return
	}
	badRequest(w, err.Error())
}
