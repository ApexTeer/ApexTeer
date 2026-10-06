package bench

import (
	"context"
	"strings"
	"testing"

	"github.com/EasySBTeam/EasySB/internal/toolbox"
)

const cpuInfoX86 = `processor	: 0
vendor_id	: GenuineIntel
cpu family	: 6
model		: 85
model name	: Intel(R) Xeon(R) Platinum 8269CY CPU @ 2.50GHz
stepping	: 7
cpu MHz		: 2499.998
cache size	: 36608 KB
flags		: fpu vme de pse

processor	: 1
vendor_id	: GenuineIntel
model name	: Intel(R) Xeon(R) Platinum 8269CY CPU @ 2.50GHz
flags		: fpu vme de pse
`

const cpuInfoARM64 = `processor	: 0
BogoMIPS	: 50.00
Features	: fp asimd evtstrm aes pmull sha1 sha2 crc32
CPU implementer	: 0x41
CPU architecture: 8
CPU variant	: 0x0
CPU part	: 0xd0c
CPU revision	: 1

processor	: 1
BogoMIPS	: 50.00
Features	: fp asimd evtstrm aes pmull sha1 sha2 crc32
Hardware	: Ampere(R) Altra(R) Processor
`

const cpuInfoARM32 = `Processor	: ARMv7 Processor rev 5 (v7l)
processor	: 0
BogoMIPS	: 38.40
Hardware	: Generic DT based system

processor	: 1
BogoMIPS	: 38.40
Hardware	: Generic DT based system
`

func TestParseCPUInfo(t *testing.T) {
	cases := []struct {
		name      string
		data      string
		wantModel string
		wantCores int
	}{
		{"x86", cpuInfoX86, "Intel(R) Xeon(R) Platinum 8269CY CPU @ 2.50GHz", 2},
		{"arm64 hardware line", cpuInfoARM64, "Ampere(R) Altra(R) Processor", 2},
		{"armv7 processor line", cpuInfoARM32, "ARMv7 Processor rev 5 (v7l)", 2},
		{"armv7 name after the cores", "processor	: 0\nprocessor	: 1\nProcessor	: ARMv8 Processor rev 0 (v8l)\n", "ARMv8 Processor rev 0 (v8l)", 2},
		{"loongarch cpu model", "processor	: 0\ncpu model	: Loongson-3A5000\n", "Loongson-3A5000", 1},
		{"empty", "", "", 0},
		{"garbage", "not a cpuinfo at all\n\n", "", 0},
		{"whitespace collapsed", "processor : 0\nmodel name :  AMD EPYC   7763  64-Core\n", "AMD EPYC 7763 64-Core", 1},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := ParseCPUInfo([]byte(c.data))
			if got.Model != c.wantModel {
				t.Errorf("Model = %q, want %q", got.Model, c.wantModel)
			}
			if got.Cores != c.wantCores {
				t.Errorf("Cores = %d, want %d", got.Cores, c.wantCores)
			}
		})
	}
}

// TestRunCPUSmall runs the real suite on a few megabytes. It asserts shape and units, and
// that the numbers are measurements rather than zeroes; the absolute score is a property
// of the machine the test runs on, so nothing here compares it with a constant.
func TestRunCPUSmall(t *testing.T) {
	cases := []struct {
		name    string
		workers int
	}{
		{"two workers", 2},
		{"one worker", 1},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			s := smallScale()
			s.CPUWorkers = c.workers
			var logs []string
			res, err := RunCPUWith(context.Background(), toolbox.Options{
				Log: func(line string) { logs = append(logs, line) },
			}, s)
			if err != nil {
				t.Fatalf("RunCPUWith: %v", err)
			}
			if len(res.Rows) != 5 {
				t.Fatalf("got %d rows, want 5 (two scores and three workloads): %v", len(res.Rows), res.Rows)
			}
			wantHeaders := []string{"Item", "Score", "Notes"}
			for i, h := range wantHeaders {
				if res.Headers[i] != h {
					t.Errorf("header %d = %q, want %q", i, res.Headers[i], h)
				}
			}
			labels := []string{"Single core", "Multi core", "SHA-256", "Prime sieve", "Float loop"}
			units := []string{"Mops/s", "Mops/s", unitMBps, "Mops/s", "MFLOP/s"}
			for i, row := range res.Rows {
				if row[0] != labels[i] {
					t.Errorf("row %d label = %q, want %q", i, row[0], labels[i])
				}
				v, unit := scoreOf(t, row[1])
				if v <= 0 {
					t.Errorf("row %d (%s) score = %v, want a positive measurement", i, row[0], v)
				}
				if unit != units[i] {
					t.Errorf("row %d (%s) unit = %q, want %q", i, row[0], unit, units[i])
				}
				if strings.TrimSpace(row[2]) == "" {
					t.Errorf("row %d (%s) has no note explaining the number", i, row[0])
				}
			}
			if len(res.Notes) < 4 {
				t.Errorf("got %d notes, want the measurement basis spelled out", len(res.Notes))
			}
			if !notesContain(res, "sysbench") || !notesContain(res, "fixed") {
				t.Errorf("notes must say the workload is the panel's own and fixed: %v", res.Notes)
			}
			if !notesContain(res, "logical processors") && !notesContain(res, "CPU model unknown") {
				t.Errorf("notes must report the CPU either way: %v", res.Notes)
			}
			if !strings.Contains(res.Summary, "single") || !strings.Contains(res.Summary, "multi") {
				t.Errorf("summary %q must carry both scores", res.Summary)
			}
			if len(logs) == 0 {
				t.Error("no progress was logged; the panel has nothing to show while this runs")
			}
		})
	}
}

// TestRunCPUIsRepeatable checks the accumulator half of the repeatability claim: the same
// fixed workload must produce the same checksum on every run, otherwise two runs were not
// measuring the same work and their scores cannot be compared.
func TestRunCPUIsRepeatable(t *testing.T) {
	checks := func() []string {
		res, err := RunCPUWith(context.Background(), toolbox.Options{}, smallScale())
		if err != nil {
			t.Fatalf("RunCPUWith: %v", err)
		}
		var out []string
		for _, row := range res.Rows[2:] {
			parts := strings.Split(row[2], "; ")
			out = append(out, parts[len(parts)-1])
		}
		return out
	}
	first, second := checks(), checks()
	for i := range first {
		if first[i] != second[i] {
			t.Errorf("workload %d checksum changed between runs: %q then %q", i, first[i], second[i])
		}
	}
}

// TestSieveKnownCounts is the arithmetic behind the sieve row, checked against values
// anyone can look up, so a broken sieve cannot quietly change the workload's meaning.
func TestSieveKnownCounts(t *testing.T) {
	cases := []struct {
		limit int
		want  int
	}{
		{2, 1},
		{10, 4},
		{100, 25},
		{1000, 168},
		{100000, 9592},
		{1_000_000, 78498},
	}
	for _, c := range cases {
		if got := sieve(c.limit); got != c.want {
			t.Errorf("sieve(%d) = %d, want %d", c.limit, got, c.want)
		}
	}
}
