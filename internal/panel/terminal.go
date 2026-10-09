package panel

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"net"
	"net/http"
	"os"
	"os/exec"
	"time"

	"github.com/coder/websocket"
	"github.com/creack/pty"
)

// terminalMessage is the only control frame the browser sends as text. Keystrokes and
// output travel as binary frames, so a resize never races with terminal data.
type terminalMessage struct {
	Type string `json:"type"`
	Cols uint16 `json:"cols"`
	Rows uint16 `json:"rows"`
}

// handleTerminal upgrades a logged-in request to a WebSocket and bridges it to a shell
// running on a pseudo-terminal. It authenticates from the session cookie (a browser
// cannot attach a bearer token to a WebSocket handshake) and, like every other route,
// refuses an anonymous test server outright: a root shell is the last place to relax.
func (s *Service) handleTerminal(w http.ResponseWriter, r *http.Request) {
	if s.opts.AllowAnonymous {
		writeError(w, http.StatusForbidden, "terminal requires authentication")
		return
	}
	if _, ok := s.sessions.lookup(requestToken(r)); !ok {
		writeError(w, http.StatusUnauthorized, "unauthenticated")
		return
	}

	// The panel server puts a read and a write deadline on every request; a hijacked
	// connection keeps them, and a terminal that outlives them would be killed mid-session.
	// The wrapper clears both the moment the WebSocket takes the connection over.
	//
	// The origin check is skipped because the session cookie is SameSite=Strict: a page on
	// another site cannot make the browser attach it to this handshake, so the request would
	// be unauthenticated anyway. That is also what lets the terminal work behind a reverse
	// proxy that rewrites the Host header, which the default check would reject.
	conn, err := websocket.Accept(hijackClearer{ResponseWriter: w}, r, &websocket.AcceptOptions{
		InsecureSkipVerify: true,
	})
	if err != nil {
		s.opts.Log("terminal: accept: " + err.Error())
		return
	}
	defer conn.CloseNow()

	s.serveTerminal(conn)
}

// hijackClearer forwards a hijack and clears the request deadlines that would otherwise
// end a long-lived terminal. It exists because the standard library leaves the deadlines
// on the connection after Hijack returns.
type hijackClearer struct {
	http.ResponseWriter
}

func (h hijackClearer) Hijack() (net.Conn, *bufio.ReadWriter, error) {
	hijacker, ok := h.ResponseWriter.(http.Hijacker)
	if !ok {
		return nil, nil, errors.New("response writer does not support hijacking")
	}
	conn, rw, err := hijacker.Hijack()
	if err == nil {
		_ = conn.SetReadDeadline(time.Time{})
		_ = conn.SetWriteDeadline(time.Time{})
	}
	return conn, rw, err
}

// serveTerminal runs one shell for the life of the connection. A single goroutine writes
// to the socket, so frames never interleave; the caller's goroutine reads them.
func (s *Service) serveTerminal(conn *websocket.Conn) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	shell := terminalShell()
	cmd := exec.Command(shell)
	cmd.Env = append(os.Environ(),
		"TERM=xterm-256color",
		"COLORTERM=truecolor",
		"LANG=C.UTF-8",
	)
	if home, err := os.UserHomeDir(); err == nil {
		cmd.Dir = home
	}

	ptmx, err := pty.Start(cmd)
	if err != nil {
		conn.Close(websocket.StatusInternalError, "cannot start shell")
		return
	}
	defer func() {
		_ = ptmx.Close()
		_ = cmd.Process.Kill()
		_, _ = cmd.Process.Wait()
	}()

	// The shell's output is forwarded to the socket until the shell exits. Closing the
	// socket here is what tells the browser the session ended.
	done := make(chan struct{})
	go func() {
		defer close(done)
		buf := make([]byte, 32*1024)
		for {
			n, readErr := ptmx.Read(buf)
			if n > 0 {
				writeCtx, writeCancel := context.WithTimeout(ctx, 10*time.Second)
				writeErr := conn.Write(writeCtx, websocket.MessageBinary, buf[:n])
				writeCancel()
				if writeErr != nil {
					return
				}
			}
			if readErr != nil {
				_ = conn.Close(websocket.StatusNormalClosure, "")
				return
			}
		}
	}()

	for {
		kind, data, readErr := conn.Read(ctx)
		if readErr != nil {
			break
		}
		switch kind {
		case websocket.MessageBinary:
			if _, writeErr := ptmx.Write(data); writeErr != nil {
				cancel()
				<-done
				return
			}
		case websocket.MessageText:
			var msg terminalMessage
			if json.Unmarshal(data, &msg) == nil && msg.Type == "resize" && msg.Cols > 0 && msg.Rows > 0 {
				_ = pty.Setsize(ptmx, &pty.Winsize{Rows: msg.Rows, Cols: msg.Cols})
			}
		}
	}

	cancel()
	<-done
}

// terminalShell picks the shell a terminal should run: the operator's login shell when it
// exists, then bash, then the POSIX shell every supported host ships.
func terminalShell() string {
	if shell := os.Getenv("SHELL"); shell != "" {
		if info, err := os.Stat(shell); err == nil && !info.IsDir() {
			return shell
		}
	}
	for _, candidate := range []string{"/bin/bash", "/bin/sh"} {
		if _, err := os.Stat(candidate); err == nil {
			return candidate
		}
	}
	return "/bin/sh"
}
