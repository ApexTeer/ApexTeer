// Package user stores the accounts that replaced the node-wide credential in
// v4. An account owns one credential set per protocol, a traffic quota, an
// expiry date, the protocols it may use and the token its subscription URL is
// built from.
package user

import (
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/MinimaxFlora/EasySB/internal/secret"
	"github.com/MinimaxFlora/EasySB/internal/state"
)

// Status is the derived account state. Only the admin switch, the quota and the
// expiry date are stored; the rest is computed on read, so an account resumes
// by itself once the cause is gone.
type Status string

const (
	// StatusActive means the account may authenticate and move traffic.
	StatusActive Status = "active"
	// StatusDisabled means an operator switched the account off.
	StatusDisabled Status = "disabled"
	// StatusExpired means the expiry date has passed.
	StatusExpired Status = "expired"
	// StatusQuota means the traffic quota is used up.
	StatusQuota Status = "over-quota"
)

// credentialFields lists the secret fields each protocol authenticates with.
var credentialFields = map[string][]string{
	state.ProtoAnyTLS:       {"password"},
	state.ProtoHysteria2:    {"password"},
	state.ProtoTUIC:         {"uuid", "password"},
	state.ProtoVLESSReality: {"uuid"},
	state.ProtoVMessWSTLS:   {"uuid"},
}

// Credentials holds one protocol's secret fields for one account.
type Credentials struct {
	UUID     string `json:"uuid,omitempty"`
	Password string `json:"password,omitempty"`
}

// User is one account.
type User struct {
	Name          string                 `json:"name"`
	Remark        string                 `json:"remark,omitempty"`
	Token         string                 `json:"token"`
	Enabled       bool                   `json:"enabled"`
	Protocols     []string               `json:"protocols,omitempty"`
	Credentials   map[string]Credentials `json:"credentials,omitempty"`
	QuotaBytes    int64                  `json:"quota_bytes"`
	UsedBytes     int64                  `json:"used_bytes"`
	UploadBytes   int64                  `json:"upload_bytes"`
	DownloadBytes int64                  `json:"download_bytes"`
	CreatedAt     time.Time              `json:"created_at"`
	ExpireAt      time.Time              `json:"expire_at"`
	LastReset     time.Time              `json:"last_reset"`
	// Applied records whether the account is currently written into the core
	// config. It lets the accounting loop restart the core on transitions only.
	Applied bool `json:"applied"`
}

// Known reports whether a protocol key is one this build renders.
func Known(key string) bool {
	_, ok := credentialFields[key]
	return ok
}

// New returns an enabled account with a fresh token and the credential fields
// every requested protocol needs.
func New(name string, protocols []string, now time.Time) User {
	u := User{
		Name:        name,
		Token:       secret.Token(),
		Enabled:     true,
		Credentials: map[string]Credentials{},
		CreatedAt:   now.UTC(),
		LastReset:   now.UTC(),
	}
	for _, key := range protocols {
		u.Select(key)
	}
	return u
}

// Select adds a protocol and generates the credential fields it needs. Fields
// that already exist are kept, so re-selecting a protocol never invalidates a
// configuration a client has already imported.
func (u *User) Select(key string) bool {
	if !Known(key) || u.Selects(key) {
		return false
	}
	u.Protocols = append(u.Protocols, key)
	sortProtocols(u.Protocols)
	u.ensureCredential(key)
	return true
}

// Deselect removes a protocol from the account but keeps its credential, so
// selecting it again restores the same client configuration.
func (u *User) Deselect(key string) bool {
	for i, k := range u.Protocols {
		if k != key {
			continue
		}
		u.Protocols = append(u.Protocols[:i], u.Protocols[i+1:]...)
		return true
	}
	return false
}

// Selects reports whether the account picked a protocol.
func (u User) Selects(key string) bool {
	for _, k := range u.Protocols {
		if k == key {
			return true
		}
	}
	return false
}

// Credential returns the stored credential of a protocol, which may be empty
// for a protocol the account never selected.
func (u User) Credential(key string) Credentials {
	return u.Credentials[key]
}

// CredentialReady reports whether every secret field a protocol authenticates with
// is present, which is the same set Select fills in. A document is only rendered for
// a protocol that answers true, so a client is never handed a node the core refuses
// for a missing field.
func (u User) CredentialReady(key string) bool {
	if !Known(key) {
		return false
	}
	cred := u.Credentials[key]
	for _, field := range credentialFields[key] {
		switch field {
		case "uuid":
			if cred.UUID == "" {
				return false
			}
		case "password":
			if cred.Password == "" {
				return false
			}
		}
	}
	return true
}

// CredentialsReady reports whether every selected protocol has the secret fields
// it authenticates with. An account that is not ready is excluded from both the
// core config and the client documents, so the two can never disagree about a
// credential nobody ever generated.
func (u User) CredentialsReady() bool {
	if len(u.Protocols) == 0 {
		return false
	}
	for _, key := range u.Protocols {
		if !u.CredentialReady(key) {
			return false
		}
	}
	return true
}

// EnsureCredentials fills in missing fields for every selected protocol and
// drops entries for protocols this build no longer knows.
func (u *User) EnsureCredentials() {
	for _, key := range u.Protocols {
		u.ensureCredential(key)
	}
	for key := range u.Credentials {
		if !Known(key) {
			delete(u.Credentials, key)
		}
	}
}

func (u *User) ensureCredential(key string) {
	if u.Credentials == nil {
		u.Credentials = map[string]Credentials{}
	}
	cred := u.Credentials[key]
	for _, field := range credentialFields[key] {
		switch field {
		case "uuid":
			if cred.UUID == "" {
				cred.UUID = secret.UUID()
			}
		case "password":
			if cred.Password == "" {
				cred.Password = secret.Password()
			}
		}
	}
	u.Credentials[key] = cred
}

// Status derives the account state at an instant.
func (u User) Status(now time.Time) Status {
	switch {
	case !u.Enabled:
		return StatusDisabled
	case !u.ExpireAt.IsZero() && !now.Before(u.ExpireAt):
		return StatusExpired
	case u.QuotaBytes > 0 && u.UsedBytes >= u.QuotaBytes:
		return StatusQuota
	default:
		return StatusActive
	}
}

// Usable reports whether the account may still authenticate and route traffic.
func (u User) Usable(now time.Time) bool {
	return u.Status(now) == StatusActive
}

// Unlimited reports whether the account has no traffic quota.
func (u User) Unlimited() bool {
	return u.QuotaBytes <= 0
}

// Remaining returns the bytes left before the quota is reached, and 0 for an
// unlimited account.
func (u User) Remaining() int64 {
	if u.Unlimited() {
		return 0
	}
	if left := u.QuotaBytes - u.UsedBytes; left > 0 {
		return left
	}
	return 0
}

// Percent returns how much of the quota is used, capped at 100. An unlimited
// account reports 0, which is what the panel prints as "unlimited".
func (u User) Percent() int {
	if u.Unlimited() {
		return 0
	}
	if u.UsedBytes >= u.QuotaBytes {
		return 100
	}
	return int(u.UsedBytes * 100 / u.QuotaBytes)
}

// AddUsage records the traffic of one accounting cycle. Negative deltas are
// ignored: the counters come from the core and only ever grow.
func (u *User) AddUsage(upload, download int64) {
	if upload < 0 {
		upload = 0
	}
	if download < 0 {
		download = 0
	}
	u.UploadBytes += upload
	u.DownloadBytes += download
	u.UsedBytes += upload + download
}

// ResetCounters starts a new traffic period: the counters return to zero and
// the period start moves to now.
func (u *User) ResetCounters(now time.Time) {
	u.UsedBytes, u.UploadBytes, u.DownloadBytes = 0, 0, 0
	u.LastReset = now.UTC()
}

// ResetIfNewMonth zeroes the counters when the stored period began in an
// earlier calendar month, and reports whether it did.
func (u *User) ResetIfNewMonth(now time.Time) bool {
	// The period is a calendar month in the host's own timezone. Comparing the
	// stored instant in UTC put the reset at 08:00 local for a UTC+8 operator,
	// which is not what "monthly" means to the person reading the quota.
	local := now.In(time.Local)
	last := u.LastReset.In(time.Local)
	if !u.LastReset.IsZero() && last.Year() == local.Year() && last.Month() == local.Month() {
		return false
	}
	u.ResetCounters(now)
	return true
}

// Validate reports why an account cannot be stored.
func (u User) Validate() error {
	if strings.TrimSpace(u.Name) == "" {
		return errors.New("user name is required")
	}
	if len([]rune(u.Name)) > 40 {
		return errors.New("user name is longer than 40 characters")
	}
	for _, r := range u.Name {
		if r < 0x20 || r == 0x7f {
			return errors.New("user name contains a control character")
		}
	}
	if strings.TrimSpace(u.Token) != u.Token || u.Token == "" {
		return errors.New("subscription token is required")
	}
	// The token is a URL path segment and a stats filter, so it stays lowercase
	// ASCII: letters, digits and the hyphen, none of which needs escaping in a URL
	// path or carries meaning in a regexp. Anything else is hand-edited or corrupt.
	for _, r := range u.Token {
		if (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') || r == '-' {
			continue
		}
		return fmt.Errorf("subscription token %q must be lowercase ASCII letters, digits or hyphen", u.Token)
	}
	if u.QuotaBytes < 0 {
		return errors.New("quota cannot be negative")
	}
	return nil
}

// sortProtocols keeps the canonical protocol order so the rendered document and
// the panel columns do not depend on the order things were clicked.
func sortProtocols(list []string) {
	rank := make(map[string]int, len(state.Keys))
	for i, k := range state.Keys {
		rank[k] = i
	}
	sort.SliceStable(list, func(i, j int) bool { return rank[list[i]] < rank[list[j]] })
}
