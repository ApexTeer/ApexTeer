package ui

import (
	"strings"
	"testing"

	"charm.land/lipgloss/v2"

	"github.com/EasySBTeam/EasySB/internal/theme"
)

func testStyle() theme.Style {
	return theme.DefaultSkin().Style(true)
}

// A table is one line per row by contract, and every line has to fit the width it was
// given: that is what lets a tool report any number of rows inside a card without the
// screen planning a layout for it.
func TestTableFitsItsWidth(t *testing.T) {
	s := testStyle()
	rows := [][]Cell{
		{Text("ChatGPT"), Toned("解锁", KindOK), Text("US")},
		{Text("Amazon Prime Video"), Toned("不解锁", KindErr), Text("US")},
		{Text("Bilibili Taiwan"), Toned("未知", KindWarn), Text(strings.Repeat("很长的一段地区说明", 4))},
	}
	lines := Table(s, []string{"服务", "状态", "地区"}, rows, 60)
	if len(lines) != len(rows)+2 {
		t.Fatalf("got %d lines, want %d (header, rule, rows)", len(lines), len(rows)+2)
	}
	for i, line := range lines {
		if w := lipgloss.Width(line); w > 60 {
			t.Errorf("line %d is %d wide, over the 60 it was given: %q", i, w, line)
		}
		if strings.Contains(line, "\n") {
			t.Errorf("line %d wraps: %q", i, line)
		}
	}
	if !strings.Contains(lines[0], "服务") || !strings.Contains(lines[0], "状态") {
		t.Errorf("the header line names no column: %q", lines[0])
	}
	if !strings.Contains(lines[2], "解锁") {
		t.Errorf("the row lost its status: %q", lines[2])
	}
}

// A toned cell is coloured with escape sequences, which must not change the layout: the
// same table with and without tones occupies the same columns.
func TestTableTonesDoNotShiftColumns(t *testing.T) {
	s := testStyle()
	plain := Table(s, []string{"a", "b"}, [][]Cell{{Text("ChatGPT"), Text("US")}}, 40)
	toned := Table(s, []string{"a", "b"}, [][]Cell{{Text("ChatGPT"), Toned("US", KindOK)}}, 40)
	if len(plain) != len(toned) {
		t.Fatalf("line counts differ: %d vs %d", len(plain), len(toned))
	}
	for i := range plain {
		if lipgloss.Width(plain[i]) != lipgloss.Width(toned[i]) {
			t.Errorf("line %d: widths %d vs %d", i, lipgloss.Width(plain[i]), lipgloss.Width(toned[i]))
		}
	}
}

func TestTableWithoutHeadersAndTooNarrow(t *testing.T) {
	s := testStyle()
	rows := [][]Cell{{Text("a"), Text("1")}}
	if got := Table(s, nil, rows, 40); len(got) != 1 {
		t.Errorf("a headerless table should be its rows only, got %d lines", len(got))
	}
	if got := Table(s, []string{"a"}, rows, 4); got != nil {
		t.Errorf("a table narrower than the minimum should render nothing, got %v", got)
	}
	if got := Table(s, nil, nil, 40); got != nil {
		t.Errorf("a table with no columns should render nothing, got %v", got)
	}
}
