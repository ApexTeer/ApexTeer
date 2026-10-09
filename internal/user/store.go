package user

import (
	"bytes"
	"crypto/subtle"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"time"

	"github.com/EasySBTeam/EasySB/internal/atomicfile"
)

// CurrentVersion is the on-disk format version of the account file. Version 2
// re-keys the selection and the credentials from protocol keys to node ids.
const CurrentVersion = 2

type fileFormat struct {
	Version int    `json:"version"`
	Users   []User `json:"users"`
}

// Store is the account file. The panel edits a handful of accounts, so a JSON
// document that is loaded once and written whole beats an embedded database:
// the file stays inspectable and there is no schema to migrate.
type Store struct {
	path  string
	users []User
}

// Load reads the account file. A missing or empty file yields an empty store
// rather than an error, because a fresh deployment starts with no accounts.
//
// A file that cannot be parsed is quarantined and the previous content is restored
// from the sibling backup. The account store carries the only copy of every
// account's credentials: an account is authenticated by the core against the uuid
// and password in here, and nothing can regenerate them - the running config has a
// copy, but no code rebuilds the store from it. Losing the file therefore costs
// every client its configuration, so a corrupt one is recovered rather than fatal.
func Load(path string) (*Store, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		if !errors.Is(err, fs.ErrNotExist) {
			return nil, err
		}
		// No file at all: a fresh deployment. There is nothing to recover from, and
		// an unreadable-but-present file is a real error rather than this case.
		return &Store{path: path}, nil
	}
	if len(bytes.TrimSpace(data)) == 0 {
		return &Store{path: path}, nil
	}
	s, err := parseStore(path, data)
	if err == nil {
		return s, nil
	}
	// The file is there but unusable. Keep it for forensics and try the backup.
	quarantined, qerr := quarantine(path, time.Now())
	if qerr != nil {
		// With nothing to move aside, the parse error is the honest answer.
		return nil, err
	}
	recovered, rerr := recoverFromBackup(path)
	if rerr != nil {
		return nil, fmt.Errorf("%w (the unusable file was kept at %s)", err, quarantined)
	}
	return recovered, nil
}

// parseStore decodes and validates an account document that is already in memory.
func parseStore(path string, data []byte) (*Store, error) {
	// A version-1 file describes protocol selections, which the current model
	// cannot resolve without the node store. It must be upgraded by
	// user.MigrateV2 first; refusing it here is safer than serving half-parsed
	// accounts whose selections and credentials look empty.
	var f fileFormat
	if err := json.Unmarshal(data, &f); err != nil {
		return nil, fmt.Errorf("parse %s: %w", path, err)
	}
	if f.Version != CurrentVersion {
		return nil, fmt.Errorf("parse %s: account store version %d needs migration", path, f.Version)
	}
	s := &Store{path: path, users: f.Users}
	seen := make(map[string]bool, len(s.users))
	for i := range s.users {
		u := &s.users[i]
		// Load never invents credentials. A reading path that generated them would
		// hand the subscription service different values than the core config was
		// rendered with, which shows up as "the subscription imports but cannot
		// connect". Only a writer repairs, via Store.Repair.
		// A duplicate token would make ByToken hand one account the other's
		// credentials, so a file that carries one is refused rather than served.
		if seen[u.Token] {
			return nil, fmt.Errorf("parse %s: duplicate subscription token", path)
		}
		seen[u.Token] = true
		// used_bytes is derived from the two counters; a hand-edited file that
		// lowered it must not hand out free traffic.
		if total := u.UploadBytes + u.DownloadBytes; u.UsedBytes < total {
			u.UsedBytes = total
		}
	}
	return s, nil
}

// quarantine moves an unusable store aside so it is not destroyed and the next save
// can install a fresh file. The suffix keeps the moment so two bad files do not
// overwrite each other.
func quarantine(path string, now time.Time) (string, error) {
	target := fmt.Sprintf("%s.corrupt-%s", path, now.UTC().Format("20060102T150405"))
	if err := os.Rename(path, target); err != nil {
		return "", err
	}
	return target, nil
}

// recoverFromBackup loads the sibling backup and installs it as the store again.
//
// The backup is parsed with the same validation as the store itself, so a backup
// that is also unusable is refused rather than promoted: restoring a broken file
// over a broken file would only hide the problem.
func recoverFromBackup(path string) (*Store, error) {
	backup := atomicfile.BackupPath(path)
	data, err := os.ReadFile(backup)
	if err != nil {
		return nil, fmt.Errorf("no usable backup at %s: %w", backup, err)
	}
	s, err := parseStore(backup, data)
	if err != nil {
		return nil, err
	}
	// The recovered store is written back to the real path, so the deployment keeps
	// running on it and the next save has a live target again.
	if err := atomicfile.Write(path, data, 0o600); err != nil {
		return nil, err
	}
	s.path = path
	return s, nil
}

// Path returns the file the store persists to.
func (s *Store) Path() string {
	return s.path
}

// Len returns the number of accounts.
func (s *Store) Len() int {
	return len(s.users)
}

// Users returns every account, ordered by name.
func (s *Store) Users() []User {
	out := make([]User, len(s.users))
	copy(out, s.users)
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out
}

// Routable returns the accounts the core may authenticate right now: enabled,
// not expired, inside quota and with at least one protocol selected. Both the
// config renderer and the stats whitelist are built from this one predicate, so
// the counters the core reports always match the accounts it accepts.
func (s *Store) Routable(now time.Time) []User {
	var out []User
	for _, u := range s.Users() {
		// An account whose selected protocols are not all credential-ready is left
		// out entirely: handing the core a member with an empty uuid or password is
		// rejected as a whole, and handing the client one is a node that cannot
		// authenticate.
		if !u.Usable(now) || len(u.Nodes) == 0 || !u.CredentialsReady() {
			continue
		}
		out = append(out, u)
	}
	return out
}

// Repair fills in the credentials a hand-edited or legacy file is missing and
// reports whether anything changed. known maps a node id to the protocol its
// node serves, so a selection whose credential is missing entirely can be
// healed; without it only the credentials already present could be completed.
// Only writers call it, so the values it generates are the ones persisted, and
// every later read sees the same pair.
func (s *Store) Repair(known map[string]string) bool {
	changed := false
	for i := range s.users {
		if !s.users[i].CredentialsReady() {
			changed = true
		}
		s.users[i].EnsureCredentials(known)
	}
	return changed
}

// Find returns the account with a name.
func (s *Store) Find(name string) (User, bool) {
	for _, u := range s.users {
		if u.Name == name {
			return u, true
		}
	}
	return User{}, false
}

// ByToken returns the account a subscription token belongs to. The comparison
// runs in constant time so a caller cannot recover a token from the response
// time, byte by byte.
func (s *Store) ByToken(token string) (User, bool) {
	if token == "" {
		return User{}, false
	}
	for _, u := range s.users {
		if subtle.ConstantTimeCompare([]byte(u.Token), []byte(token)) == 1 {
			return u, true
		}
	}
	return User{}, false
}

// Add stores a new account.
func (s *Store) Add(u User) error {
	if err := u.Validate(); err != nil {
		return err
	}
	if _, exists := s.Find(u.Name); exists {
		return fmt.Errorf("user %q already exists", u.Name)
	}
	for _, other := range s.users {
		if other.Token == u.Token {
			return fmt.Errorf("user %q already uses this token", other.Name)
		}
	}
	u.EnsureCredentials(nil)
	s.users = append(s.users, u)
	return s.Save()
}

// Update edits one account in place.
func (s *Store) Update(name string, fn func(*User) error) error {
	for i := range s.users {
		if s.users[i].Name != name {
			continue
		}
		edited := s.users[i]
		if err := fn(&edited); err != nil {
			return err
		}
		if err := edited.Validate(); err != nil {
			return err
		}
		if edited.Name != name {
			if _, exists := s.Find(edited.Name); exists {
				return fmt.Errorf("user %q already exists", edited.Name)
			}
		}
		edited.EnsureCredentials(nil)
		s.users[i] = edited
		return s.Save()
	}
	return fmt.Errorf("user %q not found", name)
}

// ForgetNode removes every trace of a deleted node from every account: the
// selection, its credential and its usage counter.
//
// It exists because deleting a node was implemented twice and the two disagreed.
// The panel removed the selection and the credential and the counter; the TUI
// removed only the selection. The selection is the part that matters for what the
// core serves, but leaving the other two behind means a recreated node id - and
// ids are random, so this is about a store that has been edited, restored from a
// backup or cloned - would inherit a stale credential and a stale traffic count
// from a node it has nothing to do with. One node's removal now leaves one state,
// whichever interface performed it.
//
// Deselect is used rather than a hand-rolled slice edit so the selection is
// removed the same way Select adds it. Deleting from a nil map is a no-op, so an
// account that never carried a credential entry is handled without a check.
func (s *Store) ForgetNode(nodeID string) {
	for i := range s.users {
		s.users[i].Deselect(nodeID)
		delete(s.users[i].Credentials, nodeID)
		delete(s.users[i].Usage, nodeID)
	}
}

// Mutate applies fn to every account in memory. The caller finishes the batch
// with Save, so one accounting cycle writes the file once instead of once per
// account.
func (s *Store) Mutate(fn func(*User)) {
	for i := range s.users {
		fn(&s.users[i])
	}
}

// MarkApplied records which accounts match a configuration just written to the
// core, so the accounting loop can act on transitions instead of restarting the
// core every cycle.
func (s *Store) MarkApplied(now time.Time) {
	live := make(map[string]bool, len(s.users))
	for _, u := range s.Routable(now) {
		live[u.Token] = true
	}
	for i := range s.users {
		s.users[i].Applied = live[s.users[i].Token]
	}
}

// Remove deletes one account.
func (s *Store) Remove(name string) error {
	for i := range s.users {
		if s.users[i].Name != name {
			continue
		}
		s.users = append(s.users[:i], s.users[i+1:]...)
		return s.Save()
	}
	return fmt.Errorf("user %q not found", name)
}

// Save writes the file with 0600 permissions through a temporary file, so an
// interrupted write cannot truncate the account list.
func (s *Store) Save() error {
	if dir := filepath.Dir(s.path); dir != "" && dir != "." {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return err
		}
	}
	data, err := json.MarshalIndent(fileFormat{Version: CurrentVersion, Users: s.users}, "", "  ")
	if err != nil {
		return err
	}
	data = append(data, '\n')
	// A fresh temporary name per write, not s.path+".tmp": the panel and the
	// subscription service both persist this file, and two writers sharing one
	// temporary path can interleave into a truncated document that is then renamed
	// into place. atomicfile.WriteKeepingBackup creates with 0600, so the key
	// material never lands under the umask of the invoking shell, it flushes before
	// renaming, and it keeps the previous content as a sibling backup that Load
	// falls back to if this file is ever found unusable.
	return atomicfile.WriteKeepingBackup(s.path, data, 0o600)
}
