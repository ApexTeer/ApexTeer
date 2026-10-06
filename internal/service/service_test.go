package service

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/EasySB-Team/EasySB/internal/sysinfo"
)

// The node unit has to run the panel itself: the core is compiled into this binary, so
// there is no sing-box program to point at. A unit still saying `sing-box -c …` would
// look installed and never start.
func TestNodeUnitRunsThePanelAsTheNode(t *testing.T) {
	text := UnitBody("/usr/local/bin/easysb")
	if !strings.Contains(text, "/usr/local/bin/easysb") {
		t.Fatalf("the unit does not name the panel:\n%s", text)
	}
	if !strings.Contains(text, "core run -c "+sysinfo.ConfigJSON) {
		t.Fatalf("the unit does not start the node in core mode:\n%s", text)
	}
	// A unit that still names the old core binary would shadow the compiled-in
	// one, which is exactly the arrangement this release removes.
	if strings.Contains(text, sysinfo.WorkDir+"/sing-box") {
		t.Fatalf("the unit still points at a downloaded core:\n%s", text)
	}
}

// The node unit has to name the installed panel, not whatever copy happens to be
// running. A scratch copy that rewrote it once left the service unable to start when the
// copy was deleted.
func TestPickExecutable(t *testing.T) {
	dir := t.TempDir()
	installed := filepath.Join(dir, "easysb")
	scratch := filepath.Join(dir, "easysb-new")
	missing := filepath.Join(dir, "not-there")
	for _, path := range []string{installed, scratch} {
		if err := os.WriteFile(path, []byte("#!/bin/sh\n"), 0o755); err != nil {
			t.Fatalf("write %s: %v", path, err)
		}
	}

	cases := []struct {
		name       string
		self       string
		candidates []string
		want       string
	}{
		{"running from the installed panel", installed, []string{installed}, installed},
		{"running from a scratch copy", scratch, []string{installed}, installed},
		{"nothing installed yet", scratch, []string{missing}, scratch},
		{"self listed among the candidates wins", scratch, []string{missing, installed, scratch}, scratch},
		{"nothing installed and no self", "", []string{missing}, ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := pickExecutable(tc.self, tc.candidates); got != tc.want {
				t.Fatalf("pickExecutable(%q, %v) = %q, want %q", tc.self, tc.candidates, got, tc.want)
			}
		})
	}
}
