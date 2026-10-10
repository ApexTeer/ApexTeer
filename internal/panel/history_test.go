package panel

import (
	"net/http"
	"testing"
	"time"
)

// TestHistoryEndpointReportsTheTrend pins the shape the overview charts read: a
// shared time axis and the per-metric series beside it.
func TestHistoryEndpointReportsTheTrend(t *testing.T) {
	svc := New(testOptions(t))
	// One sample is enough for the endpoint contract; the rates need a second.
	svc.history.collect(time.Unix(1_700_000_000, 0))
	svc.history.collect(time.Unix(1_700_000_002, 0))

	rec := do(t, svc, http.MethodGet, "/api/v1/system/history", "", nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("history status: %d %s", rec.Code, rec.Body.String())
	}
	out := decodeBody(t, rec)
	for _, key := range []string{"intervalSecs", "capacity", "count", "times", "series"} {
		if _, ok := out[key]; !ok {
			t.Fatalf("history reply is missing %q: %s", key, rec.Body.String())
		}
	}
	if count, ok := out["count"].(float64); !ok || int(count) != 2 {
		t.Fatalf("count = %v, want 2", out["count"])
	}
	series, ok := out["series"].(map[string]any)
	if !ok {
		t.Fatalf("series is not an object: %s", rec.Body.String())
	}
	for _, key := range []string{"cpu", "mem", "swap", "disk", "netUp", "netDown", "tcp", "udp"} {
		arr, ok := series[key].([]any)
		if !ok {
			t.Fatalf("series.%s is not an array: %s", key, rec.Body.String())
		}
		if len(arr) != 2 {
			t.Fatalf("series.%s has %d points, want 2", key, len(arr))
		}
	}
}

// TestHistoryWindowTrimsToNewest checks that the window query keeps only the
// newest requested samples, so a caller can ask for a shorter chart.
func TestHistoryWindowTrimsToNewest(t *testing.T) {
	svc := New(testOptions(t))
	base := int64(1_700_000_000)
	for i := 0; i < 5; i++ {
		svc.history.collect(time.Unix(base+int64(i*2), 0))
	}
	rec := do(t, svc, http.MethodGet, "/api/v1/system/history?window=3", "", nil)
	out := decodeBody(t, rec)
	if count, ok := out["count"].(float64); !ok || int(count) != 3 {
		t.Fatalf("count = %v, want 3", out["count"])
	}
	times, ok := out["times"].([]any)
	if !ok || len(times) != 3 {
		t.Fatalf("times = %v, want 3 entries", out["times"])
	}
	if first, _ := times[0].(float64); int64(first) != base+4 {
		t.Fatalf("window kept the oldest sample: times = %v", times)
	}
}

// TestHistoryRatesNeedTwoSamples confirms the first sample reports no rate (there
// is nothing to subtract) and the second does, and that a rewound counter is
// treated as zero rather than a negative rate.
func TestHistoryRatesAndGuards(t *testing.T) {
	if got := cpuPercent(100, 200, 100, 200); got != 0 {
		t.Fatalf("cpuPercent with no elapsed jiffies = %v, want 0", got)
	}
	if got := cpuPercent(100, 200, 250, 400); got != 75 {
		t.Fatalf("cpuPercent = %v, want 75", got)
	}
	if got := ratioPercent(3, 0); got != 0 {
		t.Fatalf("ratioPercent with zero total = %v, want 0", got)
	}
	if got := ratioPercent(1, 2); got != 50 {
		t.Fatalf("ratioPercent = %v, want 50", got)
	}
	if got := counterRate(1000, 500, 1); got != 0 {
		t.Fatalf("counterRate on a rewound counter = %v, want 0", got)
	}
	if got := counterRate(1000, 3000, 2); got != 1000 {
		t.Fatalf("counterRate = %v, want 1000", got)
	}
}

// TestHistoryWindowIsBounded checks the recorder never grows past the window, so
// a long-lived panel does not hold an unbounded history.
func TestHistoryWindowIsBounded(t *testing.T) {
	h := newHistoryRecorder()
	for i := 0; i < historyWindow+20; i++ {
		h.collect(time.Unix(int64(i), 0))
	}
	if got := len(h.snapshot(0).Times); got != historyWindow {
		t.Fatalf("recorder kept %d samples, want %d", got, historyWindow)
	}
}
