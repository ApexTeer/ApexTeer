// Package stats reads the core's per-account counters over its V2Ray gRPC service and
// turns them into the usage the panel shows and the quota it enforces. The counters live
// in the running core and reset when it restarts, so everything here works in deltas
// against the previous sample rather than in absolute values.
package stats

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"time"

	"github.com/EasySBTeam/EasySB/internal/sbcore"
	"github.com/EasySBTeam/EasySB/internal/state"
	"github.com/EasySBTeam/EasySB/internal/user"
)

// DefaultInterval is how often the counters are sampled when the node state does
// not say otherwise.
const DefaultInterval = 5 * time.Minute

// Options configures an accounting Loop. Every dependency is injectable, so the
// policy can be tested without a core, a clock or a disk.
type Options struct {
	// AccountsPath is the account file the loop reads and rewrites.
	AccountsPath string
	// Node returns the node state, which carries the ports and the sync interval.
	Node func() state.Config
	// Dial opens a counter source. It is called once per cycle, so a restarted
	// core is picked up without keeping a stale connection.
	Dial func() (Counter, error)
	// Apply renders the node configuration for the given accounts, validates it
	// and restarts the core.
	Apply func(ctx context.Context, cfg state.Config, users []user.User) error
	// Interval overrides the node's sync interval when non-zero.
	Interval time.Duration
	// StatsCapable reports whether this build carries the V2Ray API the counters
	// are read over. Nil asks internal/sbcore, which is the compile-time answer:
	// the panel is built with with_v2ray_api (release/TAGS), and a build without
	// it has no counters to read at all. Tests set it explicitly, so the policy
	// stays testable in a build that cannot count.
	StatsCapable func() bool
	// Now overrides the clock in tests.
	Now func() time.Time
	// Log receives one line per notable event.
	Log func(string)
}

// Loop keeps the counters and the accounts the core accepts in step with
// reality: it samples the counters, adds the deltas, applies the monthly reset
// and asks for a restart only when an account crossed a quota or expiry line.
type Loop struct {
	opts Options
	// sample is the previous absolute reading, keyed by core user name.
	sample Counters
	// sampled records whether sample can be subtracted from; the first cycle only
	// establishes the baseline.
	sampled bool
	// announcedNoStats keeps the "no counter source" line from repeating every
	// interval on a node whose core cannot count.
	announcedNoStats bool
	// sampleLoaded records whether the on-disk baseline has been consulted yet.
	sampleLoaded bool
}

// New prepares an accounting loop.
func New(opts Options) *Loop {
	return &Loop{opts: opts}
}

// statsCapable answers whether this loop has counters to read: the injected
// answer when a caller gave one, and otherwise what the compiled build carries.
func (l *Loop) statsCapable() bool {
	if l.opts.StatsCapable != nil {
		return l.opts.StatsCapable()
	}
	return sbcore.StatsCapable()
}

// Run samples until ctx is cancelled. It ticks once immediately, because the
// service may have been started precisely to apply a policy change.
func (l *Loop) Run(ctx context.Context) error {
	l.log("accounting every " + l.interval().String())
	for {
		if err := l.Tick(ctx); err != nil && ctx.Err() == nil {
			l.log("accounting: " + err.Error())
		}
		select {
		case <-ctx.Done():
			return nil
		case <-time.After(l.interval()):
		}
	}
}

// Tick runs one accounting cycle.
func (l *Loop) Tick(ctx context.Context) error {
	now := l.now()
	// A build without the V2Ray API has nothing to read: the deployed config
	// carries no stats block, so the cycle would only fail on a dead socket every
	// interval. The node is announced once, when the build says so.
	if !l.statsCapable() {
		if !l.announcedNoStats {
			l.announcedNoStats = true
			l.log("accounting off: this build carries no v2ray api")
		}
		return nil
	}
	// Restore the baseline a previous run persisted. Without it a restart of the
	// subscription service swallows everything between the last sample before it
	// stopped and the first sample after it came back.
	l.loadSample()

	// The account names are read without the lock: sampling the core is the slow
	// part of the cycle and only needs the names, not the file held open.
	peek, err := user.Load(l.opts.AccountsPath)
	if err != nil {
		return err
	}
	if peek.Len() == 0 {
		l.sample, l.sampled = nil, false
		l.saveSample()
		return nil
	}

	names := make([]string, 0, peek.Len())
	for _, u := range peek.Users() {
		names = append(names, u.Token)
	}
	counters, err := l.counters(ctx, names)
	if err != nil {
		return err
	}
	deltas := diffCounters(l.sample, counters, l.sampled)

	// The deltas are added under the account lock, on a store reloaded from disk: the
	// panel may have changed an account while the counters were being read, and
	// writing back the copy that predates that change would lose it.
	store, lock, err := user.Locked(l.opts.AccountsPath)
	if err != nil {
		return err
	}
	changed := false
	store.Mutate(func(u *user.User) {
		if d, ok := deltas[u.Token]; ok {
			u.AddUsage(d.Upload, d.Download)
			changed = true
		}
		if u.ResetIfNewMonth(now) {
			changed = true
		}
	})

	// A restart is only needed when the set of accounts the core accepts has to
	// change; accounting itself never touches the running core. A loop without an
	// applier only keeps the counters correct, which is what tests exercise.
	if l.opts.Apply != nil && transitions(store, now) {
		if err := l.opts.Apply(ctx, l.opts.Node(), store.Routable(now)); err != nil {
			lock.Unlock()
			return err
		}
		store.MarkApplied(now)
		changed = true
	}
	if changed {
		if err := store.Save(); err != nil {
			lock.Unlock()
			return err
		}
	}
	lock.Unlock()
	// The baseline moves only after the deltas it produced are on disk. The store
	// is reloaded from the file at the top of every cycle, so advancing the sample
	// before a failed Apply or Save would subtract those bytes from the next diff
	// and drop that interval's traffic for good.
	l.sample, l.sampled = counters, true
	l.saveSample()
	return nil
}

// counters opens a source and reads the absolute counters of the given users.
func (l *Loop) counters(ctx context.Context, names []string) (Counters, error) {
	if l.opts.Dial == nil {
		return nil, errors.New("stats: no counter source")
	}
	source, err := l.opts.Dial()
	if err != nil {
		return nil, err
	}
	defer source.Close()
	return source.Counters(ctx, names)
}

// transitions reports whether the accounts the core accepts have to change,
// which is the only reason the accounting loop restarts it.
func transitions(store *user.Store, now time.Time) bool {
	live := make(map[string]bool, store.Len())
	for _, u := range store.Routable(now) {
		live[u.Token] = true
	}
	for _, u := range store.Users() {
		if u.Applied != live[u.Token] {
			return true
		}
	}
	return false
}

// diffCounters subtracts a previous sample from the current one.
//
// A counter that went backwards means the core restarted and its counters
// restarted with it, so that account's delta is zero and the baseline moves. A
// user missing from the previous sample is charged its whole reading instead:
// that traffic happened while nobody was accounting for it, and undercounting a
// quota is the worse error.
func diffCounters(prev, cur Counters, sampled bool) Counters {
	out := Counters{}
	if !sampled {
		return out
	}
	for name, c := range cur {
		p := prev[name]
		up, down := c.Upload-p.Upload, c.Download-p.Download
		// A counter that went backwards means the core restarted and its counters
		// restarted with it. The whole current reading is then traffic nobody has
		// counted yet, so it is charged rather than clamped away.
		if up < 0 {
			up = c.Upload
		}
		if down < 0 {
			down = c.Download
		}
		if up != 0 || down != 0 {
			out[name] = Usage{Upload: up, Download: down}
		}
	}
	return out
}

func (l *Loop) interval() time.Duration {
	if l.opts.Interval > 0 {
		return l.opts.Interval
	}
	if l.opts.Node != nil {
		if d := l.opts.Node().SyncInterval(); d > 0 {
			return d
		}
	}
	return DefaultInterval
}

func (l *Loop) now() time.Time {
	if l.opts.Now != nil {
		return l.opts.Now()
	}
	return time.Now()
}

func (l *Loop) log(line string) {
	if l.opts.Log != nil {
		l.opts.Log(line)
	}
}

// sampleFile is the on-disk form of the counter baseline.
type sampleFile struct {
	Counters map[string]sampleUsage `json:"counters"`
}

type sampleUsage struct {
	Upload   int64 `json:"upload"`
	Download int64 `json:"download"`
}

// samplePath is where the baseline is persisted. It sits beside the account file
// so a deployment keeps its two state files together.
func (l *Loop) samplePath() string {
	if l.opts.AccountsPath == "" {
		return ""
	}
	return filepath.Join(filepath.Dir(l.opts.AccountsPath), "easysb-stats.json")
}

// loadSample restores the baseline a previous run left behind. It runs once per
// loop; a missing or unreadable file simply leaves the loop without a baseline,
// which is what this did before persistence existed.
func (l *Loop) loadSample() {
	if l.sampleLoaded {
		return
	}
	l.sampleLoaded = true
	path := l.samplePath()
	if path == "" {
		return
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return
	}
	var doc sampleFile
	if err := json.Unmarshal(data, &doc); err != nil {
		return
	}
	if len(doc.Counters) == 0 {
		return
	}
	sample := make(Counters, len(doc.Counters))
	for name, u := range doc.Counters {
		sample[name] = Usage{Upload: u.Upload, Download: u.Download}
	}
	l.sample, l.sampled = sample, true
}

// saveSample writes the current baseline so a restart of the subscription
// service does not discard the traffic accumulated since the last cycle. It is
// called only after the deltas it produced are safely on disk.
func (l *Loop) saveSample() {
	path := l.samplePath()
	if path == "" {
		return
	}
	doc := sampleFile{Counters: make(map[string]sampleUsage, len(l.sample))}
	for name, u := range l.sample {
		doc.Counters[name] = sampleUsage{Upload: u.Upload, Download: u.Download}
	}
	data, err := json.Marshal(doc)
	if err != nil {
		return
	}
	_ = os.WriteFile(path, data, 0o600)
}
