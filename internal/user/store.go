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
)

// CurrentVersion is the on-disk format version of the account file.
const CurrentVersion = 1

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
func Load(path string) (*Store, error) {
	s := &Store{path: path}
	data, err := os.ReadFile(path)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return s, nil
		}
		return nil, err
	}
	if len(bytes.TrimSpace(data)) == 0 {
		return s, nil
	}
	var f fileFormat
	if err := json.Unmarshal(data, &f); err != nil {
		return nil, fmt.Errorf("parse %s: %w", path, err)
	}
	s.users = f.Users
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
		if !u.Usable(now) || len(u.Protocols) == 0 || !u.CredentialsReady() {
			continue
		}
		out = append(out, u)
	}
	return out
}

// Repair fills in the credentials a hand-edited or legacy file is missing and
// reports whether anything changed. Only writers call it, so the values it
// generates are the ones persisted, and every later read sees the same pair.
func (s *Store) Repair() bool {
	changed := false
	for i := range s.users {
		if !s.users[i].CredentialsReady() {
			changed = true
		}
		s.users[i].EnsureCredentials()
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
	u.EnsureCredentials()
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
		edited.EnsureCredentials()
		s.users[i] = edited
		return s.Save()
	}
	return fmt.Errorf("user %q not found", name)
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
	// into place. CreateTemp already applies 0600, so the key material never lands
	// under the umask of the invoking shell.
	f, err := os.CreateTemp(filepath.Dir(s.path), filepath.Base(s.path)+".tmp-*")
	if err != nil {
		return err
	}
	tmp := f.Name()
	if _, err := f.Write(data); err != nil {
		f.Close()
		os.Remove(tmp)
		return err
	}
	if err := f.Close(); err != nil {
		os.Remove(tmp)
		return err
	}
	if err := os.Rename(tmp, s.path); err != nil {
		os.Remove(tmp)
		return err
	}
	return nil
}
