package hw

import (
	"context"
	"fmt"
	"io"
	"io/fs"
	"net/http"
	"os/exec"
	"strings"
	"testing"
	"time"

	"github.com/EasySBTeam/EasySB/internal/toolbox"
)

// The fixtures below are what the tests read instead of a host: fixture text for /proc
// and /sys, canned command output, and a canned HTTP answer. Every path in them is the
// real path the collectors ask for, so a changed path shows up as a failed assertion
// rather than as a silently empty reading.

// fakeFS is a filesystem built from strings.
type fakeFS struct {
	files map[string]string
	dirs  map[string][]string
	links map[string]string
	stat  map[string][2]uint64
}

func newFakeFS() *fakeFS {
	return &fakeFS{
		files: map[string]string{},
		dirs:  map[string][]string{},
		links: map[string]string{},
		stat:  map[string][2]uint64{},
	}
}

func (f *fakeFS) withFile(path, content string) *fakeFS {
	f.files[path] = content
	return f
}

func (f *fakeFS) withDir(path string, names ...string) *fakeFS {
	f.dirs[path] = names
	return f
}

func (f *fakeFS) withLink(path, target string) *fakeFS {
	f.links[path] = target
	return f
}

// withStat registers the size and available bytes of a mount point.
func (f *fakeFS) withStat(path string, total, available uint64) *fakeFS {
	f.stat[path] = [2]uint64{total, available}
	return f
}

func (f *fakeFS) ReadFile(path string) ([]byte, error) {
	if v, ok := f.files[path]; ok {
		return []byte(v), nil
	}
	return nil, &fs.PathError{Op: "open", Path: path, Err: fs.ErrNotExist}
}

func (f *fakeFS) ReadDir(path string) ([]string, error) {
	if v, ok := f.dirs[path]; ok {
		return v, nil
	}
	return nil, &fs.PathError{Op: "open", Path: path, Err: fs.ErrNotExist}
}

func (f *fakeFS) Readlink(path string) (string, error) {
	if v, ok := f.links[path]; ok {
		return v, nil
	}
	return "", &fs.PathError{Op: "readlink", Path: path, Err: fs.ErrNotExist}
}

func (f *fakeFS) Statfs(path string) (uint64, uint64, error) {
	if v, ok := f.stat[path]; ok {
		return v[0], v[1], nil
	}
	return 0, 0, &fs.PathError{Op: "statfs", Path: path, Err: fs.ErrNotExist}
}

// cmdAnswer is one canned command result.
type cmdAnswer struct {
	out string
	err error
}

// fakeCmd is a commander with a script: commands it does not know are not installed,
// and commands without an answer fail the way a real broken one would.
type fakeCmd struct {
	present map[string]bool
	answers map[string]cmdAnswer
	// log records every command that was actually run, so a test can prove that a tool
	// the host does not have was never called.
	log []string
}

func newFakeCmd() *fakeCmd {
	return &fakeCmd{present: map[string]bool{}, answers: map[string]cmdAnswer{}}
}

func (c *fakeCmd) installed(names ...string) *fakeCmd {
	for _, name := range names {
		c.present[name] = true
	}
	return c
}

func (c *fakeCmd) answer(name string, args []string, out string, err error) *fakeCmd {
	c.answers[cmdKey(name, args)] = cmdAnswer{out: out, err: err}
	return c
}

func (c *fakeCmd) LookPath(name string) (string, error) {
	if c.present[name] {
		return "/usr/sbin/" + name, nil
	}
	return "", &exec.Error{Name: name, Err: exec.ErrNotFound}
}

func (c *fakeCmd) Run(_ context.Context, name string, args ...string) (string, error) {
	key := cmdKey(name, args)
	c.log = append(c.log, key)
	if a, ok := c.answers[key]; ok {
		return a.out, a.err
	}
	return "", fmt.Errorf("fake commander has no answer for %s", key)
}

func (c *fakeCmd) ran(key string) bool {
	for _, logged := range c.log {
		if strings.Contains(logged, key) {
			return true
		}
	}
	return false
}

func cmdKey(name string, args []string) string {
	return strings.Join(append([]string{name}, args...), " ")
}

// fakeClient answers one canned HTTP response, or fails.
type fakeClient struct {
	status int
	body   string
	err    error
	urls   []string
}

func (c *fakeClient) Do(req *http.Request) (*http.Response, error) {
	c.urls = append(c.urls, req.URL.String())
	if c.err != nil {
		return nil, c.err
	}
	status := c.status
	if status == 0 {
		status = http.StatusOK
	}
	return &http.Response{
		StatusCode: status,
		Header:     http.Header{"Content-Type": []string{"application/json"}},
		Body:       io.NopCloser(strings.NewReader(c.body)),
		Request:    req,
	}, nil
}

// fixedNow is the clock every test injects, so the collected-at row is deterministic.
var fixedNow = time.Date(2026, 9, 26, 12, 0, 0, 0, time.UTC)

// testOptions is the panel's side of the contract: an HTTP client, a clock and a log.
func testOptions(client toolbox.HTTPDoer) toolbox.Options {
	if client == nil {
		client = &fakeClient{status: http.StatusOK, body: ipAPISuccess}
	}
	return toolbox.Options{
		Client:  client,
		Timeout: 30 * time.Second,
		Log:     func(string) {},
		Clock:   func() time.Time { return fixedNow },
	}
}

const ipAPISuccess = `{"status":"success","query":"203.0.113.7","country":"美国",` +
	`"regionName":"加利福尼亚","city":"洛杉矶","isp":"Cloudflare, Inc.","org":"Cloudflare, Inc.",` +
	`"as":"AS13335 Cloudflare, Inc."}`

// cpuInfoFixture is a two-socket Xeon host: four records, four distinct
// physical-id/core-id pairs, four different sampled frequencies, and the hypervisor
// flag a guest carries.
const cpuInfoFixture = `processor	: 0
vendor_id	: GenuineIntel
cpu family	: 6
model		: 79
model name	: Intel(R) Xeon(R) CPU E5-2680 v4 @ 2.40GHz
physical id	: 0
siblings	: 2
core id		: 0
cpu cores	: 2
cpu MHz		: 2394.375
flags		: fpu vme de pse tsc msr pae mce cx8 apic sep mtrr pge mca cmov pat pse36 clflush
		dts acpi mmx fxsr sse sse2 ss ht tm pbe syscall nx pdpe1gb lm constant_tsc
		rep_good nopl xtopology cpuid pni pclmulqdq ssse3 fma cx16 pcid sse4_1
		sse4_2 x2apic movbe popcnt aes xsave osxsave avx f16c rdrand hypervisor

processor	: 1
model name	: Intel(R) Xeon(R) CPU E5-2680 v4 @ 2.40GHz
physical id	: 0
core id		: 1
cpu MHz		: 2394.375

processor	: 2
model name	: Intel(R) Xeon(R) CPU E5-2680 v4 @ 2.40GHz
physical id	: 1
core id		: 0
cpu MHz		: 2400.000

processor	: 3
model name	: Intel(R) Xeon(R) CPU E5-2680 v4 @ 2.40GHz
physical id	: 1
core id		: 1
cpu MHz		: 2599.999
`

// cpuInfoARM is an ARM record: no physical id, no core id, no cpu MHz, and a model name
// under a different key.
const cpuInfoARM = `processor	: 0
BogoMIPS	: 108.80
Features	: fp asimd evtstrm aes pmull sha1 sha2 crc32 cpuid
CPU implementer	: 0x41
CPU architecture: 8
model name	: ARMv8 Processor rev 1 (v8l)

processor	: 1
BogoMIPS	: 108.80
model name	: ARMv8 Processor rev 1 (v8l)
`

const memInfoFixture = `MemTotal:        8192000 kB
MemFree:          512000 kB
MemAvailable:    4096000 kB
Buffers:          128000 kB
Cached:          2048000 kB
SwapCached:            0 kB
SwapTotal:       2097152 kB
SwapFree:        2096128 kB
`

const osReleaseFixture = `NAME="Ubuntu"
VERSION="24.04.1 LTS (Noble Numbat)"
PRETTY_NAME="Ubuntu 24.04.1 LTS"
ID=ubuntu
VERSION_ID="24.04"
`

const dmiFixtureVendor = "QEMU\n"
const dmiFixtureProduct = "Standard PC (i440FX + PIIX, 1996)\n"

const timedatectlFixture = "Timezone=Asia/Shanghai\nNTP=yes\nNTPSynchronized=yes\n"

// cacheIndexes is the cache hierarchy of one Xeon core.
var cacheIndexes = []struct{ name, level, kind, size string }{
	{"index0", "1", "Data", "32K"},
	{"index1", "1", "Instruction", "32K"},
	{"index2", "2", "Unified", "256K"},
	{"index3", "3", "Unified", "35840K"},
}

// withCaches adds the cache hierarchy of cpu0.
func (f *fakeFS) withCaches() *fakeFS {
	names := make([]string, 0, len(cacheIndexes)+1)
	names = append(names, "uevent")
	for _, idx := range cacheIndexes {
		names = append(names, idx.name)
		base := pathCacheRoot + "/" + idx.name
		f.withFile(base+"/level", idx.level+"\n")
		f.withFile(base+"/type", idx.kind+"\n")
		f.withFile(base+"/size", idx.size+"\n")
	}
	return f.withDir(pathCacheRoot, names...)
}

// fullSystemFS is the "everything readable" host: an Ubuntu cloud image on KVM with two
// sockets, caches, swap and a timezone.
func fullSystemFS() *fakeFS {
	return newFakeFS().
		withCaches().
		withFile(pathCPUInfo, cpuInfoFixture).
		withFile(pathMemInfo, memInfoFixture).
		withFile(pathUptime, "987654.32 3950617.28\n").
		withFile(pathLoadAvg, "0.52 0.31 0.22 2/312 45678\n").
		withDir(pathProcDir, "1", "2", "312", "acpi", "buddyinfo", "self", "vmstat").
		withFile(pathHostname, "vps01\n").
		withFile(pathOSRelease, osReleaseFixture).
		withFile(pathKernel, "6.1.0-18-cloud-amd64\n").
		withFile(pathTimezone, "Asia/Shanghai\n").
		withFile(pathDMIVendor, dmiFixtureVendor).
		withFile(pathDMIProduct, dmiFixtureProduct)
}

// fullSystemCmd is the same host with both optional commands installed and answering.
func fullSystemCmd() *fakeCmd {
	return newFakeCmd().
		installed(toolVirt, toolTimeDateCt).
		answer(toolVirt, nil, "kvm\n", nil).
		answer(toolTimeDateCt, []string{"show", "--property=Timezone", "--property=NTP", "--property=NTPSynchronized"},
			timedatectlFixture, nil)
}

// findRow returns the cells of the first row whose first cell is key.
func findRow(res toolbox.Result, key string) ([]string, bool) {
	for _, row := range res.Rows {
		if len(row) > 0 && row[0] == key {
			return row, true
		}
	}
	return nil, false
}

// rowValue returns a two-column row's value.
func rowValue(res toolbox.Result, label string) (string, bool) {
	row, ok := findRow(res, label)
	if !ok || len(row) < 2 {
		return "", false
	}
	return row[1], true
}

// notesContain reports whether any note carries the substring.
func notesContain(res toolbox.Result, want string) bool {
	for _, n := range res.Notes {
		if strings.Contains(n, want) {
			return true
		}
	}
	return false
}

// checkShape asserts the table contract: the header count matches every row, no cell is
// empty, and the summary is one line.
func checkShape(t *testing.T, res toolbox.Result) {
	t.Helper()
	for i, row := range res.Rows {
		if len(row) != len(res.Headers) {
			t.Fatalf("row %d has %d cells, want %d (%v)", i, len(row), len(res.Headers), row)
		}
		for j, cell := range row {
			if strings.TrimSpace(cell) == "" {
				t.Fatalf("row %d cell %d (%s) is empty; a missing reading has to say so", i, j, res.Headers[j])
			}
		}
	}
	if res.Summary == "" {
		t.Fatal("summary is empty")
	}
	if strings.Contains(res.Summary, "\n") {
		t.Fatalf("summary is not one line: %q", res.Summary)
	}
}
