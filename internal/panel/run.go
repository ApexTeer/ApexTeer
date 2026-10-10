package panel

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"strings"
	"time"
)

// Run serves the panel until ctx is cancelled. The listen address and the admin
// credential come from the panel configuration file.
func Run(ctx context.Context, opts Options) error {
	if opts.ConfigPath == "" {
		opts.ConfigPath = ConfigPath
	}
	cfg, err := LoadConfig(opts.ConfigPath)
	if err != nil {
		return err
	}
	svc := New(opts)
	// The overview trend is gathered in the background and read by the history
	// endpoint; it stops with the server.
	go svc.history.run(ctx)
	addr := cfg.Addr()

	listener, err := net.Listen("tcp", addr)
	if err != nil {
		return fmt.Errorf("panel listen %s: %w", addr, err)
	}
	opts.Log(fmt.Sprintf("EasySB panel %s · api v%s · %s", opts.Version, APIVersion, cfg.AccessURL()))
	// The panel default is loopback, so reaching a public interface is a deliberate
	// act. Say plainly what it costs when it is not paired with TLS, because the
	// password and the session cookie both cross that interface in the clear and an
	// authenticated session is root-equivalent.
	if !cfg.TLS && !isLoopback(cfg.Listen) {
		opts.Log("panel: WARNING listen=" + cfg.Listen + " without TLS: the admin password and " +
			"the session cookie are sent in clear text. Configure TLS, or use a reverse proxy " +
			"and bind loopback.")
	}

	server := &http.Server{
		Handler:           svc.Handler(),
		ReadHeaderTimeout: 10 * time.Second,
		ReadTimeout:       60 * time.Second,
		// A toolbox run can take minutes before it writes its response, and a hijacked
		// terminal is exempt from this deadline anyway (see hijackClearer), so the write
		// timeout is sized for the slowest handler the panel has.
		WriteTimeout: 6 * time.Minute,
		IdleTimeout:  120 * time.Second,
		ConnState:    trackConn,
	}

	serveErr := make(chan error, 1)
	go func() {
		var err error
		if cfg.TLS && cfg.CertFile != "" && cfg.KeyFile != "" {
			err = server.ServeTLS(listener, cfg.CertFile, cfg.KeyFile)
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
		_ = server.Close()
		return err
	}
	shutdown, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	return server.Shutdown(shutdown)
}

// isLoopback reports whether a listen address is reachable only from this host. An
// empty or wildcard host is not.
func isLoopback(host string) bool {
	host = strings.TrimSpace(host)
	if host == "" || host == "0.0.0.0" || host == "::" {
		return false
	}
	if strings.EqualFold(host, "localhost") {
		return true
	}
	ip := net.ParseIP(strings.Trim(host, "[]"))
	return ip != nil && ip.IsLoopback()
}

// currentConfig returns the panel configuration, or defaults when it cannot be
// read. The credential check uses it on every login.
func (s *Service) currentConfig() Config {
	cfg, err := LoadConfig(s.opts.ConfigPath)
	if err != nil {
		return DefaultConfig()
	}
	return cfg
}
