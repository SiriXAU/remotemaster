package main

import (
	"context"
	"embed"
	"encoding/json"
	"fmt"
	"io/fs"
	"log"
	"net"
	"net/http"
	"net/url"
	"os"
	"strings"
	"sync"
	"time"

	"nhooyr.io/websocket"
	"nhooyr.io/websocket/wsjson"

	"github.com/sirixau/remotemaster/server/relay"
	"github.com/sirixau/remotemaster/server/session"
)

//go:embed agent
var agentFS embed.FS

var store = session.NewStore()
var joinAttempts = newAttemptLimiter(8, time.Minute, 5*time.Minute)

// bg is used for WebSocket operations after the HTTP handler returns.
// WebSocket connections are hijacked and outlive the request context.
var bg = context.Background()

type wireMsg struct {
	Type string `json:"type"`
	Code string `json:"code,omitempty"`
	Msg  string `json:"msg,omitempty"`
}

func main() {
	addr := os.Getenv("SERVER_ADDR")
	if addr == "" {
		addr = ":8080"
	}

	sub, err := fs.Sub(agentFS, "agent")
	if err != nil {
		log.Fatal(err)
	}

	mux := http.NewServeMux()
	mux.Handle("/", http.FileServer(http.FS(sub)))
	mux.HandleFunc("/health", healthHandler)
	mux.HandleFunc("/launch.ps1", launchScriptHandler)
	mux.HandleFunc("/ws/client", clientHandler)
	mux.HandleFunc("/ws/agent", agentHandler)

	log.Printf("remotemaster server listening on %s", addr)
	if err := http.ListenAndServe(addr, mux); err != nil {
		log.Fatal(err)
	}
}

// launchScriptHandler serves a PowerShell one-liner bootstrap: downloads the
// latest client EXE from GitHub Releases, writes server.txt, and launches it.
// Usage: irm http://<host>/launch.ps1 | iex
func launchScriptHandler(w http.ResponseWriter, r *http.Request) {
	scheme := "ws"
	if r.TLS != nil || r.Header.Get("X-Forwarded-Proto") == "https" {
		scheme = "wss"
	}
	host := r.Header.Get("X-Forwarded-Host")
	if host == "" {
		host = r.Host
	}
	serverURL := scheme + "://" + host

	const ps1 = `$ErrorActionPreference = 'Stop'
[Net.ServicePointManager]::SecurityProtocol = [Net.SecurityProtocolType]::Tls12
$dir = Join-Path $env:LOCALAPPDATA 'RemoteMaster'
New-Item -ItemType Directory -Force -Path $dir | Out-Null
$exe = Join-Path $dir 'remotemaster-client.exe'
Write-Host 'Downloading RemoteMaster client...'
Invoke-WebRequest -Uri 'https://github.com/sirixau/remotemaster/releases/download/latest/remotemaster-client.exe' -OutFile $exe -UseBasicParsing
'%s' | Set-Content -Path (Join-Path $dir 'server.txt') -NoNewline -Encoding UTF8
Write-Host 'Starting RemoteMaster...'
Start-Process -FilePath $exe -WorkingDirectory $dir
`
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	fmt.Fprintf(w, ps1, serverURL)
}

func healthHandler(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]string{
		"status": "ok",
		"time":   time.Now().UTC().Format(time.RFC3339),
	})
}

// clientHandler accepts the client's WebSocket, assigns a session code, sends
// it back, and then waits for an agent to join before handing off to the relay.
func clientHandler(w http.ResponseWriter, r *http.Request) {
	if !allowWebSocketOrigin(r) {
		http.Error(w, "forbidden origin", http.StatusForbidden)
		return
	}

	conn, err := websocket.Accept(w, r, &websocket.AcceptOptions{
		InsecureSkipVerify: true,
	})
	if err != nil {
		log.Printf("client ws accept: %v", err)
		return
	}

	sess, err := store.Create(conn)
	if err != nil {
		conn.Close(websocket.StatusInternalError, "server error")
		log.Printf("session create: %v", err)
		return
	}

	log.Printf("client registered code=%s", sess.Code)

	if err := wsjson.Write(bg, conn, wireMsg{Type: "registered", Code: sess.Code}); err != nil {
		store.Remove(sess.Code)
		log.Printf("client registered write: %v", err)
		return
	}

	go monitorPendingClient(sess)

	// The HTTP handler returns here. The WebSocket connection is hijacked and
	// stays open. The agent handler will call relay.Bridge to drive both conns
	// once an agent connects.
}

// agentHandler validates the session code, joins the session, notifies the
// client, and starts the bidirectional relay bridge.
func agentHandler(w http.ResponseWriter, r *http.Request) {
	if !allowWebSocketOrigin(r) {
		http.Error(w, "forbidden origin", http.StatusForbidden)
		return
	}
	if !joinAttempts.Allow(clientIP(r)) {
		http.Error(w, "too many attempts", http.StatusTooManyRequests)
		return
	}

	code := r.URL.Query().Get("code")
	if !isSixDigitCode(code) {
		http.Error(w, "code must be 6 digits", http.StatusBadRequest)
		return
	}

	conn, err := websocket.Accept(w, r, &websocket.AcceptOptions{
		InsecureSkipVerify: true,
	})
	if err != nil {
		log.Printf("agent ws accept: %v", err)
		return
	}

	sess, ok := store.Join(code, conn)
	if !ok {
		wsjson.Write(bg, conn, wireMsg{Type: "error", Msg: "invalid or already-claimed code"})
		conn.Close(websocket.StatusNormalClosure, "invalid code")
		return
	}

	log.Printf("agent joined code=%s", code)

	// Tell agent it's joined
	if err := wsjson.Write(bg, conn, wireMsg{Type: "joined", Code: code}); err != nil {
		store.Remove(code)
		sess.ClientConn.Close(websocket.StatusNormalClosure, "agent gone")
		conn.Close(websocket.StatusInternalError, "server error")
		log.Printf("agent joined write: %v", err)
		return
	}

	sess.WaitPendingProbe()

	// Notify the waiting client
	if err := wsjson.Write(bg, sess.ClientConn, wireMsg{Type: "agent_connected"}); err != nil {
		store.Remove(code)
		log.Printf("client notify failed code=%s: %v", code, err)
		wsjson.Write(bg, conn, wireMsg{Type: "error", Msg: "client is no longer connected"})
		conn.Close(websocket.StatusNormalClosure, "client gone")
		sess.ClientConn.Close(websocket.StatusNormalClosure, "client gone")
		return
	}

	// Bridge both connections — blocks until one side disconnects.
	// This keeps the agentHandler goroutine alive to own the agent conn.
	relay.Bridge(bg, sess, func() {
		store.Remove(code)
		log.Printf("session ended code=%s", code)
	})
}

func monitorPendingClient(sess *session.Session) {
	t := time.NewTicker(30 * time.Second)
	defer t.Stop()

	for {
		select {
		case <-sess.Joined():
			return
		case <-t.C:
		}

		joined, err := sess.ProbePending(func() error {
			pingCtx, cancel := context.WithTimeout(bg, 5*time.Second)
			defer cancel()
			return sess.ClientConn.Ping(pingCtx)
		})
		if joined {
			return
		}
		if err != nil {
			store.Remove(sess.Code)
			sess.ClientConn.Close(websocket.StatusNormalClosure, "client disconnected")
			log.Printf("pending client disconnected code=%s: %v", sess.Code, err)
			return
		}
	}
}

func isSixDigitCode(code string) bool {
	if len(code) != 6 {
		return false
	}
	for _, r := range code {
		if r < '0' || r > '9' {
			return false
		}
	}
	return true
}

func allowWebSocketOrigin(r *http.Request) bool {
	origin := r.Header.Get("Origin")
	if origin == "" {
		return true
	}

	u, err := url.Parse(origin)
	if err != nil {
		return false
	}

	host := r.Header.Get("X-Forwarded-Host")
	if host == "" {
		host = r.Host
	}
	return strings.EqualFold(u.Host, host)
}

func clientIP(r *http.Request) string {
	if xff := r.Header.Get("X-Forwarded-For"); xff != "" {
		if first := strings.TrimSpace(strings.Split(xff, ",")[0]); first != "" {
			return first
		}
	}
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err == nil {
		return host
	}
	return r.RemoteAddr
}

type attemptLimiter struct {
	mu          sync.Mutex
	limit       int
	window      time.Duration
	block       time.Duration
	byRequester map[string]attemptState
}

type attemptState struct {
	WindowStart  time.Time
	Count        int
	BlockedUntil time.Time
}

func newAttemptLimiter(limit int, window, block time.Duration) *attemptLimiter {
	return &attemptLimiter{
		limit:       limit,
		window:      window,
		block:       block,
		byRequester: make(map[string]attemptState),
	}
}

func (l *attemptLimiter) Allow(requester string) bool {
	now := time.Now()

	l.mu.Lock()
	defer l.mu.Unlock()

	state := l.byRequester[requester]
	if now.Before(state.BlockedUntil) {
		return false
	}
	if state.WindowStart.IsZero() || now.Sub(state.WindowStart) > l.window {
		state = attemptState{WindowStart: now}
	}

	state.Count++
	if state.Count > l.limit {
		state.BlockedUntil = now.Add(l.block)
		l.byRequester[requester] = state
		return false
	}

	l.byRequester[requester] = state
	return true
}
