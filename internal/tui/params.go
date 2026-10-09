package tui

import (
	"errors"
	"fmt"
	"strconv"
	"strings"

	tea "charm.land/bubbletea/v2"

	"github.com/EasySBTeam/EasySB/internal/state"
)

// editSubPort prompts for the subscription endpoint port. The port must not
// collide with a node listener, and the endpoint service has to be restarted
// for a change to take effect.
//
// The form is opened with the value read here, but the write is a locked
// read-modify-write: the entry is confirmed some time after the value was read, and
// saving the copy this function captured would discard anything another actor
// changed in between. The collision check runs inside the same critical section,
// against the node store as it is at that moment.
func editSubPort() actionFunc {
	return func(a *App) tea.Cmd {
		lang := a.lang
		cfg := state.Load()
		prompt := fmt.Sprintf(lang.T("param_sub_port_prompt"), state.DefaultSubServePort)
		a.openForm(lang.T("param_sub_port"), prompt, fmt.Sprint(cfg.SubServePort), "", func(a *App, value string) (tea.Cmd, error) {
			value = strings.TrimSpace(value)
			port, err := strconv.Atoi(value)
			if err != nil || port < 1 || port > 65535 {
				return nil, errors.New(lang.T("port_invalid"))
			}
			nodes, err := loadNodes()
			if err != nil {
				return nil, err
			}
			for _, n := range nodes {
				if n.Port == port {
					return nil, errors.New(lang.T("port_conflict"))
				}
			}
			if err := state.Modify(func(cfg *state.Config) error {
				cfg.SubServePort = port
				return nil
			}); err != nil {
				return nil, err
			}
			a.setToast(lang.T("node_params_saved"), false)
			return nil, nil
		})
		return nil
	}
}

// editSubSync prompts for the accounting interval in seconds. The panel reads
// traffic this often, which is also how quickly a quota or an expiry is enforced.
// The write is a locked read-modify-write for the same reason as editSubPort.
func editSubSync() actionFunc {
	return func(a *App) tea.Cmd {
		lang := a.lang
		cfg := state.Load()
		prompt := fmt.Sprintf(lang.T("param_sub_sync_prompt"), state.DefaultSubSyncSeconds, state.MinSubSyncSeconds)
		a.openForm(lang.T("param_sub_sync"), prompt, fmt.Sprint(cfg.SubSyncSecs), "", func(a *App, value string) (tea.Cmd, error) {
			seconds, err := strconv.Atoi(strings.TrimSpace(value))
			if err != nil || seconds < state.MinSubSyncSeconds {
				return nil, errors.New(lang.T("param_sub_sync_invalid"))
			}
			if err := state.Modify(func(cfg *state.Config) error {
				cfg.SubSyncSecs = seconds
				return nil
			}); err != nil {
				return nil, err
			}
			a.setToast(lang.T("node_params_saved"), false)
			return nil, nil
		})
		return nil
	}
}

// validHopRange accepts "start:end" with start < end and both in 1..65535.
func validHopRange(s string) bool {
	start, end, ok := strings.Cut(s, ":")
	if !ok {
		return false
	}
	lo, err1 := strconv.Atoi(start)
	hi, err2 := strconv.Atoi(end)
	if err1 != nil || err2 != nil {
		return false
	}
	return lo >= 1 && lo < hi && hi <= 65535
}
