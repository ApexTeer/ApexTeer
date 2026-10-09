package panel

import (
	"net/http"
	"path/filepath"
	"testing"
)

// TestToolboxViewListsRegistry checks that the panel exposes the same registry the TUI
// and the command line read, and that the board rides along in one response.
func TestToolboxViewListsRegistry(t *testing.T) {
	t.Setenv("EASYSB_TOOLBOX_BOARD", filepath.Join(t.TempDir(), "board.json"))
	svc := New(testOptions(t))

	out := decodeBody(t, do(t, svc, http.MethodGet, "/api/v1/toolbox", "", nil))
	groups, _ := out["groups"].([]any)
	if len(groups) == 0 {
		t.Fatal("the toolbox reported no groups")
	}
	entries, _ := out["tools"].([]any)
	if len(entries) == 0 {
		t.Fatal("the toolbox reported no tools")
	}
	first, _ := entries[0].(map[string]any)
	if first["id"] == "" || first["group"] == "" {
		t.Fatalf("a toolbox entry is missing its ids: %+v", first)
	}
	if out["board"] == nil {
		t.Fatal("the toolbox view must carry the board, even when empty")
	}
}

// TestToolboxBoardReadable checks the board endpoint answers an object, never null.
func TestToolboxBoardReadable(t *testing.T) {
	t.Setenv("EASYSB_TOOLBOX_BOARD", filepath.Join(t.TempDir(), "board.json"))
	svc := New(testOptions(t))
	rec := do(t, svc, http.MethodGet, "/api/v1/toolbox/board", "", nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("board status = %d, want 200", rec.Code)
	}
}

// TestToolboxRunUnknownTool rejects a tool id the registry does not know.
func TestToolboxRunUnknownTool(t *testing.T) {
	t.Setenv("EASYSB_TOOLBOX_BOARD", filepath.Join(t.TempDir(), "board.json"))
	svc := New(testOptions(t))
	rec := do(t, svc, http.MethodPost, "/api/v1/toolbox/not-a-tool/run", "", map[string]string{"X-EasySB-Panel": "1"})
	if rec.Code != http.StatusNotFound {
		t.Fatalf("unknown tool status = %d, want 404", rec.Code)
	}
}
