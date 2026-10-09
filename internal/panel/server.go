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

// route is one API endpoint: the method and the ServeMux pattern it answers on, and
// whether it is reachable without a session.
//
// The table is separate from the registration loop so a test can read it, and it is
// the single list of the API surface. Every entry that is not public is asserted to
// reject an unauthenticated request, which is what turns "a new endpoint was
// registered without require" into a test failure instead of a hole nobody notices.
type route struct {
	method  string
	pattern string
	public  bool
}

// routes is the API surface, in one place.
func (s *Service) routes() []route {
	return []route{
		// Public by design: there is no session to check when logging in, and logout
		// revokes whatever token it was handed.
		{http.MethodPost, "/api/v1/auth/login", true},
		{http.MethodPost, "/api/v1/auth/logout", true},

		{http.MethodGet, "/api/v1/auth/session", false},
		{http.MethodPost, "/api/v1/auth/password", false},

		{http.MethodGet, "/api/v1/dashboard", false},

		{http.MethodGet, "/api/v1/nodes", false},
		{http.MethodPost, "/api/v1/nodes", false},
		{http.MethodGet, "/api/v1/nodes/{id}", false},
		{http.MethodPut, "/api/v1/nodes/{id}", false},
		{http.MethodDelete, "/api/v1/nodes/{id}", false},
		{http.MethodPost, "/api/v1/nodes/{id}/enable", false},
		{http.MethodPost, "/api/v1/nodes/{id}/disable", false},
		{http.MethodGet, "/api/v1/nodes/{id}/config", false},

		{http.MethodGet, "/api/v1/users", false},
		{http.MethodPost, "/api/v1/users", false},
		{http.MethodGet, "/api/v1/users/{name}", false},
		{http.MethodPut, "/api/v1/users/{name}", false},
		{http.MethodDelete, "/api/v1/users/{name}", false},
		{http.MethodPost, "/api/v1/users/{name}/enable", false},
		{http.MethodPost, "/api/v1/users/{name}/disable", false},
		{http.MethodPost, "/api/v1/users/{name}/reset", false},
		{http.MethodGet, "/api/v1/users/{name}/subscriptions", false},
		{http.MethodGet, "/api/v1/users/{name}/document", false},

		{http.MethodGet, "/api/v1/subscriptions", false},

		{http.MethodGet, "/api/v1/domains", false},
		{http.MethodPost, "/api/v1/domains/issue", false},
		{http.MethodPost, "/api/v1/domains/renew", false},
		{http.MethodPost, "/api/v1/domains/remove", false},
		{http.MethodPost, "/api/v1/domains/activate", false},
		{http.MethodGet, "/api/v1/domains/timer", false},
		{http.MethodPost, "/api/v1/domains/timer", false},

		{http.MethodGet, "/api/v1/core", false},
		{http.MethodGet, "/api/v1/core/config", false},
		{http.MethodPost, "/api/v1/core/apply", false},
		{http.MethodPost, "/api/v1/core/check", false},
		{http.MethodPost, "/api/v1/core/{action}", false},

		{http.MethodGet, "/api/v1/system", false},
		{http.MethodGet, "/api/v1/system/network", false},
		{http.MethodPost, "/api/v1/system/subscription/{action}", false},

		{http.MethodGet, "/api/v1/logs", false},

		{http.MethodGet, "/api/v1/panel", false},
		{http.MethodPost, "/api/v1/panel/config", false},
		{http.MethodPost, "/api/v1/panel/{action}", false},

		{http.MethodGet, "/api/v1/security", false},
		{http.MethodPost, "/api/v1/security/tls", false},
		{http.MethodPost, "/api/v1/security/firewall/{action}", false},

		{http.MethodGet, "/api/v1/bbr", false},
		{http.MethodPost, "/api/v1/bbr/enable", false},
		{http.MethodPost, "/api/v1/bbr/clear", false},

		{http.MethodGet, "/api/v1/toolbox", false},
		{http.MethodGet, "/api/v1/toolbox/board", false},
		{http.MethodPost, "/api/v1/toolbox/{id}/run", false},

		// The terminal authenticates inside the handler rather than through require:
		// a WebSocket handshake is a GET and carries the cookie, but the upgrade and
		// its deadlines need the raw connection. It is listed so the surface stays
		// complete, and marked public because a failed handshake answers with its own
		// status rather than the JSON 401 the sweep below expects.
		{http.MethodGet, "/api/v1/terminal/ws", true},
	}
}

// handlerFor returns the handler a route is served by.
func (s *Service) handlerFor(rt route) http.HandlerFunc {
	switch rt.pattern {
	case "/api/v1/auth/login":
		return s.handleLogin
	case "/api/v1/auth/logout":
		return s.handleLogout
	case "/api/v1/auth/session":
		return s.handleSession
	case "/api/v1/auth/password":
		return s.handleChangePassword

	case "/api/v1/dashboard":
		return s.handleDashboard

	case "/api/v1/nodes":
		if rt.method == http.MethodPost {
			return s.handleCreateNode
		}
		return s.handleListNodes
	case "/api/v1/nodes/{id}":
		switch rt.method {
		case http.MethodPut:
			return s.handleUpdateNode
		case http.MethodDelete:
			return s.handleDeleteNode
		}
		return s.handleGetNode
	case "/api/v1/nodes/{id}/enable":
		return s.handleSetNodeEnabled(true)
	case "/api/v1/nodes/{id}/disable":
		return s.handleSetNodeEnabled(false)
	case "/api/v1/nodes/{id}/config":
		return s.handleNodeConfig

	case "/api/v1/users":
		if rt.method == http.MethodPost {
			return s.handleCreateUser
		}
		return s.handleListUsers
	case "/api/v1/users/{name}":
		switch rt.method {
		case http.MethodPut:
			return s.handleUpdateUser
		case http.MethodDelete:
			return s.handleDeleteUser
		}
		return s.handleGetUser
	case "/api/v1/users/{name}/enable":
		return s.handleSetUserEnabled(true)
	case "/api/v1/users/{name}/disable":
		return s.handleSetUserEnabled(false)
	case "/api/v1/users/{name}/reset":
		return s.handleResetUser
	case "/api/v1/users/{name}/subscriptions":
		return s.handleUserSubscriptions
	case "/api/v1/users/{name}/document":
		return s.handleUserDocument

	case "/api/v1/subscriptions":
		return s.handleSubscriptions

	case "/api/v1/domains":
		return s.handleListDomains
	case "/api/v1/domains/issue":
		return s.handleIssueDomain
	case "/api/v1/domains/renew":
		return s.handleRenewDomains
	case "/api/v1/domains/remove":
		return s.handleRemoveDomain
	case "/api/v1/domains/activate":
		return s.handleSetActiveDomain
	case "/api/v1/domains/timer":
		if rt.method == http.MethodPost {
			return s.handleTimer
		}
		return s.handleTimerStatus

	case "/api/v1/core":
		return s.handleCore
	case "/api/v1/core/config":
		return s.handleCoreConfig
	case "/api/v1/core/apply":
		return s.handleApply
	case "/api/v1/core/check":
		return s.handleCheckConfig
	case "/api/v1/core/{action}":
		return s.handleCoreAction

	case "/api/v1/system":
		return s.handleSystem
	case "/api/v1/system/network":
		return s.handleNetwork
	case "/api/v1/system/subscription/{action}":
		return s.handleSubscriptionServiceAction

	case "/api/v1/logs":
		return s.handleLogs

	case "/api/v1/panel":
		return s.handlePanel
	case "/api/v1/panel/config":
		return s.handlePanelConfig
	case "/api/v1/panel/{action}":
		return s.handlePanelAction

	case "/api/v1/security":
		return s.handleSecurity
	case "/api/v1/security/tls":
		return s.handleSecurityTLS
	case "/api/v1/security/firewall/{action}":
		return s.handleSecurityFirewall

	case "/api/v1/bbr":
		return s.handleBBR
	case "/api/v1/bbr/enable":
		return s.handleBBREnable
	case "/api/v1/bbr/clear":
		return s.handleBBRClear

	case "/api/v1/toolbox":
		return s.handleToolbox
	case "/api/v1/toolbox/board":
		return s.handleToolboxBoard
	case "/api/v1/toolbox/{id}/run":
		return s.handleToolboxRun

	case "/api/v1/terminal/ws":
		return s.handleTerminal
	}
	// Unreachable while routes() and this table agree; the test that walks routes()
	// is what keeps them in step.
	panic("panel: no handler for route " + rt.method + " " + rt.pattern)
}

// Handler builds the HTTP handler tree from routes().
func (s *Service) Handler() http.Handler {
	mux := http.NewServeMux()

	for _, rt := range s.routes() {
		handler := s.handlerFor(rt)
		if rt.public {
			mux.HandleFunc(rt.method+" "+rt.pattern, handler)
			continue
		}
		mux.HandleFunc(rt.method+" "+rt.pattern, s.require(handler))
	}

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
//
// The path is logged in its escaped form. r.URL.Path is the *decoded* path, and
// net/url rejects a malformed escape but not a C0 control byte, so a request for
// /api/v1/x%0A... reaches this line carrying a real newline: the access log would
// then hold a second, forged entry that reads as genuine, and this line is
// reachable without authenticating. EscapedPath returns the original escaped form,
// which cannot contain a raw newline.
func (s *Service) withLogging(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()
		rec := &statusRecorder{ResponseWriter: w, status: http.StatusOK}
		next.ServeHTTP(rec, r)
		s.opts.Log(fmt.Sprintf("%s %s %d %s", r.Method, r.URL.EscapedPath(), rec.status, time.Since(start).Round(time.Millisecond)))
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
	//
	// Only the deployed flag is written, and it is written as a locked
	// read-modify-write rather than by saving this caller's copy. The copy above was
	// taken before a deploy that can take seconds, and writing it back whole would
	// discard anything another actor changed in the meantime - a domain, the sync
	// interval, the subscription port.
	if saveErr := state.UpdateNodeDeployed(true); saveErr != nil {
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
