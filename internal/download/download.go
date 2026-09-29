// Package download is the one HTTP-to-file path left in the panel.
//
// It stands alone because both of the panel's remaining downloads want the same
// behaviour — stream to a file, report the bytes to the interface, give up after a
// budget: the panel's own release (internal/update) and the BBR kernel packages
// (internal/bbr). The sing-box core is no longer among them: it is a compiled-in
// module requirement, so there is no core tarball to fetch, unpack or install.
package download

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"os"
	"path"
	"time"
)

// Progress reports a download in flight: the file being fetched, how many bytes have
// arrived and the size the server announced (0 when it sends no length). It is called
// from the goroutine doing the download, at most every downloadTick.
type Progress func(label string, done, total int64)

// downloadTick is how often a download reports itself. Ten readings a second is smooth
// on a terminal and costs nothing next to a 100 MB kernel package.
const downloadTick = 100 * time.Millisecond

// countingBody wraps a response body and reports how much of it has been read.
type countingBody struct {
	body   io.Reader
	report Progress
	label  string
	total  int64
	done   int64
	last   time.Time
}

func (c *countingBody) Read(b []byte) (int, error) {
	n, err := c.body.Read(b)
	if n > 0 {
		c.done += int64(n)
		now := time.Now()
		if c.last.IsZero() || now.Sub(c.last) >= downloadTick {
			c.last = now
			c.emit()
		}
	}
	return n, err
}

// emit sends one reading, if there is a listener. The reader runs on the download
// goroutine, so the callback has to be cheap and has to tolerate being called from
// there.
func (c *countingBody) emit() {
	if c.report != nil {
		c.report(c.label, c.done, c.total)
	}
}

// Download streams a URL to dest, with the budget a panel release needs.
func Download(ctx context.Context, url, dest string) error {
	return WithProgress(ctx, url, dest, 5*time.Minute, nil)
}

// WithProgress is the whole download path: it streams url to dest and reports
// the bytes as they arrive to progress. A nil progress means the caller only wants the
// file, which is what the non-interactive entry points do.
func WithProgress(ctx context.Context, url, dest string, timeout time.Duration, progress Progress) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return err
	}
	req.Header.Set("User-Agent", "EasySB")

	client := &http.Client{Timeout: timeout}
	resp, err := client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("download %s: %s", url, resp.Status)
	}

	f, err := os.Create(dest)
	if err != nil {
		return err
	}
	body := &countingBody{body: resp.Body, report: progress, label: path.Base(url), total: resp.ContentLength}
	if _, err := io.Copy(f, body); err != nil {
		f.Close()
		return err
	}
	// The last tick may have been up to downloadTick before the end, so the finished
	// download reports its final size rather than a percentage short of 100.
	body.emit()
	// Sync alone is not enough to call the file written: a filesystem is free to report
	// a deferred write error only when the descriptor is closed (a full disk on an
	// overlay or a network mount, for one), and this is the path that replaces the
	// running binary. A truncated download has to fail here rather than be installed.
	if err := f.Sync(); err != nil {
		f.Close()
		return err
	}
	return f.Close()
}
