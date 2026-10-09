package panel

import (
	"context"
	"net/http"

	"github.com/EasySBTeam/EasySB/internal/toolbox"
	"github.com/EasySBTeam/EasySB/internal/toolbox/tools"
)

// toolboxEntry is one tool as the panel menu reads it. Only the stable ids cross the API;
// the browser words them, so a host and the menu share one registry and one vocabulary.
type toolboxEntry struct {
	ID    string `json:"id"`
	Group string `json:"group"`
}

// toolboxView is the whole toolbox in one response: the groups in menu order, every entry,
// and the board of the last run of each. The board rides along so opening the page is one
// request, not one per entry.
type toolboxView struct {
	Groups []string                  `json:"groups"`
	Tools  []toolboxEntry            `json:"tools"`
	Board  map[string]toolbox.Record `json:"board"`
}

// handleToolbox lists the registry and the stored board.
func (s *Service) handleToolbox(w http.ResponseWriter, r *http.Request) {
	view := toolboxView{
		Groups: tools.Groups(),
		Tools:  make([]toolboxEntry, 0, 16),
		Board:  toolbox.LoadBoard(tools.BoardPath()),
	}
	for _, tool := range tools.All() {
		view.Tools = append(view.Tools, toolboxEntry{ID: tool.ID, Group: tool.Group})
	}
	writeJSON(w, http.StatusOK, view)
}

// handleToolboxBoard returns just the stored board, which the page reloads after a run.
func (s *Service) handleToolboxBoard(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, toolbox.LoadBoard(tools.BoardPath()))
}

// handleToolboxRun runs one tool and records the outcome on the board. The run is bounded
// by the registry's own default timeout, and a failed run is still recorded so the board
// shows why rather than pretending the tool never ran.
func (s *Service) handleToolboxRun(w http.ResponseWriter, r *http.Request) {
	tool, ok := tools.Lookup(r.PathValue("id"))
	if !ok {
		writeError(w, http.StatusNotFound, "unknown tool")
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), toolbox.DefaultTimeout)
	defer cancel()
	opts := toolbox.Options{
		Timeout: toolbox.DefaultTimeout,
		Log:     toolbox.SafeLog(s.opts.Log),
	}
	result, runErr := tool.Run(ctx, opts)
	if err := tools.Record(tool.ID, result, runErr); err != nil {
		s.opts.Log("toolbox: record " + tool.ID + ": " + err.Error())
	}
	if runErr != nil {
		writeError(w, http.StatusBadGateway, runErr.Error())
		return
	}
	writeJSON(w, http.StatusOK, result)
}
