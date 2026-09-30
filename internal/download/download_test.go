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
//
// It also pins what the destination looks like afterwards. The bytes go to a temporary
// file that is renamed into place, so a failed download leaves dest exactly as it found
// it - not present, if it was not there - rather than a short file whose only tell is its
// size.
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

	dir := t.TempDir()
	dest := filepath.Join(dir, "easysb-linux-amd64")
	if err := Download(context.Background(), srv.URL+"/easysb-linux-amd64", dest); err == nil {
		t.Fatal("a truncated body must fail the download")
	}
	if _, err := os.Stat(dest); !os.IsNotExist(err) {
		t.Fatalf("a failed download left %s behind (stat: %v)", dest, err)
	}
	// And no temporary beside it: a stale part file would be litter that the next run has
	// to reason about.
	if entries := readDirNames(t, dir); len(entries) != 0 {
		t.Fatalf("a failed download left %v behind", entries)
	}
}

// TestDownloadDoesNotFollowASymlinkAtTheDestination covers the reason the bytes go to a
// file of our own and are renamed into place. os.Create writes through whatever is at
// dest, so a symlink left there directs the download somewhere else - and what is written
// is not a throwaway: the release tarball replaces the running binary, and the kernel
// packages are handed to dpkg as root.
func TestDownloadDoesNotFollowASymlinkAtTheDestination(t *testing.T) {
	body := []byte("downloaded")
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Length", strconv.Itoa(len(body)))
		if _, err := w.Write(body); err != nil {
			t.Errorf("write: %v", err)
		}
	}))
	defer srv.Close()

	dir := t.TempDir()
	victim := filepath.Join(dir, "victim")
	if err := os.WriteFile(victim, []byte("original"), 0o600); err != nil {
		t.Fatalf("write the victim: %v", err)
	}
	dest := filepath.Join(dir, "easysb-linux-amd64")
	if err := os.Symlink(victim, dest); err != nil {
		// Not a platform skip: creating a symlink needs a privilege Windows grants only
		// to an elevated account, so this runs wherever the account has it - the Linux
		// runner among them - instead of being skipped by GOOS and never executed.
		t.Skipf("this account cannot create a symlink: %v", err)
	}

	if err := Download(context.Background(), srv.URL+"/easysb-linux-amd64", dest); err != nil {
		t.Fatalf("Download: %v", err)
	}
	if got, err := os.ReadFile(victim); err != nil || string(got) != "original" {
		t.Fatalf("the symlink target was written through: %q (%v)", got, err)
	}
	info, err := os.Lstat(dest)
	if err != nil {
		t.Fatalf("lstat dest: %v", err)
	}
	if info.Mode()&os.ModeSymlink != 0 {
		t.Fatal("dest is still a symlink, so the download replaced nothing")
	}
	if got, err := os.ReadFile(dest); err != nil || string(got) != string(body) {
		t.Fatalf("dest = %q (%v), want the downloaded bytes", got, err)
	}
	// The temporary is renamed, not copied, so nothing is left for the next run to find.
	if entries := readDirNames(t, dir); len(entries) != 2 {
		t.Fatalf("directory holds %v, want just the victim and dest", entries)
	}
}

func readDirNames(t *testing.T, dir string) []string {
	t.Helper()
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("read %s: %v", dir, err)
	}
	names := make([]string, 0, len(entries))
	for _, e := range entries {
		names = append(names, e.Name())
	}
	return names
}
