// Package service installs and controls the sing-box system service, supporting
// both systemd and OpenRC like the legacy shell implementation.
package service

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	"github.com/MinimaxFlora/EasySB/internal/sysinfo"
)

// ProjectHome is referenced in the generated unit for documentation.
const ProjectHome = "https://github.com/MinimaxFlora/EasySB"

// Manager identifies the init system in use.
type Manager string

// The init systems the panel can drive. Unknown means neither was found, which the
// callers treat as "leave the host alone" rather than as an error.
const (
	Systemd Manager = "systemd"
	OpenRC  Manager = "openrc"
	Unknown Manager = "unknown"
)

// Detect reports which init system is available.
func Detect() Manager {
	if _, err := os.Stat("/run/systemd/system"); err == nil {
		return Systemd
	}
	// A systemctl binary on PATH is not evidence that systemd is the running init:
	// containers, chroots and WSL ship the client without the daemon, and writing
	// units plus calling systemctl there fails at every step. Only the running
	// systemd's own runtime directory counts; everything else falls through to
	// Unknown, which callers treat as "leave the host alone".
	if _, err := exec.LookPath("rc-service"); err == nil {
		return OpenRC
	}
	return Unknown
}

// UnitPath returns where the service unit should live.
func UnitPath() string {
	if Detect() == OpenRC {
		return sysinfo.OpenRCUnit
	}
	return sysinfo.SystemdUnit
}

// PanelExecutable picks the path a unit should run. The installed panel wins over
// wherever this process happens to live: running a scratch copy once used to rewrite
// a unit to that copy's path, and removing the copy then left the service unable to
// start. A tree that was never installed still points at itself.
//
// Every unit the panel writes is this binary — the node is `easysb core run` and the
// subscription service is `easysb --serve` — so the one helper serves both.
func PanelExecutable() (string, error) {
	self, err := os.Executable()
	if err != nil {
		self = ""
	}
	picked := pickExecutable(self, sysinfo.PanelPaths)
	if picked == "" {
		return "", errors.New("cannot determine the panel executable")
	}
	return picked, nil
}

// pickExecutable returns the first candidate that exists and is executable, preferring
// the one this process is running from; self is the fallback when none is installed.
func pickExecutable(self string, candidates []string) string {
	installed := ""
	selfResolved := ""
	if self != "" {
		if resolved, err := filepath.EvalSymlinks(self); err == nil {
			selfResolved = resolved
		}
	}
	for _, candidate := range candidates {
		info, err := os.Stat(candidate)
		if err != nil || !executable(info) {
			continue
		}
		if installed == "" {
			installed = candidate
		}
		if selfResolved != "" {
			if resolved, err := filepath.EvalSymlinks(candidate); err == nil && resolved == selfResolved {
				return candidate
			}
		}
	}
	if installed != "" {
		return installed
	}
	return self
}

// executable reports whether a file may be run. Windows has no executable bit, so there
// any regular file counts; everywhere the panel installs to, the bit is what decides, so
// a half-written download is never mistaken for the installed panel.
func executable(info os.FileInfo) bool {
	if info.IsDir() {
		return false
	}
	if runtime.GOOS == "windows" {
		return true
	}
	return info.Mode()&0o111 != 0
}

// WriteUnit writes the service definition for the current manager. The unit runs this
// panel in node mode: the core is compiled into the binary, so `easysb core run -c
// <config>` is the node and no separate sing-box program exists to point at.
func WriteUnit() error {
	exe, err := PanelExecutable()
	if err != nil {
		return err
	}
	return writeNodeUnit(UnitPath(), exe, Detect())
}

// writeNodeUnit renders the node unit for one executable and writes it.
func writeNodeUnit(path, exe string, manager Manager) error {
	if manager == OpenRC {
		return os.WriteFile(path, []byte(UnitBody(exe, manager)), 0o755)
	}
	if err := os.WriteFile(path, []byte(UnitBody(exe, manager)), 0o644); err != nil {
		return err
	}
	return DaemonReload()
}

// UnitBody is the node unit text: this panel, in core mode, on the node's config. It is
// exported because the packaging targets write the very same text into the .deb, so the
// unit has one definition instead of a package copy that drifts from the runtime one.
// Keeping the text on its own also lets a test read what the unit will run without
// writing to a real unit directory.
func UnitBody(exe string, manager Manager) string {
	if manager == OpenRC {
		return fmt.Sprintf(`#!/sbin/openrc-run
name="sing-box"
description="sing-box service (EasySB)"
command="%s"
command_args="core run -c %s"
command_background=true
pidfile="/run/${RC_SVCNAME}.pid"
output_log="%s"
error_log="%s"
`, exe, sysinfo.ConfigJSON, sysinfo.LogFile, sysinfo.LogFile)
	}
	return fmt.Sprintf(`[Unit]
Description=sing-box service (EasySB)
Documentation=%s
After=network.target nss-lookup.target

[Service]
Type=simple
ExecStart=%s core run -c %s
Restart=on-failure
RestartSec=3
LimitNOFILE=infinity

[Install]
WantedBy=multi-user.target
`, ProjectHome, exe, sysinfo.ConfigJSON)
}

// RemoveUnit deletes the service definition for the current manager.
func RemoveUnit() error {
	if err := os.Remove(UnitPath()); err != nil && !os.IsNotExist(err) {
		return err
	}
	if Detect() == Systemd {
		return DaemonReload()
	}
	return nil
}

// DaemonReload refreshes the systemd unit cache.
func DaemonReload() error {
	if Detect() != Systemd {
		return nil
	}
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, "systemctl", "daemon-reload")
	cmd.Env = append(os.Environ(), "LC_ALL=C")
	out, err := cmd.CombinedOutput()
	if err != nil {
		return wrap("daemon-reload", out, err)
	}
	return nil
}

// Do performs a lifecycle action: start, stop, restart, enable or disable.
func Do(ctx context.Context, action string) error {
	var name string
	var args []string
	if Detect() == OpenRC {
		name = "rc-service"
		args = []string{sysinfo.ServiceName, action}
		if action == "enable" {
			name, args = "rc-update", []string{"add", sysinfo.ServiceName, "default"}
		} else if action == "disable" {
			name, args = "rc-update", []string{"del", sysinfo.ServiceName, "default"}
		}
	} else {
		name = "systemctl"
		args = []string{action, sysinfo.ServiceName}
	}

	cmd := exec.CommandContext(ctx, name, args...)
	cmd.Env = append(os.Environ(), "LC_ALL=C")
	out, err := cmd.CombinedOutput()
	if err != nil {
		return wrap(action, out, err)
	}
	return nil
}

// Active reports whether the sing-box service is currently running.
func Active(ctx context.Context) bool {
	if Detect() == OpenRC {
		cmd := exec.CommandContext(ctx, "rc-service", sysinfo.ServiceName, "status")
		return cmd.Run() == nil
	}
	cmd := exec.CommandContext(ctx, "systemctl", "is-active", "--quiet", sysinfo.ServiceName)
	return cmd.Run() == nil
}

func wrap(action string, out []byte, err error) error {
	msg := strings.TrimSpace(string(out))
	if msg == "" {
		msg = err.Error()
	}
	return errors.New(action + " " + sysinfo.ServiceName + ": " + msg)
}
