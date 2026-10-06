package tui

import (
	"context"
	"errors"
	"fmt"
	"strings"

	tea "charm.land/bubbletea/v2"

	"github.com/EasySB-Team/EasySB/internal/cert"
	"github.com/EasySB-Team/EasySB/internal/deploy"
	"github.com/EasySB-Team/EasySB/internal/i18n"
	"github.com/EasySB-Team/EasySB/internal/netutil"
	"github.com/EasySB-Team/EasySB/internal/service"
	"github.com/EasySB-Team/EasySB/internal/state"
	"github.com/EasySB-Team/EasySB/internal/subd"
	"github.com/EasySB-Team/EasySB/internal/sysinfo"
)

// issueCertAction collects the acme email (when needed) and domain, then issues
// a certificate in a background task.
func issueCertAction() actionFunc {
	return func(a *App) tea.Cmd {
		lang := a.lang

		startDomainPrompt := func(email string) {
			a.openForm(lang.T("domain_issue"), lang.T("domain_prompt"), "", "", func(_ *App, value string) (tea.Cmd, error) {
				domain := strings.TrimSpace(value)
				if domain == "" {
					return nil, errors.New(lang.T("cancelled"))
				}
				return a.startTask(lang.T("domain_issuing"), issueCertTask(lang, email, domain)), nil
			})
		}

		// The address is asked for once and then remembered: it is the ACME
		// account's contact address, and a later issue does not ask for it again.
		if saved := strings.TrimSpace(state.Load().ACMEEmail); saved != "" {
			startDomainPrompt(saved)
			return nil
		}
		a.openForm(lang.T("domain_email"), lang.T("domain_email"), "", "", func(_ *App, value string) (tea.Cmd, error) {
			email := strings.TrimSpace(value)
			if email == "" {
				return nil, errors.New(lang.T("domain_email_required"))
			}
			startDomainPrompt(email)
			return nil, nil
		})
		return nil
	}
}

func issueCertTask(lang i18n.Lang, email, domain string) taskFunc {
	return func(ctx context.Context, r *taskReporter) error {
		// What can be known before the attempt is spent: a standalone listener and
		// where the domain points. Both are cheap, and both turn a two-minute wait
		// into a specific sentence when they are wrong.
		report := cert.Preflight(ctx, domain)
		r.Log(fmt.Sprintf("%s: %s / %s", lang.T("domain_check"), resolvedText(report, lang), ipText(report.PublicIP, lang)))
		if report.Mismatch() {
			r.Log(lang.T("domain_dns_mismatch") + ": " + strings.Join(report.Resolved, ", "))
			r.Log(lang.T("domain_dns_hint"))
		} else if others := report.Others(); len(others) > 0 {
			// One address is this server and another is not. Let's Encrypt checks every
			// address, so a stale record fails the order on the record's address while
			// this server answers correctly, which reads like a server problem and is not.
			r.Log(lang.T("domain_dns_extra") + ": " + strings.Join(others, ", "))
			r.Log(lang.T("domain_dns_extra_hint"))
		}
		if report.DNSFail != nil {
			r.Log(lang.T("domain_dns_failed") + ": " + report.DNSFail.Error())
		}
		if cert.Staging() {
			r.Log(lang.T("domain_staging"))
		}

		// The account is registered before the core is stopped, so a network or
		// address problem costs nothing but a message. It is a no-op once the panel
		// has an account, which is why it is called on every issuance.
		if err := cert.EnsureAccount(ctx, email, r.Log); err != nil {
			return err
		}

		cfg := state.Load()
		cfg.ACMEEmail = email
		if cfg.ServerIP == "" {
			if ip, err := netutil.PublicIP(ctx); err == nil {
				cfg.ServerIP = ip
			}
		}

		// Temporarily stop sing-box so the standalone challenge can bind 80.
		stopped := service.Active(ctx)
		if stopped {
			r.Log("$ systemctl stop " + sysinfo.ServiceName)
			if err := service.Do(ctx, "stop"); err != nil {
				r.Log(lang.T("service_restart_failed") + ": " + err.Error())
			}
		}
		running := func() {
			if stopped && hasServerConfig() {
				r.Log("$ systemctl start " + sysinfo.ServiceName)
				if err := service.Do(ctx, "start"); err != nil {
					r.Log(lang.T("service_restart_failed") + ": " + err.Error())
				}
			}
		}

		if err := cert.CheckPort80(); err != nil {
			running()
			return errors.New(lang.T("domain_port_busy") + ": " + err.Error())
		}

		issueErr := cert.Issue(ctx, domain, email, r.Log)
		running()
		if issueErr != nil {
			// A failure that names an address Let's Encrypt could not reach is almost
			// always a stale record next to the correct one, so name the address and the
			// fix instead of leaving the raw ACME error to be decoded.
			if stray := cert.StrayAddress(issueErr); stray != "" {
				r.Log(lang.T("domain_dns_stray_failed") + ": " + stray)
				r.Log(lang.T("domain_dns_extra_hint"))
			}
			return issueErr
		}
		if _, _, ok := cert.Paths(domain); !ok {
			return errors.New(lang.T("domain_issue_failed"))
		}

		cfg.Domain = domain
		cfg.CertDomain = domain
		if err := cfg.Save(); err != nil {
			return err
		}
		r.Log(lang.T("domain_issued") + ": " + domain)

		// The certificate is renewed by our own timer, which is installed here:
		// nothing else on the host renews it, because the panel does the ACME work
		// itself.
		if err := cert.InstallTimer(ctx, r.Log); err != nil {
			r.Log(lang.T("domain_timer_failed") + ": " + err.Error())
		} else {
			r.Log(lang.T("domain_timer_on"))
		}

		if cfg.NodeDeployed {
			if err := deploy.ApplyStore(ctx, cfg, sysinfo.UsersFile); err != nil {
				return err
			}
			r.Log(lang.T("domain_applied"))
			// The endpoint serves the certificate of the active domain, so the
			// subscription service is restarted with it.
			if err := subd.Do(ctx, "restart"); err != nil {
				r.Log(lang.T("sub_svc_failed") + ": " + err.Error())
			}
		}
		return nil
	}
}

// resolvedText describes where a domain currently points.
func resolvedText(r cert.Report, lang i18n.Lang) string {
	if len(r.Resolved) == 0 {
		return lang.T("domain_unresolved")
	}
	return strings.Join(r.Resolved, ", ")
}

func ipText(ip string, lang i18n.Lang) string {
	if ip == "" {
		return lang.T("domain_public_unknown")
	}
	return ip
}

// listCerts pushes a dynamic menu listing the certificates found on disk.
func listCerts() actionFunc {
	return func(a *App) tea.Cmd {
		domains := cert.Domains()
		if len(domains) == 0 {
			a.setToast(a.lang.T("domain_empty"), true)
			return nil
		}
		a.push(certMenu("domain-active", domains, switchCert))
		return nil
	}
}

// renewCertAction runs one renewal pass on demand, then reloads what holds the
// certificate: the timer does the same thing nightly.
func renewCertAction() actionFunc {
	return func(a *App) tea.Cmd {
		lang := a.lang
		return a.startTask(lang.T("domain_renew"), func(ctx context.Context, r *taskReporter) error {
			if !cert.Registered() {
				return errors.New(lang.T("domain_no_acme"))
			}
			if len(cert.Domains()) == 0 {
				return errors.New(lang.T("domain_empty"))
			}
			renewed, err := cert.Renew(ctx, r.Log)
			// Nothing changed, so nothing has to be reloaded: the certificate the
			// running core holds is still the current one. This matters because the
			// renewal timer takes this same path every night.
			if len(renewed) == 0 {
				if err != nil {
					return err
				}
				r.Log(lang.T("domain_renew_uptodate"))
				return nil
			}
			// A renewal overwrites the certificate files in place, so the config on disk
			// does not change and only the running services have to restart. Restarting
			// whatever is actually running, rather than following the deployed flag, is
			// what makes this work on a host whose node was deployed outside the panel.
			if service.Active(ctx) {
				if err := service.Do(ctx, "restart"); err != nil {
					r.Log(lang.T("service_restart_failed") + ": " + err.Error())
				}
			}
			if subd.Active(ctx) {
				if err := subd.Do(ctx, "restart"); err != nil {
					r.Log(lang.T("sub_svc_failed") + ": " + err.Error())
				}
			}
			r.Log(lang.T("domain_renewed"))
			// A domain whose renewal failed does not hold back the ones that
			// succeeded: what was renewed has just been reloaded above, and the
			// failure is still reported.
			return err
		})
	}
}

// renewTimerAction shows whether the renewal timer is installed and offers the
// other state. It is the menu half of cert.InstallTimer.
func renewTimerAction() actionFunc {
	return func(a *App) tea.Cmd {
		lang := a.lang
		installed := cert.TimerInstalled()
		next := cert.TimerStatus()
		if next != "" {
			a.setToast(lang.T("domain_timer_next")+": "+next, false)
		}
		detail := lang.T("domain_timer_off")
		if installed {
			detail = lang.T("domain_timer_on")
		}
		a.openForm(lang.T("domain_timer"), domainTimerPrompt(lang, detail), "", "", func(_ *App, value string) (tea.Cmd, error) {
			switch strings.ToLower(strings.TrimSpace(value)) {
			case "y", "yes":
				return a.startTask(lang.T("domain_timer"), timerTask(lang, !installed)), nil
			default:
				return nil, errors.New(lang.T("cancelled"))
			}
		})
		return nil
	}
}

func timerTask(lang i18n.Lang, install bool) taskFunc {
	return func(ctx context.Context, r *taskReporter) error {
		if install {
			if err := cert.InstallTimer(ctx, r.Log); err != nil {
				return err
			}
			r.Log(lang.T("domain_timer_on"))
			return nil
		}
		if err := cert.RemoveTimer(ctx, r.Log); err != nil {
			return err
		}
		r.Log(lang.T("domain_timer_off"))
		return nil
	}
}

// switchCertAction pushes a menu for choosing the active certificate.
func switchCertAction() actionFunc {
	return func(a *App) tea.Cmd {
		domains := cert.Domains()
		if len(domains) == 0 {
			a.setToast(a.lang.T("domain_empty"), true)
			return nil
		}
		a.push(certMenu("domain-switch", domains, switchCert))
		return nil
	}
}

// removeCertAction pushes a menu for choosing a certificate to delete.
func removeCertAction() actionFunc {
	return func(a *App) tea.Cmd {
		domains := cert.Domains()
		if len(domains) == 0 {
			a.setToast(a.lang.T("domain_empty"), true)
			return nil
		}
		a.push(certMenu("domain-remove", domains, confirmRemoveCert))
		return nil
	}
}

func switchCert(a *App, domain string) tea.Cmd {
	lang := a.lang
	cfg := state.Load()
	cfg.Domain = domain
	cfg.CertDomain = domain
	if err := cfg.Save(); err != nil {
		a.setToast(err.Error(), true)
		return nil
	}
	if !cfg.NodeDeployed {
		a.setToast(lang.T("domain_switched")+": "+domain, false)
		return nil
	}
	return a.startTask(lang.T("domain_switch"), func(ctx context.Context, r *taskReporter) error {
		if err := deploy.ApplyStore(ctx, cfg, sysinfo.UsersFile); err != nil {
			return err
		}
		r.Log(lang.T("domain_switched") + ": " + domain)
		r.Log(lang.T("domain_applied"))
		if err := subd.Do(ctx, "restart"); err != nil {
			r.Log(lang.T("sub_svc_failed") + ": " + err.Error())
		}
		return nil
	})
}

func confirmRemoveCert(a *App, domain string) tea.Cmd {
	lang := a.lang
	cfg := state.Load()
	clearActive := cfg.CertDomain == domain
	a.openForm(lang.T("domain_remove"), domainRemovePrompt(lang, domain), "", "", func(_ *App, value string) (tea.Cmd, error) {
		switch strings.ToLower(strings.TrimSpace(value)) {
		case "y", "yes":
			return a.startTask(lang.T("domain_remove"), removeCertTask(lang, domain, clearActive)), nil
		default:
			return nil, errors.New(lang.T("cancelled"))
		}
	})
	return nil
}

func removeCertTask(lang i18n.Lang, domain string, clearActive bool) taskFunc {
	return func(ctx context.Context, r *taskReporter) error {
		if err := cert.Remove(ctx, domain); err != nil {
			r.Log("acme remove: " + err.Error())
		}
		if clearActive {
			cfg := state.Load()
			cfg.CertDomain = ""
			if cfg.Domain == domain {
				cfg.Domain = ""
			}
			if err := cfg.Save(); err != nil {
				return err
			}
		}
		r.Log(lang.T("domain_removed") + ": " + domain)
		return nil
	}
}

// certMenu builds a menu whose rows are the certificates currently on disk.
func certMenu(id string, domains []string, pick func(*App, string) tea.Cmd) *menu {
	nodes := make([]*node, 0, len(domains))
	for _, domain := range domains {
		domain := domain
		nodes = append(nodes, &node{
			id: id + "-" + domain,
			label: func(l i18n.Lang) string {
				if state.Load().CertDomain == domain {
					return domain + "  (" + l.T("domain_active") + ")"
				}
				return domain
			},
			desc: tk("domain_select"),
			action: func(a *App) tea.Cmd {
				return pick(a, domain)
			},
		})
	}
	return &menu{id: id, title: tk("domain_title"), nodes: nodes}
}

func domainRemovePrompt(lang i18n.Lang, domain string) string {
	return strings.Replace(lang.T("domain_remove_confirm"), "%s", domain, 1) + " (y/N)"
}

func domainTimerPrompt(lang i18n.Lang, current string) string {
	return strings.Replace(lang.T("domain_timer_confirm"), "%s", current, 1) + " (y/N)"
}
