package panel

import (
	"crypto/rand"
	"crypto/subtle"
	"encoding/hex"
	"net"
	"net/http"
	"strings"
	"sync"
	"time"

	"golang.org/x/crypto/bcrypt"
)

// sessionCookie is the cookie the browser carries between requests. It is
// HttpOnly and SameSite=Strict, so a cross-site page cannot read it or have the
// browser attach it to a forged request.
const sessionCookie = "easysb_panel_session"

// HashPassword hashes a password with bcrypt.
func HashPassword(password string) (string, error) {
	digest, err := bcrypt.GenerateFromPassword([]byte(password), bcrypt.DefaultCost)
	if err != nil {
		return "", err
	}
	return string(digest), nil
}

// VerifyPassword reports whether a password matches a stored hash.
func VerifyPassword(hash, password string) bool {
	if hash == "" {
		return false
	}
	return bcrypt.CompareHashAndPassword([]byte(hash), []byte(password)) == nil
}

// session is one logged-in browser or API client.
type session struct {
	username string
	expires  time.Time
}

// attempts tracks failed logins from one source so a brute-force run is locked
// out instead of being allowed to try passwords at full speed.
type attempts struct {
	failures int
	until    time.Time
}

// sessionStore keeps sessions and login attempts in memory. A panel restart
// invalidates sessions, which is acceptable: the admin logs in again.
type sessionStore struct {
	mu       sync.Mutex
	ttl      time.Duration
	sessions map[string]session
	fails    map[string]attempts
	now      func() time.Time
}

func newSessionStore(ttl time.Duration, now func() time.Time) *sessionStore {
	if now == nil {
		now = time.Now
	}
	if ttl <= 0 {
		ttl = 12 * time.Hour
	}
	return &sessionStore{
		ttl:      ttl,
		sessions: map[string]session{},
		fails:    map[string]attempts{},
		now:      now,
	}
}

// issue creates a fresh session token for a user.
func (s *sessionStore) issue(username string) (string, time.Time, error) {
	raw := make([]byte, 32)
	if _, err := rand.Read(raw); err != nil {
		return "", time.Time{}, err
	}
	token := hex.EncodeToString(raw)
	expires := s.now().Add(s.ttl)
	s.mu.Lock()
	defer s.mu.Unlock()
	s.sweepLocked()
	s.sessions[token] = session{username: username, expires: expires}
	return token, expires, nil
}

// lookup returns the username behind a live token.
func (s *sessionStore) lookup(token string) (string, bool) {
	if token == "" {
		return "", false
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	item, ok := s.sessions[token]
	if !ok || s.now().After(item.expires) {
		delete(s.sessions, token)
		return "", false
	}
	return item.username, true
}

// revoke drops one session (logout).
func (s *sessionStore) revoke(token string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.sessions, token)
}

// revokeAll drops every session, which is what a credential change has to do. The
// sessions are held in memory and a password change is the operator's remedy for a
// leaked cookie, so leaving the old tokens live would keep the very access the
// change was made to end - for up to the full TTL.
func (s *sessionStore) revokeAll() {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.sessions = map[string]session{}
}

// sweepLocked removes expired sessions. The caller holds the lock.
func (s *sessionStore) sweepLocked() {
	now := s.now()
	for token, item := range s.sessions {
		if now.After(item.expires) {
			delete(s.sessions, token)
		}
	}
}

// locked reports whether a login source is currently locked out.
func (s *sessionStore) locked(key string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	item := s.fails[key]
	return s.now().Before(item.until)
}

// failed records one failed login and locks the source out after five failures
// for fifteen minutes.
func (s *sessionStore) failed(key string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	item := s.fails[key]
	item.failures++
	if item.failures >= 5 {
		item.until = s.now().Add(15 * time.Minute)
		item.failures = 0
	}
	s.fails[key] = item
}

// succeeded clears the failure count after a successful login.
func (s *sessionStore) succeeded(key string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.fails, key)
}

// credentialsMatch compares the submitted username and password against the
// stored ones. The username comparison is constant time so it cannot be probed
// byte by byte; bcrypt already runs in constant time.
func (s *Service) credentialsMatch(username, password string) bool {
	cfg := s.currentConfig()
	if subtle.ConstantTimeCompare([]byte(cfg.Username), []byte(username)) != 1 {
		// Still run a bcrypt comparison so a wrong username and a wrong password
		// take about the same time.
		_ = bcrypt.CompareHashAndPassword([]byte(cfg.PasswordHash), []byte(password))
		return false
	}
	return VerifyPassword(cfg.PasswordHash, password)
}

// clientKey identifies a login source for rate limiting.
func clientKey(r *http.Request) string {
	if host, _, err := net.SplitHostPort(r.RemoteAddr); err == nil && host != "" {
		return host
	}
	return r.RemoteAddr
}

// setSessionCookie writes the session cookie for a login.
func (s *Service) setSessionCookie(w http.ResponseWriter, token string, expires time.Time, secure bool) {
	http.SetCookie(w, &http.Cookie{
		Name:     sessionCookie,
		Value:    token,
		Path:     "/",
		HttpOnly: true,
		Secure:   secure,
		SameSite: http.SameSiteStrictMode,
		Expires:  expires,
	})
}

// requestIsSecure reports whether the browser reached the panel over HTTPS.
//
// It is asked about the request rather than read from cfg.TLS, because the two
// differ in the arrangement the installation docs recommend: a reverse proxy
// terminates TLS and forwards plain HTTP to a loopback listener. There cfg.TLS is
// false - the panel itself does not do TLS - while the browser's hop is HTTPS, and
// tying the cookie to cfg.TLS left it without Secure on a deployment that is
// entirely HTTPS from the client's point of view.
//
// X-Forwarded-Proto is trusted here, which is safe for this question specifically
// and would not be for a client address: a secure cookie that does not need to be
// secure only stops the browser sending it over plain HTTP, so a forged header
// cannot gain access, only lose it. The panel is deployed behind a proxy that sets
// it; where none does, r.TLS answers.
func requestIsSecure(r *http.Request) bool {
	if r.TLS != nil {
		return true
	}
	return strings.EqualFold(strings.TrimSpace(r.Header.Get("X-Forwarded-Proto")), "https")
}

// clearSessionCookie expires the browser's session cookie on logout.
func (s *Service) clearSessionCookie(w http.ResponseWriter) {
	http.SetCookie(w, &http.Cookie{
		Name:     sessionCookie,
		Value:    "",
		Path:     "/",
		HttpOnly: true,
		MaxAge:   -1,
		SameSite: http.SameSiteStrictMode,
	})
}

// requestToken reads the session token from the Authorization header first, then
// from the cookie, so a scripted API client can work without a browser.
func requestToken(r *http.Request) string {
	if header := r.Header.Get("Authorization"); strings.HasPrefix(header, "Bearer ") {
		return strings.TrimSpace(strings.TrimPrefix(header, "Bearer "))
	}
	cookie, err := r.Cookie(sessionCookie)
	if err != nil {
		return ""
	}
	return cookie.Value
}
