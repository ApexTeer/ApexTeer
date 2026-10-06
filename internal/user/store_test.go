package user

import (
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"sync"
	"testing"
	"time"

	"github.com/EasySB-Team/EasySB/internal/state"
)

func storePath(t *testing.T) string {
	t.Helper()
	return filepath.Join(t.TempDir(), "easysb-users.json")
}

func TestLoadMissingFileIsEmptyStore(t *testing.T) {
	s, err := Load(storePath(t))
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if s.Len() != 0 {
		t.Fatalf("len = %d, want 0", s.Len())
	}
	if _, ok := s.Find("alice"); ok {
		t.Fatal("empty store must not find anyone")
	}
}

func TestSaveLoadRoundTrip(t *testing.T) {
	path := storePath(t)
	s, err := Load(path)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	alice := New("alice", []string{state.ProtoAnyTLS, state.ProtoTUIC}, testNow)
	alice.QuotaBytes = 1 << 30
	alice.ExpireAt = testNow.Add(24 * time.Hour)
	if err := s.Add(alice); err != nil {
		t.Fatalf("Add: %v", err)
	}

	if fi, err := os.Stat(path); err != nil {
		t.Fatalf("stat: %v", err)
	} else if runtime.GOOS != "windows" && fi.Mode().Perm() != 0o600 {
		t.Fatalf("mode = %v, want 0600", fi.Mode().Perm())
	}

	reloaded, err := Load(path)
	if err != nil {
		t.Fatalf("reload: %v", err)
	}
	got, ok := reloaded.Find("alice")
	if !ok {
		t.Fatal("account lost after reload")
	}
	if got.Token != alice.Token || got.QuotaBytes != alice.QuotaBytes {
		t.Fatalf("scalar fields changed: %+v", got)
	}
	if got.Credential(state.ProtoTUIC) != alice.Credential(state.ProtoTUIC) {
		t.Fatal("credentials changed across a reload")
	}
	if !got.ExpireAt.Equal(alice.ExpireAt) {
		t.Fatalf("expire_at = %v, want %v", got.ExpireAt, alice.ExpireAt)
	}
	if got.Applied {
		t.Fatal("a newly added account must not claim to be applied")
	}
}

func TestAddRejectsDuplicates(t *testing.T) {
	s, err := Load(storePath(t))
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	alice := New("alice", []string{state.ProtoAnyTLS}, testNow)
	if err := s.Add(alice); err != nil {
		t.Fatalf("Add: %v", err)
	}
	if err := s.Add(New("alice", []string{state.ProtoAnyTLS}, testNow)); err == nil {
		t.Fatal("duplicate name accepted")
	}
	bob := New("bob", []string{state.ProtoAnyTLS}, testNow)
	bob.Token = alice.Token
	if err := s.Add(bob); err == nil {
		t.Fatal("duplicate token accepted")
	}
	if s.Len() != 1 {
		t.Fatalf("len = %d, want 1", s.Len())
	}
}

func TestUpdateRenameAndErrors(t *testing.T) {
	s, err := Load(storePath(t))
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if err := s.Add(New("alice", []string{state.ProtoAnyTLS}, testNow)); err != nil {
		t.Fatalf("Add: %v", err)
	}
	if err := s.Add(New("bob", []string{state.ProtoAnyTLS}, testNow)); err != nil {
		t.Fatalf("Add: %v", err)
	}

	if err := s.Update("alice", func(u *User) error {
		u.QuotaBytes = 42
		u.Select(state.ProtoTUIC)
		return nil
	}); err != nil {
		t.Fatalf("Update: %v", err)
	}
	got, _ := s.Find("alice")
	if got.QuotaBytes != 42 || !got.Selects(state.ProtoTUIC) {
		t.Fatalf("update not applied: %+v", got)
	}
	if got.Remark != "" {
		t.Fatal("update invented a remark")
	}

	if err := s.Update("alice", func(u *User) error { u.Name = "bob"; return nil }); err == nil {
		t.Fatal("rename onto an existing name accepted")
	}
	if err := s.Update("alice", func(u *User) error { u.Name = "carol"; return nil }); err != nil {
		t.Fatalf("rename: %v", err)
	}
	if _, ok := s.Find("carol"); !ok {
		t.Fatal("rename lost the account")
	}
	if _, ok := s.Find("alice"); ok {
		t.Fatal("old name still present after a rename")
	}
	if err := s.Update("nobody", func(*User) error { return nil }); err == nil {
		t.Fatal("updating an unknown account succeeded")
	}
	if err := s.Update("carol", func(u *User) error { u.QuotaBytes = -1; return nil }); err == nil {
		t.Fatal("an invalid edit was accepted")
	}
}

func TestRemove(t *testing.T) {
	s, err := Load(storePath(t))
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if err := s.Add(New("alice", []string{state.ProtoAnyTLS}, testNow)); err != nil {
		t.Fatalf("Add: %v", err)
	}
	if err := s.Remove("nobody"); err == nil {
		t.Fatal("removing an unknown account succeeded")
	}
	if err := s.Remove("alice"); err != nil {
		t.Fatalf("Remove: %v", err)
	}
	if s.Len() != 0 {
		t.Fatalf("len = %d, want 0", s.Len())
	}
	reloaded, err := Load(s.Path())
	if err != nil {
		t.Fatalf("reload: %v", err)
	}
	if reloaded.Len() != 0 {
		t.Fatal("removal did not reach the file")
	}
}

func TestByToken(t *testing.T) {
	s, err := Load(storePath(t))
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	alice := New("alice", []string{state.ProtoAnyTLS}, testNow)
	if err := s.Add(alice); err != nil {
		t.Fatalf("Add: %v", err)
	}
	if got, ok := s.ByToken(alice.Token); !ok || got.Name != "alice" {
		t.Fatalf("ByToken = %+v/%v", got, ok)
	}
	if _, ok := s.ByToken("wrong-token-value"); ok {
		t.Fatal("unknown token resolved")
	}
	if _, ok := s.ByToken(""); ok {
		t.Fatal("empty token resolved")
	}
}

func TestRoutable(t *testing.T) {
	s, err := Load(storePath(t))
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	active := New("active", []string{state.ProtoAnyTLS}, testNow)
	disabled := New("disabled", []string{state.ProtoAnyTLS}, testNow)
	disabled.Enabled = false
	expired := New("expired", []string{state.ProtoAnyTLS}, testNow)
	expired.ExpireAt = testNow.Add(-time.Minute)
	overQuota := New("over-quota", []string{state.ProtoAnyTLS}, testNow)
	overQuota.QuotaBytes, overQuota.UsedBytes = 10, 10
	noProtocol := New("no-protocol", nil, testNow)
	for _, u := range []User{active, disabled, expired, overQuota, noProtocol} {
		if err := s.Add(u); err != nil {
			t.Fatalf("Add %s: %v", u.Name, err)
		}
	}

	routable := s.Routable(testNow)
	if len(routable) != 1 || routable[0].Name != "active" {
		t.Fatalf("routable = %+v, want only the active account", routable)
	}
}

func TestLoadLeavesMissingCredentialsAlone(t *testing.T) {
	path := storePath(t)
	body := `{"version":1,"users":[{"name":"alice","token":"tok","enabled":true,
		"protocols":["anytls"],"quota_bytes":1000,"used_bytes":10,
		"upload_bytes":400,"download_bytes":600,"applied":true}]}`
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatalf("write: %v", err)
	}
	s, err := Load(path)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	got, ok := s.Find("alice")
	if !ok {
		t.Fatal("account lost")
	}
	if got.UsedBytes != 1000 {
		t.Fatalf("used_bytes = %d, want 1000 (upload+download)", got.UsedBytes)
	}
	// Load must not invent a credential: every read would then produce a
	// different one, and the core config and the subscription document would
	// disagree about a password neither of them persisted.
	if got.Credential(state.ProtoAnyTLS).Password != "" {
		t.Fatal("Load must not generate a missing credential")
	}
	if got.CredentialsReady() {
		t.Fatal("an account with no credential is not ready")
	}
	if !s.Repair() {
		t.Fatal("Repair must report the account it healed")
	}
	healed, _ := s.Find("alice")
	if healed.Credential(state.ProtoAnyTLS).Password == "" {
		t.Fatal("Repair must fill the missing credential")
	}
	if !healed.CredentialsReady() {
		t.Fatal("the repaired account must be ready")
	}
	if !got.Applied {
		t.Fatal("applied flag lost")
	}
}

func TestMutateAndSavePersistEveryAccount(t *testing.T) {
	path := storePath(t)
	s, err := Load(path)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	for _, name := range []string{"alice", "bob"} {
		if err := s.Add(New(name, []string{state.ProtoAnyTLS}, testNow)); err != nil {
			t.Fatalf("Add %s: %v", name, err)
		}
	}
	s.Mutate(func(u *User) {
		u.AddUsage(10, 20)
		u.Applied = true
	})
	if err := s.Save(); err != nil {
		t.Fatalf("Save: %v", err)
	}
	reloaded, err := Load(path)
	if err != nil {
		t.Fatalf("reload: %v", err)
	}
	for _, u := range reloaded.Users() {
		if u.UsedBytes != 30 || !u.Applied {
			t.Fatalf("%s = used %d applied %v, want 30/true", u.Name, u.UsedBytes, u.Applied)
		}
	}
}

// TestLockedKeepsConcurrentWriters makes the lost-update case real: every writer
// loads the file, adds its own account and saves. Under the lock each writer loads
// what the previous one committed, so all of them survive; the same cycle without
// the lock has each writer start from the copy it read and write back over the rest.
func TestLockedKeepsConcurrentWriters(t *testing.T) {
	path := storePath(t)
	base, lock, err := Locked(path)
	if err != nil {
		t.Fatalf("Locked: %v", err)
	}
	if err := base.Save(); err != nil {
		t.Fatalf("Save: %v", err)
	}
	lock.Unlock()

	const writers = 8
	var wg sync.WaitGroup
	errs := make(chan error, writers)
	for i := 0; i < writers; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			store, lock, err := Locked(path)
			if err != nil {
				errs <- err
				return
			}
			defer lock.Unlock()
			name := fmt.Sprintf("user-%d", i)
			if err := store.Add(New(name, []string{state.ProtoAnyTLS}, testNow)); err != nil {
				errs <- err
			}
		}(i)
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		t.Fatalf("writer: %v", err)
	}

	final, err := Load(path)
	if err != nil {
		t.Fatalf("reload: %v", err)
	}
	if final.Len() != writers {
		t.Fatalf("accounts = %d, want %d: a concurrent write was lost", final.Len(), writers)
	}
}
