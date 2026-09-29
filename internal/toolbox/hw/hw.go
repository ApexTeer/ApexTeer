// Package hw reports what the machine under the panel is made of: one table for the
// system (CPU, caches, virtualization, memory, uptime, load, time, egress IP) and one
// for the block devices, including the power-on hours SMART reports when they are
// readable.
//
// The two tables are separate menu entries because they answer different questions and
// one of them can be slow: reading SMART means running smartctl once per device.
//
// Three rules shape both:
//
//   - Every reading goes through an interface. The filesystem and the external
//     commands come from an Env, the HTTP client from toolbox.Options, so the tests
//     drive every parser with fixture text and never depend on the host they run on.
//   - A value that could not be read is a note explaining why, never a zero. A host
//     without smartctl, a container whose root is an overlay, and a kernel that does
//     not report cpu MHz each say so instead of printing a number that looks measured.
//   - An external command is used only when it exists. A missing tool is a fact about
//     the host, not a failed reading.
package hw

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"time"

	"github.com/MinimaxFlora/EasySB/internal/toolbox"
)

// The toolbox entries this package implements. ID is the menu node id, the i18n key
// suffix ("toolbox_"+ID) and the --tool argument. The registry in internal/toolbox/tools
// is the list the menu, the board and --tool actually read; these values mirror the ones
// docs/toolbox.md and internal/i18n/table.go already carry, so the two cannot drift.
const (
	// ToolInfoID is the system information entry ("系统信息").
	ToolInfoID = "hw-info"
	// ToolDiskID is the block device entry ("硬盘信息"), SMART readings included.
	ToolDiskID = "hw-disk"
	// GroupHardware is the toolbox group both entries are filed under.
	GroupHardware = "hardware"
)

// Tools returns the entries this package contributes, in menu order. The registry may
// build the same entries itself; this exists so a registration can be a one-liner whose
// ids come from the package that implements them.
func Tools() []toolbox.Tool {
	return []toolbox.Tool{
		{ID: ToolInfoID, Group: GroupHardware, Run: SystemInfo},
		{ID: ToolDiskID, Group: GroupHardware, Run: Disks},
	}
}

// FS is the filesystem surface a collector reads. Paths are the absolute paths a Linux
// host exposes (/proc/..., /sys/...), so a test hands back fixture text instead of
// reading the development machine.
type FS interface {
	// ReadFile returns a file's contents.
	ReadFile(path string) ([]byte, error)
	// ReadDir lists the names in a directory.
	ReadDir(path string) ([]string, error)
	// Readlink resolves a symbolic link. The /sys/block entries are links and their
	// target names the driver behind the device.
	Readlink(path string) (string, error)
	// Statfs reports the total and available bytes of the filesystem holding a mount
	// point: the two numbers df prints as size and avail.
	Statfs(path string) (total, available uint64, err error)
}

// Commander is the external-command surface. A collector asks LookPath before it runs
// anything, so a host without smartctl is reported as a missing tool instead of a disk
// without a reading.
type Commander interface {
	// LookPath reports the absolute path of name, or an error when it is not installed.
	LookPath(name string) (string, error)
	// Run executes name with args and returns its combined output. A non-zero exit is
	// reported through the error with the output still returned.
	Run(ctx context.Context, name string, args ...string) (string, error)
}

// Env is the machine a collector reads: its files and its commands. toolbox.Options
// carries what the panel injects (HTTP client, clock, log, timeout); Env carries the
// host itself, which is the part a test replaces.
type Env struct {
	// FS reads the host's files.
	FS FS
	// Cmd runs the host's commands.
	Cmd Commander
}

// Host returns the Env that reads the machine the panel runs on.
func Host() Env { return Env{FS: osFS{}, Cmd: osCommander{}} }

// withDefaults fills anything the caller left nil, so a zero Env still reads the host.
func (e Env) withDefaults() Env {
	if e.FS == nil {
		e.FS = osFS{}
	}
	if e.Cmd == nil {
		e.Cmd = osCommander{}
	}
	return e
}

// SystemInfo reports the host's CPU, caches, virtualization, memory, uptime, load, time
// and egress IP as a two-column table.
func SystemInfo(ctx context.Context, opts toolbox.Options) (toolbox.Result, error) {
	return SystemInfoIn(ctx, opts, Host())
}

// SystemInfoIn is SystemInfo against an injected environment, which is how the tests
// drive it with fixtures. It never returns an error: a reading that failed is a note,
// because a table with an explained hole in it is worth more than no table at all.
func SystemInfoIn(ctx context.Context, opts toolbox.Options, env Env) (toolbox.Result, error) {
	h := &systemHost{env: env.withDefaults(), opts: opts}
	return h.collect(ctx), nil
}

// Disks lists the host's block devices with their size, kind, model, mounts, free
// space and, when SMART answers, their power-on hours.
func Disks(ctx context.Context, opts toolbox.Options) (toolbox.Result, error) {
	return DisksIn(ctx, opts, Host())
}

// DisksIn is Disks against an injected environment.
func DisksIn(ctx context.Context, opts toolbox.Options, env Env) (toolbox.Result, error) {
	h := &diskHost{env: env.withDefaults(), opts: opts}
	return h.collect(ctx), nil
}

// reading is one file read with its outcome, so a caller can put the text in a cell or
// the reason in a note without branching twice.
type reading struct {
	text string
	why  string
}

// readFile reads one path. A file that exists but is empty is reported as such: "the
// kernel said nothing" and "we could not look" are different facts about a host.
func readFile(fsys FS, path string) reading {
	data, err := fsys.ReadFile(path)
	if err != nil {
		return reading{why: fmt.Sprintf("无法读取 %s（%v）", path, err)}
	}
	text := strings.TrimSpace(string(data))
	if text == "" {
		return reading{why: path + " 没有内容"}
	}
	return reading{text: text}
}

// row puts one file reading in a row, or explains in a note why the row is not there.
// It returns the value so a caller can reuse it for the summary.
func (r reading) row(res *toolbox.Result, label string) string {
	if r.text == "" {
		res.Note("%s：%s", label, r.why)
		return ""
	}
	res.Add(label, r.text)
	return r.text
}

// runCommand runs an external command under its own timeout. Every call site bounds a
// hung binary this way instead of spending the whole tool budget on it.
func runCommand(ctx context.Context, cmd Commander, timeout time.Duration, name string, args ...string) (string, error) {
	cctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	return cmd.Run(cctx, name, args...)
}

// failureText is what a note says when a command failed: the tool's own first line when
// it printed one, the Go error otherwise. Command output is the honest part of a
// failure — "smartctl: command not found" and "Device does not support SMART" are
// different problems and the operator needs to see which one they have.
func failureText(out string, err error) string {
	if line := firstLine(out); line != "" {
		return line
	}
	if err != nil {
		return err.Error()
	}
	return "没有输出"
}

// firstLine returns the first non-empty trimmed line of text, capped so one very long
// line cannot push the rest of the notes off the screen.
func firstLine(text string) string {
	for _, line := range strings.Split(text, "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		const maxLine = 200
		if len(line) > maxLine {
			return line[:maxLine] + "…"
		}
		return line
	}
	return ""
}

// nonEmpty drops the empty strings from a list, so a value assembled from several files
// does not grow separators for the ones that were missing.
func nonEmpty(values ...string) []string {
	out := make([]string, 0, len(values))
	for _, v := range values {
		if v = strings.TrimSpace(v); v != "" {
			out = append(out, v)
		}
	}
	return out
}

// osFS reads the host the panel runs on.
type osFS struct{}

func (osFS) ReadFile(path string) ([]byte, error) { return os.ReadFile(path) }

func (osFS) ReadDir(path string) ([]string, error) {
	entries, err := os.ReadDir(path)
	if err != nil {
		return nil, err
	}
	names := make([]string, 0, len(entries))
	for _, e := range entries {
		names = append(names, e.Name())
	}
	return names, nil
}

func (osFS) Readlink(path string) (string, error) { return os.Readlink(path) }

// osCommander runs the host's commands. LC_ALL=C keeps the parsers away from a
// translated output a locale would produce.
type osCommander struct{}

func (osCommander) LookPath(name string) (string, error) { return exec.LookPath(name) }

func (osCommander) Run(ctx context.Context, name string, args ...string) (string, error) {
	cmd := exec.CommandContext(ctx, name, args...)
	cmd.Env = append(os.Environ(), "LC_ALL=C")
	out, err := cmd.CombinedOutput()
	return string(out), err
}
