package panel

import (
	"encoding/json"
	"net/http"
	"os"
	"strings"

	"github.com/EasySBTeam/EasySB/internal/deploy"
	"github.com/EasySBTeam/EasySB/internal/sbcore"
	"github.com/EasySBTeam/EasySB/internal/service"
	"github.com/EasySBTeam/EasySB/internal/sysinfo"
)

// handleCore reports the compiled core and the node service state.
func (s *Service) handleCore(w http.ResponseWriter, r *http.Request) {
	cfg := s.stateConfig()
	nodes, _ := s.loadNodes()
	users, _ := s.loadUsers()
	writeJSON(w, http.StatusOK, map[string]any{
		"version":      s.opts.Version,
		"coreVersion":  sbcore.Version(),
		"statsCapable": sbcore.StatsCapable(),
		"active":       service.Active(r.Context()),
		"enabled":      serviceEnabled(r.Context(), sysinfo.ServiceName),
		"deployed":     cfg.NodeDeployed,
		"configPath":   s.opts.ConfigJSON,
		"nodeCount":    nodes.Len(),
		"userCount":    users.Len(),
	})
}

// handleCoreConfig returns the rendered core configuration. It contains account
// credentials, so it is only ever served to an authenticated administrator.
// Without a file on disk the document is rendered from the stores but not saved.
//
// The REALITY private key is redacted before the document leaves the server. The front
// end only pretty-prints this response for display (it never reads the key), and the
// private key is the one field in the document whose exposure would let anyone who reads
// it impersonate the node's TLS identity. An administrator is root-equivalent and can read
// the file directly, but an admin session is not the same thing as root: it survives in a
// browser, in a screenshot and in a support paste, and the key does not need to travel for
// the page to work.
//
// Redacting here cannot break saving. handleApply re-renders the document from the node
// and account stores and never accepts a submitted one, so the stored key is untouched by
// anything the browser sends back.
func (s *Service) handleCoreConfig(w http.ResponseWriter, r *http.Request) {
	data, err := os.ReadFile(s.opts.ConfigJSON)
	if err != nil || len(strings.TrimSpace(string(data))) == 0 {
		nodes, nerr := s.loadNodes()
		if nerr != nil {
			writeError(w, http.StatusInternalServerError, nerr.Error())
			return
		}
		users, uerr := s.loadUsers()
		if uerr != nil {
			writeError(w, http.StatusInternalServerError, uerr.Error())
			return
		}
		rendered, rerr := deploy.ServerConfig(s.stateConfig(), nodes.Nodes(), users.Routable(s.now()))
		if rerr != nil {
			writeError(w, http.StatusUnprocessableEntity, rerr.Error())
			return
		}
		data = rendered
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"path":   s.opts.ConfigJSON,
		"config": redactKeyMaterial(string(data)),
	})
}

// keyMaterialPlaceholder is what the front end displays where a private key was. It carries
// no angle brackets on purpose: encoding/json escapes them, and the operator should see the
// note, not an escape sequence.
const keyMaterialPlaceholder = "redacted by EasySB: the real key stays on the server"

// redactKeyMaterial removes the private keys a rendered core configuration carries,
// leaving everything else byte-for-byte as it was.
//
// It edits the decoded document and re-marshals it rather than doing a textual
// substitution, so a key that happens to appear inside another value is not mangled. A
// document that does not parse is returned unchanged: this is a display path, and a
// rendering problem is not a reason to return nothing at all.
func redactKeyMaterial(doc string) string {
	var parsed map[string]any
	if err := json.Unmarshal([]byte(doc), &parsed); err != nil {
		return doc
	}
	inbounds, _ := parsed["inbounds"].([]any)
	changed := false
	for _, raw := range inbounds {
		in, ok := raw.(map[string]any)
		if !ok {
			continue
		}
		tls, ok := in["tls"].(map[string]any)
		if !ok {
			continue
		}
		reality, ok := tls["reality"].(map[string]any)
		if !ok {
			continue
		}
		if _, present := reality["private_key"]; present {
			reality["private_key"] = keyMaterialPlaceholder
			changed = true
		}
	}
	if !changed {
		return doc
	}
	out, err := json.MarshalIndent(parsed, "", "  ")
	if err != nil {
		return doc
	}
	return string(out)
}

// handleApply renders the stores, has the core accept the document and restarts
// the node, the single write path every change ends with.
func (s *Service) handleApply(w http.ResponseWriter, r *http.Request) {
	if err := s.apply(r.Context()); err != nil {
		s.applyError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

// handleCheckConfig renders the current stores and runs the real core's acceptance
// test on the result without touching the running configuration.
func (s *Service) handleCheckConfig(w http.ResponseWriter, r *http.Request) {
	nodes, err := s.loadNodes()
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	users, err := s.loadUsers()
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	data, err := deploy.ServerConfig(s.stateConfig(), nodes.Nodes(), users.Routable(s.now()))
	if err != nil {
		writeJSON(w, http.StatusOK, map[string]any{"ok": false, "error": err.Error()})
		return
	}
	tmp, err := os.CreateTemp("", "easysb-check-*.json")
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	path := tmp.Name()
	defer os.Remove(path)
	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	tmp.Close()
	if err := sbcore.Check(r.Context(), path); err != nil {
		writeJSON(w, http.StatusOK, map[string]any{"ok": false, "error": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

// handleCoreAction performs start, stop, restart, enable, disable or status on the
// node service.
func (s *Service) handleCoreAction(w http.ResponseWriter, r *http.Request) {
	action := strings.TrimSpace(r.PathValue("action"))
	switch action {
	case "start", "stop", "restart", "enable", "disable":
		if err := service.Do(r.Context(), action); err != nil {
			writeError(w, http.StatusUnprocessableEntity, err.Error())
			return
		}
	case "status":
		out, _ := runSystemctl(r.Context(), "status", sysinfo.ServiceName, "--no-pager")
		enabled, _ := runSystemctl(r.Context(), "is-enabled", sysinfo.ServiceName)
		writeJSON(w, http.StatusOK, map[string]any{
			"active":        service.Active(r.Context()),
			"enabled":       serviceEnabled(r.Context(), sysinfo.ServiceName),
			"output":        out,
			"enabledOutput": enabled,
		})
		return
	default:
		writeError(w, http.StatusNotFound, "unknown core action")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"ok":      true,
		"active":  service.Active(r.Context()),
		"enabled": serviceEnabled(r.Context(), sysinfo.ServiceName),
	})
}
