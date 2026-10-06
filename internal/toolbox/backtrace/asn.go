package backtrace

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"math"
	"net"
	"net/http"
	"sort"
	"strings"
	"time"

	"github.com/EasySBTeam/EasySB/internal/toolbox"
)

// apiBatchURL is the free, key-less ip-api batch endpoint. It is deliberately
// http: the free tier does not serve TLS (https is paid), which is harmless here
// because nothing secret crosses it — the questions are public addresses.
const apiBatchURL = "http://ip-api.com/batch"

// apiBatchMax is the endpoint's documented cap: one request carries at most 100
// addresses, so a run whose hops outnumber that is split into several calls.
const apiBatchMax = 100

// apiFields trims the answer to what a verdict reads, which also keeps the
// response small on a VPS.
const apiFields = "status,message,countryCode,isp,org,as,asname,query"

// apiMinInterval paces the batch calls. The free tier allows 45 requests a minute
// from one address and answers 429 past it; waiting two seconds between calls
// stays comfortably under that (a run needs one call per 100 addresses, and the
// twelve targets of the default table fit in one), which is far cheaper than a
// failed batch that costs the whole run's verdicts.
const apiMinInterval = 2 * time.Second

// apiMaxAnswer caps how much of a response is read: the answer to 100 addresses
// is a few kilobytes, and a broken or hostile response must not fill memory on a
// small VPS.
const apiMaxAnswer = 1 << 20

// Fact is what the lookup knows about one address.
type Fact struct {
	// Country is the ISO 3166-1 alpha-2 code ip-api reports, upper-cased ("CN"
	// for the mainland). Empty when the answer carried none.
	Country string
	ISP     string
	Org     string
	ASName  string
	// ASN is the autonomous system number parsed out of ip-api's "as" field
	// ("AS4134 CHINANET-BACKBONE" is 4134). Zero when there was none.
	ASN int
	// Known is false when the lookup produced no usable data for the address:
	// ip-api answered "fail" (a private range, for instance), the batch failed,
	// or it was rate limited. A verdict may not be built on an unknown fact,
	// which is why this flag exists separately from the empty fields.
	Known bool
	// Reason says why an unknown fact is unknown. It reaches Notes verbatim.
	Reason string
}

// unusable reports whether a fact came from no answer at all.
func (f Fact) unusable() bool { return !f.Known }

// maxBackboneLen caps the backbone text the 回程线路 column carries. Only the
// fallback name (a provider ip-api knows but that has no AS number of its own) can
// run long; a number is always short, and the column has to stay drawable.
const maxBackboneLen = 24

// label is the short name of an address's system, for the 回程线路 cell of a
// reading that is not one of the three carriers.
func (f Fact) label() string {
	if f.ASN > 0 {
		return fmt.Sprintf("AS%d", f.ASN)
	}
	return shortText(f.ISP, f.Org, maxBackboneLen)
}

// shortText returns the first non-empty string, trimmed to limit runes so a long
// provider name cannot stretch a table column.
func shortText(first, second string, limit int) string {
	s := first
	if s == "" {
		s = second
	}
	r := []rune(s)
	if len(r) <= limit {
		return s
	}
	return string(r[:limit-1]) + "…"
}

// descriptor names the system the way a note can print it: the number and its
// name when ip-api supplied both, otherwise whichever of the names it did.
func (f Fact) descriptor() string {
	switch {
	case f.ASN > 0 && f.ASName != "":
		return fmt.Sprintf("AS%d %s", f.ASN, f.ASName)
	case f.ASN > 0:
		return fmt.Sprintf("AS%d", f.ASN)
	case f.ASName != "":
		return f.ASName
	case f.Org != "":
		return f.Org
	default:
		return f.ISP
	}
}

// Lookuper resolves addresses to the facts a verdict needs.
//
// Lookup returns one fact per address asked about, and an error only when it
// could not run at all (a cancelled context). A failed batch is not that error:
// the addresses it covered come back as unknown facts carrying a Reason, so a run
// still reports the paths it measured.
type Lookuper interface {
	Lookup(ctx context.Context, ips []net.IP) (map[string]Fact, error)
}

// apiLookup is the Lookuper the panel uses: ip-api's batch endpoint over the
// client the toolbox injects, paced to stay inside the free tier's rate limit.
type apiLookup struct {
	client toolbox.HTTPDoer
	// minInterval is the smallest gap between two batch calls; zero disables
	// pacing, which is what the unit tests want.
	minInterval time.Duration
	// sleep spends the wait and now reads the clock, so a test can prove the
	// pacing without waiting for it.
	sleep func(context.Context, time.Duration) error
	now   func() time.Time
	last  time.Time
}

// newAPILookup builds the ip-api lookup. now is the toolbox clock.
func newAPILookup(client toolbox.HTTPDoer, now func() time.Time) *apiLookup {
	if now == nil {
		now = time.Now
	}
	return &apiLookup{
		client:      client,
		minInterval: apiMinInterval,
		sleep:       sleepCtx,
		now:         now,
	}
}

// Lookup asks ip-api about every address, in batches of at most apiBatchMax.
//
// Every failure degrades to per-address reasons rather than an aborted run: a
// twelve-target trace spends seconds of network time, and a rate limit on the
// last batch must not throw the measured paths away.
func (a *apiLookup) Lookup(ctx context.Context, ips []net.IP) (map[string]Fact, error) {
	out := make(map[string]Fact, len(ips))
	addrs := dedupeAddrs(ips)
	for len(addrs) > 0 {
		n := min(len(addrs), apiBatchMax)
		batch := addrs[:n]
		addrs = addrs[n:]

		if err := a.pace(ctx); err != nil {
			return out, err
		}
		answers, err := a.request(ctx, batch)
		a.last = a.now()
		if err != nil {
			for _, ip := range batch {
				out[ip] = Fact{Reason: err.Error()}
			}
			continue
		}
		for _, ip := range batch {
			if f, ok := answers[ip]; ok {
				out[ip] = f
				continue
			}
			out[ip] = Fact{Reason: "ip-api 未返回该地址的数据"}
		}
	}
	return out, nil
}

// request performs one batch call and maps the answer back onto the addresses
// asked about, keyed by the address ip-api echoes in "query".
func (a *apiLookup) request(ctx context.Context, addrs []string) (map[string]Fact, error) {
	body, err := json.Marshal(addrs)
	if err != nil {
		return nil, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, apiBatchURL+"?fields="+apiFields, bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := a.client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("ip-api 查询失败：%w", err)
	}
	defer resp.Body.Close()
	data, err := io.ReadAll(io.LimitReader(resp.Body, apiMaxAnswer))
	if err != nil {
		return nil, fmt.Errorf("ip-api 查询失败：%w", err)
	}
	switch {
	case resp.StatusCode == http.StatusTooManyRequests:
		// The free tier is 45 requests a minute; a 429 means this source
		// address has spent them, and retrying now would only spend more.
		return nil, fmt.Errorf("ip-api 限流（HTTP 429），本轮无法判定这些地址")
	case resp.StatusCode != http.StatusOK:
		return nil, fmt.Errorf("ip-api 返回 HTTP %d", resp.StatusCode)
	}
	var answers []apiAnswer
	if err := json.Unmarshal(data, &answers); err != nil {
		return nil, fmt.Errorf("解析 ip-api 响应失败：%w", err)
	}
	out := make(map[string]Fact, len(answers))
	for i, ans := range answers {
		key := ans.Query
		if key == "" && i < len(addrs) {
			// The endpoint answers in request order, so a missing echo of the
			// address itself can still be placed.
			key = addrs[i]
		}
		if key != "" {
			out[key] = ans.fact()
		}
	}
	return out, nil
}

// pace waits out the interval between two calls.
func (a *apiLookup) pace(ctx context.Context) error {
	if a.minInterval <= 0 || a.last.IsZero() {
		return nil
	}
	wait := a.minInterval - a.now().Sub(a.last)
	if wait <= 0 {
		return nil
	}
	return a.sleep(ctx, wait)
}

// apiAnswer is one entry of the batch response.
type apiAnswer struct {
	Status      string `json:"status"`
	Message     string `json:"message"`
	CountryCode string `json:"countryCode"`
	ISP         string `json:"isp"`
	Org         string `json:"org"`
	AS          string `json:"as"`
	ASName      string `json:"asname"`
	Query       string `json:"query"`
}

// fact converts one answer. A failure carries ip-api's own message, which says
// why (a private range, a reserved range, a bad address) far better than a
// generic sentence would.
func (a apiAnswer) fact() Fact {
	if a.Status != "success" {
		reason := a.Message
		if reason == "" {
			reason = "ip-api 未说明原因"
		}
		return Fact{Reason: reason}
	}
	return Fact{
		Country: strings.ToUpper(a.CountryCode),
		ISP:     a.ISP,
		Org:     a.Org,
		ASName:  a.ASName,
		ASN:     parseASN(a.AS),
		Known:   true,
	}
}

// dedupeAddrs turns the addresses of a run into the sorted, deduplicated strings
// the endpoint is asked about, so a hop shared by several targets costs one slot
// and the batches are deterministic.
func dedupeAddrs(ips []net.IP) []string {
	seen := make(map[string]bool, len(ips))
	out := make([]string, 0, len(ips))
	for _, ip := range ips {
		if ip == nil {
			continue
		}
		s := ip.String()
		if seen[s] {
			continue
		}
		seen[s] = true
		out = append(out, s)
	}
	sort.Strings(out)
	return out
}

// sleepCtx waits, and gives up early when the run is cancelled.
func sleepCtx(ctx context.Context, d time.Duration) error {
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-t.C:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

// parseASN reads the number out of ip-api's "as" field, which is formatted
// "AS4134 CHINANET-BACKBONE". Anything without a leading AS<digits> yields zero,
// and a verdict may not be built on that.
func parseASN(s string) int {
	s = strings.TrimPrefix(strings.ToUpper(strings.TrimSpace(s)), "AS")
	n := 0
	for i := 0; i < len(s); i++ {
		if s[i] < '0' || s[i] > '9' {
			break
		}
		n = n*10 + int(s[i]-'0')
		if n > math.MaxInt32 {
			// Not an AS number; refuse rather than wrapping on a 32-bit build.
			return 0
		}
	}
	if n == 0 {
		return 0
	}
	return n
}

// carrierAS is what one known autonomous system says about a path.
type carrierAS struct {
	carrier  Carrier
	backbone string
}

// carrierASNs maps the autonomous systems the three mainland carriers use for
// their international traffic to the carrier and the backbone name the reference
// tools print next to it. The numbers are the ones oneclickvirt/backtrace
// (model/model.go, M) and zhanghanyun/backtrace (asn.go, m) label: 电信
// 163/CN2/CTGNet, 联通 4837/9929/CUG, 移动 CMI/CMIN2.
var carrierASNs = map[int]carrierAS{
	4134:  {CarrierTelecom, "163"},
	4809:  {CarrierTelecom, "CN2"},
	4812:  {CarrierTelecom, "163"},
	23764: {CarrierTelecom, "CTGNet"},
	4837:  {CarrierUnicom, "4837"},
	9929:  {CarrierUnicom, "9929"},
	10099: {CarrierUnicom, "CUG"},
	17623: {CarrierUnicom, "4837"},
	4808:  {CarrierUnicom, "4837"},
	17816: {CarrierUnicom, "4837"},
	9808:  {CarrierMobile, "CMI"},
	58453: {CarrierMobile, "CMI"},
	58807: {CarrierMobile, "CMIN2"},
	24400: {CarrierMobile, "CMI"},
	56048: {CarrierMobile, "CMI"},
}

// carrierMarkers reads the operator out of the names ip-api reports. It is the
// fallback for a hop whose AS number is not in the table, because a carrier
// numbers its regional networks separately and still calls them 电信/联通/移动.
// Only the fact's own country gates it (the caller requires the mainland), so
// "China Mobile Hong Kong" in Hong Kong cannot be read as 移动.
//
// The markers are the spellings ip-api actually returns, taken from readings on a
// real host: a Beijing Unicom path ends on a hop whose only name is "UNICOM", a
// Guangzhou Unicom path on "CNCGROUP-SZ", and a CN2 path on "Chinatelecom Next
// Carrying Network backbone" — none of which the earlier, tidier marker list
// matched, so three Chinese paths were reported as 国际多线. Order matters:
// telecom's markers are checked before unicom's, because a Chinatelecom name
// never contains "unicom" but a joint venture's might.
var carrierMarkers = []struct {
	marker  string
	carrier Carrier
}{
	{"china telecom", CarrierTelecom},
	{"chinatelecom", CarrierTelecom},
	{"chinanet", CarrierTelecom},
	{"ctgnet", CarrierTelecom},
	{"next carrying network", CarrierTelecom},
	{"cn2", CarrierTelecom},
	{"china unicom", CarrierUnicom},
	{"china169", CarrierUnicom},
	{"unicom", CarrierUnicom},
	{"cncgroup", CarrierUnicom},
	{"cnc group", CarrierUnicom},
	{"china mobile", CarrierMobile},
	{"cmnet", CarrierMobile},
	{"cmin2", CarrierMobile},
	{"cmi", CarrierMobile},
}

// name is what one address contributes to a verdict.
type name struct {
	// carrier is the verdict this address settles; meaningful when known.
	carrier  Carrier
	backbone string
	// evidence names the system behind a 国际多线 reading, for Notes.
	evidence string
	// known is true when this address settled the verdict.
	known bool
	// usable is true when ip-api answered for the address at all, whatever the
	// answer was. It is what separates "the lookup gave nothing" from "the
	// lookup says this address is not in China".
	usable bool
	// cn is true when the address is in the mainland.
	cn bool
	// reason explains an address that settled nothing.
	reason string
}

// nameFact reads one address's facts into a verdict contribution.
//
//   - A mainland address in one of the three carriers' systems is that carrier,
//     with the backbone the AS number named.
//   - A mainland address in any other network is CarrierInternational: the return
//     path is demonstrably not one of the three, and the evidence names what was
//     seen instead (an education network, a foreign transit, a regional
//     provider). That is a reading, not a guess.
//   - A mainland address with neither an AS number nor an operator name is
//     unnameable, and stays 无法判定.
//   - An address outside the mainland settles nothing on its own: it is not the
//     network the packet comes back through, it is where the trace still is.
func nameFact(f Fact, have bool) name {
	if !have || f.unusable() {
		reason := f.Reason
		if reason == "" {
			reason = "ip-api 没有返回该地址的数据"
		}
		return name{reason: reason}
	}
	if f.Country != "CN" {
		where := f.Country
		if where == "" {
			where = "未知"
		}
		return name{usable: true, reason: fmt.Sprintf("地址在中国大陆之外（%s）", where)}
	}
	if as, ok := carrierASNs[f.ASN]; ok {
		return name{usable: true, cn: true, known: true, carrier: as.carrier, backbone: as.backbone}
	}
	if c, ok := carrierFromNames(f); ok {
		return name{usable: true, cn: true, known: true, carrier: c}
	}
	if f.ASN == 0 && f.ISP == "" && f.Org == "" && f.ASName == "" {
		return name{usable: true, cn: true, reason: "ip-api 只给出国家，没有 ISP/ASN 信息，无法判定"}
	}
	return name{
		usable:   true,
		cn:       true,
		known:    true,
		carrier:  CarrierInternational,
		backbone: f.label(),
		evidence: f.descriptor(),
	}
}

// carrierFromNames looks for a carrier's name among the fields ip-api filled in.
func carrierFromNames(f Fact) (Carrier, bool) {
	hay := strings.ToLower(strings.Join([]string{f.ISP, f.Org, f.ASName}, " "))
	for _, m := range carrierMarkers {
		if strings.Contains(hay, m.marker) {
			return m.carrier, true
		}
	}
	return "", false
}
