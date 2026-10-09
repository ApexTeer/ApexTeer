package cmd

import (
	"context"
	"fmt"
	"os"

	"github.com/EasySBTeam/EasySB/internal/cert"
	"github.com/EasySBTeam/EasySB/internal/service"
	"github.com/EasySBTeam/EasySB/internal/subd"
)

// runRenewCerts is the entry point of the renewal timer: it renews the
// certificates the panel manages and then reloads the services that hold the old
// one open, so a renewal is actually served instead of only stored on disk.
func runRenewCerts() {
	ctx := context.Background()
	log := func(line string) { fmt.Println(line) }
	renewed, err := cert.Renew(ctx, log)
	// A renewal that changed nothing needs no reload: the core keeps running with
	// the certificate it already has. This timer runs every night, so restarting
	// the services unconditionally would drop every connection once a day.
	if len(renewed) == 0 {
		if err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(1)
		}
		fmt.Println("no certificate needed renewal")
		return
	}
	// A renewal overwrites the certificate files in place, so nothing in the config
	// on disk changes: only the services that are actually running have to restart
	// to pick the new pair up. Following the running services rather than the
	// deployed flag keeps this correct on a host whose node was deployed outside the
	// panel, where that flag is not set and a renewed certificate would otherwise
	// never be served.
	if service.Active(ctx) {
		if err := service.Do(ctx, "restart"); err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(1)
		}
	}
	if subd.Active(ctx) {
		if err := subd.Do(ctx, "restart"); err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(1)
		}
	}
	// A domain whose renewal failed does not hold back the ones that succeeded:
	// they were just reloaded, and the failure is still reported so the timer shows
	// up as failed in the journal.
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

// runRenewTimer installs or removes the renewal timer, the same work the domain
// screen offers. It is a command line mode because the unit names this binary's own
// path, so the binary has to be the thing that writes it: a headless or scripted
// setup has no panel to click, and a unit pointing at some other path renews nothing.
func runRenewTimer(install bool) {
	ctx := context.Background()
	log := func(line string) { fmt.Println(line) }
	if install {
		if err := cert.InstallTimer(ctx, log); err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(1)
		}
		if next := cert.TimerStatus(); next != "" {
			fmt.Println(next)
		}
		return
	}
	if err := cert.RemoveTimer(ctx, log); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
