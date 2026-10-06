package subd

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"strings"

	"github.com/MinimaxFlora/EasySB/internal/service"
	"github.com/MinimaxFlora/EasySB/internal/sysinfo"
)

// UnitPath returns where the subscription service unit should live.
func UnitPath() string {
	return sysinfo.SubSystemdUnit
}

// WriteUnit installs the unit that keeps the endpoint running. It runs this same
// binary with --serve, so the endpoint needs no separate program.
func WriteUnit() error {
	exe, err := service.PanelExecutable()
	if err != nil {
		return err
	}
	return writeUnit(UnitPath(), exe)
}

// daemonReload is the reload the init system needs after a unit changes. It is a variable so
// that a test can write a unit file without touching the machine it runs on: what such a test
// is about is the file, and on a CI runner there is no systemd at PID 1 to answer — the
// command comes back with an authentication error that has nothing to do with the unit that
// was written.
var daemonReload = service.DaemonReload

func writeUnit(path, exe string) error {
	if err := os.WriteFile(path, []byte(UnitBody(exe)), 0o644); err != nil {
		return err
	}
	return daemonReload()
}

// UnitBody is the subscription unit text for one executable. It is exported because the
// packaging targets write the very same text into the .deb, so the unit has one
// definition instead of a package copy that drifts from the runtime one.
func UnitBody(exe string) string {
	return fmt.Sprintf(`[Unit]
Description=EasySB subscription service
Documentation=%s
After=network.target nss-lookup.target
Wants=sing-box.service

[Service]
Type=simple
ExecStart=%s --serve
Restart=always
RestartSec=5
LimitNOFILE=infinity

[Install]
WantedBy=multi-user.target
`, service.ProjectHome, exe)
}

// RemoveUnit deletes the unit and forgets the service.
func RemoveUnit() error {
	if err := os.Remove(UnitPath()); err != nil && !os.IsNotExist(err) {
		return err
	}
	return service.DaemonReload()
}

// Do starts, stops, restarts, enables or disables the subscription service.
func Do(ctx context.Context, action string) error {
	if _, err := exec.LookPath("systemctl"); err != nil {
		return errors.New("systemctl not available: " + err.Error())
	}
	out, err := exec.CommandContext(ctx, "systemctl", action, sysinfo.SubServiceName).CombinedOutput()
	if err != nil {
		return wrap(action, out, err)
	}
	return nil
}

// Active reports whether the subscription service is running.
func Active(ctx context.Context) bool {
	return exec.CommandContext(ctx, "systemctl", "is-active", "--quiet", sysinfo.SubServiceName).Run() == nil
}

func wrap(action string, out []byte, err error) error {
	msg := strings.TrimSpace(string(out))
	if msg == "" {
		msg = err.Error()
	}
	return errors.New(action + " " + sysinfo.SubServiceName + ": " + msg)
}
