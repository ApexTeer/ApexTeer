package tools

import (
	"context"
	"errors"
	"net/http"
	"path/filepath"
	"testing"
	"time"

	"github.com/EasySB-Team/EasySB/internal/i18n"
	"github.com/EasySB-Team/EasySB/internal/toolbox"
)

// The registry is the one list the menu, the board and --tool read, so these tests guard
// what a hand-written menu used to get wrong: an entry with no name, two entries sharing an
// id, an entry filed under a group the menu does not list.
func TestEveryEntryIsComplete(t *testing.T) {
	entries := All()
	if len(entries) < 12 {
		t.Fatalf("the toolbox lists %d entries, which is fewer than the tools this project ships", len(entries))
	}
	for _, entry := range entries {
		if entry.ID == "" {
			t.Errorf("an entry has no id: %+v", entry)
			continue
		}
		if entry.Run == nil {
			t.Errorf("%s has nothing to run", entry.ID)
		}
		for _, lang := range []i18n.Lang{i18n.Chinese, i18n.English} {
			if got := lang.T("toolbox_" + entry.ID); got == "toolbox_"+entry.ID {
				t.Errorf("%s has no name in %s", entry.ID, lang)
			}
			if got := lang.T("desc_toolbox_" + entry.ID); got == "desc_toolbox_"+entry.ID {
				t.Errorf("%s has no description in %s", entry.ID, lang)
			}
		}
	}
}

func TestEntryIDsAreUnique(t *testing.T) {
	seen := make(map[string]bool)
	for _, entry := range All() {
		if seen[entry.ID] {
			t.Errorf("%s is registered twice", entry.ID)
		}
		seen[entry.ID] = true
	}
}

func TestEveryEntryIsInAListedGroup(t *testing.T) {
	known := make(map[string]bool)
	for _, group := range Groups() {
		if known[group] {
			t.Errorf("%s is listed twice", group)
		}
		known[group] = true
		if got := i18n.Chinese.T("toolbox_group_" + group); got == "toolbox_group_"+group {
			t.Errorf("group %s has no name", group)
		}
	}
	for _, entry := range All() {
		if !known[entry.Group] {
			t.Errorf("%s is filed under %q, which the menu does not list", entry.ID, entry.Group)
		}
	}
}

// Every group has to hold something: an empty entry in the menu is a door into a room with
// nothing in it.
func TestEveryGroupHasEntries(t *testing.T) {
	for _, group := range Groups() {
		if len(InGroup(group)) == 0 {
			t.Errorf("group %s has no entries", group)
		}
	}
}

func TestLookupFindsWhatAllLists(t *testing.T) {
	for _, entry := range All() {
		found, ok := Lookup(entry.ID)
		if !ok {
			t.Fatalf("%s is listed but not found", entry.ID)
		}
		if found.Group != entry.Group {
			t.Errorf("%s is in %s when looked up but %s when listed", entry.ID, found.Group, entry.Group)
		}
	}
	if _, ok := Lookup("no-such-tool"); ok {
		t.Fatal("Lookup should not invent an entry")
	}
}

// The board is written by whoever ran the tool: `--tool` records its run so a measurement
// taken without a terminal still shows up in the panel's 看板.
func TestRecordWritesTheBoardReadByBothReaders(t *testing.T) {
	path := filepath.Join(t.TempDir(), "easysb-toolbox.json")
	t.Setenv(toolbox.BoardEnv, path)
	if got := BoardPath(); got != path {
		t.Errorf("BoardPath() = %q, want the override %q", got, path)
	}

	if err := Record("unlock-ai", toolbox.Result{Summary: "解锁 2 · 不解锁 1 (3)"}, nil); err != nil {
		t.Fatalf("Record: %v", err)
	}
	if err := Record("backtrace", toolbox.Result{}, errors.New("需要 root")); err != nil {
		t.Fatalf("Record: %v", err)
	}
	board := toolbox.LoadBoard(path)
	if got := board["unlock-ai"].Result.Summary; got != "解锁 2 · 不解锁 1 (3)" {
		t.Errorf("stored summary = %q", got)
	}
	if got := board["backtrace"].Error; got != "需要 root" {
		t.Errorf("stored error = %q, want the reason the run failed", got)
	}
	if board["backtrace"].When.IsZero() {
		t.Error("a stored run needs the time it ran")
	}

	// Running the same entry again replaces its record: the board holds the last run, not a
	// history of every run.
	if err := Record("unlock-ai", toolbox.Result{Summary: "全解锁 (3)"}, nil); err != nil {
		t.Fatal(err)
	}
	board = toolbox.LoadBoard(path)
	if len(board) != 2 {
		t.Errorf("board holds %d records, want one per entry", len(board))
	}
	if got := board["unlock-ai"].Result.Summary; got != "全解锁 (3)" {
		t.Errorf("the second run should have replaced the first, got %q", got)
	}
}

// Verdict wording is the panel's vocabulary, so it has to survive a round trip through
// both languages.
func TestVerdictWording(t *testing.T) {
	for _, token := range []string{"unlocked", "blocked", "unknown"} {
		if !IsVerdict(token) {
			t.Errorf("%s should be a verdict", token)
		}
		for _, lang := range []i18n.Lang{i18n.Chinese, i18n.English} {
			got := Verdict(lang, token)
			if got == token && lang == i18n.Chinese {
				t.Errorf("%s is not worded in Chinese", token)
			}
			if got == "" {
				t.Errorf("%s is worded as nothing in %s", token, lang)
			}
		}
	}
	if IsVerdict("maybe") {
		t.Error("a verdict the panel did not define should not pass as one")
	}
	if got := Verdict(i18n.Chinese, "maybe"); got != "maybe" {
		t.Errorf("an unknown token should pass through, got %q", got)
	}
}

// The unlock entries are the ones the panel had before the toolbox existed, so they have to
// keep working exactly as they did: every service in the catalogue belongs to one of them,
// and none of them disappears into another entry.
//
// The count is taken with a client that refuses every request: each probe then reports its
// service as unknown, and the row count is the number of services the entry covers, with no
// network involved.
func TestUnlockEntriesCoverTheCatalogue(t *testing.T) {
	counted := make(map[string]int)
	total := 0
	for _, entry := range All() {
		if entry.Group != GroupUnlock {
			continue
		}
		result, err := entry.Run(context.Background(), toolbox.Options{
			Client:  refusingClient{},
			Timeout: 5 * time.Second,
		})
		if err != nil {
			t.Fatalf("%s: %v", entry.ID, err)
		}
		counted[entry.ID] = len(result.Rows)
		total += len(result.Rows)
	}
	if len(counted) != 3 {
		t.Fatalf("expected three unlock entries, got %d", len(counted))
	}
	for id, count := range counted {
		if count == 0 {
			t.Errorf("%s covers no service", id)
		}
	}
	if total != 17 {
		t.Errorf("the unlock entries cover %d services, the catalogue has 17", total)
	}
}

// refusingClient stands in for a host with no route to the services: every request fails, so
// a probe cannot mistake a test run for a real answer.
type refusingClient struct{}

func (refusingClient) Do(*http.Request) (*http.Response, error) {
	return nil, errors.New("the tests do not reach the network")
}
