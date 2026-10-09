package panel

import (
	"context"
	"errors"
	"net"
	"net/http"
	"strings"
	"time"

	"github.com/EasySBTeam/EasySB/internal/service"
	"github.com/EasySBTeam/EasySB/internal/sysinfo"
)

// handlePanel reports the panel's own configuration and unit state. It never
// returns the password hash.
func (s *Service) handlePanel(w http.ResponseWriter, r *http.Request) {
	cfg, err := LoadConfig(s.opts.ConfigPath)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"version":     s.opts.Version,
		"apiVersion":  APIVersion,
		"listen":      cfg.Listen,
		"port":        cfg.Port,
		"username":    cfg.Username,
		"security":    cfg.SecurityEntry,
		"entryPrefix": s.EntryPrefix(),
		"tls":         cfg.TLS,
		"accessUrl":   cfg.AccessURL(),
		"configPath":  s.opts.ConfigPath,
		"webDir":      WebDir,
		"unitPath":    UnitPath,
		"installed":   Installed(),
		"active":      Active(r.Context()),
		"enabled":     Enabled(r.Context()),
		"serviceName": ServiceName,
	})
}

// panelConfigRequest is the settings form for the transport and the security
// entry. Pointer fields distinguish "leave unchanged" from "set to zero".
type panelConfigRequest struct {
	Listen        *string `json:"listen"`
	Port          *int    `json:"port"`
	SecurityEntry *string `json:"securityEntry"`
}

// handlePanelConfig updates the panel's listen address, port and security entry.
// The listen address and port are owned by the listener, so changing either
// requires a restart; the security entry is swapped in memory immediately. The
// restart is scheduled after the response is flushed, so the caller hears the
// outcome before systemd tears the process down.
func (s *Service) handlePanelConfig(w http.ResponseWriter, r *http.Request) {
	var req panelConfigRequest
	if err := decodeJSON(r, &req); err != nil {
		badRequest(w, err.Error())
		return
	}
	cfg, err := LoadConfig(s.opts.ConfigPath)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}

	restart := false
	if req.Listen != nil {
		host := strings.TrimSpace(*req.Listen)
		if err := validateListenHost(host); err != nil {
			badRequest(w, err.Error())
			return
		}
		if host != cfg.Listen {
			cfg.Listen = host
			restart = true
		}
	}
	if req.Port != nil {
		if *req.Port <= 0 || *req.Port > 65535 {
			badRequest(w, "port must be between 1 and 65535")
			return
		}
		if *req.Port != cfg.Port {
			cfg.Port = *req.Port
			restart = true
		}
	}
	if req.SecurityEntry != nil {
		if err := ValidateSecurityEntry(*req.SecurityEntry); err != nil {
			badRequest(w, err.Error())
			return
		}
		cfg.SecurityEntry = NormalizeSecurityEntry(*req.SecurityEntry)
	}

	if err := cfg.Save(s.opts.ConfigPath); err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	s.setSecurityEntry(cfg.SecurityEntry)

	if restart && Active(r.Context()) {
		go func() {
			time.Sleep(700 * time.Millisecond)
			if err := Do(context.Background(), "restart"); err != nil {
				s.opts.Log("panel restart: " + err.Error())
			}
		}()
	}

	writeJSON(w, http.StatusOK, map[string]any{
		"ok":              true,
		"restartRequired": restart,
		"listen":          cfg.Listen,
		"port":            cfg.Port,
		"securityEntry":   cfg.SecurityEntry,
		"accessUrl":       cfg.AccessURL(),
	})
}

// validateListenHost accepts an empty-cleared host, an IP address or a host
// name. It is a routing guard, not a resolver: net.Listen does the final check.
func validateListenHost(host string) error {
	if host == "" {
		return errors.New("listen address must not be empty")
	}
	if len(host) > 253 {
		return errors.New("listen address is too long")
	}
	if net.ParseIP(host) != nil {
		return nil
	}
	for _, r := range host {
		ok := (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9') || r == '.' || r == '-'
		if !ok {
			return errors.New("listen address must be an IP address or a host name")
		}
	}
	return nil
}

// handlePanelAction runs a lifecycle action on the panel's own service. It is what
// turns `easysb panel` run from a shell into an installed, boot-started service.
func (s *Service) handlePanelAction(w http.ResponseWriter, r *http.Request) {
	action := strings.TrimSpace(r.PathValue("action"))
	switch action {
	case "install":
		if err := WriteUnit(); err != nil {
			writeError(w, http.StatusUnprocessableEntity, err.Error())
			return
		}
		if err := Do(r.Context(), "enable"); err != nil {
			s.opts.Log("panel enable: " + err.Error())
		}
		// Only restart when the panel already runs under systemd, so an
		// interactive `easysb panel` session is not killed mid-request.
		if Active(r.Context()) {
			if err := Do(r.Context(), "restart"); err != nil {
				s.opts.Log("panel restart: " + err.Error())
			}
		}
	case "uninstall":
		_ = Do(r.Context(), "disable")
		_ = Do(r.Context(), "stop")
		if err := RemoveUnit(); err != nil {
			writeError(w, http.StatusUnprocessableEntity, err.Error())
			return
		}
	case "start", "stop", "restart", "enable", "disable":
		// Restarting or stopping the panel's own unit kills this very process,
		// so systemctl reports the command as terminated even though the action
		// succeeded. That is the intended outcome, not a failure.
		if err := Do(r.Context(), action); err != nil && !selfTerminated(action, err) {
			writeError(w, http.StatusUnprocessableEntity, err.Error())
			return
		}
	default:
		writeError(w, http.StatusNotFound, "unknown panel action")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"ok":        true,
		"installed": Installed(),
		"active":    Active(r.Context()),
		"enabled":   Enabled(r.Context()),
	})
}

// selfTerminated reports whether a stop or restart of the panel's own unit ended
// by systemd tearing this process down rather than by the action failing.
func selfTerminated(action string, err error) bool {
	if action != "stop" && action != "restart" {
		return false
	}
	if errors.Is(err, context.Canceled) {
		return true
	}
	return strings.Contains(err.Error(), "signal: terminated")
}

// handleDashboard is the landing view: the host snapshot, the service states and
// the deployment counts in one response.
func (s *Service) handleDashboard(w http.ResponseWriter, r *http.Request) {
	status := sysinfo.Collect(s.opts.Version)
	nodes, _ := s.loadNodes()
	users, _ := s.loadUsers()
	enabled := 0
	for _, n := range nodes.Nodes() {
		if n.Enabled {
			enabled++
		}
	}
	now := s.now()
	activeUsers := 0
	for _, u := range users.Users() {
		if u.Usable(now) {
			activeUsers++
		}
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"version":    s.opts.Version,
		"apiVersion": APIVersion,
		"host":       status,
		"counts": map[string]int{
			"nodes":        nodes.Len(),
			"nodesEnabled": enabled,
			"users":        users.Len(),
			"usersActive":  activeUsers,
		},
		"core": map[string]any{
			"active":  service.Active(r.Context()),
			"enabled": serviceEnabled(r.Context(), sysinfo.ServiceName),
		},
		"panel": map[string]any{
			"installed": Installed(),
			"active":    Active(r.Context()),
		},
	})
}
