package tui

import (
	"strings"

	"github.com/EasySB-Team/EasySB/internal/sysinfo"
)

// enabledPorts joins the listening ports of enabled protocols for the status
// board.
func enabledPorts(ports []sysinfo.PortInfo) string {
	var out []string
	for _, p := range ports {
		if p.Enabled && p.Port != "" {
			out = append(out, p.Port)
		}
	}
	return strings.Join(out, " ")
}
