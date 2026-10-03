package stats

import (
	"context"
	"errors"
	"path/filepath"
	"testing"
	"time"

	"github.com/MinimaxFlora/EasySB/internal/state"
	"github.com/MinimaxFlora/EasySB/internal/user"
)

// fakeSource is a counter source whose readings the test controls.
type fakeSource struct {
	readings Counters
	err      error
	closed   bool
}

func (f *fakeSource) Counters(context.Context, []string) (Counters, error) {
	if f.err != nil {
		return nil, f.err
	}
	return f.readings, nil
}

func (f *fakeSource) Close() error {
	f.closed = true
	return nil
}

// account writes one account to a fresh file and returns the store.
func account(t *testing.T, name string, edit func(*user.User)) (*user.Store, string, time.Time) {
	t.Helper()
	now := time.Date(2026, 3, 10, 12, 0, 0, 0, time.UTC)
	path := filepath.Join(t.TempDir(), "users.json")
	store, err := user.Load(path)
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	u := user.New(name, state.Keys, now)
	u.Token = "token-" + name
	edit(&u)
	if err := store.Add(u); err != nil {
		t.Fatalf("add: %v", err)
	}
	// The deploy path is what marks accounts as present in the core; without it
	// every cycle would look like a membership change.
	store.MarkApplied(now)
	if err := store.Save(); err != nil {
		t.Fatalf("save: %v", err)
	}
	return store, path, now
}

// newLoop wires a loop around a fake source and records every apply.
func newLoop(t *testing.T, path string, source *fakeSource, now time.Time) (*Loop, *int) {
	t.Helper()
	applied := 0
	loop := New(Options{
		AccountsPath: path,
		Node:         state.Default,
		Dial:         func() (Counter, error) { return source, nil },
		Apply: func(context.Context, state.Config, []user.User) error {
			applied++
			return nil
		},
		Interval:     time.Minute,
		StatsCapable: func() bool { return true },
		Now:          func() time.Time { return now },
		Log:          func(string) {},
	})
	return loop, &applied
}

// reload reads the account file back, which is what the panel and the endpoint
// do between cycles.
func reload(t *testing.T, path string) *user.Store {
	t.Helper()
	store, err := user.Load(path)
	if err != nil {
		t.Fatalf("reload: %v", err)
	}
	return store
}

func TestLoopAccountsDeltas(t *testing.T) {
	store, path, now := account(t, "alice", func(*user.User) {})
	_ = store
	source := &fakeSource{}
	loop, applied := newLoop(t, path, source, now)

	// The first cycle only establishes the baseline: the core counters began
	// before EasySB could observe them.
	source.readings = Counters{"token-alice": {Upload: 100, Download: 400}}
	if err := loop.Tick(context.Background()); err != nil {
		t.Fatalf("first tick: %v", err)
	}
	if got := reload(t, path).Users()[0]; got.UsedBytes != 0 {
		t.Fatalf("first cycle should not charge traffic, used = %d", got.UsedBytes)
	}

	source.readings = Counters{"token-alice": {Upload: 300, Download: 900}}
	if err := loop.Tick(context.Background()); err != nil {
		t.Fatalf("second tick: %v", err)
	}
	got := reload(t, path).Users()[0]
	if got.UsedBytes != 700 || got.UploadBytes != 200 || got.DownloadBytes != 500 {
		t.Fatalf("usage = %d (%d up, %d down), want 700 (200 up, 500 down)", got.UsedBytes, got.UploadBytes, got.DownloadBytes)
	}
	if *applied != 0 {
		t.Fatalf("accounting alone must not restart the core, applied %d times", *applied)
	}
	if !source.closed {
		t.Fatal("the counter source should be closed after each cycle")
	}
}

func TestLoopKeepsUsageAcrossCoreRestart(t *testing.T) {
	_, path, now := account(t, "alice", func(*user.User) {})
	source := &fakeSource{readings: Counters{"token-alice": {Upload: 0, Download: 0}}}
	loop, _ := newLoop(t, path, source, now)
	if err := loop.Tick(context.Background()); err != nil {
		t.Fatalf("baseline: %v", err)
	}
	source.readings = Counters{"token-alice": {Upload: 5000, Download: 5000}}
	if err := loop.Tick(context.Background()); err != nil {
		t.Fatalf("charge: %v", err)
	}

	// A core restart resets its counters. The account keeps what it already used,
	// and the post-restart reading is charged as well: those bytes happened after
	// the restart and nobody has counted them yet.
	source.readings = Counters{"token-alice": {Upload: 10, Download: 10}}
	if err := loop.Tick(context.Background()); err != nil {
		t.Fatalf("after restart: %v", err)
	}
	store := reload(t, path)
	if got := store.Users()[0]; got.UsedBytes != 10020 {
		t.Fatalf("used = %d, want 10020 (10000 kept + 20 post-restart)", got.UsedBytes)
	}
}

// TestLoopKeepsBaselineAcrossServiceRestart covers a restart of the subscription
// service while the core keeps running: the persisted baseline is what stops the
// traffic between the last sample and the restart from being swallowed.
func TestLoopKeepsBaselineAcrossServiceRestart(t *testing.T) {
	_, path, now := account(t, "alice", func(*user.User) {})
	source := &fakeSource{readings: Counters{"token-alice": {Upload: 0, Download: 0}}}
	loop, _ := newLoop(t, path, source, now)
	if err := loop.Tick(context.Background()); err != nil {
		t.Fatalf("baseline: %v", err)
	}
	source.readings = Counters{"token-alice": {Upload: 1000, Download: 0}}
	if err := loop.Tick(context.Background()); err != nil {
		t.Fatalf("charge: %v", err)
	}

	// The service restarts, the core does not.
	restarted, _ := newLoop(t, path, source, now)
	source.readings = Counters{"token-alice": {Upload: 1500, Download: 0}}
	if err := restarted.Tick(context.Background()); err != nil {
		t.Fatalf("after restart: %v", err)
	}
	store := reload(t, path)
	if got := store.Users()[0]; got.UsedBytes != 1500 {
		t.Fatalf("used = %d, want 1500 (1000 before + 500 after the restart)", got.UsedBytes)
	}
}

func TestLoopSuspendsOverQuotaOnce(t *testing.T) {
	_, path, now := account(t, "alice", func(u *user.User) { u.QuotaBytes = 1000 })
	source := &fakeSource{readings: Counters{"token-alice": {Upload: 0, Download: 0}}}
	loop, applied := newLoop(t, path, source, now)
	if err := loop.Tick(context.Background()); err != nil {
		t.Fatalf("baseline: %v", err)
	}

	source.readings = Counters{"token-alice": {Upload: 800, Download: 800}}
	if err := loop.Tick(context.Background()); err != nil {
		t.Fatalf("over quota: %v", err)
	}
	store := reload(t, path)
	u := store.Users()[0]
	if u.Status(now) != user.StatusQuota {
		t.Fatalf("status = %s, want %s", u.Status(now), user.StatusQuota)
	}
	// The account is no longer part of what the core accepts, so it is recorded
	// as not applied: that is what keeps later cycles from restarting the core.
	if u.Applied {
		t.Fatal("a suspended account must not be recorded as applied")
	}
	if *applied != 1 {
		t.Fatalf("applied %d times, want 1", *applied)
	}
	if routable := store.Routable(now); len(routable) != 0 {
		t.Fatalf("a suspended account must not be routable: %v", routable)
	}

	// The core no longer accepts the account, so later cycles must not restart
	// it again.
	if err := loop.Tick(context.Background()); err != nil {
		t.Fatalf("steady state: %v", err)
	}
	if *applied != 1 {
		t.Fatalf("applied %d times, want 1", *applied)
	}
}

func TestLoopResumesAfterReset(t *testing.T) {
	store, path, now := account(t, "alice", func(u *user.User) { u.QuotaBytes = 1000 })
	_ = store
	source := &fakeSource{readings: Counters{"token-alice": {Upload: 0, Download: 0}}}
	loop, applied := newLoop(t, path, source, now)
	if err := loop.Tick(context.Background()); err != nil {
		t.Fatalf("baseline: %v", err)
	}
	source.readings = Counters{"token-alice": {Upload: 900, Download: 900}}
	if err := loop.Tick(context.Background()); err != nil {
		t.Fatalf("over quota: %v", err)
	}

	// The panel's reset action zeroes the counters, which lifts the suspension.
	if err := reload(t, path).Update("alice", func(u *user.User) error {
		u.ResetCounters(now)
		return nil
	}); err != nil {
		t.Fatalf("reset: %v", err)
	}
	source.readings = Counters{"token-alice": {Upload: 900, Download: 900}}
	if err := loop.Tick(context.Background()); err != nil {
		t.Fatalf("after reset: %v", err)
	}
	if *applied != 2 {
		t.Fatalf("applied %d times, want 2", *applied)
	}
	if routable := reload(t, path).Routable(now); len(routable) != 1 {
		t.Fatalf("the reset account should be routable again: %v", routable)
	}
}

func TestLoopMonthlyReset(t *testing.T) {
	_, path, now := account(t, "alice", func(u *user.User) { u.QuotaBytes = 1000 })
	source := &fakeSource{readings: Counters{"token-alice": {Upload: 0, Download: 0}}}
	loop, applied := newLoop(t, path, source, now)
	if err := loop.Tick(context.Background()); err != nil {
		t.Fatalf("baseline: %v", err)
	}
	source.readings = Counters{"token-alice": {Upload: 900, Download: 900}}
	if err := loop.Tick(context.Background()); err != nil {
		t.Fatalf("over quota: %v", err)
	}
	// A new calendar month starts the counters over, so an over-quota account
	// becomes usable again on its own.
	next := now.AddDate(0, 1, 0)
	loop.opts.Now = func() time.Time { return next }
	if err := loop.Tick(context.Background()); err != nil {
		t.Fatalf("new month: %v", err)
	}
	store := reload(t, path)
	if got := store.Users()[0]; got.UsedBytes != 0 {
		t.Fatalf("used = %d, want a monthly reset to 0", got.UsedBytes)
	}
	if *applied != 2 {
		t.Fatalf("applied %d times, want 2: the suspension and its monthly lift", *applied)
	}
}

func TestLoopEmptyStoreResetsBaseline(t *testing.T) {
	path := filepath.Join(t.TempDir(), "users.json")
	source := &fakeSource{}
	loop, _ := newLoop(t, path, source, time.Now())
	if err := loop.Tick(context.Background()); err != nil {
		t.Fatalf("empty store should not fail: %v", err)
	}
	if loop.sampled {
		t.Fatal("an empty store should clear the baseline")
	}
}

func TestLoopReportsSourceFailure(t *testing.T) {
	_, path, now := account(t, "alice", func(*user.User) {})
	source := &fakeSource{err: errors.New("dial failed")}
	loop, _ := newLoop(t, path, source, now)
	if err := loop.Tick(context.Background()); err == nil {
		t.Fatal("expected the source failure to surface")
	}
}

func TestLoopWithoutApplierSkipsRestart(t *testing.T) {
	_, path, now := account(t, "alice", func(u *user.User) { u.QuotaBytes = 1000 })
	source := &fakeSource{readings: Counters{"token-alice": {Upload: 0, Download: 0}}}
	loop := New(Options{
		AccountsPath: path,
		Node:         state.Default,
		Dial:         func() (Counter, error) { return source, nil },
		StatsCapable: func() bool { return true },
		Now:          func() time.Time { return now },
	})
	if err := loop.Tick(context.Background()); err != nil {
		t.Fatalf("baseline: %v", err)
	}
	source.readings = Counters{"token-alice": {Upload: 900, Download: 900}}
	if err := loop.Tick(context.Background()); err != nil {
		t.Fatalf("tick with no applier: %v", err)
	}
	if st := reload(t, path).Users()[0].Status(now); st != user.StatusQuota {
		t.Fatalf("status = %s, want %s", st, user.StatusQuota)
	}
}

// A build without the V2Ray API has nothing to read, so the cycle must cost nothing:
// no dial, no failure line every interval, and one sentence saying why the counters
// stay empty. The panel still deploys a working node in that build.
func TestLoopSkipsSamplingWithoutCounters(t *testing.T) {
	now := time.Date(2026, 3, 10, 12, 0, 0, 0, time.UTC)
	_, path, _ := account(t, "alice", func(_ *user.User) {})
	dialed := 0
	lines := []string{}
	loop := New(Options{
		AccountsPath: path,
		Node:         state.Default,
		Dial: func() (Counter, error) {
			dialed++
			return &fakeSource{}, nil
		},
		StatsCapable: func() bool { return false },
		Now:          func() time.Time { return now },
		Log:          func(line string) { lines = append(lines, line) },
	})
	for i := 0; i < 3; i++ {
		if err := loop.Tick(context.Background()); err != nil {
			t.Fatalf("tick %d: %v", i, err)
		}
	}
	if dialed != 0 {
		t.Fatalf("dialed %d times, want none: there is no API to read", dialed)
	}
	if len(lines) != 1 {
		t.Fatalf("logged %d lines, want exactly one explanation: %v", len(lines), lines)
	}
}

func TestDiffCounters(t *testing.T) {
	prev := Counters{"a": {Upload: 10, Download: 20}, "b": {Upload: 5, Download: 5}}
	cur := Counters{
		"a": {Upload: 30, Download: 50},
		"b": {Upload: 1, Download: 1}, // restarted core
		"c": {Upload: 7, Download: 7}, // appeared mid-cycle
	}
	got := diffCounters(prev, cur, true)
	if got["a"] != (Usage{Upload: 20, Download: 30}) {
		t.Fatalf("delta for a = %+v", got["a"])
	}
	// A counter that went backwards means the core restarted: that reading is
	// traffic made since the restart, so it is charged rather than clamped away.
	if got["b"] != (Usage{Upload: 1, Download: 1}) {
		t.Fatalf("a restarted counter should be charged its reading: %+v", got["b"])
	}
	// An account nobody has seen before is charged its whole reading: that
	// traffic happened while it was unobserved, and undercounting a quota is the
	// worse error.
	if got["c"] != (Usage{Upload: 7, Download: 7}) {
		t.Fatalf("a new account should be charged its reading: %+v", got["c"])
	}
	if out := diffCounters(prev, cur, false); len(out) != 0 {
		t.Fatalf("the baseline cycle must not charge anything: %+v", out)
	}
}
