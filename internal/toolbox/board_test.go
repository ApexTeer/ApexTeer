package toolbox

import (
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"
)

// The board is a cache of measurements, so these tests are about what happens when it is
// missing, unreadable or half-written: the panel has to keep working in every case.

func sampleBoard() Board {
	return Board{
		"unlock-media": {
			ID:   "unlock-media",
			When: time.Date(2026, 9, 26, 7, 42, 11, 0, time.UTC),
			Result: Result{
				Headers: []string{"service", "status", "region"},
				Rows:    [][]string{{"Netflix", "unlocked", "US"}, {"Spotify", "blocked", "—"}},
				Notes:   []string{"Spotify: refused this address"},
				Summary: "unlocked 1 · blocked 1 (2)",
			},
		},
		"backtrace": {
			ID:    "backtrace",
			When:  time.Date(2026, 9, 26, 7, 50, 0, 0, time.UTC),
			Error: "需要 root",
		},
	}
}

func TestBoardRoundTrip(t *testing.T) {
	path := filepath.Join(t.TempDir(), "easysb-toolbox.json")
	want := sampleBoard()
	if err := SaveBoard(path, want); err != nil {
		t.Fatalf("SaveBoard: %v", err)
	}
	got := LoadBoard(path)
	if len(got) != len(want) {
		t.Fatalf("loaded %d records, stored %d", len(got), len(want))
	}
	media := got["unlock-media"]
	if media.ID != "unlock-media" || !media.When.Equal(want["unlock-media"].When) {
		t.Errorf("unlock-media came back as %+v", media)
	}
	if media.Result.Summary != want["unlock-media"].Result.Summary {
		t.Errorf("summary = %q, want %q", media.Result.Summary, want["unlock-media"].Result.Summary)
	}
	if len(media.Result.Rows) != 2 || media.Result.Rows[1][1] != "blocked" {
		t.Errorf("rows = %v, want the stored table", media.Result.Rows)
	}
	if len(media.Result.Notes) != 1 {
		t.Errorf("notes = %v, want one line", media.Result.Notes)
	}
	// A failed run keeps its reason: the board says 检测失败 and the report can say why.
	if got["backtrace"].Error != "需要 root" {
		t.Errorf("backtrace error = %q", got["backtrace"].Error)
	}
}

// TestUpdateBoardMergesConcurrentWriters is the board's version of the lost update:
// the panel and `--tool` add different records, and each write has to keep what the
// other committed rather than replace the whole file with its own copy.
func TestUpdateBoardMergesConcurrentWriters(t *testing.T) {
	path := filepath.Join(t.TempDir(), "easysb-toolbox.json")
	if err := SaveBoard(path, sampleBoard()); err != nil {
		t.Fatalf("SaveBoard: %v", err)
	}

	before := len(LoadBoard(path))
	ids := []string{"speed", "disk", "ping", "unlock-ai"}
	var wg sync.WaitGroup
	errs := make(chan error, len(ids))
	for _, id := range ids {
		wg.Add(1)
		go func(id string) {
			defer wg.Done()
			if err := UpdateBoard(path, func(board Board) {
				board[id] = Record{ID: id, When: time.Now()}
			}); err != nil {
				errs <- err
			}
		}(id)
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		t.Fatalf("UpdateBoard: %v", err)
	}

	got := LoadBoard(path)
	if want := before + len(ids); len(got) != want {
		t.Fatalf("records = %d, want %d: a concurrent write was lost", len(got), want)
	}
	for _, id := range ids {
		if got[id].ID != id {
			t.Errorf("record %q missing after concurrent updates", id)
		}
	}
}

func TestLoadBoardOfAMissingFileIsEmpty(t *testing.T) {
	board := LoadBoard(filepath.Join(t.TempDir(), "not-there.json"))
	if len(board) != 0 {
		t.Fatalf("expected an empty board, got %d records", len(board))
	}
}

func TestLoadBoardOfACorruptFileIsEmpty(t *testing.T) {
	path := filepath.Join(t.TempDir(), "easysb-toolbox.json")
	if err := os.WriteFile(path, []byte(`{"unlock-media": {"id": "unlock-media", "resu`), 0o600); err != nil {
		t.Fatal(err)
	}
	if board := LoadBoard(path); len(board) != 0 {
		t.Fatalf("a truncated file should load as empty, got %d records", len(board))
	}
}

// A record nothing can be matched to is not a result: the board is keyed by tool id and the
// menu only draws ids the registry knows.
func TestLoadBoardDropsRecordsWithoutAnID(t *testing.T) {
	path := filepath.Join(t.TempDir(), "easysb-toolbox.json")
	content := `{"": {"id": ""}, "speed-near": {"id": ""}}`
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	if board := LoadBoard(path); len(board) != 0 {
		t.Fatalf("expected no records, got %v", board)
	}
}

func TestSaveBoardCreatesItsDirectory(t *testing.T) {
	path := filepath.Join(t.TempDir(), "nested", "dir", "easysb-toolbox.json")
	if err := SaveBoard(path, sampleBoard()); err != nil {
		t.Fatalf("SaveBoard: %v", err)
	}
	if _, err := os.Stat(path); err != nil {
		t.Fatalf("the file should exist: %v", err)
	}
}

// Saving twice leaves no temporary file behind, and the second write wins.
func TestSaveBoardReplacesThePreviousBoard(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "easysb-toolbox.json")
	if err := SaveBoard(path, sampleBoard()); err != nil {
		t.Fatal(err)
	}
	second := Board{"ipquality": {ID: "ipquality", When: time.Now(), Result: Result{Summary: "机房"}}}
	if err := SaveBoard(path, second); err != nil {
		t.Fatal(err)
	}
	got := LoadBoard(path)
	if len(got) != 1 || got["ipquality"].Result.Summary != "机房" {
		t.Fatalf("the second save should have replaced the first: %v", got)
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	for _, entry := range entries {
		if entry.Name() != "easysb-toolbox.json" {
			t.Errorf("a temporary file was left behind: %s", entry.Name())
		}
	}
}
