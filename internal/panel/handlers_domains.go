package panel

import (
	"context"
	"errors"
	"net/http"
	"strings"

	"github.com/EasySBTeam/EasySB/internal/cert"
	"github.com/EasySBTeam/EasySB/internal/netutil"
	"github.com/EasySBTeam/EasySB/internal/service"
	"github.com/EasySBTeam/EasySB/internal/subd"
	"github.com/EasySBTeam/EasySB/internal/sysinfo"
)

// handleListDomains returns the issued certificates, which one is active and the
// state of the renewal timer.
func (s *Service) handleListDomains(w http.ResponseWriter, r *http.Request) {
	cfg := s.stateConfig()
	writeJSON(w, http.StatusOK, map[string]any{
		"domains":        emptyIfNil(cert.Domains()),
		"active":         cfg.CertDomain,
		"configured":     cfg.Domain,
		"acmeEmail":      cfg.ACMEEmail,
		"timerInstalled": cert.TimerInstalled(),
		"timerNext":      cert.TimerStatus(),
		"staging":        cert.Staging(),
		"registered":     cert.Registered(),
	})
}

// handleIssueDomain performs the same issuance flow the TUI does: preflight the
// domain, register the ACME account, stop the core so the standalone challenge
// can bind port 80, issue, restart the core, install the renewal timer and point
// the state at the new certificate.
func (s *Service) handleIssueDomain(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Domain string `json:"domain"`
		Email  string `json:"email"`
	}
	if err := decodeJSON(r, &body); err != nil {
		badRequest(w, "invalid request body: "+err.Error())
		return
	}
	domain := strings.TrimSpace(body.Domain)
	email := strings.TrimSpace(body.Email)
	if domain == "" || email == "" {
		badRequest(w, "domain and email are required")
		return
	}
	steps, err := s.issueCertificate(r.Context(), domain, email)
	result := map[string]any{"steps": steps}
	if err != nil {
		result["error"] = err.Error()
		writeJSON(w, http.StatusUnprocessableEntity, result)
		return
	}
	writeJSON(w, http.StatusOK, result)
}

// issueCertificate is the issuance workflow. Every step is recorded so the panel
// can show what happened even when a later step fails.
func (s *Service) issueCertificate(ctx context.Context, domain, email string) ([]string, error) {
	var steps []string
	log := func(line string) {
		steps = append(steps, line)
		s.opts.Log("panel: " + line)
	}
	report := cert.Preflight(ctx, domain)
	if len(report.Resolved) > 0 {
		log("dns: " + strings.Join(report.Resolved, ", "))
	} else if report.DNSFail != nil {
		log("dns lookup failed: " + report.DNSFail.Error())
	}
	if report.Mismatch() {
		log("the domain does not resolve to this server; issuance may fail")
	}
	if err := cert.EnsureAccount(ctx, email, log); err != nil {
		return steps, err
	}

	cfg := s.stateConfig()
	cfg.ACMEEmail = email
	if cfg.ServerIP == "" {
		if ip, err := netutil.PublicIP(ctx); err == nil {
			cfg.ServerIP = ip
		}
	}
	if err := cfg.Save(); err != nil {
		return steps, err
	}

	stopped := service.Active(ctx)
	if stopped {
		log("$ systemctl stop " + sysinfo.ServiceName)
		if err := service.Do(ctx, "stop"); err != nil {
			log("stop failed: " + err.Error())
		}
	}
	running := func() {
		if stopped && hasConfigJSON(s.opts.ConfigJSON) {
			if err := service.Do(ctx, "start"); err != nil {
				log("start failed: " + err.Error())
			}
		}
	}
	if err := cert.CheckPort80(); err != nil {
		running()
		return steps, errors.New("port 80 is not free: " + err.Error())
	}

	issueErr := cert.Issue(ctx, domain, email, log)
	running()
	if issueErr != nil {
		return steps, issueErr
	}
	if _, _, ok := cert.Paths(domain); !ok {
		return steps, errors.New("issuance reported success but no certificate is on disk")
	}
	cfg.Domain = domain
	cfg.CertDomain = domain
	if err := cfg.Save(); err != nil {
		return steps, err
	}
	if err := cert.InstallTimer(ctx, log); err != nil {
		log("renewal timer: " + err.Error())
	}
	if cfg.NodeDeployed {
		if err := s.apply(ctx); err != nil {
			return steps, err
		}
		if err := subd.Do(ctx, "restart"); err != nil {
			log("subscription service: " + err.Error())
		}
	}
	return steps, nil
}

// handleRenewDomains runs one renewal pass and reloads whatever holds the
// certificate, matching the TUI and the nightly timer.
func (s *Service) handleRenewDomains(w http.ResponseWriter, r *http.Request) {
	if !cert.Registered() {
		writeError(w, http.StatusUnprocessableEntity, "no ACME account is registered yet")
		return
	}
	if len(cert.Domains()) == 0 {
		writeError(w, http.StatusUnprocessableEntity, "no certificate has been issued yet")
		return
	}
	renewed, err := cert.Renew(r.Context(), s.opts.Log)
	if len(renewed) > 0 {
		if service.Active(r.Context()) {
			if err := service.Do(r.Context(), "restart"); err != nil {
				s.opts.Log("restart core: " + err.Error())
			}
		}
		if subd.Active(r.Context()) {
			if err := subd.Do(r.Context(), "restart"); err != nil {
				s.opts.Log("restart subscription service: " + err.Error())
			}
		}
	}
	if err != nil {
		writeError(w, http.StatusUnprocessableEntity, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"renewed": emptyIfNil(renewed)})
}

// handleRemoveDomain deletes one certificate and clears it from the state when it
// was the active one.
func (s *Service) handleRemoveDomain(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Domain string `json:"domain"`
	}
	if err := decodeJSON(r, &body); err != nil {
		badRequest(w, "invalid request body: "+err.Error())
		return
	}
	domain := strings.TrimSpace(body.Domain)
	if domain == "" {
		badRequest(w, "domain is required")
		return
	}
	s.writeMu.Lock()
	defer s.writeMu.Unlock()
	if err := cert.Remove(r.Context(), domain); err != nil {
		writeError(w, http.StatusUnprocessableEntity, err.Error())
		return
	}
	cfg := s.stateConfig()
	if cfg.CertDomain == domain {
		cfg.CertDomain = ""
		if cfg.Domain == domain {
			cfg.Domain = ""
		}
		if err := cfg.Save(); err != nil {
			writeError(w, http.StatusInternalServerError, err.Error())
			return
		}
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

// handleTimerStatus reports the renewal timer.
func (s *Service) handleTimerStatus(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, map[string]any{
		"installed": cert.TimerInstalled(),
		"next":      cert.TimerStatus(),
	})
}

// handleTimer installs or removes the renewal timer.
func (s *Service) handleTimer(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Action string `json:"action"`
	}
	if err := decodeJSON(r, &body); err != nil {
		badRequest(w, "invalid request body: "+err.Error())
		return
	}
	switch body.Action {
	case "install", "enable":
		if err := cert.InstallTimer(r.Context(), s.opts.Log); err != nil {
			writeError(w, http.StatusUnprocessableEntity, err.Error())
			return
		}
	case "remove", "disable":
		if err := cert.RemoveTimer(r.Context(), s.opts.Log); err != nil {
			writeError(w, http.StatusUnprocessableEntity, err.Error())
			return
		}
	default:
		badRequest(w, "action must be install or remove")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "installed": cert.TimerInstalled()})
}

// handleSetActiveDomain switches the active certificate to another issued domain.
func (s *Service) handleSetActiveDomain(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Domain string `json:"domain"`
	}
	if err := decodeJSON(r, &body); err != nil {
		badRequest(w, "invalid request body: "+err.Error())
		return
	}
	domain := strings.TrimSpace(body.Domain)
	if domain == "" {
		badRequest(w, "domain is required")
		return
	}
	s.writeMu.Lock()
	cfg := s.stateConfig()
	cfg.Domain = domain
	cfg.CertDomain = domain
	err := cfg.Save()
	s.writeMu.Unlock()
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	if cfg.NodeDeployed {
		if err := s.apply(r.Context()); err != nil {
			s.applyError(w, err)
			return
		}
		if err := subd.Do(r.Context(), "restart"); err != nil {
			s.opts.Log("subscription service: " + err.Error())
		}
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "active": domain})
}

// hasConfigJSON reports whether a rendered node configuration exists on disk.
func hasConfigJSON(path string) bool {
	info, err := statFile(path)
	return err == nil && info.Size() > 0
}
