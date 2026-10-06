// Package subd serves the subscription endpoint. It is the only component that
// answers the outside world: it binds one TCP port on the node, resolves a
// subscription token to an account, renders the document the requesting client
// can read and refuses the request unless the account is active, inside its
// quota and not expired.
package subd

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/EasySB-Team/EasySB/internal/cert"
	"github.com/EasySB-Team/EasySB/internal/state"
	"github.com/EasySB-Team/EasySB/internal/stats"
	"github.com/EasySB-Team/EasySB/internal/subscribe"
	"github.com/EasySB-Team/EasySB/internal/user"
)

// SubPath is the single endpoint every client format is served from. The
// account token follows it, and one URL serves sing-box, mihomo and v2rayN
// alike because the format is chosen from the client's User-Agent.
const SubPath = "/sub/"

// Options configures the subscription service.
type Options struct {
	// Version is reported in the startup log line.
	Version string
	// AccountsPath is the account file. It is re-read on every request, so an
	// edit made in the panel takes effect without restarting this service.
	AccountsPath string
	// Node returns the node state; it defaults to reading the state file.
	Node func() state.Config
	// Dial opens the counter source for the accounting loop.
	Dial func() (stats.Counter, error)
	// Apply rewrites the core configuration for the accounts that may be live.
	Apply func(ctx context.Context, cfg state.Config, users []user.User) error
	// Interval overrides the accounting interval; zero takes the node's value.
	Interval time.Duration
	// Now overrides the clock in tests.
	Now func() time.Time
	// Log receives one line per event.
	Log func(string)
	// Ready receives the bound address once the listener is up, then closes.
	// Tests use it to learn the ephemeral port.
	Ready chan string
	// cache keeps the parsed account file between requests; Run fills it in.
	cache *storeCache
}

// store returns the parsed account file, re-reading it only when the file on
// disk changed. Every client poll reaches this path, so parsing the whole file
// per request is wasted work; the mtime is the invalidation signal, which keeps
// an edit made in the panel visible on the very next request.
func (o Options) store() (*user.Store, error) {
	if o.cache == nil {
		return user.Load(o.AccountsPath)
	}
	return o.cache.load(o.AccountsPath)
}

// storeCache is the mtime-keyed parse cache behind Options.store.
type storeCache struct {
	mu    sync.Mutex
	path  string
	mod   time.Time
	size  int64
	store *user.Store
}

func (c *storeCache) load(path string) (*user.Store, error) {
	info, err := os.Stat(path)
	if err != nil {
		// A missing file is an empty store, which is what user.Load reports.
		return user.Load(path)
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.store != nil && c.path == path && c.mod.Equal(info.ModTime()) && c.size == info.Size() {
		return c.store, nil
	}
	store, err := user.Load(path)
	if err != nil {
		return nil, err
	}
	c.path, c.mod, c.size, c.store = path, info.ModTime(), info.Size(), store
	return store, nil
}

// Run serves the endpoint and accounts traffic until ctx is cancelled.
func (o Options) Run(ctx context.Context) error {
	o.cache = &storeCache{}
	cfg := o.node()
	addr, certFile, keyFile := o.transport(cfg)

	loop := stats.New(stats.Options{
		AccountsPath: o.AccountsPath,
		Node:         o.node,
		Dial:         o.Dial,
		Apply:        o.Apply,
		Interval:     o.Interval,
		Now:          o.Now,
		Log:          o.Log,
	})
	go loop.Run(ctx)

	listener, err := net.Listen("tcp", addr)
	if err != nil {
		return err
	}
	o.logf(fmt.Sprintf("subscription endpoint %s (tls=%v) · EasySB %s", listener.Addr(), certFile != "", o.version()))

	server := &http.Server{
		Handler:           o.handler(),
		ReadHeaderTimeout: 10 * time.Second,
		ReadTimeout:       30 * time.Second,
		WriteTimeout:      30 * time.Second,
		IdleTimeout:       60 * time.Second,
	}
	if o.Ready != nil {
		o.Ready <- listener.Addr().String()
		close(o.Ready)
	}

	serveErr := make(chan error, 1)
	go func() {
		var err error
		if certFile != "" {
			err = server.ServeTLS(listener, certFile, keyFile)
		} else {
			err = server.Serve(listener)
		}
		if err != nil && !errors.Is(err, http.ErrServerClosed) {
			serveErr <- err
		}
	}()

	select {
	case <-ctx.Done():
	case err := <-serveErr:
		// Serving failed on its own. Returning here used to leave the listener bound
		// and the accounting loop running, so a service that came up and then failed
		// still held the port against the restart that would have recovered it.
		_ = server.Close()
		return err
	}
	shutdown, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	return server.Shutdown(shutdown)
}

// transport picks the listen address and, when the node holds a real
// certificate for its domain, the files that serve the endpoint over TLS. A
// self-signed certificate is never used: clients would reject it, so plain HTTP
// is the honest fallback and the panel warns about it. The question is asked
// through cert.Usable, the same call subscribe.Endpoint and the panel's own
// warning use, so the protocol the listener speaks and the scheme the printed URL
// promises can never disagree.
func (o Options) transport(cfg state.Config) (addr, certFile, keyFile string) {
	addr = fmt.Sprintf("0.0.0.0:%d", cfg.SubPort())
	if !cert.Usable(cfg.Domain) {
		return addr, "", ""
	}
	fullchain, key, ok := cert.Paths(cfg.Domain)
	if !ok {
		return addr, "", ""
	}
	return addr, fullchain, key
}

func (o Options) handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc(SubPath, o.handleSubscription)
	mux.HandleFunc("/", o.handleUnknown)
	return mux
}

// handleUnknown answers everything but the subscription path with a plain 404,
// so a scanner cannot tell the service apart from any other web server.
func (o Options) handleUnknown(w http.ResponseWriter, _ *http.Request) {
	http.Error(w, "not found", http.StatusNotFound)
}

// profileName is the base name of every downloaded subscription file. It is what a
// client shows as the profile's name, so it is the product rather than the account:
// the file lands in a download folder, and the account token it used to carry would
// put a credential in a file name that nobody reads as one. The name carries no
// extension either: the client reads the format from the Content-Type.
const profileName = "EasySB"

// dispositionName is the Content-Disposition value for a profile download. It sends
// the name in both forms RFC 6266 defines, and the plain one is deliberately
// unquoted: the Clash family (Clash Verge Rev, Clash Orbit) reads the header through
// a Debug-formatted string and strips the surrounding quotes only, so the quoted
// form arrives there as `\"EasySB\"` and ends up as the profile's name. Those clients
// resolve filename* first, which needs no quoting.
func dispositionName() string {
	return fmt.Sprintf("attachment; filename=%s; filename*=UTF-8''%s", profileName, profileName)
}

// handleSubscription serves one account's profile in the format its client reads.
func (o Options) handleSubscription(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet && r.Method != http.MethodHead {
		w.Header().Set("Allow", "GET, HEAD")
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}

	store, err := o.store()
	if err != nil {
		o.logf("accounts: " + err.Error())
		http.Error(w, "account store unavailable", http.StatusInternalServerError)
		return
	}
	account, found := store.ByToken(tokenOf(r.URL.Path))
	if !found {
		o.refuse(w, unknownToken)
		return
	}
	if status := account.Status(o.now()); status != user.StatusActive {
		o.refuse(w, refusalFor(status))
		return
	}

	cfg := o.node()
	if len(subscribe.ActiveTags(cfg, account)) == 0 {
		o.refuse(w, noProtocol)
		return
	}

	client := clientFromRequest(r)
	body, err := subscribe.Document(cfg, account, client)
	if err != nil {
		o.logf("render " + string(client) + ": " + err.Error())
		http.Error(w, "cannot render subscription", http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", subscribe.ContentType(client))
	w.Header().Set("Content-Disposition", dispositionName())
	// The document carries the account's credentials, so nothing may cache it.
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("Subscription-Userinfo", userinfo(account))
	w.WriteHeader(http.StatusOK)
	if r.Method == http.MethodHead {
		return
	}
	if _, err := w.Write(body); err != nil {
		o.logf("write " + account.Name + ": " + err.Error())
	}
}

// tokenOf extracts the account token from a subscription path. Extra segments
// and query strings are ignored: a client that appends its own suffix must still
// reach the account.
func tokenOf(path string) string {
	rest := strings.TrimPrefix(path, SubPath)
	if i := strings.IndexByte(rest, '/'); i >= 0 {
		rest = rest[:i]
	}
	return rest
}

// userinfo renders the quota header the Clash family reads. Every value is a
// byte count and expire is a unix timestamp. `total=0` means unlimited, which
// those clients render as "unlimited"; `expire` is omitted entirely when the
// account never expires, because clients disagree about `expire=0` and several
// of them read it as "already expired".
func userinfo(u user.User) string {
	parts := []string{
		fmt.Sprintf("upload=%d", u.UploadBytes),
		fmt.Sprintf("download=%d", u.DownloadBytes),
		fmt.Sprintf("total=%d", u.QuotaBytes),
	}
	if !u.ExpireAt.IsZero() {
		parts = append(parts, fmt.Sprintf("expire=%d", u.ExpireAt.Unix()))
	}
	return strings.Join(parts, "; ")
}

// refusal is one reason a subscription request is denied.
type refusal struct {
	code   int
	reason string
}

// Refusals are written in both panel languages: a client shows the body in its
// error dialog, and the operator may be looking at a customer's screenshot.
var (
	unknownToken = refusal{http.StatusNotFound, "订阅不存在 / unknown subscription"}
	notActive    = refusal{http.StatusForbidden, "账号已停用 / account disabled"}
	expired      = refusal{http.StatusForbidden, "账号已过期 / account expired"}
	overQuota    = refusal{http.StatusForbidden, "流量已用尽 / traffic quota exhausted"}
	noProtocol   = refusal{http.StatusForbidden, "未为该账号启用任何协议 / no protocol enabled for this account"}
)

func refusalFor(status user.Status) refusal {
	switch status {
	case user.StatusExpired:
		return expired
	case user.StatusQuota:
		return overQuota
	default:
		return notActive
	}
}

func (o Options) refuse(w http.ResponseWriter, r refusal) {
	w.Header().Set("Cache-Control", "no-store")
	http.Error(w, r.reason, r.code)
}

func (o Options) node() state.Config {
	if o.Node != nil {
		return o.Node()
	}
	return state.Load()
}

func (o Options) now() time.Time {
	if o.Now != nil {
		return o.Now()
	}
	return time.Now()
}

// version words the panel version for the startup line. The field is what main.go
// fills in from the embedded VERSION file; a test that leaves it out still gets a
// line that reads as a line rather than trailing off.
func (o Options) version() string {
	if o.Version == "" {
		return "unknown"
	}
	return o.Version
}

func (o Options) logf(line string) {
	if o.Log != nil {
		o.Log(line)
	}
}
