package panel

import (
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
		"config": string(data),
	})
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
