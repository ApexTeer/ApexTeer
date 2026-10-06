package speed

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"sync"
	"time"

	"github.com/showwin/speedtest-go/speedtest"

	"github.com/EasySB-Team/EasySB/internal/toolbox"
)

// rate is a measured throughput in bits per second.
//
// speedtest-go reports bytes per second (its ByteRate), so the conversion happens once,
// in the runner, and every line above it thinks in bits per second. Everything the table
// prints is fixed to Mbps for the same reason the type exists: an auto-scaled rate (Kbps
// in one row, Gbps in the next) cannot be compared down a column.
type rate float64

// mbps is the rate in megabits per second.
func (r rate) mbps() float64 { return float64(r) / 1e6 }

// String renders the rate in Mbps, the unit the table uses.
func (r rate) String() string {
	return strconv.FormatFloat(r.mbps(), 'f', 2, 64) + " Mbps"
}

// bitsPerSecond converts speedtest-go's ByteRate into this package's unit. The library
// reports bytes per second — its ByteRate.Mbps divides by 125000, the byte count of one
// megabit — so one multiplication is the whole conversion, and it is exact.
func bitsPerSecond(bytesPerSecond float64) rate { return rate(bytesPerSecond * 8) }

// sample is one server's measurements, each value taken from the run that produced it.
type sample struct {
	// latency is the round trip the server answered its ping with.
	latency time.Duration
	// download and upload are the measured throughput in bits per second.
	download rate
	upload   rate
}

// valid rejects a test that produced no reading. speedtest-go reports -1 for a transfer
// whose requests failed past its threshold (its "N/A") and leaves 0 when its counters saw
// nothing; either would be drawn as a real speed if it reached the table.
func (s sample) valid() error {
	switch {
	case s.latency <= 0:
		return errors.New("延迟未测得")
	case s.download <= 0:
		return errors.New("下行未测得")
	case s.upload <= 0:
		return errors.New("上行未测得")
	default:
		return nil
	}
}

// runner is the boundary between this package and speedtest.net: the panel's runner
// talks to the public server list, a test's runner answers from a table. Nothing above
// it opens a connection, which is what keeps `go test` offline.
type runner interface {
	// servers returns the public speedtest.net server list, in any order.
	servers(ctx context.Context) ([]server, error)
	// measure tests one server: latency, then download, then upload. A sample always
	// carries a measurement, because a step that produced no reading is an error and
	// never a zero.
	measure(ctx context.Context, srv server) (sample, error)
}

// newRunner returns the runner the panel uses: speedtest-go against speedtest.net.
// opts is only read for its log, so a progress line the library triggers lands in the
// panel's log rather than on the screen.
func newRunner(opts toolbox.Options) runner {
	return &speedtestRunner{client: newClient(), logf: opts.Logf}
}

// newClient builds the library client, and puts http.DefaultClient back as it was.
//
// speedtest.New() assigns its own RoundTripper to http.DefaultClient.Transport while it
// applies its default configuration, before any option is seen. A panel that later uses
// http.DefaultClient — internal/netutil does — would then send speedtest.net's user agent
// to an unrelated service, so the field is restored inside the call that clobbered it.
// The panel runs one tool at a time, so the single assignment window is not a race it can
// lose.
//
// Nothing in the library writes to the screen of its own accord: its debug logger prints
// to stdout and stays off unless UserConfig.Debug is set, and its progress display lives
// in its own CLI (package main), which the panel does not import. Leaving the default
// configuration alone is therefore the whole of "do not pollute the panel's output"; do
// not set Debug here.
//
// The private client is handed the library's own RoundTripper, because that is the part
// that stamps the user agent speedtest.net expects. The client carries no timeout of its
// own: the tool's whole-run deadline governs the measurement, and a second, invisible
// timeout here would turn a slow line into an unexplained failure.
func newClient() *speedtest.Speedtest {
	client := &http.Client{}
	previous := http.DefaultClient.Transport
	st := speedtest.New(
		speedtest.WithDoer(client),
		func(s *speedtest.Speedtest) { client.Transport = s },
	)
	http.DefaultClient.Transport = previous
	return st
}

// speedtestRunner measures through speedtest-go.
type speedtestRunner struct {
	client *speedtest.Speedtest
	logf   func(string, ...any)

	// mu guards byID. FetchServerListContext hands back server objects that carry the
	// client inside them, and measure receives a plain server value, so the fetched
	// objects are kept here and found again by id.
	mu   sync.Mutex
	byID map[string]*speedtest.Server
}

// servers identifies this host to speedtest.net and then fetches the public list.
//
// The user info call comes first on purpose: it is where the library learns the host's
// coordinates, and it is only with those that it fills in each server's distance and
// sorts the list by it. A list fetched without them carries a distance of zero
// everywhere, and "nearest" would degrade to whatever order the API happened to return.
// A failed lookup only costs the distances, so it is a note, not an error.
func (r *speedtestRunner) servers(ctx context.Context) ([]server, error) {
	info, err := r.client.FetchUserInfoContext(ctx)
	if err != nil {
		r.logf("测速：取得本机坐标失败，节点距离不可用：%v", err)
	} else if where := userSpot(info); where != "" {
		// The nearest servers come from speedtest.net's own idea of where this host
		// is, which is its database and not this panel's: a host it maps to another
		// country gets that country's servers, and the distances in the table are
		// consistent with the place named here, not with the operator's own belief
		// about where the machine sits. Naming the place is what makes an
		// unexpected node list readable instead of wrong-looking.
		r.logf("测速：speedtest.net 把本机定位在 %s，节点按这个坐标就近选取", where)
	}
	list, err := r.client.FetchServerListContext(ctx)
	if err != nil {
		return nil, err
	}
	out := make([]server, 0, len(list))
	byID := make(map[string]*speedtest.Server, len(list))
	for _, s := range list {
		byID[s.ID] = s
		out = append(out, server{
			id:       s.ID,
			name:     s.Name,
			host:     s.Host,
			sponsor:  s.Sponsor,
			distance: s.Distance,
		})
	}
	r.mu.Lock()
	r.byID = byID
	r.mu.Unlock()
	return out, nil
}

// userSpot describes the location speedtest.net assigned to this host out of the fields its
// API filled in, so a run can say which place its "nearest" servers were nearest to.
func userSpot(u *speedtest.User) string {
	if u == nil {
		return ""
	}
	switch {
	case u.Country != "" && u.Isp != "":
		return u.Country + " · " + u.Isp
	case u.Country != "":
		return u.Country
	case u.Lat != "" && u.Lon != "":
		return u.Lat + ", " + u.Lon
	}
	return ""
}

// measure runs the library's three tests against one server, in the order its own CLI
// runs them.
func (r *speedtestRunner) measure(ctx context.Context, srv server) (sample, error) {
	target, err := r.lookup(srv.id)
	if err != nil {
		return sample{}, err
	}
	// The counters live on the client, so without this one server's transfer would be
	// counted into the next server's reading.
	r.client.Reset()
	// The ping is the mean of ten echoes (the library's default), and a server that
	// answers none of them is unreachable: the node ends here.
	if err := target.PingTestContext(ctx, nil); err != nil {
		return sample{}, fmt.Errorf("延迟测量失败：%w", err)
	}
	if err := target.DownloadTestContext(ctx); err != nil {
		return sample{}, fmt.Errorf("下行测量失败：%w", err)
	}
	if err := target.UploadTestContext(ctx); err != nil {
		return sample{}, fmt.Errorf("上行测量失败：%w", err)
	}
	s := sample{
		latency:  target.Latency,
		download: bitsPerSecond(float64(target.DLSpeed)),
		upload:   bitsPerSecond(float64(target.ULSpeed)),
	}
	if err := s.valid(); err != nil {
		return sample{}, err
	}
	return s, nil
}

// lookup finds the fetched server object measure has to run against.
func (r *speedtestRunner) lookup(id string) (*speedtest.Server, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	target, ok := r.byID[id]
	if !ok {
		return nil, fmt.Errorf("服务器列表里没有 id %q", id)
	}
	return target, nil
}
