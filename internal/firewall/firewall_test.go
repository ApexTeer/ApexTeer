package firewall

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/EasySB-Team/EasySB/internal/state"
)

func TestHopRange(t *testing.T) {
	cfg := state.Default()
	start, end, err := hopRange(cfg)
	if err != nil {
		t.Fatal(err)
	}
	if start != "2080" || end != "3000" {
		t.Fatalf("unexpected range %s:%s", start, end)
	}
}

func TestHopRangeInvalid(t *testing.T) {
	cfg := state.Default()
	cfg.HopRange = "3000"
	if _, _, err := hopRange(cfg); err == nil {
		t.Fatal("expected error for malformed hop range")
	}
}

func TestDetectNeverPanics(t *testing.T) {
	if got := Detect(); got != IPTables && got != NFTables && got != None {
		t.Fatalf("unexpected backend %q", got)
	}
}

// stubUnitAction replaces the init system's tool for one test.
func stubUnitAction(t *testing.T, out string, err error) {
	t.Helper()
	original := runUnitAction
	t.Cleanup(func() { runUnitAction = original })
	runUnitAction = func(context.Context, string, ...string) ([]byte, error) {
		return []byte(out), err
	}
}

func TestUnitActionReportsFailure(t *testing.T) {
	stubUnitAction(t, "Failed to enable unit: Unit file easysb-firewall.service does not exist.\n",
		errors.New("exit status 1"))

	err := UnitAction(context.Background(), "enable")
	if err == nil {
		t.Fatal("a failed unit action was reported as success")
	}
	// The panel shows this string to the operator, so it has to name the action, the
	// unit and what the tool actually said.
	for _, want := range []string{"enable", UnitName, "does not exist"} {
		if !strings.Contains(err.Error(), want) {
			t.Fatalf("error %q does not carry %q", err, want)
		}
	}
}

func TestUnitActionReportsSuccess(t *testing.T) {
	stubUnitAction(t, "", nil)

	if err := UnitAction(context.Background(), "disable"); err != nil {
		t.Fatalf("a unit action that succeeded returned %v", err)
	}
}

func TestUnitActionErrorFallsBackToTheExitStatus(t *testing.T) {
	// systemctl stays silent on some failures; the exit status is then all there is
	// to report, and an empty message would read as "no reason".
	err := unitActionError("enable", nil, errors.New("exit status 1"))
	if !strings.Contains(err.Error(), "exit status 1") {
		t.Fatalf("error %q dropped the exit status", err)
	}
}
