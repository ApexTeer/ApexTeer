package cert

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"time"

	"github.com/MinimaxFlora/EasySB/internal/service"
)

// The renewal timer is what renews the certificates: no crontab is installed and
// nothing else on the host renews them. A minimal server image has no cron at all,
// and a container has no init to run it, so the panel drives renewal the same way
// it drives everything else: a systemd unit or an OpenRC init script.
const (
	systemdServicePath = "/etc/systemd/system/easysb-acme.service"
	systemdTimerPath   = "/etc/systemd/system/easysb-acme.timer"
	openRCPath         = "/etc/init.d/easysb-acme"
)

// renewTimerUnit is the daily timer that runs the renewal pass.
const renewTimerUnit = `[Unit]
Description=EasySB certificate renewal timer

[Timer]
OnCalendar=daily
RandomizedDelaySec=6h
Persistent=true

[Install]
WantedBy=timers.target
`

// renewServiceUnit runs one renewal pass. --renew-certs also restarts the services
// that hold the certificate open, so a renewed certificate actually takes effect.
const renewServiceUnit = `[Unit]
Description=EasySB certificate renewal
After=network-online.target sing-box.service
Wants=network-online.target

[Service]
Type=oneshot
ExecStart=%s --renew-certs
`

// renewOpenRC is the OpenRC equivalent: a daily cron-style entry that runs the
// same command.
const renewOpenRC = `#!/sbin/openrc-run
name="easysb-acme"
description="EasySB certificate renewal"

depend() {
  need net
}

start() {
  ebegin "Renewing EasySB certificates"
  %s --renew-certs
  eend $?
}
`

// TimerInstalled reports whether the renewal timer is on disk.
func TimerInstalled() bool {
	for _, p := range []string{systemdTimerPath, openRCPath} {
		if _, err := os.Stat(p); err == nil {
			return true
		}
	}
	return false
}

// InstallTimer writes the renewal unit and enables it. It is idempotent: issuing
// a second certificate simply rewrites the same files.
func InstallTimer(ctx context.Context, log func(string)) error {
	exe, err := service.PanelExecutable()
	if err != nil {
		return err
	}
	if service.Detect() == service.OpenRC {
		if err := os.WriteFile(openRCPath, []byte(fmt.Sprintf(renewOpenRC, exe)), 0o755); err != nil {
			return err
		}
		log("$ rc-update add easysb-acme default")
		return runQuiet(ctx, "rc-update", "add", "easysb-acme", "default")
	}

	if err := os.WriteFile(systemdServicePath, []byte(fmt.Sprintf(renewServiceUnit, exe)), 0o644); err != nil {
		return err
	}
	if err := os.WriteFile(systemdTimerPath, []byte(renewTimerUnit), 0o644); err != nil {
		return err
	}
	if err := service.DaemonReload(); err != nil {
		return err
	}
	log("$ systemctl enable --now easysb-acme.timer")
	return runQuiet(ctx, "systemctl", "enable", "--now", "easysb-acme.timer")
}

// RemoveTimer deletes the renewal unit.
func RemoveTimer(ctx context.Context, log func(string)) error {
	if service.Detect() == service.OpenRC {
		if err := runQuiet(ctx, "rc-update", "del", "easysb-acme", "default"); err != nil {
			return err
		}
		if err := os.Remove(openRCPath); err != nil && !os.IsNotExist(err) {
			return err
		}
		return nil
	}
	log("$ systemctl disable --now easysb-acme.timer")
	if err := runQuiet(ctx, "systemctl", "disable", "--now", "easysb-acme.timer"); err != nil {
		return err
	}
	for _, p := range []string{systemdTimerPath, systemdServicePath} {
		if err := os.Remove(p); err != nil && !os.IsNotExist(err) {
			return err
		}
	}
	return service.DaemonReload()
}

// TimerStatus is the next scheduled renewal as systemd reports it, empty when the
// timer is absent or systemd cannot say.
func TimerStatus() string {
	if service.Detect() == service.OpenRC {
		return ""
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	out, err := exec.CommandContext(ctx, "systemctl", "list-timers", "easysb-acme.timer", "--no-pager").Output()
	if err != nil {
		return ""
	}
	return parseTimerStatus(string(out))
}

// parseTimerStatus reads the NEXT and LEFT columns of a list-timers row. The
// column offsets come from the header line rather than from counting fields, for
// two reasons: NEXT is a five-field timestamp once it is expanded, and an empty
// column is a dash instead of a missing field, so field counting drifts.
func parseTimerStatus(out string) string {
	var lines []string
	for _, line := range strings.Split(out, "\n") {
		if strings.TrimSpace(line) != "" {
			lines = append(lines, line)
		}
	}
	if len(lines) == 0 {
		return ""
	}
	header := lines[0]
	nextAt := strings.Index(header, "NEXT")
	leftAt := strings.Index(header, "LEFT")
	if nextAt < 0 || leftAt <= nextAt {
		return ""
	}
	// LEFT ends where the next column begins. That column differs between
	// systemd versions (LAST is not always printed), so take the nearest one.
	leftEnd := -1
	for _, column := range []string{"LAST", "PASSED", "UNIT"} {
		if at := strings.Index(header, column); at > leftAt && (leftEnd < 0 || at < leftEnd) {
			leftEnd = at
		}
	}
	for _, line := range lines[1:] {
		if !strings.Contains(line, "easysb-acme.timer") || len(line) <= leftAt {
			continue
		}
		next := strings.TrimSpace(line[nextAt:leftAt])
		left := line[leftAt:]
		if leftEnd > leftAt && leftEnd <= len(line) {
			left = line[leftAt:leftEnd]
		}
		left = strings.TrimSpace(left)
		// systemd writes a dash for a column it has nothing to say about.
		if left == "-" {
			left = ""
		}
		if next == "" {
			return ""
		}
		if left == "" {
			return next
		}
		return next + " (" + left + ")"
	}
	return ""
}

func runQuiet(ctx context.Context, name string, args ...string) error {
	cmd := exec.CommandContext(ctx, name, args...)
	out, err := cmd.CombinedOutput()
	if err != nil {
		msg := strings.TrimSpace(string(out))
		if msg == "" {
			msg = err.Error()
		}
		return fmt.Errorf("%s %s: %s", name, strings.Join(args, " "), msg)
	}
	return nil
}
