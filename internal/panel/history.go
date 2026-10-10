package panel

import (
	"context"
	"net/http"
	"strconv"
	"sync"
	"time"

	"github.com/EasySBTeam/EasySB/internal/sysinfo"
)

// The overview trend keeps a short rolling window of host readings in memory, so
// the dashboard charts ride on data the panel itself gathered rather than on a
// second round trip per metric. The interval sets the resolution and the window
// sets how far back a chart reaches.
const (
	historyInterval = 2 * time.Second
	historyWindow   = 120
)

// historySample is one tick of the host: the same readings a 3x-ui-style
// overview draws a tile or a chart from.
type historySample struct {
	at      int64
	cpu     float64
	mem     float64
	swap    float64
	disk    float64
	netUp   float64
	netDown float64
	tcp     int
	udp     int
}

// historyRecorder samples the host on a fixed cadence into a bounded slice. Each
// rate is a difference from the previous sample, so the recorder owns the
// previous counter values; nothing else has to remember them.
type historyRecorder struct {
	mu      sync.Mutex
	samples []historySample

	started bool
	// previous counter readings, for the deltas that become rates.
	prevBusy, prevTotal uint64
	prevRx, prevTx      uint64
	prevAt              time.Time
}

// newHistoryRecorder returns an empty recorder. It samples nothing until run is
// called, so constructing a Service in a test never starts a background loop.
func newHistoryRecorder() *historyRecorder {
	return &historyRecorder{}
}

// run samples once immediately, then on every tick until ctx is cancelled. It is
// the only writer of the sample set.
func (h *historyRecorder) run(ctx context.Context) {
	h.collect(time.Now())
	ticker := time.NewTicker(historyInterval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case now := <-ticker.C:
			h.collect(now)
		}
	}
}

// collect appends one sample and trims the window. It is safe to call directly
// so a test can seed the buffer without waiting on a ticker.
func (h *historyRecorder) collect(now time.Time) {
	busy, total := sysinfo.CPUJiffies()
	memUsed, memTotal, swapUsed, swapTotal, diskUsed, diskTotal := sysinfo.Usage()
	rx, tx := sysinfo.NetworkTotals()
	tcp, udp := sysinfo.SocketCounts()

	s := historySample{
		at:   now.Unix(),
		cpu:  cpuPercent(h.prevBusy, h.prevTotal, busy, total),
		mem:  ratioPercent(memUsed, memTotal),
		swap: ratioPercent(swapUsed, swapTotal),
		disk: ratioPercent(diskUsed, diskTotal),
		tcp:  tcp,
		udp:  udp,
	}
	if !h.prevAt.IsZero() {
		if seconds := now.Sub(h.prevAt).Seconds(); seconds > 0 {
			s.netDown = counterRate(h.prevRx, rx, seconds)
			s.netUp = counterRate(h.prevTx, tx, seconds)
		}
	}

	h.prevBusy, h.prevTotal = busy, total
	h.prevRx, h.prevTx, h.prevAt = rx, tx, now

	h.mu.Lock()
	h.samples = append(h.samples, s)
	if len(h.samples) > historyWindow {
		h.samples = h.samples[len(h.samples)-historyWindow:]
	}
	h.mu.Unlock()
}

// snapshot copies the newest window samples into arrays the handler can encode.
func (h *historyRecorder) snapshot(window int) historyResponse {
	h.mu.Lock()
	defer h.mu.Unlock()
	samples := h.samples
	if window > 0 && len(samples) > window {
		samples = samples[len(samples)-window:]
	}
	resp := historyResponse{
		IntervalSecs: int(historyInterval / time.Second),
		Capacity:     historyWindow,
		Count:        len(samples),
		Times:        make([]int64, 0, len(samples)),
		Series: historySeries{
			CPU:     make([]float64, 0, len(samples)),
			Mem:     make([]float64, 0, len(samples)),
			Swap:    make([]float64, 0, len(samples)),
			Disk:    make([]float64, 0, len(samples)),
			NetUp:   make([]float64, 0, len(samples)),
			NetDown: make([]float64, 0, len(samples)),
			TCP:     make([]int, 0, len(samples)),
			UDP:     make([]int, 0, len(samples)),
		},
	}
	for _, s := range samples {
		resp.Times = append(resp.Times, s.at)
		resp.Series.CPU = append(resp.Series.CPU, s.cpu)
		resp.Series.Mem = append(resp.Series.Mem, s.mem)
		resp.Series.Swap = append(resp.Series.Swap, s.swap)
		resp.Series.Disk = append(resp.Series.Disk, s.disk)
		resp.Series.NetUp = append(resp.Series.NetUp, s.netUp)
		resp.Series.NetDown = append(resp.Series.NetDown, s.netDown)
		resp.Series.TCP = append(resp.Series.TCP, s.tcp)
		resp.Series.UDP = append(resp.Series.UDP, s.udp)
	}
	return resp
}

// historyResponse is the wire shape of /api/v1/system/history: one shared time
// axis and the series that ride on it.
type historyResponse struct {
	IntervalSecs int           `json:"intervalSecs"`
	Capacity     int           `json:"capacity"`
	Count        int           `json:"count"`
	Times        []int64       `json:"times"`
	Series       historySeries `json:"series"`
}

// historySeries is the per-metric array set. Percentages are 0-100, net rates
// are bytes per second, and the socket counts are whole numbers.
type historySeries struct {
	CPU     []float64 `json:"cpu"`
	Mem     []float64 `json:"mem"`
	Swap    []float64 `json:"swap"`
	Disk    []float64 `json:"disk"`
	NetUp   []float64 `json:"netUp"`
	NetDown []float64 `json:"netDown"`
	TCP     []int     `json:"tcp"`
	UDP     []int     `json:"udp"`
}

// handleHistory returns the rolling host trend the overview charts draw. The
// optional window query trims the reply to the newest N samples.
func (s *Service) handleHistory(w http.ResponseWriter, r *http.Request) {
	window := historyWindow
	if n, err := strconv.Atoi(r.URL.Query().Get("window")); err == nil && n > 0 && n <= historyWindow {
		window = n
	}
	writeJSON(w, http.StatusOK, s.history.snapshot(window))
}

// cpuPercent is the busy share of the jiffies elapsed between two readings. A
// missing or rewound counter reports zero rather than a negative percentage.
func cpuPercent(prevBusy, prevTotal, busy, total uint64) float64 {
	if prevTotal == 0 || total <= prevTotal {
		return 0
	}
	busyDelta := safeDelta(prevBusy, busy)
	totalDelta := total - prevTotal
	if totalDelta == 0 {
		return 0
	}
	return clampPercent(float64(busyDelta) / float64(totalDelta) * 100)
}

// ratioPercent is used/total as a 0-100 percentage, or zero when the total is
// unknown (for example a host with no swap).
func ratioPercent(used, total uint64) float64 {
	if total == 0 {
		return 0
	}
	return clampPercent(float64(used) / float64(total) * 100)
}

// counterRate is the per-second rate between two counter readings, tolerating a
// counter that went backwards (a reset) by reporting zero.
func counterRate(prev, current uint64, seconds float64) float64 {
	if current < prev || seconds <= 0 {
		return 0
	}
	return float64(current-prev) / seconds
}

func safeDelta(prev, current uint64) uint64 {
	if current < prev {
		return 0
	}
	return current - prev
}

func clampPercent(v float64) float64 {
	if v < 0 {
		return 0
	}
	if v > 100 {
		return 100
	}
	return v
}
