package panel

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
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
	addr := cfg.Addr()

	listener, err := net.Listen("tcp", addr)
	if err != nil {
		return fmt.Errorf("panel listen %s: %w", addr, err)
	}
	opts.Log(fmt.Sprintf("EasySB panel %s · api v%s · %s", opts.Version, APIVersion, cfg.AccessURL()))

	server := &http.Server{
		Handler:           svc.Handler(),
		ReadHeaderTimeout: 10 * time.Second,
		ReadTimeout:       60 * time.Second,
		// A toolbox run can take minutes before it writes its response, and a hijacked
		// terminal is exempt from this deadline anyway (see hijackClearer), so the write
		// timeout is sized for the slowest handler the panel has.
		WriteTimeout: 6 * time.Minute,
		IdleTimeout:  120 * time.Second,
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

// currentConfig returns the panel configuration, or defaults when it cannot be
// read. The credential check uses it on every login.
func (s *Service) currentConfig() Config {
	cfg, err := LoadConfig(s.opts.ConfigPath)
	if err != nil {
		return DefaultConfig()
	}
	return cfg
}
