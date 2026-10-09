package panel

import (
	"net/http"
	"os"
	"strconv"
	"strings"

	"github.com/EasySBTeam/EasySB/internal/service"
	"github.com/EasySBTeam/EasySB/internal/subd"
	"github.com/EasySBTeam/EasySB/internal/sysinfo"
)

// handleSystem reports the host snapshot plus the state of the three services the
// panel cares about: the node, the subscription endpoint and the panel itself.
func (s *Service) handleSystem(w http.ResponseWriter, r *http.Request) {
	status := sysinfo.Collect(s.opts.Version)
	writeJSON(w, http.StatusOK, map[string]any{
		"host": status,
		"services": map[string]any{
			"core": map[string]any{
				"name":    sysinfo.ServiceName,
				"active":  service.Active(r.Context()),
				"enabled": serviceEnabled(r.Context(), sysinfo.ServiceName),
			},
			"subscription": map[string]any{
				"name":      sysinfo.SubServiceName,
				"active":    subd.Active(r.Context()),
				"enabled":   serviceEnabled(r.Context(), sysinfo.SubServiceName),
				"installed": subdInstalled(),
				"unit":      subd.UnitPath(),
			},
			"panel": map[string]any{
				"name":    ServiceName,
				"active":  Active(r.Context()),
				"enabled": Enabled(r.Context()),
				"unit":    UnitPath,
			},
		},
	})
}

// handleNetwork reports the cumulative interface and disk counters, so the
// dashboard can poll for a live rate without paying for a full host snapshot.
// The four are totals since boot; a rate is the difference between two samples.
func (s *Service) handleNetwork(w http.ResponseWriter, r *http.Request) {
	rx, tx := sysinfo.NetworkTotals()
	read, write := sysinfo.DiskTotals()
	writeJSON(w, http.StatusOK, map[string]uint64{
		"rxBytes":    rx,
		"txBytes":    tx,
		"readBytes":  read,
		"writeBytes": write,
	})
}

// handleSubscriptionServiceAction manages the built-in subscription service.
func (s *Service) handleSubscriptionServiceAction(w http.ResponseWriter, r *http.Request) {
	action := strings.TrimSpace(r.PathValue("action"))
	switch action {
	case "install":
		if err := subd.WriteUnit(); err != nil {
			writeError(w, http.StatusUnprocessableEntity, err.Error())
			return
		}
		if err := subd.Do(r.Context(), "enable"); err != nil {
			writeError(w, http.StatusUnprocessableEntity, err.Error())
			return
		}
	case "uninstall":
		if err := subd.Do(r.Context(), "disable"); err != nil {
			s.opts.Log("subscription service disable: " + err.Error())
		}
		if err := subd.RemoveUnit(); err != nil {
			writeError(w, http.StatusUnprocessableEntity, err.Error())
			return
		}
	case "start", "stop", "restart", "enable", "disable":
		if err := subd.Do(r.Context(), action); err != nil {
			writeError(w, http.StatusUnprocessableEntity, err.Error())
			return
		}
	default:
		writeError(w, http.StatusNotFound, "unknown subscription service action")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"ok":        true,
		"installed": subdInstalled(),
		"active":    subd.Active(r.Context()),
	})
}

// subdInstalled reports whether the subscription unit file is on disk.
func subdInstalled() bool {
	info, err := os.Stat(subd.UnitPath())
	return err == nil && !info.IsDir()
}

// handleLogs returns the tail of one service's log file. The panel writes its own
// log file; the node and the subscription service log through journald or their
// own files.
func (s *Service) handleLogs(w http.ResponseWriter, r *http.Request) {
	which := strings.TrimSpace(r.URL.Query().Get("service"))
	lines := 200
	if n, err := strconv.Atoi(r.URL.Query().Get("lines")); err == nil && n > 0 && n <= 2000 {
		lines = n
	}
	var path string
	switch which {
	case "core":
		path = sysinfo.LogFile
	case "subscription":
		path = sysinfo.SubLogFile
	case "panel", "":
		path = LogFile
	default:
		badRequest(w, "service must be core, subscription or panel")
		return
	}
	out, err := tailLines(path, lines, 2<<20)
	if err != nil {
		writeJSON(w, http.StatusOK, map[string]any{
			"service": which,
			"path":    path,
			"lines":   []string{},
			"note":    "log file is not readable: " + err.Error(),
		})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"service": which,
		"path":    path,
		"lines":   out,
	})
}
