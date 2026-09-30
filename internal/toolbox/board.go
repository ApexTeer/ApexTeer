package toolbox

import (
	"encoding/json"
	"os"
	"path/filepath"
	"time"

	"github.com/MinimaxFlora/EasySB/internal/filelock"
)

// BoardEnv names the environment variable that moves the stored board. It exists for the
// tests and for a second panel on the same host, the way the interface preferences file has
// its own override.
const BoardEnv = "EASYSB_TOOLBOX_BOARD"

// Board is what the panel wrote down about the last run of each tool entry, keyed by tool id.
//
// It exists so the 工具箱's 看板 survives a restart. A result costs a traceroute, a disk
// benchmark or a set of unlock probes, and an operator comparing two hosts — or coming back
// the next morning — should not have to run everything again because the panel was closed in
// between. The board is a cache of measurements, never a source of truth: a tool that has not
// run in this session still says 尚未检测 when its record is missing.
type Board map[string]Record

// Record is one stored run. The table is kept whole rather than as a summary line, so the
// report screen can be reopened after a restart without running the tool again.
type Record struct {
	ID     string    `json:"id"`
	When   time.Time `json:"when"`
	Result Result    `json:"result"`
	// Error is what the run failed with, rendered by the tool that failed. An empty string
	// means the run produced a table.
	Error string `json:"error,omitempty"`
}

// LoadBoard reads the stored board.
//
// A missing, unreadable or corrupt file is an empty board and never an error: the board is a
// convenience, and a panel that refused to draw its toolbox because a cache file was
// truncated would be trading a working screen for a stale one. Records without an id are
// dropped, because a record nothing can be matched to is not a result.
func LoadBoard(path string) Board {
	board := Board{}
	data, err := os.ReadFile(path)
	if err != nil {
		return board
	}
	var stored Board
	if err := json.Unmarshal(data, &stored); err != nil {
		return board
	}
	for id, record := range stored {
		if id == "" || record.ID == "" {
			continue
		}
		board[id] = record
	}
	return board
}

// UpdateBoard runs fn over the stored board while holding the board's lock, then
// saves the result. The panel and `--tool` both read-modify-write this file, often
// on the same host, so the whole cycle runs under one lock: a run recorded by one
// process is seen by the other instead of being overwritten by a copy read before it.
func UpdateBoard(path string, fn func(Board)) error {
	lock, err := filelock.Acquire(path)
	if err != nil {
		return err
	}
	defer lock.Unlock()
	board := LoadBoard(path)
	fn(board)
	return SaveBoard(path, board)
}

// SaveBoard writes the board atomically: a temporary file beside it, then a rename, so a
// panel that dies mid-write leaves the previous board rather than half of a new one.
func SaveBoard(path string, board Board) error {
	if dir := filepath.Dir(path); dir != "" {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return err
		}
	}
	data, err := json.MarshalIndent(board, "", "  ")
	if err != nil {
		return err
	}
	tmp, err := os.CreateTemp(filepath.Dir(path), ".easysb-toolbox-*")
	if err != nil {
		return err
	}
	name := tmp.Name()
	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		os.Remove(name)
		return err
	}
	if err := tmp.Close(); err != nil {
		os.Remove(name)
		return err
	}
	if err := os.Chmod(name, 0o600); err != nil {
		os.Remove(name)
		return err
	}
	if err := os.Rename(name, path); err != nil {
		os.Remove(name)
		return err
	}
	return nil
}
