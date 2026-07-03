# remotemaster

Ultra-lightweight on-demand remote support tool. A user on the machine that
needs help runs a single EXE (no install), gets a 6-digit code, and reads it to
a support agent. The agent enters the code in a browser and gets a live view
with full mouse and keyboard control.

## Architecture

```
Client EXE (Windows)  ──WebSocket──▶  Relay Server  ◀──WebSocket──  Agent Browser
  GDI screen capture                   (Go + WS)                      canvas viewer
  SendInput injection                  6-digit code routing           mouse/keyboard
  native Win32 window                  bidirectional bridge           WebCodecs-ready
```

All traffic is relayed through a self-hosted server. There is no P2P and no NAT
traversal: both the client and the agent make outbound WebSocket connections to
the relay, which pairs them by session code and copies bytes between them.

## Quick start

### 1. Run the relay server

```bash
docker compose -f deploy/docker-compose.yml up -d
```

The agent web UI is served at `http://localhost:8080`. Health check:
`curl http://localhost:8080/health`.

For a production deployment with TLS, a reverse proxy, and the
`TRUST_PROXY_HEADERS` setting, see [`docs/deployment.md`](docs/deployment.md).

### 2. Get the client onto the target machine

The relay serves a PowerShell bootstrap at `/launch.ps1`. On the Windows machine
to be controlled, open PowerShell and run:

```powershell
irm https://yourdomain.com/launch.ps1 | iex
```

This downloads the latest signed-in-time client build to
`%LOCALAPPDATA%\RemoteMaster`, writes a `server.txt` pointing back at your relay,
and launches the client. The home page of the relay shows this exact one-liner
(pre-filled with its own origin) plus a "Download launch.ps1" fallback and a
direct EXE link.

Alternatively, build the client yourself (see [Building](#building)) and place a
`server.txt` containing your relay URL (e.g. `wss://yourdomain.com`) next to the
EXE.

### 3. Connect

1. The client window shows a 6-digit code (or `NOCONN` if it cannot reach the
   relay, `------` while idle).
2. The agent opens `https://yourdomain.com` and enters the 6-digit code.
3. The remote screen appears with full mouse and keyboard control. Either side
   can end the session; the client also has an **End Session** button.

## How a session works

1. **Client connects** to `/ws/client`. The server allocates an unused 6-digit
   code (CSPRNG) and returns it as a `registered` JSON message. The client shows
   the code and waits.
2. While waiting, the server pings the pending client every 30 s so a client
   that quietly disappears is reaped instead of holding a code (10-minute
   pending TTL).
3. **Agent connects** to `/ws/agent?code=XXXXXX`. The server validates the code,
   attaches the agent to the session, tells both sides, and starts a
   bidirectional byte bridge.
4. **Streaming.** The client captures the primary screen (GDI `BitBlt`), skips
   frames identical to the last one (FNV-1a hash of the full frame), encodes
   changed frames as WebP, and pushes them at up to 15 fps. The browser decodes
   each frame to a `<canvas>`.
5. **Input.** The agent's browser sends mouse/keyboard events as compact binary
   messages; the client injects them with the Win32 `SendInput` API.
6. **Teardown.** When either side disconnects, the relay closes both connections
   and frees the code. Active sessions also have an 8-hour hard TTL.

The wire protocol (video + input message formats and JSON control messages) is
documented in [`docs/protocol.md`](docs/protocol.md).

## Project layout

```
client/          Windows client (Go, Win32 syscalls + CGo WebP encoder)
  capture/       GDI screen capture via Win32 syscalls (reused DC/bitmap buffers)
  input/         SendInput mouse/keyboard injection
  ui/            Native Win32 floating window (code display + End Session)
  relay/         WebSocket client, capture loop, input dispatch, wire protocol
server/          Relay server (Go)
  session/       6-digit code registry, TTLs, pending-client probing
  relay/         Bidirectional WebSocket bridge
  agent/         Agent web UI (HTML/JS, embedded into the server binary)
deploy/          Docker Compose, Dockerfile, optional nginx TLS config
build/           Cross-compile scripts
docs/            Protocol, deployment, security, and H.264 design notes
dist/            Build output (gitignored)
```

## Building

### Server

```bash
./build/build-server.sh        # → dist/remotemaster-server
```

Or just use the Docker image (see Quick start).

### Client EXE

Cross-compiled from Linux/macOS with mingw-w64 (the WebP encoder uses CGo):

```bash
# Development (connects to ws://localhost:8080)
./build/build-client.sh

# Production — bake the relay URL into the EXE
SERVER_URL=wss://yourdomain.com ./build/build-client.sh
```

Output: `dist/remotemaster-client.exe` (~6 MB, no install required).

CI (`.github/workflows/release.yml`) builds the EXE on every push to `main` and
publishes it to a rolling `latest` pre-release, which is what `/launch.ps1`
downloads. Pushing a `vX.Y.Z` tag cuts a numbered release.

## Configuration

### Client

The relay URL is resolved at runtime in this order:

1. First command-line argument, if it starts with `ws` (e.g.
   `remotemaster-client.exe wss://host`).
2. `server.txt` in the same directory as the EXE.
3. The value baked in at build time via `-X main.RelayServer=...`
   (`SERVER_URL` in `build-client.sh`), default `ws://localhost:8080`.

### Server

| Variable | Default | Purpose |
| --- | --- | --- |
| `SERVER_ADDR` | `:8080` | Listen address. |
| `TRUST_PROXY_HEADERS` | unset | When `1`, honor `X-Forwarded-Proto/Host/For`. Set this **only** behind a trusted reverse proxy — see [`docs/security.md`](docs/security.md). |

## Security model

Sessions are gated by a 6-digit code, so treat this as a "share a code with
someone you trust for the length of a call" tool, not a hardened multi-tenant
service. The relay adds brute-force protection (rate-limited join attempts),
same-origin checks on WebSocket upgrades, host-header validation for the
generated PowerShell script, CSPRNG codes, and session TTLs. Traffic is only
encrypted if you terminate TLS in front of the relay. Read
[`docs/security.md`](docs/security.md) before exposing it to the internet.

## Documentation

- [`docs/protocol.md`](docs/protocol.md) — WebSocket wire protocol reference.
- [`docs/deployment.md`](docs/deployment.md) — production deployment with TLS.
- [`docs/security.md`](docs/security.md) — threat model and hardening notes.
- [`docs/h264-streaming.md`](docs/h264-streaming.md) — the planned H.264 path.
- [`ROADMAP.md`](ROADMAP.md) — planned features and improvements.

## Roadmap highlights

The viewer and wire protocol already carry a WebCodecs-ready H.264 message
format; the next codec step is a Media Foundation encoder on the client. Other
planned work includes multi-monitor capture, adaptive bitrate, clipboard/file
transfer, an explicit client consent prompt, and macOS/Linux clients. See
[`ROADMAP.md`](ROADMAP.md).
