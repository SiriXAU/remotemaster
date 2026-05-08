# remotemaster

Ultra-lightweight on-demand remote support tool. A client runs a single EXE, gets a 6-digit code, and shares it with a support agent who connects via a browser.

## Architecture

```
Client EXE (Windows)  ──WebSocket──▶  Relay Server  ◀──WebSocket──  Agent Browser
  screen capture                       (Go + WS)                      canvas viewer
  input injection                      code routing                   mouse/keyboard
  Win32 native UI
```

All traffic is relayed through a self-hosted server. No P2P, no NAT traversal complexity.

## Quick start

### 1. Run the relay server

```bash
docker compose -f deploy/docker-compose.yml up -d
```

The agent web UI is served at `http://localhost:8080`.

### 2. Build the client EXE

```bash
# For development (connects to localhost)
./build/build-client.sh

# For production
SERVER_URL=wss://yourdomain.com ./build/build-client.sh
```

Output: `dist/remotemaster-client.exe` (~6 MB, no install required)

### 3. Connect

1. Client runs `remotemaster-client.exe` — a small window shows a 6-digit code
2. Agent opens `http://yourdomain.com` in a browser
3. Agent enters the 6-digit code → screen appears, full mouse/keyboard control

## Project layout

```
client/          Windows client (Go, pure — no CGo)
  capture/       GDI screen capture via Win32 syscalls
  input/         SendInput mouse/keyboard injection
  ui/            Native Win32 floating window
  relay/         WebSocket client, frame loop, input dispatch

server/          Relay server (Go)
  session/       6-digit code registry
  relay/         Bidirectional WebSocket bridge
  agent/         Agent web UI (HTML/JS, embedded into server binary)

deploy/          Docker Compose + optional nginx TLS config
build/           Cross-compile scripts
dist/            Build output (gitignored)
```

## macOS client (future)

The `capture/` and `input/` packages define Go interfaces. Adding macOS support means implementing `capture_darwin.go` (using `CGImageCreateScreenShot`) and `input_darwin.go` (using `CGEventPost`) with `//go:build darwin` build tags.

## Configuration

The relay server URL is baked into the client EXE at build time:

```bash
SERVER_URL=wss://yourdomain.com ./build/build-client.sh
```

The server listens on the address set by the `SERVER_ADDR` environment variable (default `:8080`).
