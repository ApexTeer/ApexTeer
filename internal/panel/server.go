package panel

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"net"
	"net/http"
	"os"
	"path"
	"strings"
	"sync"
	"time"

	"github.com/EasySBTeam/EasySB/internal/deploy"
	"github.com/EasySBTeam/EasySB/internal/firewall"
	"github.com/EasySBTeam/EasySB/internal/node"
	"github.com/EasySBTeam/EasySB/internal/state"
	"github.com/EasySBTeam/EasySB/internal/subd"
	"github.com/EasySBTeam/EasySB/internal/sysinfo"
	"github.com/EasySBTeam/EasySB/internal/user"
	"github.com/EasySBTeam/EasySB/public"
)

// Options configures the panel server. Every path is injectable so a test can run
// the whole server against a temporary directory instead of /etc/sing-box.
type Options struct {
	// Version is the running EasySB version, reported by /api/v1/core.
	Version string
	// ConfigPath is the panel configuration file.
	ConfigPath string
	// NodesPath and UsersPath are the stores the panel reads and writes.
	NodesPath string
	UsersPath string
	// ConfigJSON is the rendered core configuration.
	ConfigJSON string
	// WebDir overrides the static bundle directory. Empty uses WebDir when it
	// exists, then the embedded bundle.
	WebDir string
	// State supplies the host configuration. Empty reads the state file.
	State func() state.Config
	// Log receives one line per request or event.
	Log func(string)
	// Now overrides the clock in tests.
	Now func() time.Time
	// Apply makes a store change live. Empty uses the real deployment path.
	// Tests inject a no-op so a CRUD test never writes the host's systemd units
	// or the live core configuration.
	Apply func(ctx context.Context) error
	// SessionTTL is how long a login lasts. Zero means twelve hours.
	SessionTTL time.Duration
	// AllowAnonymous disables authentication. It exists only so package tests can
	// exercise handlers without a login dance, and is never set by main.
	AllowAnonymous bool
}

// Service is the panel HTTP application.
type Service struct {
	opts     Options
	sessions *sessionStore
	// writeMu serializes the read-modify-write operations on the stores, so two
	// concurrent panel requests cannot interleave into a lost update. The file
	// locks in internal/node and internal/user protect against other processes.
	writeMu sync.Mutex
	// entryMu guards the live security entry, which is read on every request and
	// changed by the settings API without a restart.
	entryMu sync.RWMutex
	entry   string
}

// New builds the panel service, filling defaults.
func New(opts Options) *Service {
	if opts.ConfigPath == "" {
		opts.ConfigPath = ConfigPath
	}
	if opts.NodesPath == "" {
		opts.NodesPath = sysinfo.NodesFile
	}
	if opts.UsersPath == "" {
		opts.UsersPath = sysinfo.UsersFile
	}
	if opts.ConfigJSON == "" {
		opts.ConfigJSON = sysinfo.ConfigJSON
	}
	if opts.Log == nil {
		opts.Log = func(string) {}
	}
	svc := &Service{
		opts:     opts,
		sessions: newSessionStore(opts.SessionTTL, opts.Now),
	}
	if cfg, err := LoadConfig(opts.ConfigPath); err == nil {
		svc.entry = NormalizeSecurityEntry(cfg.SecurityEntry)
	}
	return svc
}

// Handler builds the HTTP handler tree.
func (s *Service) Handler() http.Handler {
	mux := http.NewServeMux()

	// Authentication (unauthenticated).
	mux.HandleFunc("POST /api/v1/auth/login", s.handleLogin)
	mux.HandleFunc("POST /api/v1/auth/logout", s.handleLogout)

	// Everything else requires a session.
	mux.HandleFunc("GET /api/v1/auth/session", s.require(s.handleSession))
	mux.HandleFunc("POST /api/v1/auth/password", s.require(s.handleChangePassword))

	mux.HandleFunc("GET /api/v1/dashboard", s.require(s.handleDashboard))

	mux.HandleFunc("GET /api/v1/nodes", s.require(s.handleListNodes))
	mux.HandleFunc("POST /api/v1/nodes", s.require(s.handleCreateNode))
	mux.HandleFunc("GET /api/v1/nodes/{id}", s.require(s.handleGetNode))
	mux.HandleFunc("PUT /api/v1/nodes/{id}", s.require(s.handleUpdateNode))
	mux.HandleFunc("DELETE /api/v1/nodes/{id}", s.require(s.handleDeleteNode))
	mux.HandleFunc("POST /api/v1/nodes/{id}/enable", s.require(s.handleSetNodeEnabled(true)))
	mux.HandleFunc("POST /api/v1/nodes/{id}/disable", s.require(s.handleSetNodeEnabled(false)))
	mux.HandleFunc("GET /api/v1/nodes/{id}/config", s.require(s.handleNodeConfig))

	mux.HandleFunc("GET /api/v1/users", s.require(s.handleListUsers))
	mux.HandleFunc("POST /api/v1/users", s.require(s.handleCreateUser))
	mux.HandleFunc("GET /api/v1/users/{name}", s.require(s.handleGetUser))
	mux.HandleFunc("PUT /api/v1/users/{name}", s.require(s.handleUpdateUser))
	mux.HandleFunc("DELETE /api/v1/users/{name}", s.require(s.handleDeleteUser))
	mux.HandleFunc("POST /api/v1/users/{name}/enable", s.require(s.handleSetUserEnabled(true)))
	mux.HandleFunc("POST /api/v1/users/{name}/disable", s.require(s.handleSetUserEnabled(false)))
	mux.HandleFunc("POST /api/v1/users/{name}/reset", s.require(s.handleResetUser))
	mux.HandleFunc("GET /api/v1/users/{name}/subscriptions", s.require(s.handleUserSubscriptions))
	mux.HandleFunc("GET /api/v1/users/{name}/document", s.require(s.handleUserDocument))

	mux.HandleFunc("GET /api/v1/subscriptions", s.require(s.handleSubscriptions))

	mux.HandleFunc("GET /api/v1/domains", s.require(s.handleListDomains))
	mux.HandleFunc("POST /api/v1/domains/issue", s.require(s.handleIssueDomain))
	mux.HandleFunc("POST /api/v1/domains/renew", s.require(s.handleRenewDomains))
	mux.HandleFunc("POST /api/v1/domains/remove", s.require(s.handleRemoveDomain))
	mux.HandleFunc("POST /api/v1/domains/activate", s.require(s.handleSetActiveDomain))
	mux.HandleFunc("GET /api/v1/domains/timer", s.require(s.handleTimerStatus))
	mux.HandleFunc("POST /api/v1/domains/timer", s.require(s.handleTimer))

	mux.HandleFunc("GET /api/v1/core", s.require(s.handleCore))
	mux.HandleFunc("GET /api/v1/core/config", s.require(s.handleCoreConfig))
	mux.HandleFunc("POST /api/v1/core/apply", s.require(s.handleApply))
	mux.HandleFunc("POST /api/v1/core/check", s.require(s.handleCheckConfig))
	mux.HandleFunc("POST /api/v1/core/{action}", s.require(s.handleCoreAction))

	mux.HandleFunc("GET /api/v1/system", s.require(s.handleSystem))
	mux.HandleFunc("GET /api/v1/system/network", s.require(s.handleNetwork))
	mux.HandleFunc("POST /api/v1/system/subscription/{action}", s.require(s.handleSubscriptionServiceAction))

	mux.HandleFunc("GET /api/v1/logs", s.require(s.handleLogs))

	mux.HandleFunc("GET /api/v1/panel", s.require(s.handlePanel))
	mux.HandleFunc("POST /api/v1/panel/config", s.require(s.handlePanelConfig))
	mux.HandleFunc("POST /api/v1/panel/{action}", s.require(s.handlePanelAction))

	mux.HandleFunc("GET /api/v1/security", s.require(s.handleSecurity))
	mux.HandleFunc("POST /api/v1/security/tls", s.require(s.handleSecurityTLS))
	mux.HandleFunc("POST /api/v1/security/firewall/{action}", s.require(s.handleSecurityFirewall))

	mux.HandleFunc("GET /api/v1/bbr", s.require(s.handleBBR))
	mux.HandleFunc("POST /api/v1/bbr/enable", s.require(s.handleBBREnable))
	mux.HandleFunc("POST /api/v1/bbr/clear", s.require(s.handleBBRClear))

	mux.HandleFunc("GET /api/v1/toolbox", s.require(s.handleToolbox))
	mux.HandleFunc("GET /api/v1/toolbox/board", s.require(s.handleToolboxBoard))
	mux.HandleFunc("POST /api/v1/toolbox/{id}/run", s.require(s.handleToolboxRun))

	// The terminal is authenticated inside the handler, not by require: a WebSocket
	// handshake is a GET and carries the cookie, but the upgrade plus its deadlines
	// need the raw connection.
	mux.HandleFunc("GET /api/v1/terminal/ws", s.handleTerminal)

	// Unknown API paths answer JSON, never the SPA.
	mux.HandleFunc("/api/", s.handleUnknownAPI)
	// Everything else is the single-page application.
	mux.HandleFunc("/", s.handleStatic)

	return s.withLogging(s.withSecurityEntry(mux))
}

// securityEntry returns the live security entry segment, or "" when the panel is
// served at the root.
func (s *Service) securityEntry() string {
	s.entryMu.RLock()
	defer s.entryMu.RUnlock()
	return s.entry
}

// setSecurityEntry swaps the live entry without a restart.
func (s *Service) setSecurityEntry(raw string) {
	s.entryMu.Lock()
	s.entry = NormalizeSecurityEntry(raw)
	s.entryMu.Unlock()
}

// EntryPrefix is the URL path the panel is served under, always with a leading
// and trailing slash ("/manage/"), or "/" when no entry is configured. It feeds
// the <base> tag the SPA resolves its relative URLs against.
func (s *Service) EntryPrefix() string {
	entry := s.securityEntry()
	if entry == "" {
		return "/"
	}
	return "/" + entry + "/"
}

// withSecurityEntry mounts the whole panel under the security entry prefix. A
// request that does not start with the prefix is answered as a 404, so the panel
// is invisible at the bare address, the way 1Panel's 安全入口 behaves.
func (s *Service) withSecurityEntry(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		entry := s.securityEntry()
		if entry == "" {
			next.ServeHTTP(w, r)
			return
		}
		prefix := "/" + entry
		switch {
		case r.URL.Path == prefix:
			r.URL.Path = "/"
		case strings.HasPrefix(r.URL.Path, prefix+"/"):
			r.URL.Path = strings.TrimPrefix(r.URL.Path, prefix)
		default:
			http.NotFound(w, r)
			return
		}
		next.ServeHTTP(w, r)
	})
}

// withLogging records one line per request, without the query string (which can
// carry sensitive values such as a subscription token).
func (s *Service) withLogging(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()
		rec := &statusRecorder{ResponseWriter: w, status: http.StatusOK}
		next.ServeHTTP(rec, r)
		s.opts.Log(fmt.Sprintf("%s %s %d %s", r.Method, r.URL.Path, rec.status, time.Since(start).Round(time.Millisecond)))
	})
}

// statusRecorder captures the status code for the access log.
type statusRecorder struct {
	http.ResponseWriter
	status int
}

func (r *statusRecorder) WriteHeader(code int) {
	r.status = code
	r.ResponseWriter.WriteHeader(code)
}

// Hijack lets a WebSocket take over the connection. Without it the wrapper the access
// log installs would hide the underlying writer's Hijacker and the terminal could not
// upgrade at all.
func (r *statusRecorder) Hijack() (net.Conn, *bufio.ReadWriter, error) {
	hijacker, ok := r.ResponseWriter.(http.Hijacker)
	if !ok {
		return nil, nil, errors.New("response writer does not support hijacking")
	}
	conn, rw, err := hijacker.Hijack()
	if err == nil {
		r.status = http.StatusSwitchingProtocols
	}
	return conn, rw, err
}

// require wraps a handler with session authentication. A mutating request that
// authenticated through a cookie must carry the X-EasySB-Panel header, which a
// cross-site form cannot set; a bearer-token client is exempt because it does not
// rely on the browser's ambient cookie.
func (s *Service) require(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if s.opts.AllowAnonymous {
			next(w, r)
			return
		}
		token := requestToken(r)
		if _, ok := s.sessions.lookup(token); !ok {
			writeError(w, http.StatusUnauthorized, "unauthenticated")
			return
		}
		if isMutating(r.Method) && !strings.HasPrefix(r.Header.Get("Authorization"), "Bearer ") {
			if r.Header.Get("X-EasySB-Panel") == "" {
				writeError(w, http.StatusForbidden, "missing X-EasySB-Panel header")
				return
			}
		}
		next(w, r)
	}
}

func isMutating(method string) bool {
	switch method {
	case http.MethodPost, http.MethodPut, http.MethodPatch, http.MethodDelete:
		return true
	}
	return false
}

// handleUnknownAPI answers an unknown /api path with JSON, so a client never
// receives the HTML of the single-page application where it expected JSON.
func (s *Service) handleUnknownAPI(w http.ResponseWriter, r *http.Request) {
	writeError(w, http.StatusNotFound, "unknown API endpoint")
}

// handleStatic serves the built front end, falling back to index.html so a
// client-side route survives a refresh.
func (s *Service) handleStatic(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet && r.Method != http.MethodHead {
		writeError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	fsys := s.webFS()
	name := strings.TrimPrefix(path.Clean(r.URL.Path), "/")
	if name == "" || name == "." {
		name = "index.html"
	}
	data, err := fs.ReadFile(fsys, name)
	if err != nil {
		data, err = fs.ReadFile(fsys, "index.html")
		if err != nil {
			writeError(w, http.StatusNotFound, "not found")
			return
		}
		name = "index.html"
	}
	if name == "index.html" {
		data = injectBase(data, s.EntryPrefix())
	}
	w.Header().Set("Content-Type", contentTypeFor(name))
	w.Header().Set("Cache-Control", "no-cache")
	http.ServeContent(w, r, name, time.Time{}, strings.NewReader(string(data)))
}

// injectBase rewrites the bundle's <base href="/"> to the security entry prefix,
// so the SPA's relative asset and API URLs resolve under the prefix. When no
// entry is configured the tag already points at the root and is left alone.
func injectBase(html []byte, prefix string) []byte {
	if prefix == "/" {
		return html
	}
	return bytes.Replace(html, []byte(`<base href="/"`), []byte(`<base href="`+prefix+`"`), 1)
}

// webFS returns the front-end file system: the on-disk bundle when present, the
// embedded bundle otherwise.
func (s *Service) webFS() fs.FS {
	dir := s.opts.WebDir
	if dir == "" {
		dir = WebDir
	}
	if info, err := os.Stat(dir); err == nil && info.IsDir() {
		return os.DirFS(dir)
	}
	sub, err := fs.Sub(public.Dist, "dist")
	if err != nil {
		return public.Dist
	}
	return sub
}

func contentTypeFor(name string) string {
	switch path.Ext(name) {
	case ".html":
		return "text/html; charset=utf-8"
	case ".js", ".mjs":
		return "text/javascript; charset=utf-8"
	case ".css":
		return "text/css; charset=utf-8"
	case ".json":
		return "application/json; charset=utf-8"
	case ".svg":
		return "image/svg+xml"
	case ".png":
		return "image/png"
	case ".ico":
		return "image/x-icon"
	case ".woff2":
		return "font/woff2"
	default:
		return "application/octet-stream"
	}
}

// --- JSON helpers -----------------------------------------------------------

func writeJSON(w http.ResponseWriter, status int, body any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(status)
	if body == nil {
		return
	}
	_ = json.NewEncoder(w).Encode(body)
}

func writeError(w http.ResponseWriter, status int, message string) {
	writeJSON(w, status, map[string]any{"error": message})
}

// badRequest reports a client error in the shared error shape.
func badRequest(w http.ResponseWriter, message string) {
	writeError(w, http.StatusBadRequest, message)
}

// decodeJSON reads a JSON body into dst, refusing a body that is not a single
// object and a body that is absurdly large.
func decodeJSON(r *http.Request, dst any) error {
	dec := json.NewDecoder(io.LimitReader(r.Body, 1<<20))
	dec.DisallowUnknownFields()
	if err := dec.Decode(dst); err != nil {
		return err
	}
	if dec.More() {
		return errors.New("the request body must contain a single JSON object")
	}
	return nil
}

// --- business helpers -------------------------------------------------------

// stateConfig returns the host configuration the panel operates on.
func (s *Service) stateConfig() state.Config {
	if s.opts.State != nil {
		return s.opts.State()
	}
	return state.Load()
}

func (s *Service) loadNodes() (*node.Store, error) {
	return node.Load(s.opts.NodesPath)
}

func (s *Service) loadUsers() (*user.Store, error) {
	return user.Load(s.opts.UsersPath)
}

// now is the service clock, overridable in tests.
func (s *Service) now() time.Time {
	if s.opts.Now != nil {
		return s.opts.Now()
	}
	return time.Now()
}

// nodeProtocols maps a node id to its protocol, the value account selection needs.
func nodeProtocols(nodes []node.Node) map[string]string {
	out := make(map[string]string, len(nodes))
	for _, n := range nodes {
		out[n.ID] = n.Protocol
	}
	return out
}

// apply runs the single write path both the panel and the TUI use: render, have
// the core accept the document, install or restart the service. An empty node set
// is a legal state and is reported as "nothing applied" rather than an error.
func (s *Service) apply(ctx context.Context) error {
	if s.opts.Apply != nil {
		return s.opts.Apply(ctx)
	}
	s.writeMu.Lock()
	defer s.writeMu.Unlock()
	cfg := s.stateConfig()
	first := !cfg.NodeDeployed
	err := deploy.ApplyStore(ctx, cfg, s.opts.NodesPath, s.opts.UsersPath)
	if errors.Is(err, deploy.ErrNoNodes) {
		return nil
	}
	if err != nil {
		return err
	}
	// The first successful deploy also installs the host-level pieces the panel
	// and the TUI both rely on: the firewall rules and the subscription service.
	// A failure of either is reported but does not undo the node, matching the
	// TUI's deploy path.
	cfg.NodeDeployed = true
	if saveErr := cfg.Save(); saveErr != nil {
		return saveErr
	}
	if !first {
		return nil
	}
	nodes, err := deploy.LoadNodes(s.opts.NodesPath)
	if err != nil {
		return err
	}
	if err := firewall.Apply(ctx, cfg, nodes, s.opts.Log); err != nil {
		s.opts.Log("firewall: " + err.Error())
	} else if err := firewall.WriteUnit(nodes); err == nil {
		_ = firewall.UnitAction(ctx, "enable")
	}
	if err := subd.WriteUnit(); err != nil {
		s.opts.Log("subscription service: " + err.Error())
	} else {
		_ = subd.Do(ctx, "enable")
		if err := subd.Do(ctx, "restart"); err != nil {
			s.opts.Log("subscription service: " + err.Error())
		}
	}
	return nil
}
