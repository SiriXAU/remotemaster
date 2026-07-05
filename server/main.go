package main

import (
	"context"
	"crypto/subtle"
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

	"github.com/sirixau/remotemaster/server/audit"
	"github.com/sirixau/remotemaster/server/metrics"
	"github.com/sirixau/remotemaster/server/relay"
	"github.com/sirixau/remotemaster/server/session"
)

//go:embed agent
var agentFS embed.FS

// store and joinAttempts are initialized in main from environment-configurable
// limits (see docs/deployment.md); the defaults here keep tests self-contained.
var store = session.NewStore()
var joinAttempts = newAttemptLimiter(8, time.Minute, 5*time.Minute)

// trustProxyHeaders controls whether X-Forwarded-* headers are honored. Enable
// it (TRUST_PROXY_HEADERS=1) only when the server sits behind a reverse proxy
// that sets these headers; otherwise they are client-controlled and unsafe.
var trustProxyHeaders bool

// agentToken, when non-empty (AGENT_TOKEN), is a pre-shared secret agents must
// present to join any session — a leaked 6-digit code alone is then not enough.
var agentToken string

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

	trustProxyHeaders = os.Getenv("TRUST_PROXY_HEADERS") == "1"

	if err := audit.Configure(os.Getenv("AUDIT_LOG")); err != nil {
		log.Fatal(err)
	}
	if audit.Enabled() {
		log.Printf("audit logging enabled (AUDIT_LOG=%s)", os.Getenv("AUDIT_LOG"))
	}

	agentToken = os.Getenv("AGENT_TOKEN")
	if agentToken != "" {
		log.Printf("agent auth: pre-shared token required to join sessions")
	}

	store = session.NewStoreWithTTLs(
		envDuration("PENDING_SESSION_TTL", session.DefaultPendingTTL),
		envDuration("ACTIVE_SESSION_TTL", session.DefaultActiveTTL),
	)
	joinAttempts = newAttemptLimiter(
		envInt("JOIN_ATTEMPT_LIMIT", 8),
		envDuration("JOIN_ATTEMPT_WINDOW", time.Minute),
		envDuration("JOIN_ATTEMPT_BLOCK", 5*time.Minute),
	)
	relay.MaxMessageBytes = envInt64("MAX_MESSAGE_BYTES", relay.MaxMessageBytes)

	sub, err := fs.Sub(agentFS, "agent")
	if err != nil {
		log.Fatal(err)
	}

	mux := http.NewServeMux()
	mux.Handle("/", http.FileServer(http.FS(sub)))
	mux.HandleFunc("/health", healthHandler)
	mux.Handle("/metrics", metrics.Handler(func() (int, int) { return store.Counts() }))
	mux.HandleFunc("/launch.ps1", launchScriptHandler)
	mux.HandleFunc("/ws/client", clientHandler)
	mux.HandleFunc("/ws/agent", agentHandler)

	log.Printf("remotemaster server listening on %s", addr)
	if err := http.ListenAndServe(addr, mux); err != nil {
		log.Fatal(err)
	}
}

// launchScriptHandler serves a PowerShell one-liner bootstrap: downloads the
// latest client EXE and FFmpeg dependency, writes server.txt, and launches it.
// Usage: irm http://<host>/launch.ps1 | iex
func launchScriptHandler(w http.ResponseWriter, r *http.Request) {
	scheme := "ws"
	if r.TLS != nil || (trustProxyHeaders && r.Header.Get("X-Forwarded-Proto") == "https") {
		scheme = "wss"
	}
	host := requestHost(r)
	// serverURL is interpolated into the PowerShell script below, so an
	// unsanitized host (e.g. from a spoofed Host header) could break out of the
	// single-quoted string literal and inject commands into a script users run
	// with `iex`. Reject anything that is not a plain host[:port].
	if !isValidHost(host) {
		http.Error(w, "invalid host", http.StatusBadRequest)
		return
	}
	serverURL := scheme + "://" + host

	const ps1 = `$ErrorActionPreference = 'Stop'
# Windows PowerShell 5.1 redraws the Invoke-WebRequest progress bar for every
# buffer, slowing downloads by an order of magnitude. Suppress it.
$ProgressPreference = 'SilentlyContinue'
[Net.ServicePointManager]::SecurityProtocol = [Net.SecurityProtocolType]::Tls12
$dir = Join-Path $env:LOCALAPPDATA 'RemoteMaster'
New-Item -ItemType Directory -Force -Path $dir | Out-Null
$exe = Join-Path $dir 'remotemaster-client.exe'
$ffmpeg = Join-Path $dir 'ffmpeg.exe'
Write-Host 'Downloading RemoteMaster client...'
Invoke-WebRequest -Uri 'https://github.com/sirixau/remotemaster/releases/download/latest/remotemaster-client.exe' -OutFile $exe -UseBasicParsing
if (-not (Test-Path $ffmpeg)) {
  try {
    $zip = Join-Path $dir 'ffmpeg-release-essentials.zip'
    $extract = Join-Path $dir 'ffmpeg-download'
    if (Test-Path $extract) { Remove-Item -Recurse -Force $extract }
    Write-Host 'Downloading FFmpeg dependency...'
    Invoke-WebRequest -Uri 'https://www.gyan.dev/ffmpeg/builds/ffmpeg-release-essentials.zip' -OutFile $zip -UseBasicParsing
    Expand-Archive -Path $zip -DestinationPath $extract -Force
    $found = Get-ChildItem -Path $extract -Filter 'ffmpeg.exe' -Recurse | Select-Object -First 1
    if ($null -eq $found) { throw 'ffmpeg.exe was not found in the downloaded FFmpeg package' }
    Copy-Item -Path $found.FullName -Destination $ffmpeg -Force
    Remove-Item -Force $zip -ErrorAction SilentlyContinue
    Remove-Item -Recurse -Force $extract -ErrorAction SilentlyContinue
  } catch {
    Write-Warning "FFmpeg download failed; continuing without H.264 (WebP fallback): $($_.Exception.Message)"
  }
}
# Write server.txt without a BOM: Windows PowerShell 5.1's Set-Content
# -Encoding UTF8 prepends a UTF-8 BOM, which corrupts the URL for readers
# that treat the file as plain bytes.
[IO.File]::WriteAllText((Join-Path $dir 'server.txt'), '%s')
Write-Host 'Starting RemoteMaster...'
# Redirect stderr so Go runtime panics (invisible under -H windowsgui) are
# preserved for diagnosis alongside client.log.
Start-Process -FilePath $exe -WorkingDirectory $dir -RedirectStandardError (Join-Path $dir 'client-err.log')
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

	// Disable the per-message read limit immediately after accept. The default
	// nhooyr.io/websocket limit is 32 KiB and is enforced at frame-receipt time,
	// before the relay bridge gets a chance to raise it — so a video frame sent
	// while waiting for the viewer would otherwise close the connection.
	conn.SetReadLimit(-1)

	sess, err := store.Create(conn)
	if err != nil {
		conn.Close(websocket.StatusInternalError, "server error")
		log.Printf("session create: %v", err)
		return
	}

	metrics.SessionsCreated.Add(1)
	log.Printf("client registered code=%s", sess.Code)
	audit.Log(audit.Event{Event: audit.EventSessionCreated, Code: sess.Code, IP: clientIP(r)})

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

	// The rate limiter only counts *failed* join attempts (bad code format or an
	// unknown/already-claimed code) — the signature of a brute-force scan of the
	// code space. A successful join costs nothing, so a legitimate viewer that
	// reconnects to its own session is never locked out.
	ip := clientIP(r)
	if joinAttempts.Blocked(ip) {
		metrics.JoinBlocked.Add(1)
		audit.Log(audit.Event{Event: audit.EventJoinRejected, IP: ip, Reason: "rate_limited"})
		http.Error(w, "too many attempts", http.StatusTooManyRequests)
		return
	}

	code := r.URL.Query().Get("code")
	if !isSixDigitCode(code) {
		joinAttempts.Fail(ip)
		metrics.JoinFailures.Add(1)
		audit.Log(audit.Event{Event: audit.EventJoinRejected, IP: ip, Reason: "bad_code_format"})
		http.Error(w, "code must be 6 digits", http.StatusBadRequest)
		return
	}

	if !agentTokenValid(r) {
		joinAttempts.Fail(ip)
		metrics.JoinFailures.Add(1)
		audit.Log(audit.Event{Event: audit.EventJoinRejected, Code: code, IP: ip, Reason: "bad_token"})
		http.Error(w, "invalid or missing token", http.StatusForbidden)
		return
	}

	conn, err := websocket.Accept(w, r, &websocket.AcceptOptions{
		InsecureSkipVerify: true,
	})
	if err != nil {
		log.Printf("agent ws accept: %v", err)
		return
	}

	// Match clientHandler: remove the default 32 KiB read limit up front.
	conn.SetReadLimit(-1)

	sess, ok := store.Join(code, conn)
	if !ok {
		joinAttempts.Fail(ip)
		metrics.JoinFailures.Add(1)
		audit.Log(audit.Event{Event: audit.EventJoinRejected, Code: code, IP: ip, Reason: "unknown_or_claimed_code"})
		wsjson.Write(bg, conn, wireMsg{Type: "error", Msg: "invalid or already-claimed code"})
		conn.Close(websocket.StatusNormalClosure, "invalid code")
		return
	}

	metrics.SessionsJoined.Add(1)
	log.Printf("agent joined code=%s", code)
	audit.Log(audit.Event{
		Event: audit.EventAgentJoined, Code: code, IP: ip,
		DurationSeconds: sess.JoinedAt.Sub(sess.CreatedAt).Seconds(),
	})

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
		audit.Log(audit.Event{
			Event: audit.EventSessionEnded, Code: code, IP: ip,
			DurationSeconds: time.Since(sess.JoinedAt).Seconds(),
		})
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
			audit.Log(audit.Event{Event: audit.EventClientLost, Code: sess.Code})
			return
		}
	}
}

// agentTokenValid reports whether the request may join sessions. With no
// AGENT_TOKEN configured every request passes; otherwise the token query
// parameter must match, compared in constant time.
func agentTokenValid(r *http.Request) bool {
	if agentToken == "" {
		return true
	}
	got := r.URL.Query().Get("token")
	return subtle.ConstantTimeCompare([]byte(got), []byte(agentToken)) == 1
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

	return strings.EqualFold(u.Host, requestHost(r))
}

// requestHost returns the host the request was addressed to. The forwarded
// header is only honored when the server is explicitly configured to sit behind
// a trusted proxy; otherwise it is client-controlled and must not be trusted.
func requestHost(r *http.Request) string {
	if trustProxyHeaders {
		if h := r.Header.Get("X-Forwarded-Host"); h != "" {
			return h
		}
	}
	return r.Host
}

// isValidHost reports whether host is a plain hostname or host:port containing
// only characters valid in that context (letters, digits, and .-:[]). It is used
// to reject header values before they are interpolated into generated scripts.
func isValidHost(host string) bool {
	if host == "" || len(host) > 255 {
		return false
	}
	for _, c := range host {
		switch {
		case c >= 'a' && c <= 'z', c >= 'A' && c <= 'Z', c >= '0' && c <= '9':
		case c == '.' || c == '-' || c == ':' || c == '[' || c == ']':
		default:
			return false
		}
	}
	return true
}

func clientIP(r *http.Request) string {
	// Only trust X-Forwarded-For behind a configured proxy. Otherwise it is
	// attacker-controlled and would let a single host spoof unlimited distinct
	// keys, defeating the join-attempt rate limiter.
	if trustProxyHeaders {
		if xff := r.Header.Get("X-Forwarded-For"); xff != "" {
			if first := strings.TrimSpace(strings.Split(xff, ",")[0]); first != "" {
				return first
			}
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
	l := &attemptLimiter{
		limit:       limit,
		window:      window,
		block:       block,
		byRequester: make(map[string]attemptState),
	}
	go l.cleanupLoop()
	return l
}

// Blocked reports whether the requester is currently within a block window.
// It does not record an attempt.
func (l *attemptLimiter) Blocked(requester string) bool {
	now := time.Now()

	l.mu.Lock()
	defer l.mu.Unlock()

	state, ok := l.byRequester[requester]
	return ok && now.Before(state.BlockedUntil)
}

// Fail records one failed attempt for the requester, blocking it once the
// per-window limit is exceeded.
func (l *attemptLimiter) Fail(requester string) {
	now := time.Now()

	l.mu.Lock()
	defer l.mu.Unlock()

	state := l.byRequester[requester]
	if now.Before(state.BlockedUntil) {
		return
	}
	if state.WindowStart.IsZero() || now.Sub(state.WindowStart) > l.window {
		state = attemptState{WindowStart: now}
	}

	state.Count++
	if state.Count > l.limit {
		state.BlockedUntil = now.Add(l.block)
	}
	l.byRequester[requester] = state
}

// cleanupLoop periodically evicts entries that are neither blocked nor within an
// active counting window, so the map cannot grow without bound.
func (l *attemptLimiter) cleanupLoop() {
	t := time.NewTicker(l.block)
	defer t.Stop()
	for range t.C {
		now := time.Now()
		l.mu.Lock()
		for requester, state := range l.byRequester {
			if now.After(state.BlockedUntil) && now.Sub(state.WindowStart) > l.window {
				delete(l.byRequester, requester)
			}
		}
		l.mu.Unlock()
	}
}
