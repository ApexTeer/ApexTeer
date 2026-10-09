package panel

import (
	"net/http"
	"strings"
	"time"
)

// handleLogin verifies the admin credential and starts a session.
func (s *Service) handleLogin(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Username string `json:"username"`
		Password string `json:"password"`
	}
	if err := decodeJSON(r, &body); err != nil {
		badRequest(w, "invalid request body: "+err.Error())
		return
	}
	key := clientKey(r)
	if s.sessions.locked(key) {
		writeError(w, http.StatusTooManyRequests, "too many failed attempts, try again later")
		return
	}
	if !s.credentialsMatch(strings.TrimSpace(body.Username), body.Password) {
		s.sessions.failed(key)
		s.opts.Log("login failed from " + key)
		writeError(w, http.StatusUnauthorized, "invalid username or password")
		return
	}
	s.sessions.succeeded(key)

	token, expires, err := s.sessions.issue(strings.TrimSpace(body.Username))
	if err != nil {
		writeError(w, http.StatusInternalServerError, "cannot create session")
		return
	}
	cfg := s.currentConfig()
	s.setSessionCookie(w, token, expires, cfg.TLS)
	s.opts.Log("login ok from " + key)
	writeJSON(w, http.StatusOK, map[string]any{
		"username":   strings.TrimSpace(body.Username),
		"expiresAt":  expires.UTC().Format(time.RFC3339),
		"token":      token,
		"version":    s.opts.Version,
		"apiVersion": APIVersion,
	})
}

// handleLogout ends the current session.
func (s *Service) handleLogout(w http.ResponseWriter, r *http.Request) {
	s.sessions.revoke(requestToken(r))
	s.clearSessionCookie(w)
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

// handleSession reports who is logged in.
func (s *Service) handleSession(w http.ResponseWriter, r *http.Request) {
	username := "anonymous"
	if token := requestToken(r); token != "" {
		if name, ok := s.sessions.lookup(token); ok {
			username = name
		}
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"username":   username,
		"version":    s.opts.Version,
		"apiVersion": APIVersion,
	})
}

// handleChangePassword replaces the admin password after checking the current one.
func (s *Service) handleChangePassword(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Current string `json:"current"`
		Next    string `json:"next"`
	}
	if err := decodeJSON(r, &body); err != nil {
		badRequest(w, "invalid request body: "+err.Error())
		return
	}
	if len([]rune(body.Next)) < 8 {
		badRequest(w, "the new password must be at least 8 characters")
		return
	}
	cfg, err := LoadConfig(s.opts.ConfigPath)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "cannot read the panel configuration")
		return
	}
	if !VerifyPassword(cfg.PasswordHash, body.Current) {
		writeError(w, http.StatusUnauthorized, "the current password is wrong")
		return
	}
	hash, err := HashPassword(body.Next)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "cannot hash the new password")
		return
	}
	cfg.PasswordHash = hash
	if err := cfg.Save(s.opts.ConfigPath); err != nil {
		writeError(w, http.StatusInternalServerError, "cannot save the panel configuration")
		return
	}
	s.opts.Log("admin password changed")
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}
