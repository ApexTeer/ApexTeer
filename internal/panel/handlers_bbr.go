package panel

import (
	"net/http"
	"strings"

	"github.com/EasySBTeam/EasySB/internal/bbr"
)

// handleBBR reports the machine's congestion-control state without touching the
// network: which algorithm is active, which ones the running kernel offers, and
// whether a BBRv3 kernel from the upstream project is installed. Enabling BBR in
// this kernel and installing that kernel are separate actions, so the page can
// show the state even when the version lookup would fail.
func (s *Service) handleBBR(w http.ResponseWriter, r *http.Request) {
	status := bbr.LocalStatus(r.Context())
	writeJSON(w, http.StatusOK, map[string]any{
		"enabled":      status.Enabled(),
		"congestion":   status.Congestion,
		"available":    status.Available,
		"qdisc":        status.Qdisc,
		"running":      status.Running,
		"arch":         status.Arch,
		"supported":    status.Supported(),
		"kernels":      emptyIfNil(status.Kernels),
		"customKernel": status.CustomKernel(),
		"needsReboot":  status.NeedsReboot(),
		"qdiscs":       bbr.Qdiscs,
	})
}

// handleBBREnable switches the running kernel to BBR with the requested queue
// discipline. The qdisc is validated against bbr.Qdiscs, so a request cannot
// persist an arbitrary value into the drop-in.
func (s *Service) handleBBREnable(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Qdisc string `json:"qdisc"`
	}
	if err := decodeJSON(r, &body); err != nil {
		badRequest(w, "invalid request body: "+err.Error())
		return
	}
	qdisc := strings.TrimSpace(body.Qdisc)
	if qdisc == "" {
		qdisc = "fq"
	}
	if !containsString(bbr.Qdiscs, qdisc) {
		badRequest(w, "qdisc must be one of "+strings.Join(bbr.Qdiscs, ", "))
		return
	}
	if err := bbr.Enable(r.Context(), s.opts.Log, qdisc); err != nil {
		writeError(w, http.StatusBadGateway, err.Error())
		return
	}
	status := bbr.LocalStatus(r.Context())
	writeJSON(w, http.StatusOK, map[string]any{
		"ok":         true,
		"enabled":    status.Enabled(),
		"congestion": status.Congestion,
		"qdisc":      status.Qdisc,
	})
}

// handleBBRClear removes the drop-ins EasySB wrote and restores the values it
// replaced, leaving hand-written configuration elsewhere on the host alone.
func (s *Service) handleBBRClear(w http.ResponseWriter, r *http.Request) {
	cleared, err := bbr.Clear(r.Context(), s.opts.Log)
	if err != nil {
		writeError(w, http.StatusBadGateway, err.Error())
		return
	}
	status := bbr.LocalStatus(r.Context())
	writeJSON(w, http.StatusOK, map[string]any{
		"ok":         true,
		"cleared":    cleared,
		"enabled":    status.Enabled(),
		"congestion": status.Congestion,
		"qdisc":      status.Qdisc,
	})
}

func containsString(list []string, want string) bool {
	for _, item := range list {
		if item == want {
			return true
		}
	}
	return false
}
