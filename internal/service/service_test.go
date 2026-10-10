package service

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/EasySBTeam/EasySB/internal/sysinfo"
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

// TestWrapKeepsTheReasonTheActionFailed covers the error a failed systemctl call
// produces, which is the only thing an operator sees: every caller in the panel, the
// TUI and the first-deploy path reports it verbatim.
//
// The commands run with LC_ALL=C so this text is in the C locale and stable, and the
// output fallback is what makes a failure that prints nothing - a timeout, or
// systemd's start-limit latch - still name a reason rather than ending at the colon.
func TestWrapKeepsTheReasonTheActionFailed(t *testing.T) {
	cause := errors.New("exit status 1")

	withOutput := wrap("restart", []byte("Job for sing-box.service failed.\n"), cause).Error()
	for _, want := range []string{"restart", sysinfo.ServiceName, "Job for sing-box.service failed."} {
		if !strings.Contains(withOutput, want) {
			t.Errorf("wrap() = %q, missing %q", withOutput, want)
		}
	}
	// The output is trimmed: a trailing newline would split a log line.
	if strings.Contains(withOutput, "\n") {
		t.Errorf("wrap() kept a newline: %q", withOutput)
	}

	// No output: the underlying error is the only reason there is.
	silent := wrap("start", nil, cause).Error()
	if !strings.Contains(silent, "exit status 1") || !strings.Contains(silent, "start") {
		t.Errorf("wrap() dropped the cause when systemctl printed nothing: %q", silent)
	}

	// Whitespace-only output is treated as no output.
	blank := wrap("stop", []byte("  \n\t\n"), cause).Error()
	if !strings.Contains(blank, "exit status 1") {
		t.Errorf("wrap() did not fall back to the cause for blank output: %q", blank)
	}
}

// TestDoAndActiveFailClosedWithoutSystemd pins the behaviour on a host that has the
// systemctl binary but no systemd running as init - the arrangement in a container,
// and the one this repository is developed in. The status question answers "not
// running", which is what makes a caller take the install-and-start branch instead of
// restarting something that is not there.
//
// The commands carry their own deadline (commandTimeout for Do, a shorter one for
// Active), but that path cannot be exercised here: with no systemd bus the call fails
// immediately rather than blocking, so the ceiling is verified by inspection only.
func TestDoAndActiveFailClosedWithoutSystemd(t *testing.T) {
	if Detect() != Systemd {
		t.Skip("systemctl is not on PATH, so there is nothing to fail against")
	}
	// A lifecycle action against a bus that is not there is an error, not a silent
	// success: pretending to have restarted would leave the caller believing it.
	if err := Do(context.Background(), "restart"); err == nil {
		t.Error("Do reported success without a systemd to talk to")
	}
	// The status question must never report a service it could not see.
	if Active(context.Background()) {
		t.Fatal("Active reported a running service although systemd could not be reached")
	}
}

// TestDaemonReloadIsSkippedWithoutSystemd pins that the no-op gate comes before any
// command is run: on a host without systemd, WriteUnit must not fail because the
// daemon reload could not be performed.
func TestDaemonReloadIsSkippedWithoutSystemd(t *testing.T) {
	if Detect() == Systemd {
		t.Skip("this host has systemd, so the skip branch is not taken")
	}
	if err := DaemonReload(); err != nil {
		t.Fatalf("DaemonReload on a non-systemd host = %v, want nil", err)
	}
}
