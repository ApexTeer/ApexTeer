package panel

import (
	"crypto/tls"
	"net/http"
	"strings"

	"github.com/EasySBTeam/EasySB/internal/cert"
	"github.com/EasySBTeam/EasySB/internal/deploy"
	"github.com/EasySBTeam/EasySB/internal/firewall"
	"github.com/EasySBTeam/EasySB/internal/node"
	"github.com/EasySBTeam/EasySB/internal/state"
)

// handleSecurity reports the two host-level protections the panel manages: the
// TLS pair it serves itself with, and the firewall rules that open the node
// ports. Both are the same state the TUI reads and writes, so a change made on
// either surface shows up on the other.
func (s *Service) handleSecurity(w http.ResponseWriter, r *http.Request) {
	cfg, err := LoadConfig(s.opts.ConfigPath)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}

	domains := []map[string]any{}
	for _, domain := range cert.Domains() {
		certFile, keyFile, ok := cert.Paths(domain)
		if !ok {
			continue
		}
		domains = append(domains, map[string]any{
			"domain":   domain,
			"certFile": certFile,
			"keyFile":  keyFile,
			"usable":   cert.Usable(domain),
		})
	}

	writeJSON(w, http.StatusOK, map[string]any{
		"tls": map[string]any{
			"enabled":  cfg.TLS,
			"certFile": cfg.CertFile,
			"keyFile":  cfg.KeyFile,
		},
		"domains": domains,
		"firewall": map[string]any{
			"backend": string(firewall.Detect()),
			"unit":    firewall.UnitName,
			"ports":   s.firewallPorts(),
		},
	})
}

// firewallPorts lists the ports the firewall rules open for the enabled nodes,
// so the operator can see what Apply is about to allow.
func (s *Service) firewallPorts() []map[string]any {
	nodes, err := s.loadNodes()
	if err != nil {
		return []map[string]any{}
	}
	out := []map[string]any{}
	for _, n := range nodes.Nodes() {
		if !n.Enabled {
			continue
		}
		transport := "tcp"
		switch n.Protocol {
		case state.ProtoHysteria2, state.ProtoTUIC:
			transport = "udp"
		}
		entry := map[string]any{
			"protocol": n.Protocol,
			"port":     n.Port,
			"network":  transport,
		}
		if n.Protocol == state.ProtoHysteria2 {
			hop := n.Param(node.ParamHopRange)
			if hop == "" {
				hop = state.DefaultHopRange
			}
			entry["hop"] = hop
		}
		out = append(out, entry)
	}
	return out
}

// handleSecurityTLS saves the panel's own TLS setting. Turning it on requires a
// readable certificate/key pair that actually parse as one, so the panel cannot
// be left unable to start after a restart.
func (s *Service) handleSecurityTLS(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Enabled  bool   `json:"enabled"`
		CertFile string `json:"certFile"`
		KeyFile  string `json:"keyFile"`
		Domain   string `json:"domain"`
	}
	if err := decodeJSON(r, &body); err != nil {
		badRequest(w, "invalid request body: "+err.Error())
		return
	}

	certFile := strings.TrimSpace(body.CertFile)
	keyFile := strings.TrimSpace(body.KeyFile)
	domain := strings.TrimSpace(body.Domain)
	if domain != "" {
		resolvedCert, resolvedKey, ok := cert.Paths(domain)
		if !ok {
			writeError(w, http.StatusUnprocessableEntity, "no certificate found for "+domain)
			return
		}
		certFile, keyFile = resolvedCert, resolvedKey
	}

	if body.Enabled {
		if certFile == "" || keyFile == "" {
			badRequest(w, "a certificate file and a key file are required to enable TLS")
			return
		}
		if _, err := tls.LoadX509KeyPair(certFile, keyFile); err != nil {
			// The pair is an answer to the request, so the refusal keeps its 422; the
			// underlying error does not. It is a *tls error that names the file and
			// the OS failure behind it ("open /etc/shadow: permission denied"), which
			// turns this endpoint into a path existence and permission oracle. The
			// detail goes to the log under a reference instead.
			ref := internalErrorRef()
			s.opts.Log("panel: " + ref + " certificate pair refused: " + err.Error())
			writeError(w, http.StatusUnprocessableEntity,
				"the certificate and key could not be loaded as a pair (ref "+ref+")")
			return
		}
	}

	cfg, err := LoadConfig(s.opts.ConfigPath)
	if err != nil {
		s.internalError(w, "cannot read the panel configuration", err)
		return
	}
	cfg.TLS = body.Enabled
	cfg.CertFile = certFile
	cfg.KeyFile = keyFile
	if err := cfg.Save(s.opts.ConfigPath); err != nil {
		s.internalError(w, "cannot save the panel configuration", err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"ok":        true,
		"enabled":   cfg.TLS,
		"certFile":  cfg.CertFile,
		"keyFile":   cfg.KeyFile,
		"accessUrl": cfg.AccessURL(),
		"note":      "restart the panel service for the change to take effect",
	})
}

// handleSecurityFirewall applies or removes the firewall rules the nodes need.
// It reuses the same firewall package the TUI and the first deploy call, so the
// rules are identical no matter which surface ran them.
func (s *Service) handleSecurityFirewall(w http.ResponseWriter, r *http.Request) {
	action := strings.TrimSpace(r.PathValue("action"))
	cfg := s.stateConfig()
	nodes, err := deploy.LoadNodes(s.opts.NodesPath)
	if err != nil {
		writeError(w, http.StatusUnprocessableEntity, err.Error())
		return
	}
	switch action {
	case "apply":
		if err := firewall.Apply(r.Context(), cfg, nodes, s.opts.Log); err != nil {
			writeError(w, http.StatusUnprocessableEntity, err.Error())
			return
		}
		if err := firewall.WriteUnit(nodes); err == nil {
			_ = firewall.UnitAction(r.Context(), "enable")
		}
	case "remove":
		if err := firewall.Remove(r.Context(), nodes); err != nil {
			writeError(w, http.StatusUnprocessableEntity, err.Error())
			return
		}
		_ = firewall.RemoveUnit()
	default:
		writeError(w, http.StatusNotFound, "unknown firewall action")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"ok":      true,
		"backend": string(firewall.Detect()),
		"ports":   s.firewallPorts(),
	})
}
