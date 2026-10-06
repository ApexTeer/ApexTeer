package bench

import (
	"context"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/EasySBTeam/EasySB/internal/toolbox"
)

// TestDefaultScaleSmoke runs the sizes the panel actually uses. It is skipped unless
// EASYSB_BENCH_SMOKE is set, because a full run writes half a gigabyte and takes minutes on
// a slow host: this is the one place the real workload is exercised, so a reviewer can see
// real numbers and check that Default() still fits inside toolbox.DefaultTimeout.
//
//	go test ./internal/toolbox/bench/... -run TestDefaultScaleSmoke -v -timeout 20m
func TestDefaultScaleSmoke(t *testing.T) {
	if os.Getenv("EASYSB_BENCH_SMOKE") == "" {
		t.Skip("set EASYSB_BENCH_SMOKE=1 to run the full-size workload")
	}
	opts := toolbox.Options{
		Scratch: t.TempDir(),
		Timeout: toolbox.DefaultTimeout,
		Log:     func(line string) { t.Log(line) },
	}
	cases := []struct {
		name string
		run  func(context.Context, toolbox.Options) (toolbox.Result, error)
	}{
		{"cpu", RunCPU},
		{"memory", RunMemory},
		{"disk", RunDisk},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			start := time.Now()
			res, err := c.run(context.Background(), opts)
			if err != nil {
				t.Fatalf("%s: %v", c.name, err)
			}
			elapsed := time.Since(start)
			if elapsed > toolbox.DefaultTimeout {
				t.Errorf("%s took %s, which is past the toolbox timeout of %s", c.name, elapsed, toolbox.DefaultTimeout)
			}
			t.Logf("%s summary: %s (workload ran for %s)", c.name, res.Summary, elapsed)
			for _, row := range res.Rows {
				t.Logf("  %s", strings.Join(row, " | "))
			}
			for _, note := range res.Notes {
				t.Logf("  note: %s", note)
			}
		})
	}
}
