package sysinfo

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// ReadKeyValues is the only parser for /etc/sing-box/easysb.conf. internal/state
// loads the whole host configuration through it, and internal/panel reads its own
// file the same way, so a value it mangles is a deployment configured with the wrong
// domain, address or email. It had no test.

// writeConf plants a state file and returns its path.
func writeConf(t *testing.T, body string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "easysb.conf")
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

// TestReadKeyValuesSkipsWhatIsNotAKey covers the shapes an operator-edited or
// legacy-written file actually contains. A line that cannot be a key is passed over
// rather than making the whole file unreadable, which is what keeps one stray line
// from resetting every setting to its default.
func TestReadKeyValuesSkipsWhatIsNotAKey(t *testing.T) {
	path := writeConf(t, strings.Join([]string{
		"# EasySB state",
		"",
		"   ",
		"#REALITY_PRIVATE=\"hidden\"",
		"DOMAIN=\"example.com\"",
		"this line has no separator",
		"=\"a value with an empty key\"",
		"=nokey",
		"SERVER_IP=\"203.0.113.9\"",
		"",
	}, "\n"))

	got := ReadKeyValues(path)
	want := map[string]string{"DOMAIN": "example.com", "SERVER_IP": "203.0.113.9"}
	if len(got) != len(want) {
		t.Fatalf("ReadKeyValues = %v, want exactly %v", got, want)
	}
	for k, v := range want {
		if got[k] != v {
			t.Errorf("%s = %q, want %q", k, got[k], v)
		}
	}
	// A commented-out key must not be read: the file carries them deliberately.
	if _, ok := got["REALITY_PRIVATE"]; ok {
		t.Error("a commented-out key was parsed")
	}
}

// TestReadKeyValuesRoundTripsWhatTheWriterWrites is the property internal/state
// depends on and documents: the state file is written with %q, so the parser has to
// read it back with the matching rule. A plain Trim of the quotes - which is what the
// single-quoted and bare paths do - would corrupt any of these.
func TestReadKeyValuesRoundTripsWhatTheWriterWrites(t *testing.T) {
	// Exactly what state.Config.Save emits.
	values := map[string]string{
		"DOMAIN":          "example.com",
		"CERT_DOMAIN":     "",
		"ACME_EMAIL":      "ops@example.com",
		"QUOTED":          `a"b`,
		"BACKSLASHED":     `C:\path\to\thing`,
		"TRAILING_SLASH":  `ends\`,
		"UNICODE":         "域名.example",
		"TAB_AND_NEWLINE": "a\tb\nc",
		"SPACED":          "  padded  ",
	}

	var lines []string
	for k, v := range values {
		lines = append(lines, fmt.Sprintf("%s=%q", k, v))
	}
	got := ReadKeyValues(writeConf(t, strings.Join(lines, "\n")+"\n"))

	for k, want := range values {
		if got[k] != want {
			t.Errorf("%s round-tripped to %q, want %q", k, got[k], want)
		}
	}
}

// TestReadKeyValuesLongLineDoesNotTruncateTheFile pins the documented reason the
// scanner buffer is raised: bufio.Scanner's default limit is 64 KiB, and exceeding it
// stops the scan silently, so every key after the long line would be lost and the host
// would come up on defaults.
func TestReadKeyValuesLongLineDoesNotTruncateTheFile(t *testing.T) {
	long := strings.Repeat("x", 200*1024) // well past the default 64 KiB
	path := writeConf(t, "LONG=\""+long+"\"\n"+"AFTER_LONG=\"kept\"\n")

	got := ReadKeyValues(path)
	if got["AFTER_LONG"] != "kept" {
		t.Fatal("a line longer than the default scanner limit truncated the rest of the file")
	}
	if got["LONG"] != long {
		t.Fatalf("the long value came back at %d bytes, want %d", len(got["LONG"]), len(long))
	}
}

// TestReadKeyValuesLastDuplicateWins records which value a hand-edited file resolves
// to, because the answer decides whether a duplicate key silently overrides a setting.
func TestReadKeyValuesLastDuplicateWins(t *testing.T) {
	got := ReadKeyValues(writeConf(t, "DOMAIN=\"first.example\"\nDOMAIN=\"second.example\"\n"))
	if got["DOMAIN"] != "second.example" {
		t.Fatalf("DOMAIN = %q, want the last occurrence", got["DOMAIN"])
	}
}

// TestReadKeyValuesMissingFileIsEmpty pins that a host with no state file is not an
// error: a fresh deployment has none, and state.Load has to fall back to defaults.
func TestReadKeyValuesMissingFileIsEmpty(t *testing.T) {
	got := ReadKeyValues(filepath.Join(t.TempDir(), "absent.conf"))
	if len(got) != 0 {
		t.Fatalf("a missing file yielded %v, want nothing", got)
	}
}

// TestUnquoteValue covers the three value shapes directly, including the fallback for
// a double-quoted value that is not a valid Go literal - where the quotes are stripped
// rather than the value being dropped.
func TestUnquoteValue(t *testing.T) {
	cases := map[string]string{
		`"example.com"`:    "example.com",
		`"a\"b"`:           `a"b`,
		`"C:\\path"`:       `C:\path`,
		`"ends\\"`:         `ends\`,
		`""`:               "",
		`'single'`:         "single",
		`bare`:             "bare",
		`  "  spaced  "  `: "  spaced  ",
		`"unterminated`:    "unterminated",
		`"bad \q escape"`:  `bad \q escape`,
		`"only opening`:    "only opening",
		`'mixed"'`:         `mixed`,
		`"has'apostrophe"`: "has'apostrophe",
	}
	for in, want := range cases {
		if got := unquoteValue(in); got != want {
			t.Errorf("unquoteValue(%q) = %q, want %q", in, got, want)
		}
	}
}
