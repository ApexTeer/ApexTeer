package user

import (
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/EasySBTeam/EasySB/internal/atomicfile"
	"github.com/EasySBTeam/EasySB/internal/state"
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
	alice := New("alice", []Selection{
		{Node: "n1", Protocol: state.ProtoAnyTLS},
		{Node: "n2", Protocol: state.ProtoTUIC},
	}, testNow)
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
	if got.Credential("n2") != alice.Credential("n2") {
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
	alice := New("alice", []Selection{{Node: "n1", Protocol: state.ProtoAnyTLS}}, testNow)
	if err := s.Add(alice); err != nil {
		t.Fatalf("Add: %v", err)
	}
	if err := s.Add(New("alice", []Selection{{Node: "n1", Protocol: state.ProtoAnyTLS}}, testNow)); err == nil {
		t.Fatal("duplicate name accepted")
	}
	bob := New("bob", []Selection{{Node: "n1", Protocol: state.ProtoAnyTLS}}, testNow)
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
	if err := s.Add(New("alice", []Selection{{Node: "n1", Protocol: state.ProtoAnyTLS}}, testNow)); err != nil {
		t.Fatalf("Add: %v", err)
	}
	if err := s.Add(New("bob", []Selection{{Node: "n1", Protocol: state.ProtoAnyTLS}}, testNow)); err != nil {
		t.Fatalf("Add: %v", err)
	}

	if err := s.Update("alice", func(u *User) error {
		u.QuotaBytes = 42
		u.Select("n2", state.ProtoTUIC)
		return nil
	}); err != nil {
		t.Fatalf("Update: %v", err)
	}
	got, _ := s.Find("alice")
	if got.QuotaBytes != 42 || !got.Selects("n2") {
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
	if err := s.Add(New("alice", []Selection{{Node: "n1", Protocol: state.ProtoAnyTLS}}, testNow)); err != nil {
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

// TestForgetNodeRemovesEveryTraceOfADeletedNode is the test for the shared deletion
// cascade.
//
// It is a test of the whole cascade because the cascade was previously implemented
// twice and the two disagreed: the panel removed the selection, the credential and
// the usage counter, while the TUI removed only the selection. Both interfaces now
// call this one function, so the state left by a delete is the same whichever one
// performed it, and this pins what that state is.
func TestForgetNodeRemovesEveryTraceOfADeletedNode(t *testing.T) {
	s, err := Load(storePath(t))
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	// Two nodes on one account, so "removed only what was asked" is checked as well
	// as "removed everything it should".
	alice := New("alice", []Selection{
		{Node: "n1", Protocol: state.ProtoAnyTLS},
		{Node: "n2", Protocol: state.ProtoTUIC},
	}, testNow)
	if err := s.Add(alice); err != nil {
		t.Fatalf("Add: %v", err)
	}
	// Traffic on both nodes, so a usage entry exists for each.
	if err := s.Update("alice", func(u *User) error {
		u.AddNodeUsage("n1", 100, 200)
		u.AddNodeUsage("n2", 300, 400)
		return nil
	}); err != nil {
		t.Fatalf("AddUsage: %v", err)
	}

	before, ok := s.Find("alice")
	if !ok {
		t.Fatal("alice is missing before the cascade")
	}
	if _, ok := before.Credentials["n1"]; !ok {
		t.Fatal("n1 has no credential, so the test would not observe its removal")
	}
	if _, ok := before.Usage["n1"]; !ok {
		t.Fatal("n1 has no usage entry, so the test would not observe its removal")
	}
	keptCredential := before.Credentials["n2"]
	keptUsage := before.Usage["n2"]

	s.ForgetNode("n1")

	after, ok := s.Find("alice")
	if !ok {
		t.Fatal("alice disappeared")
	}
	if after.Selects("n1") {
		t.Error("n1 is still selected")
	}
	if _, ok := after.Credentials["n1"]; ok {
		t.Error("n1's credential survived, and a node id reused later would inherit it")
	}
	if _, ok := after.Usage["n1"]; ok {
		t.Error("n1's usage counter survived, and a node id reused later would inherit it")
	}
	// The other node is untouched: this must not be a blanket reset.
	if !after.Selects("n2") {
		t.Error("n2 was deselected by removing n1")
	}
	if after.Credentials["n2"] != keptCredential {
		t.Error("n2's credential changed")
	}
	if after.Usage["n2"] != keptUsage {
		t.Error("n2's usage changed")
	}
}

// TestForgetNodeIsSafeWhenNothingMatches covers the delete of a node no account ever
// selected: the TUI used to skip the account store entirely in that case, which left
// a credential or usage entry behind if one existed. The cascade now always runs and
// has to tolerate an account that never knew the node.
func TestForgetNodeIsSafeWhenNothingMatches(t *testing.T) {
	s, err := Load(storePath(t))
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if err := s.Add(New("alice", []Selection{{Node: "n1", Protocol: state.ProtoAnyTLS}}, testNow)); err != nil {
		t.Fatalf("Add: %v", err)
	}
	before, _ := s.Find("alice")

	s.ForgetNode("never-selected")

	after, ok := s.Find("alice")
	if !ok {
		t.Fatal("alice disappeared")
	}
	if !after.Selects("n1") || after.Credentials["n1"] != before.Credentials["n1"] {
		t.Fatal("removing an unrelated node changed the account")
	}
}

// TestForgetNodeReachesEveryAccount checks the cascade is not accidentally scoped to
// one account: a node can be selected by any number of them.
func TestForgetNodeReachesEveryAccount(t *testing.T) {
	s, err := Load(storePath(t))
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	for _, name := range []string{"alice", "bob", "carol"} {
		if err := s.Add(New(name, []Selection{{Node: "n1", Protocol: state.ProtoAnyTLS}}, testNow)); err != nil {
			t.Fatalf("Add %s: %v", name, err)
		}
	}

	s.ForgetNode("n1")

	for _, u := range s.Users() {
		if u.Selects("n1") {
			t.Errorf("%s still selects the deleted node", u.Name)
		}
		if _, ok := u.Credentials["n1"]; ok {
			t.Errorf("%s still holds the deleted node's credential", u.Name)
		}
	}
}

// TestLoadRecoversFromTheBackupWhenTheStoreIsCorrupt is the recovery test for the one
// file whose loss cannot be undone.
//
// The account store holds the only copy of every account's credentials. Nothing
// regenerates them: the running config carries a copy, but no code rebuilds the store
// from it, so a truncated file used to mean every client had to be re-imported. Now
// the file is quarantined, the previous content is restored from the sibling backup,
// and the unusable file is kept for forensics.
func TestLoadRecoversFromTheBackupWhenTheStoreIsCorrupt(t *testing.T) {
	path := storePath(t)
	s, err := Load(path)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	alice := New("alice", []Selection{{Node: "n1", Protocol: state.ProtoAnyTLS}}, testNow)
	if err := s.Add(alice); err != nil {
		t.Fatalf("Add: %v", err)
	}
	// A second save, so the backup holds a good document rather than nothing.
	if err := s.Update("alice", func(u *User) error { u.Remark = "second save"; return nil }); err != nil {
		t.Fatalf("Update: %v", err)
	}
	if _, err := os.Stat(atomicfile.BackupPath(path)); err != nil {
		t.Fatalf("no backup was kept: %v", err)
	}

	// The store is truncated by something outside the program.
	if err := os.WriteFile(path, []byte(`{"version":2,"users":[`), 0o600); err != nil {
		t.Fatal(err)
	}

	recovered, err := Load(path)
	if err != nil {
		t.Fatalf("Load did not recover: %v", err)
	}
	got, ok := recovered.Find("alice")
	if !ok {
		t.Fatal("the recovered store lost the account")
	}
	if !got.CredentialsReady() {
		t.Fatal("the recovered account has no credentials")
	}

	// The unusable file was kept, not deleted.
	entries, err := os.ReadDir(filepath.Dir(path))
	if err != nil {
		t.Fatal(err)
	}
	var quarantined int
	for _, e := range entries {
		if strings.Contains(e.Name(), ".corrupt-") {
			quarantined++
		}
	}
	if quarantined == 0 {
		t.Fatal("the unusable file was not kept for forensics")
	}

	// And the live path is usable again, so the next save has a target.
	if _, err := Load(path); err != nil {
		t.Fatalf("the restored store is not loadable: %v", err)
	}
}

// TestLoadRefusesWhenTheBackupIsAlsoUnusable pins the other half: recovery must not
// promote a broken backup over a broken store, which would only hide the problem.
func TestLoadRefusesWhenTheBackupIsAlsoUnusable(t *testing.T) {
	path := storePath(t)
	s, err := Load(path)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if err := s.Add(New("alice", []Selection{{Node: "n1", Protocol: state.ProtoAnyTLS}}, testNow)); err != nil {
		t.Fatalf("Add: %v", err)
	}
	if err := s.Add(New("bob", []Selection{{Node: "n1", Protocol: state.ProtoAnyTLS}}, testNow)); err != nil {
		t.Fatalf("Add: %v", err)
	}
	// Both the store and its backup are unusable.
	if err := os.WriteFile(path, []byte("{"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(atomicfile.BackupPath(path), []byte("{"), 0o600); err != nil {
		t.Fatal(err)
	}

	if _, err := Load(path); err == nil {
		t.Fatal("a store with an unusable backup was accepted")
	}
}

// TestBackupHoldsThePreviousContent checks the backup is the last known good state
// rather than a second copy of the new one, which is what makes it worth restoring.
func TestBackupHoldsThePreviousContent(t *testing.T) {
	path := storePath(t)
	s, err := Load(path)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if err := s.Add(New("first", []Selection{{Node: "n1", Protocol: state.ProtoAnyTLS}}, testNow)); err != nil {
		t.Fatalf("Add: %v", err)
	}
	// The first save had nothing to back up.
	if _, err := os.Stat(atomicfile.BackupPath(path)); !os.IsNotExist(err) {
		t.Fatalf("the first save created a backup (stat err %v)", err)
	}

	if err := s.Add(New("second", []Selection{{Node: "n2", Protocol: state.ProtoTUIC}}, testNow)); err != nil {
		t.Fatalf("Add: %v", err)
	}
	backup, err := Load(atomicfile.BackupPath(path))
	if err != nil {
		t.Fatalf("the backup is not loadable: %v", err)
	}
	if _, ok := backup.Find("second"); ok {
		t.Fatal("the backup holds the new content instead of the previous one")
	}
	if _, ok := backup.Find("first"); !ok {
		t.Fatal("the backup lost the account the previous content had")
	}
}

func TestByToken(t *testing.T) {
	s, err := Load(storePath(t))
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	alice := New("alice", []Selection{{Node: "n1", Protocol: state.ProtoAnyTLS}}, testNow)
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
	active := New("active", []Selection{{Node: "n1", Protocol: state.ProtoAnyTLS}}, testNow)
	disabled := New("disabled", []Selection{{Node: "n1", Protocol: state.ProtoAnyTLS}}, testNow)
	disabled.Enabled = false
	expired := New("expired", []Selection{{Node: "n1", Protocol: state.ProtoAnyTLS}}, testNow)
	expired.ExpireAt = testNow.Add(-time.Minute)
	overQuota := New("over-quota", []Selection{{Node: "n1", Protocol: state.ProtoAnyTLS}}, testNow)
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
	body := `{"version":2,"users":[{"name":"alice","token":"tok","enabled":true,
		"nodes":["n1"],"quota_bytes":1000,"used_bytes":1000,
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
	if got.Credential("n1").Password != "" {
		t.Fatal("Load must not generate a missing credential")
	}
	if got.CredentialsReady() {
		t.Fatal("an account with no credential is not ready")
	}
	if !s.Repair(map[string]string{"n1": state.ProtoAnyTLS}) {
		t.Fatal("Repair must report the account it healed")
	}
	healed, _ := s.Find("alice")
	if healed.Credential("n1").Password == "" {
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
		if err := s.Add(New(name, []Selection{{Node: "n1", Protocol: state.ProtoAnyTLS}}, testNow)); err != nil {
			t.Fatalf("Add %s: %v", name, err)
		}
	}
	s.Mutate(func(u *User) {
		u.AddNodeUsage("n1", 10, 20)
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
			if err := store.Add(New(name, []Selection{{Node: "n1", Protocol: state.ProtoAnyTLS}}, testNow)); err != nil {
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
