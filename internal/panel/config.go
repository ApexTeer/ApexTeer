// Package panel is EasySB's native Web management panel. It is not a second
// implementation of the business logic: it is an HTTP front end that calls the
// same packages the TUI and the subscription service call (internal/node,
// internal/user, internal/subscribe, internal/deploy, internal/cert, ...), so a
// change made here and a change made in the TUI produce the same on-disk state
// and the same core configuration.
//
// The panel runs as a mode of the EasySB binary (`easysb panel`) under its own
// systemd unit, independent of the core and the subscription service: a panel
// failure never stops the node.
package panel

import (
	"crypto/rand"
	"fmt"
	"math/big"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/EasySBTeam/EasySB/internal/sysinfo"
)

// Paths and defaults. They live here rather than in sysinfo so that the panel can
// be added without touching the path constants every other package already
// depends on.
const (
	// ConfigPath is the panel's own configuration: listen address, port and the
	// admin credential. It is deliberately separate from easysb.conf, whose KV
	// layout is kept legacy-compatible and must not carry panel-only state.
	ConfigPath = sysinfo.WorkDir + "/easysb-panel.conf"
	// WebDir is where a distribution places the built React bundle. When it is
	// absent the embedded fallback bundle is served instead.
	WebDir = "/usr/share/easysb/panel"
	// ServiceName is the systemd unit name of the panel service.
	ServiceName = "easysb-panel"
	// UnitPath is where the panel unit is installed.
	UnitPath = "/etc/systemd/system/easysb-panel.service"
	// LogFile collects the panel log when it is not run under journald.
	LogFile = sysinfo.WorkDir + "/easysb-panel.log"

	// DefaultListen binds the panel to every interface. The operator is expected
	// to front it with TLS or a firewall; docs/panel-installation.md says so.
	DefaultListen = "0.0.0.0"
	// DefaultPort is the panel's listen port. It is outside the protocol defaults
	// (8000-8004) and the subscription default (8443) to avoid collisions.
	DefaultPort = 2095

	// APIVersion is the contract the React front end is written against. It is
	// reported to the client so an incompatible front end can refuse to run
	// against an older backend.
	APIVersion = "1"
)

// Config is the panel's persisted configuration.
type Config struct {
	Listen       string
	Port         int
	Username     string
	PasswordHash string
	// SecurityEntry, when set, is a single path segment the panel is served
	// under (e.g. "manage" mounts everything at /manage/). It is a light
	// obscurity layer on top of authentication, the way 1Panel's 安全入口 works:
	// a request outside the prefix is answered as if nothing were there.
	SecurityEntry string
	// TLS serves the panel over HTTPS with the given pair. Empty means plain
	// HTTP, which is the honest default until a certificate is configured.
	TLS      bool
	CertFile string
	KeyFile  string
}

// DefaultConfig returns the built-in defaults. It carries no password hash: a
// missing or cleared credential is created once at startup by EnsureConfig, which
// returns the plaintext password so the operator can actually log in rather than
// the generated value being discarded.
func DefaultConfig() Config {
	return Config{
		Listen:   DefaultListen,
		Port:     DefaultPort,
		Username: "admin",
	}
}

// LoadConfig reads the panel configuration. A missing file yields defaults with
// no password hash; EnsureConfig fills that in once at startup.
func LoadConfig(path string) (Config, error) {
	cfg := DefaultConfig()
	values := sysinfo.ReadKeyValues(path)
	if v := values["PANEL_LISTEN"]; v != "" {
		cfg.Listen = v
	}
	if v := values["PANEL_PORT"]; v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 && n < 65536 {
			cfg.Port = n
		}
	}
	if v := values["PANEL_USER"]; v != "" {
		cfg.Username = v
	}
	if v := values["PANEL_PASSWORD_HASH"]; v != "" {
		cfg.PasswordHash = v
	}
	cfg.SecurityEntry = NormalizeSecurityEntry(values["PANEL_SECURITY_ENTRY"])
	if v := values["PANEL_TLS"]; strings.EqualFold(v, "yes") || v == "1" || strings.EqualFold(v, "true") {
		cfg.TLS = true
	}
	cfg.CertFile = values["PANEL_CERT_FILE"]
	cfg.KeyFile = values["PANEL_KEY_FILE"]
	return cfg, nil
}

// EnsureConfig makes sure the panel has a usable admin credential. On the first
// run, and again whenever the stored hash is empty, it generates a password,
// writes the configuration file and returns the plaintext once so the operator
// can log in. Every later call returns "". Verification fails closed on the empty
// hash that LoadConfig yields, so a panel that skipped EnsureConfig is locked
// rather than open. The caller prints the returned password to the console and
// must not write it to a persistent log.
func EnsureConfig(path string) (string, error) {
	values := sysinfo.ReadKeyValues(path)
	if strings.TrimSpace(values["PANEL_PASSWORD_HASH"]) != "" {
		return "", nil
	}
	cfg, err := LoadConfig(path)
	if err != nil {
		return "", err
	}
	password := GeneratePassword()
	hash, err := HashPassword(password)
	if err != nil {
		return "", err
	}
	cfg.PasswordHash = hash
	if err := cfg.Save(path); err != nil {
		return "", err
	}
	return password, nil
}

// Save writes the panel configuration with 0600 permissions through a temporary
// file, the same interrupted-write protection the other stores use. The file
// carries a password hash, so it never lands under the invoking shell's umask.
func (c Config) Save(path string) error {
	if dir := filepath.Dir(path); dir != "" && dir != "." {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return err
		}
	}
	tls := "no"
	if c.TLS {
		tls = "yes"
	}
	lines := []string{
		"# EasySB panel configuration",
		fmt.Sprintf("PANEL_LISTEN=%q", c.Listen),
		fmt.Sprintf("PANEL_PORT=%q", strconv.Itoa(c.Port)),
		fmt.Sprintf("PANEL_USER=%q", c.Username),
		fmt.Sprintf("PANEL_PASSWORD_HASH=%q", c.PasswordHash),
		fmt.Sprintf("PANEL_SECURITY_ENTRY=%q", c.SecurityEntry),
		fmt.Sprintf("PANEL_TLS=%q", tls),
		fmt.Sprintf("PANEL_CERT_FILE=%q", c.CertFile),
		fmt.Sprintf("PANEL_KEY_FILE=%q", c.KeyFile),
	}
	f, err := os.CreateTemp(filepath.Dir(path), filepath.Base(path)+".tmp-*")
	if err != nil {
		return err
	}
	tmp := f.Name()
	if _, err := f.WriteString(strings.Join(lines, "\n") + "\n"); err != nil {
		f.Close()
		os.Remove(tmp)
		return err
	}
	if err := f.Close(); err != nil {
		os.Remove(tmp)
		return err
	}
	if err := os.Rename(tmp, path); err != nil {
		os.Remove(tmp)
		return err
	}
	return os.Chmod(path, 0o600)
}

// Addr is the host:port the panel listens on.
func (c Config) Addr() string {
	host := strings.TrimSpace(c.Listen)
	if host == "" {
		host = DefaultListen
	}
	port := c.Port
	if port <= 0 || port > 65535 {
		port = DefaultPort
	}
	return fmt.Sprintf("%s:%d", host, port)
}

// NormalizeSecurityEntry turns a stored or submitted security entry into a bare
// path segment, or "" when none is configured. It is deliberately forgiving of
// surrounding slashes so an operator may type "/manage" or "manage/".
func NormalizeSecurityEntry(raw string) string {
	return strings.Trim(strings.TrimSpace(raw), "/")
}

// ValidateSecurityEntry reports whether a candidate entry is usable: empty (to
// clear it) or a single URL-safe segment. It rejects anything that could break
// routing, rather than silently rewriting the operator's input.
func ValidateSecurityEntry(raw string) error {
	entry := NormalizeSecurityEntry(raw)
	if entry == "" {
		return nil
	}
	if len(entry) > 64 {
		return fmt.Errorf("security entry must be 64 characters or fewer")
	}
	for _, r := range entry {
		ok := (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9') || r == '-' || r == '_'
		if !ok {
			return fmt.Errorf("security entry may only contain letters, digits, '-' and '_'")
		}
	}
	return nil
}

// AccessURL is the URL an operator opens. It follows the scheme the panel
// actually speaks, so the printed address never promises HTTPS on a plain
// listener, and includes the security entry prefix when one is set.
func (c Config) AccessURL() string {
	scheme := "http"
	if c.TLS && c.CertFile != "" && c.KeyFile != "" {
		scheme = "https"
	}
	base := fmt.Sprintf("%s://%s", scheme, c.Addr())
	if entry := NormalizeSecurityEntry(c.SecurityEntry); entry != "" {
		base += "/" + entry
	}
	return base
}

// passwordAlphabet is the character set for a generated admin password. It avoids
// characters that are easy to confuse when typed from a terminal.
const passwordAlphabet = "abcdefghijkmnpqrstuvwxyzABCDEFGHJKLMNPQRSTUVWXYZ23456789"

// GeneratePassword returns a 20-character random password suitable for the initial
// administrator credential.
func GeneratePassword() string {
	out := make([]byte, 20)
	for i := range out {
		n, err := rand.Int(rand.Reader, big.NewInt(int64(len(passwordAlphabet))))
		if err != nil {
			// crypto/rand failing is unrecoverable; fall back to a fixed but
			// non-empty value rather than an empty password.
			return "change-me-on-first-login"
		}
		out[i] = passwordAlphabet[n.Int64()]
	}
	return string(out)
}
