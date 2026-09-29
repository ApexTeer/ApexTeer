package download

import (
	"bytes"
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"sync"
	"testing"
	"time"
)

// TestDownloadReportsProgress covers the readings the panel draws its download bar
// from: a local server so the test stays offline, and the readings have to end on the
// whole file rather than a tick short of it.
func TestDownloadReportsProgress(t *testing.T) {
	body := bytes.Repeat([]byte("x"), 1<<20)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Length", strconv.Itoa(len(body)))
		if _, err := w.Write(body); err != nil {
			t.Errorf("write: %v", err)
		}
	}))
	defer srv.Close()

	dest := filepath.Join(t.TempDir(), "easysb-linux-amd64")
	var mu sync.Mutex
	var readings []int64
	var label string
	var total int64
	err := WithProgress(context.Background(), srv.URL+"/easysb-linux-amd64", dest, time.Minute,
		func(l string, done, size int64) {
			mu.Lock()
			defer mu.Unlock()
			readings = append(readings, done)
			label, total = l, size
		})
	if err != nil {
		t.Fatalf("WithProgress: %v", err)
	}
	mu.Lock()
	defer mu.Unlock()
	if len(readings) == 0 {
		t.Fatal("no progress readings")
	}
	if last := readings[len(readings)-1]; last != int64(len(body)) {
		t.Fatalf("last reading is %d bytes, want %d", last, len(body))
	}
	if total != int64(len(body)) {
		t.Fatalf("announced total is %d, want %d", total, len(body))
	}
	if label != "easysb-linux-amd64" {
		t.Fatalf("label is %q, want the file name", label)
	}
	got, err := os.ReadFile(dest)
	if err != nil {
		t.Fatalf("read back: %v", err)
	}
	if !bytes.Equal(got, body) {
		t.Fatalf("downloaded %d bytes, want %d", len(got), len(body))
	}
}

// TestDownloadRefusesAnErrorStatus keeps a failure page from being installed as if it
// were the file: the caller only ever sees a complete body.
func TestDownloadRefusesAnErrorStatus(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, "gone", http.StatusNotFound)
	}))
	defer srv.Close()

	dest := filepath.Join(t.TempDir(), "easysb-linux-amd64")
	if err := Download(context.Background(), srv.URL+"/easysb-linux-amd64", dest); err == nil {
		t.Fatal("a 404 must fail the download")
	}
}

// TestDownloadRefusesATruncatedBody is the property the self-update leans on: a body
// that stops short of the length it announced is not a file, and reporting success here
// means installing a half-written binary over the running one.
func TestDownloadRefusesATruncatedBody(t *testing.T) {
	body := bytes.Repeat([]byte("x"), 64<<10)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Length", strconv.Itoa(len(body)))
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write(body[:len(body)/4])
		// Drop the connection rather than finishing the response, which is what the
		// client sees as a short read.
		panic(http.ErrAbortHandler)
	}))
	defer srv.Close()

	dest := filepath.Join(t.TempDir(), "easysb-linux-amd64")
	if err := Download(context.Background(), srv.URL+"/easysb-linux-amd64", dest); err == nil {
		t.Fatal("a truncated body must fail the download")
	}
}
