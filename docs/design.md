# Design Principles

These are the decisions that shape the codebase. When a change conflicts with
one of them, treat the change as a design discussion rather than a local edit.

## Single static binary

Everything except the sing-box core is compiled in, certificates included.
There is no
runtime package manager call on the hot path and no script interpreter needed to
issue a certificate either.
The binary is invoked as `sb` after `install.sh` links it.

## Legacy-compatible state

`/etc/sing-box/easysb.conf` keeps the KV layout of the old bash tool. A Go build
and a shell build can inspect the same deployment. Do not change existing keys;
add new ones instead. The exception is a key that only described a component
which no longer exists, or state another package now owns: v4 dropped `SUB_PORT`
and `SUB_PATH` in the same change that deleted the nginx site they configured,
and v6 dropped the per-protocol keys (`IS_*`, `PORT_*`, `HY2_HOP_RANGE`,
`REALITY_*`) once `internal/node` became the source of what the host serves. The
migration reads the old keys, the next save drops them, and every doc that
mentions them changes in the same commit. An unread key is dead weight.

## Templates are readable first

`templates/` holds JSONC that a human can read and copy. Comments are allowed
there even though sing-box itself would reject them; the tool strips comments
when it renders a real config. The subscription templates that ship inside the
binary are `internal/subscribe/tun-fakeip.json` and `internal/subscribe/mihomo.yaml`;
the readable mirrors are `templates/config/tun-fakeip.json` and
`templates/config/mihomo.yaml`.

## Bilingual by construction

Every user-facing string flows through `internal/i18n`. The language is a
`Lang` value (`C` or `E`), never a global boolean. `L` toggles at runtime. New
strings must be added to the table for both languages in the same commit.

## Version is data, not code

`VERSION` is the single source of truth, and it is compiled into the binary with
`go:embed`. A bare `go build` and a published release therefore report the same
number, with no `-ldflags -X main.version` to keep in step and no second constant
to drift. The workflow publishes under the tag `v<VERSION>`; `internal/update`
reads the same number from the published `VERSION` file, derives the tag from it,
and lets apt do the upgrade. Never hardcode a release tag in more than one place.

## Non-interactive entry points

Anything a boot unit needs must be reachable without a TTY. `--apply-firewall`
is the model case: it loads state, applies rules, writes the unit, and exits.
The TUI calls the same package functions.

## Dark, quiet terminal UI

The default palette is dark with cyan as the primary accent. On startup the TUI
asks the terminal for its background color and switches to a darkened light
palette when the background is light, so the near-white body text of the dark
palette never lands on white; `--theme` (or `EASYSB_THEME`) forces `dark` or
`light`. The UI uses the alternate screen, one border, and one accent per state
(ok / warn / error). Spacing is
explicit: dividers and menu items each get exactly one blank line, and the
dashboard drops low-priority panels before it overflows a short terminal.

## One frame, two boxes

Every page is the same shape: a status strip on the first line, then the page's own
看板 in the top box and its entries in the bottom one. The main menu and a section
differ only in what those two boxes hold — the wordmark and the 看板 of the panel
versus the 看板 of one section — so moving between pages never resizes the frame and
the key hints never move. A section is left with `Esc`, the way a submenu is, and
`--render --screen <id>` draws any of them for a layout check.

## Long operations report where they are

A task that downloads something streams its readings to the screen instead of
printing a line per megabyte: `internal/download` reports byte counts through a
`Progress` callback, `internal/tui` collects them in the task reporter and draws a
bar above the log. Anything that can take minutes — the panel's own binary, a
kernel package — goes through that path, and a step that only changes local state
stays silent rather than showing a bar that never moves. The core is not on that
list any more: it is compiled in, so nothing about the node downloads.

## Direct downloads, tolerant parsing

Deployment targets are overseas hosts with direct GitHub access, so binary and
kernel-package downloads go straight to `github.com` with no mirror prefix. Release
tag parsing still tolerates a missing `v` prefix, because the `releases.atom` feed
omits it.
