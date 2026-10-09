package cmd

import (
	"fmt"
	"os"

	"github.com/EasySBTeam/EasySB/internal/panel"
	"github.com/EasySBTeam/EasySB/internal/service"
	"github.com/EasySBTeam/EasySB/internal/subd"
)

// runPrintUnit writes a service unit body to stdout. The .deb is assembled from this
// output, so the unit shipped in the package is the same text the panel writes at
// runtime and there is no second copy to drift.
func runPrintUnit(kind, exe string) {
	switch kind {
	case "node":
		fmt.Print(service.UnitBody(exe))
	case "sub":
		fmt.Print(subd.UnitBody(exe))
	case "panel":
		fmt.Print(panel.UnitBody(exe))
	default:
		fmt.Fprintf(os.Stderr, "unknown unit %q: use node, sub or panel\n", kind)
		os.Exit(2)
	}
}
