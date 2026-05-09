package main

import (
	"context"
	"embed"
	"encoding/json"
	"fmt"
	"io/fs"
	"log"
	"net/http"
	"os"
	"time"

	"nhooyr.io/websocket"
	"nhooyr.io/websocket/wsjson"

	"github.com/sirixau/remotemaster/server/relay"
	"github.com/sirixau/remotemaster/server/session"
)

//go:embed agent
var agentFS embed.FS

var store = session.NewStore()

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
	conn, err := websocket.Accept(w, r, &websocket.AcceptOptions{
		InsecureSkipVerify: true, // origin checks handled by nginx in production
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

	// The HTTP handler returns here. The WebSocket connection is hijacked and
	// stays open. The agent handler will call relay.Bridge to drive both conns
	// once an agent connects.
}

// agentHandler validates the session code, joins the session, notifies the
// client, and starts the bidirectional relay bridge.
func agentHandler(w http.ResponseWriter, r *http.Request) {
	code := r.URL.Query().Get("code")
	if len(code) != 6 {
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
		log.Printf("agent joined write: %v", err)
		return
	}

	// Notify the waiting client
	wsjson.Write(bg, sess.ClientConn, wireMsg{Type: "agent_connected"})

	// Bridge both connections — blocks until one side disconnects.
	// This keeps the agentHandler goroutine alive to own the agent conn.
	relay.Bridge(bg, sess, func() {
		store.Remove(code)
		log.Printf("session ended code=%s", code)
	})
}
