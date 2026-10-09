package panel

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"strings"

	"github.com/EasySBTeam/EasySB/internal/service"
)

// UnitBody is the panel service unit text. It runs the same EasySB binary in
// panel mode, so the panel needs no separate program and its version always
// matches the binary. It is exported so `easysb --print-unit panel` and the
// packaging can write the very same text.
func UnitBody(exe string) string {
	return fmt.Sprintf(`[Unit]
Description=EasySB Web management panel
Documentation=%s
After=network.target nss-lookup.target

[Service]
Type=simple
ExecStart=%s panel
Restart=on-failure
RestartSec=5
LimitNOFILE=infinity

[Install]
WantedBy=multi-user.target
`, service.ProjectHome, exe)
}

// daemonReload is a seam for tests: writing a unit must not require a running
// systemd on the build host.
var daemonReload = service.DaemonReload

// WriteUnit installs the panel service unit pointing at the installed binary.
func WriteUnit() error {
	exe, err := service.PanelExecutable()
	if err != nil {
		return err
	}
	if err := os.MkdirAll("/etc/systemd/system", 0o755); err != nil {
		return err
	}
	if err := os.WriteFile(UnitPath, []byte(UnitBody(exe)), 0o644); err != nil {
		return err
	}
	return daemonReload()
}

// RemoveUnit deletes the panel service unit. It only touches the panel unit: the
// core, the subscription service, the node store, the account store and the
// certificates are left exactly as they were.
func RemoveUnit() error {
	if err := os.Remove(UnitPath); err != nil && !os.IsNotExist(err) {
		return err
	}
	return service.DaemonReload()
}

// Installed reports whether the panel unit file exists.
func Installed() bool {
	info, err := os.Stat(UnitPath)
	return err == nil && !info.IsDir()
}

// Do performs a lifecycle action on the panel service.
func Do(ctx context.Context, action string) error {
	if _, err := exec.LookPath("systemctl"); err != nil {
		return errors.New("systemctl not available: " + err.Error())
	}
	out, err := exec.CommandContext(ctx, "systemctl", action, ServiceName).CombinedOutput()
	if err != nil {
		msg := strings.TrimSpace(string(out))
		if msg == "" {
			msg = err.Error()
		}
		return errors.New(action + " " + ServiceName + ": " + msg)
	}
	return nil
}

// Active reports whether the panel service is running.
func Active(ctx context.Context) bool {
	return exec.CommandContext(ctx, "systemctl", "is-active", "--quiet", ServiceName).Run() == nil
}

// Enabled reports whether the panel service starts at boot.
func Enabled(ctx context.Context) bool {
	return exec.CommandContext(ctx, "systemctl", "is-enabled", "--quiet", ServiceName).Run() == nil
}
