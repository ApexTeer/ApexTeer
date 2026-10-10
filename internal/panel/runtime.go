package panel

import (
	"net"
	"net/http"
	"os"
	"runtime"
	"strconv"
	"strings"
	"sync/atomic"
	"time"
)

// procStart is when this process began serving, which the runtime endpoint
// reports as uptime.
var procStart = time.Now()

// activeConns counts the connections the panel's HTTP server currently holds,
// including hijacked ones (a live terminal is removed from the server's tracking
// when it is hijacked, so that transition is a decrement too). It lets the
// runtime endpoint report the process's real connection load instead of guessing.
var activeConns atomic.Int64

// trackConn is installed as the HTTP server's ConnState hook.
func trackConn(_ net.Conn, state http.ConnState) {
	switch state {
	case http.StateNew:
		activeConns.Add(1)
	case http.StateClosed, http.StateHijacked:
		activeConns.Add(-1)
	}
}

// handleRuntime reports the panel process's own footprint: uptime, live
// connections, goroutines and memory. It is read-only, so the dashboard can poll
// it to build a performance baseline without paying for a full host snapshot. It
// deliberately reports this process only: the core runs in its own process, and a
// counter from there would be a different process's number wearing the panel's
// label.
func (s *Service) handleRuntime(w http.ResponseWriter, r *http.Request) {
	var mem runtime.MemStats
	runtime.ReadMemStats(&mem)
	writeJSON(w, http.StatusOK, map[string]any{
		"uptimeSecs":  int64(time.Since(procStart) / time.Second),
		"connections": activeConns.Load(),
		"goroutines":  runtime.NumGoroutine(),
		"gcCount":     mem.NumGC,
		"rssBytes":    residentBytes(),
		"mem": map[string]uint64{
			"allocBytes":      mem.Alloc,
			"heapBytes":       mem.HeapAlloc,
			"heapSysBytes":    mem.HeapSys,
			"sysBytes":        mem.Sys,
			"totalAllocBytes": mem.TotalAlloc,
		},
	})
}

// residentBytes reads this process's resident set size from /proc. It returns 0
// where /proc is unavailable, which the caller reads as "not reported" rather
// than as a real zero.
func residentBytes() uint64 {
	data, err := os.ReadFile("/proc/self/statm")
	if err != nil {
		return 0
	}
	fields := strings.Fields(string(data))
	if len(fields) < 2 {
		return 0
	}
	pages, err := strconv.ParseUint(fields[1], 10, 64)
	if err != nil {
		return 0
	}
	return pages * uint64(os.Getpagesize())
}
