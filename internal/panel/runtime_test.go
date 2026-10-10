package panel

import (
	"net/http"
	"testing"
)

// TestRuntimeReportsTheProcessFootprint pins the read-only endpoint the
// performance baseline is read from: it answers with the counters that do not
// depend on the host, so a dashboard can poll it for a regression signal.
func TestRuntimeReportsTheProcessFootprint(t *testing.T) {
	svc := New(testOptions(t))

	rec := do(t, svc, http.MethodGet, "/api/v1/system/runtime", "", nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("runtime status: %d %s", rec.Code, rec.Body.String())
	}
	out := decodeBody(t, rec)
	for _, key := range []string{"uptimeSecs", "connections", "goroutines", "gcCount", "rssBytes", "mem"} {
		if _, ok := out[key]; !ok {
			t.Fatalf("runtime reply is missing %q: %s", key, rec.Body.String())
		}
	}
	if g, ok := out["goroutines"].(float64); !ok || g < 1 {
		t.Fatalf("goroutines = %v, want at least the running test's one", out["goroutines"])
	}
	mem, ok := out["mem"].(map[string]any)
	if !ok {
		t.Fatalf("mem is not an object: %s", rec.Body.String())
	}
	if _, ok := mem["heapBytes"]; !ok {
		t.Fatalf("mem is missing heapBytes: %s", rec.Body.String())
	}
}
