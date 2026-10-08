// Package user stores the accounts that replaced the node-wide credential in
// v4. An account owns one credential set per node it may use, a traffic quota,
// an expiry date and the token its subscription URL is built from. In v5 the
// selection is a set of node ids rather than a set of protocols, because the
// host can now run several nodes of the same protocol.
package user

import (
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/EasySBTeam/EasySB/internal/secret"
	"github.com/EasySBTeam/EasySB/internal/state"
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

// KnownProtocol reports whether a protocol key is one this build renders.
func KnownProtocol(key string) bool {
	_, ok := credentialFields[key]
	return ok
}

// Selection pairs a node with the protocol it serves. The account package
// cannot resolve a node id on its own, so callers that know the node store hand
// the protocol in when a selection is made.
type Selection struct {
	Node     string
	Protocol string
}

// Credentials holds one node's secret fields for one account. Protocol is
// stored so a credential can be regenerated and checked without the node store.
type Credentials struct {
	Protocol string `json:"protocol,omitempty"`
	UUID     string `json:"uuid,omitempty"`
	Password string `json:"password,omitempty"`
}

// NodeUsage is the traffic one account moved through one node.
type NodeUsage struct {
	UsedBytes     int64 `json:"used_bytes"`
	UploadBytes   int64 `json:"upload_bytes"`
	DownloadBytes int64 `json:"download_bytes"`
}

// User is one account.
type User struct {
	Name          string                 `json:"name"`
	Remark        string                 `json:"remark,omitempty"`
	Token         string                 `json:"token"`
	Enabled       bool                   `json:"enabled"`
	Nodes         []string               `json:"nodes,omitempty"`
	Credentials   map[string]Credentials `json:"credentials,omitempty"`
	Usage         map[string]NodeUsage   `json:"usage,omitempty"`
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

// New returns an enabled account with a fresh token and the credential fields
// every selected node needs.
func New(name string, selections []Selection, now time.Time) User {
	u := User{
		Name:        name,
		Token:       secret.Token(),
		Enabled:     true,
		Credentials: map[string]Credentials{},
		Usage:       map[string]NodeUsage{},
		CreatedAt:   now.UTC(),
		LastReset:   now.UTC(),
	}
	for _, sel := range selections {
		u.Select(sel.Node, sel.Protocol)
	}
	return u
}

// Select adds a node and generates the credential fields its protocol needs.
// Fields that already exist are kept, so re-selecting a node never invalidates
// a configuration a client has already imported.
func (u *User) Select(nodeID, protocol string) bool {
	if nodeID == "" || !KnownProtocol(protocol) || u.Selects(nodeID) {
		return false
	}
	u.Nodes = append(u.Nodes, nodeID)
	sortNodes(u.Nodes)
	u.ensureCredential(nodeID, protocol)
	return true
}

// Deselect removes a node from the account but keeps its credential, so
// selecting it again restores the same client configuration.
func (u *User) Deselect(nodeID string) bool {
	for i, id := range u.Nodes {
		if id != nodeID {
			continue
		}
		u.Nodes = append(u.Nodes[:i], u.Nodes[i+1:]...)
		return true
	}
	return false
}

// Selects reports whether the account picked a node.
func (u User) Selects(nodeID string) bool {
	for _, id := range u.Nodes {
		if id == nodeID {
			return true
		}
	}
	return false
}

// Credential returns the stored credential of a node, which may be empty for a
// node the account never selected.
func (u User) Credential(nodeID string) Credentials {
	return u.Credentials[nodeID]
}

// CredentialReady reports whether every secret field a node's protocol
// authenticates with is present. A document is only rendered for a node that
// answers true, so a client is never handed an entry the core refuses for a
// missing field.
func (u User) CredentialReady(nodeID string) bool {
	cred, ok := u.Credentials[nodeID]
	if !ok || !KnownProtocol(cred.Protocol) {
		return false
	}
	for _, field := range credentialFields[cred.Protocol] {
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

// CredentialsReady reports whether every selected node has the secret fields its
// protocol authenticates with. An account that is not ready is excluded from
// both the core config and the client documents.
func (u User) CredentialsReady() bool {
	if len(u.Nodes) == 0 {
		return false
	}
	for _, id := range u.Nodes {
		if !u.CredentialReady(id) {
			return false
		}
	}
	return true
}

// EnsureCredentials fills in missing credential fields and drops credential
// entries whose protocol this build does not know. known maps a node id to the
// protocol its node serves, which is what lets a selection with no credential
// entry at all be repaired; pass nil when the node store is not at hand, and
// only the credentials already present are normalised.
func (u *User) EnsureCredentials(known map[string]string) {
	for nodeID, cred := range u.Credentials {
		if !KnownProtocol(cred.Protocol) {
			delete(u.Credentials, nodeID)
			continue
		}
		u.ensureCredential(nodeID, cred.Protocol)
	}
	for _, id := range u.Nodes {
		protocol, ok := known[id]
		if !ok || !KnownProtocol(protocol) {
			continue
		}
		u.ensureCredential(id, protocol)
	}
	if u.Usage == nil {
		u.Usage = map[string]NodeUsage{}
	}
}

func (u *User) ensureCredential(nodeID, protocol string) {
	if u.Credentials == nil {
		u.Credentials = map[string]Credentials{}
	}
	cred := u.Credentials[nodeID]
	cred.Protocol = protocol
	for _, field := range credentialFields[protocol] {
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
	u.Credentials[nodeID] = cred
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

// AddNodeUsage records one accounting cycle against one node and the account
// total. Negative deltas are ignored: the counters come from the core and only
// ever grow.
func (u *User) AddNodeUsage(nodeID string, upload, download int64) {
	if upload < 0 {
		upload = 0
	}
	if download < 0 {
		download = 0
	}
	if u.Usage == nil {
		u.Usage = map[string]NodeUsage{}
	}
	usage := u.Usage[nodeID]
	usage.UploadBytes += upload
	usage.DownloadBytes += download
	usage.UsedBytes = usage.UploadBytes + usage.DownloadBytes
	u.Usage[nodeID] = usage
	u.UploadBytes += upload
	u.DownloadBytes += download
	u.UsedBytes += upload + download
}

// ResetCounters starts a new traffic period: the per-node and aggregate
// counters return to zero and the period start moves to now.
func (u *User) ResetCounters(now time.Time) {
	u.UsedBytes, u.UploadBytes, u.DownloadBytes = 0, 0, 0
	u.Usage = map[string]NodeUsage{}
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
	// The token is a URL path segment and part of the stats filter, so it stays
	// lowercase ASCII: letters, digits and the hyphen, none of which needs
	// escaping in a URL path or carries meaning in a regexp. Anything else is
	// hand-edited or corrupt.
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

// sortNodes keeps the node ids in a stable order so a rendered document does
// not depend on the order things were clicked.
func sortNodes(list []string) {
	sort.Strings(list)
}
