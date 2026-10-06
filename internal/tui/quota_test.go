package tui

import (
	"strings"
	"testing"

	"charm.land/bubbletea/v2"

	"github.com/EasySBTeam/EasySB/internal/i18n"
	"github.com/EasySBTeam/EasySB/internal/theme"
)

func TestQuotaFieldsRoundTrip(t *testing.T) {
	cases := []struct {
		gb, mb string
		bytes  int64
	}{
		{"0", "0", 0},
		{"10", "500", 10<<30 + 500<<20},
		{"1024", "0", 1024 << 30},
		{"0", "1023", 1023 << 20},
	}
	for _, c := range cases {
		got, err := parseSizeFields(c.gb, c.mb, i18n.Chinese)
		if err != nil || got != c.bytes {
			t.Fatalf("parseSizeFields(%q,%q) = %d, %v; want %d", c.gb, c.mb, got, err, c.bytes)
		}
		gb, mb := splitSize(c.bytes)
		if gb != c.gb || mb != c.mb {
			t.Fatalf("splitSize(%d) = %q/%q, want %q/%q", c.bytes, gb, mb, c.gb, c.mb)
		}
	}
}

func TestQuotaFieldsRejectOutOfRange(t *testing.T) {
	for _, c := range [][2]string{{"1025", "0"}, {"10", "1024"}, {"-1", "0"}, {"x", "0"}} {
		if _, err := parseSizeFields(c[0], c[1], i18n.Chinese); err == nil {
			t.Fatalf("parseSizeFields(%q,%q) accepted an out-of-range pair", c[0], c[1])
		}
	}
}

// TestDualFormKeepsUnitsAdjacent guards the layout: sizing the two fields from the
// card width pushed "GB" to the far edge and ran the MB field off the line, so the
// pair has to stay compact whatever the card offers.
func TestDualFormKeepsUnitsAdjacent(t *testing.T) {
	f := newDualForm("quota", "prompt", "10", "500", "",
		func(*App, string, string) (tea.Cmd, error) { return nil, nil })
	body := f.body(theme.Dark(), 100)
	if len(body) < 2 {
		t.Fatalf("dual form drew no field line: %#v", body)
	}
	line := stripANSI(body[1])
	if !strings.Contains(line, "GB") || !strings.Contains(line, "MB") {
		t.Fatalf("field line lost a unit: %q", line)
	}
	if w := len([]rune(line)); w > 24 {
		t.Fatalf("field line is %d cells wide, want a compact pair: %q", w, line)
	}
}
