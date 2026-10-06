package tui

import (
	"context"
	"slices"
	"strings"
	"time"

	tea "charm.land/bubbletea/v2"

	"github.com/EasySB-Team/EasySB/internal/bbr"
	"github.com/EasySB-Team/EasySB/internal/i18n"
	"github.com/EasySB-Team/EasySB/internal/icons"
)

// buildBBR is the BBR section: switching the running kernel's congestion control
// on, and installing the BBRv3 kernels the Linux-BBR-v3 project builds.
func buildBBR() *menu {
	return &menu{
		id:    "bbr",
		title: tk("bbr_title"),
		nodes: []*node{
			leaf("bbr-status", "bbr_status", "desc_bbr_status", bbrStatusAction()),
			{id: "bbr-enable", label: tk("bbr_enable"), desc: tk("desc_bbr_enable"), sub: buildQdisc()},
			leaf("bbr-install-standard", "bbr_install_standard", "desc_bbr_install_standard", bbrInstallAction(bbr.Standard, "")),
			leaf("bbr-install-max", "bbr_install_max", "desc_bbr_install_max", bbrInstallAction(bbr.Max, "")),
			leaf("bbr-versions", "bbr_versions", "desc_bbr_versions", bbrVersionsAction()),
			leaf("bbr-remove-kernel", "bbr_remove_kernel", "desc_bbr_remove_kernel", bbrRemoveAction()),
			leaf("bbr-clear", "bbr_clear", "desc_bbr_clear", bbrClearAction()),
		},
	}
}

// bbrVersionsMsg carries a freshly fetched kernel list back to the interface. The
// list arrives asynchronously because it is a network call, and the menu that
// opened has to show something while it is in flight.
type bbrVersionsMsg struct {
	list   []bbr.Release
	status bbr.Status
	err    error
}

// bbrVersionsAction opens the version list and starts the fetch behind it.
func bbrVersionsAction() actionFunc {
	return func(a *App) tea.Cmd {
		a.bbrVersions = nil
		a.bbrVersionsErr = nil
		a.bbrVersionsLoading = true
		a.push(a.bbrVersionsMenu())
		return fetchBBRVersions()
	}
}

// bbrStatusMsg carries a reading of the local BBR state, taken when the section is
// entered so its 看板 shows the machine rather than "not read".
type bbrStatusMsg struct{ status bbr.Status }

// readBBRStatus reads the local half of the BBR picture: the kernel that is running
// and the BBR kernels that are installed.
func readBBRStatus() tea.Cmd {
	return func() tea.Msg { return bbrStatusMsg{status: bbrStatusNow()} }
}

// bbrStatusNow is readBBRStatus without the event loop, for a screen that is rendered
// rather than driven: the CLI preview has no message loop to deliver the result.
func bbrStatusNow() bbr.Status {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	return bbr.LocalStatus(ctx)
}

// sectionRefresh loads what a section's 看板 reads the moment that section is entered,
// so the top box is never blank while the actions in the bottom box are untouched.
// Sections the status tick already answers need nothing here.
func (a *App) sectionRefresh() tea.Cmd {
	if a.section == "bbr" {
		return readBBRStatus()
	}
	return nil
}

// fetchBBRVersions reads the published kernels and the local state in one go, so the
// list can mark what is already installed.
func fetchBBRVersions() tea.Cmd {
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
		defer cancel()
		msg := bbrVersionsMsg{status: bbr.LocalStatus(ctx)}
		msg.list, msg.err = bbr.Releases(ctx)
		return msg
	}
}

// applyBBRVersions stores a fetch result and redraws the list when it is the screen
// the operator is still looking at.
func (a *App) applyBBRVersions(msg bbrVersionsMsg) {
	a.bbrVersionsLoading = false
	a.bbrVersions = msg.list
	a.bbrVersionsErr = msg.err
	a.bbrStatus = msg.status
	if a.current().id == "bbr-versions" {
		a.current().nodes = a.bbrVersionsNodes()
		if a.index >= len(a.current().nodes) {
			a.index = 0
		}
	}
}

// bbrVersionsMenu is the screen listing the installable kernels: the newest on top,
// one row per published build, opens with a placeholder until the fetch lands.
func (a *App) bbrVersionsMenu() *menu {
	return &menu{id: "bbr-versions", title: tk("bbr_versions_title"), nodes: a.bbrVersionsNodes()}
}

func (a *App) bbrVersionsNodes() []*node {
	switch {
	case a.bbrVersionsLoading:
		return []*node{{id: "bbr-versions-loading", label: tk("bbr_versions_loading"), desc: tk("bbr_versions_wait")}}
	case a.bbrVersionsErr != nil:
		err := a.bbrVersionsErr
		return []*node{{
			id:    "bbr-versions-failed",
			label: tk("bbr_versions_failed"),
			desc:  func(_ i18n.Lang) string { return err.Error() },
		}}
	case len(a.bbrVersions) == 0:
		return []*node{{id: "bbr-versions-empty", label: tk("bbr_versions_empty"), desc: tk("desc_bbr_versions_empty")}}
	}
	newest := map[bbr.Profile]string{}
	for _, rel := range a.bbrVersions {
		if newest[rel.Profile] == "" {
			newest[rel.Profile] = rel.Version
		}
	}
	nodes := make([]*node, 0, len(a.bbrVersions))
	for _, rel := range a.bbrVersions {
		rel := rel
		nodes = append(nodes, &node{
			id:    "bbr-version-" + rel.Tag,
			label: func(_ i18n.Lang) string { return releaseLabel(rel) },
			desc:  func(l i18n.Lang) string { return a.releaseDesc(l, rel, newest[rel.Profile]) },
			icon:  releaseIcon(rel.Profile),
			action: func(a *App) tea.Cmd {
				return bbrInstallAction(rel.Profile, rel.Version)(a)
			},
		})
	}
	return nodes
}

// releaseLabel is the row title: the version, with the max profile marked exactly the
// way its release tag is. The two builds of one version would otherwise read as the
// same row, since only the description tells them apart.
func releaseLabel(rel bbr.Release) string {
	return rel.Version + rel.Profile.Suffix()
}

// releaseDesc says what a row is: which profile it is, whether it is the newest of
// that profile, and whether this machine already has it.
func (a *App) releaseDesc(l i18n.Lang, rel bbr.Release, newest string) string {
	kernel := rel.Profile.KernelRelease(rel.Version)
	parts := []string{profileLabel(l, rel.Profile)}
	switch {
	case a.bbrStatus.Running == kernel:
		parts = append(parts, l.T("bbr_versions_running"))
	case slices.Contains(a.bbrStatus.Kernels, "linux-image-"+kernel):
		parts = append(parts, l.T("bbr_versions_installed"))
	}
	if rel.Version == newest {
		parts = append(parts, l.T("bbr_versions_latest"))
	}
	return strings.Join(parts, " · ")
}

// releaseIcon tells the profiles apart at a glance: the standard build is the
// conservative one, the max build is the throughput one.
func releaseIcon(profile bbr.Profile) func(icons.Set) string {
	if profile == bbr.Max {
		return func(s icons.Set) string { return s.Speed }
	}
	return func(s icons.Set) string { return s.System }
}

// previewReleases is a stand-in list for --render, which draws the screen without a
// network. The running panel never uses it: its list comes from the kernel
// project's releases every time the screen is opened.
func previewReleases() []bbr.Release {
	return []bbr.Release{
		{Tag: "x86_64-9.9.9", Version: "9.9.9", Profile: bbr.Standard},
		{Tag: "x86_64-9.9.9-max", Version: "9.9.9", Profile: bbr.Max},
		{Tag: "x86_64-9.9.8", Version: "9.9.8", Profile: bbr.Standard},
		{Tag: "x86_64-9.9.8-max", Version: "9.9.8", Profile: bbr.Max},
	}
}

// buildQdisc is the queue discipline submenu. BBR needs a fair queueing
// scheduler; which one is a trade-off between latency and fairness.
func buildQdisc() *menu {
	nodes := make([]*node, 0, len(bbr.Qdiscs))
	for _, qdisc := range bbr.Qdiscs {
		nodes = append(nodes, &node{
			id:     "bbr-qdisc-" + qdisc,
			label:  tk("bbr_qdisc_" + strings.ReplaceAll(qdisc, "-", "_")),
			desc:   tk("desc_bbr_qdisc_" + strings.ReplaceAll(qdisc, "-", "_")),
			icon:   func(s icons.Set) string { return s.Speed },
			action: bbrEnableAction(qdisc),
		})
	}
	return &menu{id: "bbr-qdisc", title: tk("bbr_qdisc_title"), nodes: nodes}
}

// bbrStatusAction prints one reading of the congestion control state, including
// the newest kernel the kernel project has published.
func bbrStatusAction() actionFunc {
	return func(a *App) tea.Cmd {
		lang := a.lang
		return a.startTask(lang.T("bbr_status"), func(ctx context.Context, r *taskReporter) error {
			st := bbr.Collect(ctx)
			r.Log(lang.T("bbr_st_running") + ": " + dash(st.Running))
			r.Log(lang.T("bbr_st_arch") + ": " + dash(st.Arch))
			if !st.Supported() {
				r.Log(lang.T("bbr_st_arch_unsupported"))
			}
			if st.Enabled() {
				r.Log(lang.T("bbr_st_congestion") + ": bbr " + lang.T("bbr_st_on"))
			} else {
				r.Log(lang.T("bbr_st_congestion") + ": " + dash(st.Congestion) + " " + lang.T("bbr_st_off"))
			}
			r.Log(lang.T("bbr_st_qdisc") + ": " + dash(st.Qdisc))
			r.Log(lang.T("bbr_st_available") + ": " + dash(st.Available))
			if kernel := st.CustomKernel(); kernel != "" {
				r.Log(lang.T("bbr_st_kernel") + ": " + kernel)
				if st.NeedsReboot() {
					r.Log(lang.T("bbr_st_reboot"))
				}
			} else {
				r.Log(lang.T("bbr_st_kernel") + ": " + lang.T("bbr_st_none"))
			}
			if st.Latest != "" {
				r.Log(lang.T("bbr_st_latest") + ": " + st.Latest)
				if newer, ok := st.Outdated(); ok {
					r.Log(lang.T("bbr_st_newer") + ": " + newer)
					r.Log(lang.T("bbr_st_newer_hint"))
				}
			} else if st.LatestErr != "" {
				r.Log(lang.T("ver_offline"))
			}
			if !st.AvailableBBR() {
				r.Log(lang.T("bbr_st_no_module"))
			}
			return nil
		})
	}
}

// bbrEnableAction turns BBR on with the chosen queue discipline.
func bbrEnableAction(qdisc string) actionFunc {
	return func(a *App) tea.Cmd {
		lang := a.lang
		title := lang.T("bbr_enable") + " · " + qdisc
		return a.startTask(title, func(ctx context.Context, r *taskReporter) error {
			if err := bbr.Enable(ctx, r.Log, qdisc); err != nil {
				return err
			}
			r.Log(lang.T("ok"))
			return nil
		})
	}
}

// bbrInstallAction downloads and installs one published kernel. An empty version
// means the newest published one; a version pins that build instead.
func bbrInstallAction(profile bbr.Profile, version string) actionFunc {
	return func(a *App) tea.Cmd {
		lang := a.lang
		title := lang.T("bbr_install") + " · " + profileLabel(lang, profile)
		if version != "" {
			title += " · " + version
		}
		return a.startTask(title, func(ctx context.Context, r *taskReporter) error {
			r.Log(lang.T("bbr_installing"))
			if err := bbr.Install(ctx, r.Log, r.Progress, profile, version); err != nil {
				return err
			}
			r.Log(lang.T("bbr_installed"))
			// The running kernel does not change under a live proxy, so the install
			// leaves the machine one reboot short of using what it just unpacked.
			// Saying so twice is deliberate: once in the log that is being read
			// now, and once on the menu this task hands back to.
			r.Log(lang.T("bbr_reboot_needed"))
			r.Log(lang.T("bbr_reboot_hint"))
			// The toast is set on the render goroutine once the task ends, so the
			// task never writes the interface itself.
			r.SetResult(taskToast{msg: lang.T("bbr_reboot_needed"), warn: true})
			return nil
		})
	}
}

// bbrRemoveAction uninstalls the published kernel and falls back to the stock one.
func bbrRemoveAction() actionFunc {
	return func(a *App) tea.Cmd {
		lang := a.lang
		return a.startTask(lang.T("bbr_remove_kernel"), func(ctx context.Context, r *taskReporter) error {
			removed, err := bbr.Remove(ctx, r.Log)
			if err != nil {
				return err
			}
			if !removed {
				r.Log(lang.T("bbr_remove_none"))
				return nil
			}
			r.Log(lang.T("bbr_removed"))
			r.Log(lang.T("bbr_reboot_hint"))
			return nil
		})
	}
}

// bbrClearAction removes the drop-ins EasySB wrote for BBR and restores the
// settings they replaced.
func bbrClearAction() actionFunc {
	return func(a *App) tea.Cmd {
		lang := a.lang
		return a.startTask(lang.T("bbr_clear"), func(ctx context.Context, r *taskReporter) error {
			cleared, err := bbr.Clear(ctx, r.Log)
			if err != nil {
				return err
			}
			if !cleared {
				r.Log(lang.T("bbr_clear_none"))
				return nil
			}
			r.Log(lang.T("bbr_cleared"))
			return nil
		})
	}
}

// profileLabel names a kernel profile the way the menu does.
func profileLabel(lang i18n.Lang, profile bbr.Profile) string {
	if profile == bbr.Max {
		return lang.T("bbr_profile_max")
	}
	return lang.T("bbr_profile_standard")
}

// dash keeps a status line readable when a reading is empty.
func dash(value string) string {
	if strings.TrimSpace(value) == "" {
		return "-"
	}
	return value
}
