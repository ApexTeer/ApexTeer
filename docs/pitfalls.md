# Pitfalls and Lessons

Traps already hit in this repository. Each entry names the symptom and the fix.

## Release and CI

- **Force push does not raise an Actions run.** After rewriting history, the
  `push` workflow may not start even though the branch moved. Trigger it with a
  normal follow-up commit and push. The release assets come from that run.
- **A rewrite can orphan an old release.** Rewriting the commit that a release
  tag pointed at can make the old tag/release unreachable (it starts returning
  404). Re-publish under the current tag scheme.
- **armv7 naming.** Go spells armv7 as `GOARCH=arm` with `GOARM=7`, but the
  published asset keeps the name `armv7`. Keep the matrix mapping explicit.
- **Older runs overwriting newer assets.** The workflow uses a `concurrency`
  group with `cancel-in-progress` so a stale build cannot publish over a fresher
  one.
- **`make_latest: false`.** The Go release is intentionally not marked latest;
  do not flip this without deciding how it interacts with core releases.
- **Publish the sources with rsync, and never wrap rsync in `sshpass`.** The tree
  is 755 MB across 400-odd files. FTP-Deploy-Action waits a round trip per write
  (~25 KB/s here), and pushing one tar with `scp` sustains only 350-850 KB/s and
  was dropped by the peer after 36 minutes. `rsync -az --delete --delay-updates`
  over one SSH connection moved the same tree in about 6.5 minutes and only sends
  changes on later runs. The catch: `sshpass` does not leave `SSHPASS` intact for
  its child (1.09 masks it, newer versions unset it), so `sshpass -e rsync ...`
  leaves the `sshpass` that rsync starts through `RSYNC_RSH` with no password -
  the run dies immediately with `sshpass: -e option given but "SSHPASS"
  environment variable is not set` and `connection unexpectedly closed (0 bytes
  received so far)`. Run `rsync` directly and let `RSYNC_RSH` own the password.
- **Build rpm format 4, or the packages look unsigned on RHEL-family hosts.**
  rpm 6 sets `%_rpmformat 6`, so `rpmbuild` on Ubuntu 26.04 emits format 6
  packages, and `rpm --addsign` then writes only an OpenPGP v6 signature
  (`RPMSIGTAG_OPENPGP`). rpm at or below 4.20 - RHEL/CentOS/Rocky 9 and 10,
  Fedora 41 and 42 - ignores that tag, so `dnf` fails with `Package ... is not
  signed` / `Error: GPG check FAILED` even though the package was signed and the
  index signature verifies. `packages.sh` therefore passes
  `--rpmbuild-define "_rpmformat 4"` to fpm; rpm 6 then writes the signature into
  the legacy RSA/DSA tag. A local rpm 4.18 cannot tell the two apart (it does not
  know `_rpmformat` and always builds format 4), so this only reproduces on the
  newer runner: check a published package with `rpm -qp --qf
  '%{RPMFORMAT}\n'`, which prints `6` before the fix and nothing after it.

## Version and identity

- **`/etc/os-release` shadows `VERSION`.** In the legacy shell, sourcing
  `/etc/os-release` overwrote the script version variable. The Go build embeds a
  dedicated `VERSION` file with `go:embed`, so there is exactly one number: there
  is no `main.version` fallback to keep in step with it.
- **One tag scheme.** `install.sh`, the workflow, and `internal/update` must all
  derive `v<VERSION>`. A hardcoded tag in one place silently breaks downloads, so
  `install.sh` detects the latest release rather than pinning a version.

## sing-box integration

- **QR payloads differ per client.** sing-box needs its deep link
  (`sing-box://import-remote-profile?url=...`, `subscribe.ImportScheme`); a bare
  URL is not recognized and this is what broke sing-box QR scanning before.
  Clash-family clients are the opposite: their scanners feed the decoded text to
  an HTTP client, so mihomo (and v2rayN) must carry the plain endpoint URL. A
  `clash://install-config?url=...` link only works as an OS deep link, never from
  a scanned QR.
- **AnyTLS and Hysteria2 URIs need a slash before the query.** Emitting
  `anytls://pass@host:port?query` makes clients reject the link; the spec form is
  `anytls://pass@host:port/?query`. Credentials must also be percent-encoded
  (`url.User` / `url.UserPassword`), otherwise an `@` or `/` in a generated
  password truncates the URI. Generated passwords avoid the problem by staying
  alphanumeric (`secret.Password`, alphabet `[A-Za-z0-9]`), the intersection
  every target parser accepts. OpenWrt's homeproxy drops userinfo containing a
  `%`, so a standard base64 password (`+`/`/`/`=`) silently loses the password;
  staying alphanumeric avoids that.
- **Share links keep the canonical UUID.** Emitting the 32 character hyphen-less
  form makes homeproxy flag the node as an invalid UUID through its LuCI `uuid`
  validation, even though sing-box's gofrs parser accepts it. Keep the
  hyphenated form in every share link.
- **The Base64 document is the universal format.** v2rayN reads it directly;
  passwall, passwall2 and homeproxy base64-decode it first. No separate "base"
  format is needed. `luci-app-nikki` runs the mihomo core and validates for a
  top-level `proxies` key, so it needs the mihomo YAML profile — which the one
  `/sub/<token>` endpoint serves once it sees a Clash-family User-Agent.
- **Template actions in comments are still expanded.** `text/template` executes
  `{{ ... }}` even inside YAML/JSON comments. A `{{ .Proxies }}` in a mihomo
  header comment injects uncommented proxy entries above the document root and
  makes the profile unparseable. Keep actions out of comments.
- **A build tag cannot be probed at run time.** Whether this binary counts traffic
  per account is `with_v2ray_api`, so the answer lives in a tagged file pair
  (`internal/sbcore/stats_on.go` / `stats_off.go`) and the deploy path asks
  `sbcore.StatsCapable()` before writing `experimental.v2ray_api` — the core
  rejects a whole configuration naming an API it was not built with.
- **`with_naive_outbound` must stay out of `release/TAGS`.** Upstream's
  `DEFAULT_BUILD_TAGS` includes it, and it drags in the cronet/Chromium libraries,
  which have no build for 386, armv7, riscv64 or s390x (and need `with_purego` on
  windows): copying upstream's list verbatim breaks four of the six release
  architectures. The panel's node configuration never uses a naive outbound, so the
  tag set is deliberately narrower.
- **Downloads assume direct GitHub access.** Deployment targets are overseas,
  so binary and kernel-package downloads go straight to `github.com`. Mirror
  prefixes were removed on purpose; do not reintroduce them to work around a local
  network problem.
- **Comments are invalid JSON.** `templates/` files are JSONC for humans. Strip
  comments before handing anything to `sing-box check`.

## The toolbox

- **A tool writes nothing to stdout.** The panel owns the terminal, so a tool that prints
  (a library's spinner, a `fmt.Println` left in during a debug run) tears the frame apart
  instead of corrupting its own table. Every toolbox tool returns a `toolbox.Result` and
  reports progress through `Options.Log`. This is why `speedtest-go` is used as a library:
  its CLI's spinner is a separate `main` package that is not imported.
- **A tool takes its dependencies from `Options`.** HTTP, DNS, dials, commands and the
  scratch directory all arrive injected, so its test drives every failure path (a refused
  connection, a 503, a stubbed `/proc`) without a network or root. A tool that reaches for
  `http.DefaultClient` or `net.Dial` directly cannot be tested where the panel is built.
- **A verdict is a token, not a word.** The unlock entries return `unlocked` / `blocked` /
  `unknown` and the interface words them, so the same run reads correctly in both languages.
  A tool that returns a translated sentence freezes the language into the data.
- **Nothing is downloaded to measure something.** The CPU, memory and disk benchmarks are
  this binary's own workloads; they are not geekbench, sysbench or fio results and must never
  be labelled as such. If a measurement needs an external binary, the entry does not exist.

- **A box that resizes moves the page under the cursor.** The section pages used to grow their
  menu box to fill whatever the 看板 left, and to drop the 看板 entirely when it did not fit, so
  every page had its own box sizes. The slots are fixed now (`internal/tui/layout.go`) and a
  page clips its content instead of changing shape; `TestEveryPageKeepsTheSameBoxes` is what
  keeps it that way.
- **A fixed box has to hold the panel's tallest menu, not the main menu.** Sizing the bottom
  slot from the main menu alone left the account detail page (twelve entries) with two entries
  pushed out of the box — and no page scrolls, so they were unreachable. The slot is sized for
  the worst case; the main menu just uses part of it.
- **Q quits, Esc goes back, Enter enters.** Any screen that treats `q` as "back" makes the one
  key an operator reaches for to leave a trap; and a report that closed on Enter made Enter
  mean "back" on exactly one page. The quit keys are read before any screen sees them
  (`App.handleKey`), and no screen closes on Enter.
- **The arrow keys move the cursor, and the keys agree with the drawing.** A page inside a
  section falls back to the panel's two columns once its entries outgrow one per line
  (`entryRows`), but `menuColumns` still reported one, so `right` ran the highlighted entry —
  on the service page, a right press started or restarted the service — and `left` went back a
  level. The key handler has to ask the question the renderer already answered:
  `menuColumns` now reads `itemCount() > boxRows(layout.menu)` for a section page. No cursor
  key opens an entry or leaves a page; the destination screens (the system screen) make that
  explicit by owning every arrow key so none of them reaches the menu behind them.

## Subscription service

- **A client that cannot parse the profile must not receive one.** Refusing an
  expired or over-quota account with `403` and a plain-text reason keeps a
  half-valid profile out of a client: an empty `proxies` list is rejected by the
  Clash-family parsers, and an empty sing-box profile fails to parse outright.
- **The URL and the listener agree on the scheme.** Both ask
  `cert.Usable(DOMAIN)`, which requires a certificate a client will accept: one
  the panel issued itself, not the self-signed placeholder. Publishing an
  `https://` URL for a listener that fell back to HTTP (no certificate, or only
  the self-signed one, which clients reject) breaks every import.
- **The core user name is the subscription token.** The V2Ray counter key is
  `user>>><name>>>traffic>>>…`, so the name is also a `QueryStats` regex
  pattern: a token is ASCII by construction and survives a rename, while a display
  name may be Chinese or contain regex metacharacters. The inbound `users` arrays
  and `stats.users` must always come from the same predicate
  (`user.Store.Routable`), or an account is authenticated but never counted.
- **Counters are deltas, never absolutes.** The counters live in the running core
  and reset on restart, so the accounting loop persists differences and clamps a
  negative delta to zero.

## Certificates

- **Issuance is in process; nothing is downloaded to do it.** The panel orders
  through `github.com/go-acme/lego/v5` with the HTTP-01 standalone challenge: no
  acme.sh script to install, no socat or python to keep on the image, and no log
  output to parse. What acme.sh's output used to be read for is a value here — the
  account URL in `<dir>/account.json`, the pair on disk, and the expiry of the
  leaf. `EASYSB_ACME_DIR` moves the state directory; the default is
  `/etc/sing-box/acme`, and the account and the certificates share it.
- **The account key is the account; the file only saves a lookup.** A panel that
  lost `account.json` re-registers with the key it still has and gets the same
  account back. Losing `account.key` means a new account, and on a domain that has
  already spent its five duplicate certificates for the week that means a wait.
- **Without the timer nothing renews.** `easysb-acme.timer` (or the OpenRC script)
  runs `easysb --renew-certs`, and it is the only thing that does. A renewal
  replaces the pair in place, so a certificate that is renewed but not reloaded is
  still the old one in a running core: `--renew-certs` restarts sing-box and the
  subscription service for exactly that reason.
- **A renewal only spends a certificate when one is due.** `cert.RenewBefore` is 30
  days: the nightly pass skips anything further out without contacting the CA, and
  `Issue()` returns success without ordering when the certificate in place is still
  valid. That is what keeps a second click from costing one of the five duplicate
  certificates Let's Encrypt allows a week; `Remove()` first when a certificate has
  to be replaced early.
- **The challenge listener needs port 80 free.** `cert.CheckPort80()` runs after
  the node is stopped, because the node is what would hold the port if a protocol
  was put on it, and the check happens before an ACME attempt is spent. The
  listener itself is the standard library now, so socat or python being absent is
  no longer a reason for a challenge to fail.
- **The challenge answers only the name it was presented for.** lego's HTTP-01
  server matches the `Host` header against the domain, its answer to DNS
  rebinding, and answers `TEST` otherwise. A check that fetches the token itself
  has to send the domain as the Host header, or it fails on that `TEST`.
- **A failing challenge usually means DNS, not the panel.** A domain behind a CDN
  (or the Cloudflare orange cloud) answers HTTP-01 from the CDN and never reaches
  the host, which shows up as a validation error naming a URL the host never saw.
  The preflight report compares the resolved addresses with the host's public IP
  and says so; `EASYSB_ACME_STAGING=1` lets the whole flow be tried without
  consuming the Let's Encrypt rate limit.
- **A stale A record next to a correct one also fails the order.** Let's Encrypt
  validates the challenge against *every* address a domain resolves to, so a
  leftover record pointing at a host that no longer answers fails the issuance
  even though this server answers correctly. The error names the address
  (`During secondary validation: <ip>: … Connection refused`), which reads like a
  server fault; `Report.Others()` warns before the attempt and
  `cert.StrayAddress()` names the record afterwards. Check with the zone's own
  nameservers before doubting the box: `nslookup -type=A <name> <ns>` gives the
  authoritative answer, and a name that resolves to two addresses has two records
  (a wildcard would also have answered for a random subdomain, and a CDN proxy
  would have answered with the CDN's addresses).
- **The CA must be pinned, not inherited.** `letsEncryptDirectory()` names Let's
  Encrypt, or its staging endpoint, instead of taking a default: acme.sh moved its
  own default from Let's Encrypt to ZeroSSL, so a default that can move silently
  changes who signs the panel's certificates.
- **The key is written before the certificate.** `installPair()` writes
  `private.key` first and `fullchain.cer` second, so a failure between the two
  leaves a pair that does not exist rather than a certificate next to a key it was
  not issued for — the one combination sing-box refuses to start with. `Paths()`
  reports a pair only when both files are there and non-empty.
- **Reissuing a valid certificate is not a failure.** `Issue()` returns success
  and says why ("… is still valid until …") when the certificate in place is not
  due; that is what a second click on the button produces.

## State and templates

- **Two subscription templates.** Runtime uses the embedded
  `internal/subscribe/tun-fakeip.json`; `templates/config/tun-fakeip.json` is the
  readable mirror. Editing only one causes drift. The same applies to the mihomo
  template pair `internal/subscribe/mihomo.yaml` and
  `templates/config/mihomo.yaml`.
- **Do not rename state keys.** `easysb.conf` stays compatible with the legacy
  shell tool; add keys, never repurpose them.
- **Renaming a directory touches docs and GitHub metadata.** A folder rename
  must update `README.md`, `README_ZH.md`, `CHANGELOG.md`, `.github/CODEOWNERS`,
  and `.github/PULL_REQUEST_TEMPLATE.md`.

## Toolchain

- **Go 1.27.1.** `go.mod` pins the toolchain. With `GOTOOLCHAIN=auto`, Go
  downloads it automatically; CI uses `go-version-file: go.mod`. Do not lower
  the version casually.
- **Charm v2 uses vanity import paths** (`charm.land/*`), not the old
  `github.com/charmbracelet/*` module paths. Follow the existing imports.

## Terminal and tests

- **Interactive behavior needs a PTY.** For one-shot frame checks use
  `--render --width W --height H`, which prints a single frame without a TTY.
  Use it to catch overflow and alignment regressions.
- **Icons assume a Unicode terminal, not a patched font.** The default palette is
  a set of single-column geometric glyphs that ordinary monospace fonts already
  ship; `--icons ascii` (or `EASYSB_ICONS=ascii`) covers terminals without
  Unicode. Never make layout depend on a glyph being wider than one column.
- **Mouse reporting steals click-drag selection.** While the task/QR screen
  enables `MouseModeCellMotion` for wheel scrolling, the terminal stops
  selecting text on drag, so users cannot copy a subscription URL the usual way.
  The screen therefore offers `C` (OSC52 clipboard copy of the whole log) and
  `M` (release the mouse, restoring native selection). If you add mouse capture
  anywhere else, provide the same escape hatch.
- **After `git filter-branch`, `refs/original/*` remains.** It is a local backup
  of the pre-rewrite refs. Leave it or clean it deliberately; do not push it.
