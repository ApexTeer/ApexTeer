package cmd

import (
	"context"
	"fmt"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/EasySBTeam/EasySB/internal/panel"
)

// runPanel starts the Web management panel. It shares every business package with
// the TUI, so a change made from the browser and a change made from the terminal
// leave the same files and the same core configuration behind. The panel is its
// own process and its own systemd unit; it never takes the node down with it.
func runPanel() {
	migrateStores()
	// A first run (or a cleared password hash) has no admin credential yet. Create
	// one and show it on the console. It is printed here rather than through the
	// logger, so the plaintext password never lands in the panel log file.
	if password, err := panel.EnsureConfig(panel.ConfigPath); err != nil {
		fmt.Fprintln(os.Stderr, "panel: "+err.Error())
		os.Exit(1)
	} else if password != "" {
		fmt.Println("panel initial administrator: admin")
		fmt.Println("panel initial password: " + password)
		fmt.Println("change it in the panel Settings page, or via POST /api/v1/auth/password, after the first login")
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	logf := func(line string) {
		stamped := time.Now().Format(time.RFC3339) + " " + line
		fmt.Println(stamped)
		appendPanelLog(stamped)
	}
	if err := panel.Run(ctx, panel.Options{
		Version: resolveVersion(),
		Log:     logf,
	}); err != nil {
		fmt.Fprintln(os.Stderr, "panel: "+err.Error())
		os.Exit(1)
	}
}

// appendPanelLog appends one line to the panel's own log file, best effort: a host
// that runs the panel under journald still gets the console output, and a host
// that does not keeps a file to read.
func appendPanelLog(line string) {
	f, err := os.OpenFile(panel.LogFile, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600)
	if err != nil {
		return
	}
	defer f.Close()
	_, _ = f.WriteString(line + "\n")
}
