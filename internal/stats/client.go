package stats

import (
	"context"
	"strings"
	"time"

	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
)

// CallTimeout bounds one stats round trip. The service is on loopback, so a slow
// reply means the core is busy or wedged and the cycle should be skipped rather
// than block the accounting loop.
const CallTimeout = 5 * time.Second

// Usage is an upload/download pair in bytes.
type Usage struct {
	Upload   int64
	Download int64
}

// Counters is the traffic of every account since the core started, keyed by the
// core user name, which EasySB sets to <token>@<node id>.
type Counters map[string]Usage

// Counter is a source of absolute counters; *Reader is the production
// implementation and tests supply their own.
type Counter interface {
	Counters(ctx context.Context, users []string) (Counters, error)
	Close() error
}

// Reader samples the core's stats service over gRPC.
type Reader struct {
	conn *grpc.ClientConn
}

// Dial prepares a reader for an endpoint such as 127.0.0.1:10085. The connection
// is established on the first call, so a core that is still starting up does not
// fail the panel.
func Dial(addr string) (*Reader, error) {
	conn, err := grpc.NewClient(addr, grpc.WithTransportCredentials(insecure.NewCredentials()))
	if err != nil {
		return nil, err
	}
	return &Reader{conn: conn}, nil
}

// Close releases the connection.
func (r *Reader) Close() error {
	return r.conn.Close()
}

// Counters returns the counters of the listed users. The request asks for every
// counter and filters locally, so one round trip serves any number of accounts
// and a user the core has not counted yet simply reads as zero.
func (r *Reader) Counters(ctx context.Context, users []string) (Counters, error) {
	ctx, cancel := context.WithTimeout(ctx, CallTimeout)
	defer cancel()

	var out rawMessage
	if err := r.conn.Invoke(ctx, methodQueryStats, queryStatsRequest(), &out, grpc.ForceCodec(rawCodec{})); err != nil {
		return nil, err
	}
	stats, err := parseQueryStatsResponse(out.body)
	if err != nil {
		return nil, err
	}

	wanted := make(map[string]bool, len(users))
	for _, name := range users {
		wanted[name] = true
	}
	counters := Counters{}
	for _, s := range stats {
		name, direction, ok := splitCounter(s.name)
		if !ok || !wanted[name] {
			continue
		}
		usage := counters[name]
		if direction == uplinkTag {
			usage.Upload = s.value
		} else {
			usage.Download = s.value
		}
		counters[name] = usage
	}
	return counters, nil
}

// splitCounter splits "user>>>token>>>traffic>>>uplink" into its user name and
// direction, and reports whether the name is a per-user traffic counter at all.
func splitCounter(name string) (string, string, bool) {
	if !strings.HasPrefix(name, userPrefix) {
		return "", "", false
	}
	rest := name[len(userPrefix):]
	switch {
	case strings.HasSuffix(rest, uplinkTag):
		return strings.TrimSuffix(rest, uplinkTag), uplinkTag, true
	case strings.HasSuffix(rest, downlinkTag):
		return strings.TrimSuffix(rest, downlinkTag), downlinkTag, true
	default:
		return "", "", false
	}
}
