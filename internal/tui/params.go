package tui

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"strings"

	tea "charm.land/bubbletea/v2"

	"github.com/MinimaxFlora/EasySB/internal/secret"
	"github.com/MinimaxFlora/EasySB/internal/state"
)

// editSubPort prompts for the subscription endpoint port. The port must not
// collide with a protocol listener, and the endpoint service has to be
// restarted for a change to take effect.
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
			for _, key := range state.Keys {
				if cfg.Ports[key] == value {
					return nil, errors.New(lang.T("port_conflict"))
				}
			}
			cfg.SubServePort = port
			if err := cfg.Save(); err != nil {
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
			cfg.SubSyncSecs = seconds
			if err := cfg.Save(); err != nil {
				return nil, err
			}
			a.setToast(lang.T("node_params_saved"), false)
			return nil, nil
		})
		return nil
	}
}

// editHop prompts for the Hysteria2 port-hopping range.
func editHop() actionFunc {
	return func(a *App) tea.Cmd {
		lang := a.lang
		cfg := state.Load()
		prompt := fmt.Sprintf(lang.T("param_hop_prompt"), state.DefaultHopRange)
		a.openForm(lang.T("param_hop"), prompt, cfg.HopRange, "", func(a *App, value string) (tea.Cmd, error) {
			value = strings.TrimSpace(value)
			if value == "" {
				value = state.DefaultHopRange
			}
			if !validHopRange(value) {
				return nil, errors.New(lang.T("param_invalid_range"))
			}
			cfg.HopRange = value
			if err := cfg.Save(); err != nil {
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

// editPort prompts for a single protocol port, rejecting conflicts.
func editPort(proto string) actionFunc {
	return func(a *App) tea.Cmd {
		lang := a.lang
		cfg := state.Load()
		prompt := fmt.Sprintf(lang.T("param_port_prompt"), cfg.Ports[proto])
		a.openForm(state.Labels[proto], prompt, cfg.Ports[proto], "", func(a *App, value string) (tea.Cmd, error) {
			value = strings.TrimSpace(value)
			if value == "" {
				value = cfg.Ports[proto]
			}
			n, err := strconv.Atoi(value)
			if err != nil || n < 1 || n > 65535 {
				return nil, errors.New(lang.T("port_invalid"))
			}
			for _, k := range state.Keys {
				if k != proto && cfg.Ports[k] == value {
					return nil, errors.New(lang.T("port_conflict"))
				}
			}
			cfg.Ports[proto] = value
			if err := cfg.Save(); err != nil {
				return nil, err
			}
			a.setToast(lang.T("node_params_saved"), false)
			return nil, nil
		})
		return nil
	}
}

// setSNI applies a preset handshake domain immediately.
func setSNI(preset string) actionFunc {
	return func(a *App) tea.Cmd {
		lang := a.lang
		cfg := state.Load()
		cfg.RealitySNI = preset
		if err := cfg.Save(); err != nil {
			a.setToast(err.Error(), true)
			return nil
		}
		a.setToast(lang.T("param_sni")+" = "+preset, false)
		return nil
	}
}

// editSNI prompts for a custom handshake domain.
func editSNI() actionFunc {
	return func(a *App) tea.Cmd {
		lang := a.lang
		cfg := state.Load()
		current := cfg.RealitySNI
		if current == "" {
			current = state.DefaultSNI
		}
		prompt := fmt.Sprintf(lang.T("param_sni_prompt"), current)
		a.openForm(lang.T("param_sni"), prompt, current, "", func(a *App, value string) (tea.Cmd, error) {
			value = strings.TrimSpace(value)
			if value == "" {
				value = current
			}
			cfg.RealitySNI = value
			if err := cfg.Save(); err != nil {
				return nil, err
			}
			a.setToast(lang.T("node_params_saved"), false)
			return nil, nil
		})
		return nil
	}
}

// regenRealityKeys regenerates the Reality keypair. The keypair is X25519, so the panel
// generates it itself: there is no core process to ask for one.
func regenRealityKeys() actionFunc {
	return func(a *App) tea.Cmd {
		lang := a.lang
		return a.startTask(lang.T("param_privkey"), func(_ context.Context, r *taskReporter) error {
			priv, pub := secret.RealityKeypair()
			if priv == "" || pub == "" {
				return errors.New(lang.T("param_key_fail"))
			}
			cfg := state.Load()
			cfg.RealityPriv = priv
			cfg.RealityPub = pub
			if err := cfg.Save(); err != nil {
				return err
			}
			r.Log(lang.T("param_regen_privkey"))
			return nil
		})
	}
}

// regenShortID regenerates the Reality short id.
func regenShortID() actionFunc {
	return func(a *App) tea.Cmd {
		lang := a.lang
		cfg := state.Load()
		cfg.RealitySID = secret.ShortID()
		if err := cfg.Save(); err != nil {
			a.setToast(err.Error(), true)
			return nil
		}
		a.setToast(lang.T("param_regen_shortid"), false)
		return collectStatus(a.scriptVersion)
	}
}

// toggleProtocol flips one protocol's enabled flag.
func toggleProtocol(proto string) actionFunc {
	return func(a *App) tea.Cmd {
		cfg := state.Load()
		cfg.Enabled[proto] = !cfg.Enabled[proto]
		if err := cfg.Save(); err != nil {
			a.setToast(err.Error(), true)
			return nil
		}
		return nil
	}
}
