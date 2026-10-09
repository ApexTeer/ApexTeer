package panel

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/coder/websocket"
)

// TestTerminalRequiresSession ensures a shell is never handed to an unauthenticated
// caller: the WebSocket route checks the session itself, not through require.
func TestTerminalRequiresSession(t *testing.T) {
	opts := testOptions(t)
	opts.AllowAnonymous = false
	svc := New(opts)

	rec := do(t, svc, http.MethodGet, "/api/v1/terminal/ws", "", nil)
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("terminal without a session = %d, want 401", rec.Code)
	}
}

// TestTerminalRefusesAnonymous keeps the test-only anonymous mode from opening a root
// shell: the one route that must always be authenticated does not honour it.
func TestTerminalRefusesAnonymous(t *testing.T) {
	svc := New(testOptions(t))
	rec := do(t, svc, http.MethodGet, "/api/v1/terminal/ws", "", nil)
	if rec.Code != http.StatusForbidden {
		t.Fatalf("anonymous terminal = %d, want 403", rec.Code)
	}
}

// TestTerminalBridgesToShell opens a real session, dials the WebSocket and runs one
// command, proving the PTY bridge carries bytes in both directions.
func TestTerminalBridgesToShell(t *testing.T) {
	if _, err := os.Stat("/bin/sh"); err != nil {
		t.Skip("no shell on this host")
	}
	opts := testOptions(t)
	opts.AllowAnonymous = false
	svc := New(opts)
	token, _, err := svc.sessions.issue("admin")
	if err != nil {
		t.Fatal(err)
	}

	server := httptest.NewServer(svc.Handler())
	defer server.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	url := "ws" + strings.TrimPrefix(server.URL, "http") + "/api/v1/terminal/ws"
	conn, _, err := websocket.Dial(ctx, url, &websocket.DialOptions{
		HTTPHeader: http.Header{"Cookie": []string{sessionCookie + "=" + token}},
	})
	if err != nil {
		t.Fatalf("dial terminal: %v", err)
	}
	defer conn.CloseNow()

	const marker = "EASYSB_TERMINAL_OK"
	// The shell echoes what is typed, so the command is quoted to break the marker up:
	// only the executed output contains the literal string.
	if err := conn.Write(ctx, websocket.MessageBinary, []byte("echo EASY\"\"SB_TERMINAL_OK\n")); err != nil {
		t.Fatalf("write command: %v", err)
	}

	var seen strings.Builder
	for {
		kind, data, err := conn.Read(ctx)
		if err != nil {
			t.Fatalf("reading output (saw %q): %v", seen.String(), err)
		}
		if kind == websocket.MessageBinary {
			seen.Write(data)
			if strings.Contains(seen.String(), marker) {
				return
			}
		}
	}
}
