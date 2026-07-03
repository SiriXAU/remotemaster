# Deployment

The relay is a single stateless Go binary that serves the agent UI and bridges
WebSocket connections. It keeps all session state in memory, so a single
instance is the simplest deployment. (Horizontal scaling would need shared
session state — see [ROADMAP.md](../ROADMAP.md).)

## Local / trusted network

```bash
docker compose -f deploy/docker-compose.yml up -d
```

This exposes port `8080` directly (`http://host:8080`). Fine for a LAN or behind
a VPN. **Do not** expose this straight to the internet: there is no transport
encryption, so screen frames and keystrokes would travel in the clear.

## Production (TLS)

Terminate TLS in front of the relay. The repo ships an example nginx config at
[`deploy/nginx.conf`](../deploy/nginx.conf).

1. Put your certificate at `deploy/certs/cert.pem` and key at
   `deploy/certs/key.pem`, and set `server_name` in `nginx.conf`.
2. In `deploy/docker-compose.yml`, stop publishing `8080` directly (set
   `ports: []` on the app service), uncomment the `nginx` service, and **set
   `TRUST_PROXY_HEADERS=1`** on the app service's `environment`.
3. `docker compose -f deploy/docker-compose.yml up -d`.

### Why `TRUST_PROXY_HEADERS` matters behind a proxy

When nginx terminates TLS, the Go server sees a plain HTTP request (`r.TLS ==
nil`) coming from the proxy's IP. Two things then depend on forwarded headers,
and the server only reads them when `TRUST_PROXY_HEADERS=1`:

- **`launch.ps1` scheme.** The bootstrap script bakes the relay URL into
  `server.txt`. Without a trusted `X-Forwarded-Proto: https`, the server emits
  `ws://…`, and clients built for a TLS-only relay fail to connect. With it, the
  server emits `wss://…`.
- **Rate-limit key.** The join-attempt limiter keys on the client IP. Without a
  trusted `X-Forwarded-For`, every request appears to come from the proxy, so
  one abusive client could either lock everyone out or (if the limiter keyed on
  the shared IP loosely) be under-counted. With it, the limiter uses the real
  client IP.

The bundled `nginx.conf` sets `X-Forwarded-Proto`, `X-Forwarded-Host`, and
`X-Forwarded-For` for you. If you front the relay with a different proxy
(Caddy, Traefik, a cloud load balancer), make sure it sets the same headers —
and only enable `TRUST_PROXY_HEADERS` when a proxy you control is guaranteed to
overwrite them. If clients can reach the app port directly, leave it unset:
those headers would then be attacker-controlled (host-header injection into the
generated script, rate-limiter bypass via spoofed `X-Forwarded-For`).

### WebSocket timeouts

Sessions are long-lived. The example config sets
`proxy_read_timeout`/`proxy_send_timeout` to 24 h on `/ws/`. Match this on any
proxy or load balancer, or long idle screens will be dropped mid-session.

## Health checks and monitoring

`GET /health` returns `200` with `{"status":"ok","time":"<RFC3339>"}`. The
Docker image already wires this into a `HEALTHCHECK`.

`GET /metrics` exposes Prometheus counters and gauges (text exposition
format, no dependency): pending/active sessions, sessions created and
joined, join failures and rate-limit blocks, and messages/bytes relayed per
direction. The endpoint reveals only aggregate numbers, but if you'd rather
not serve it publicly, block `/metrics` at your reverse proxy and scrape the
app port directly.

### Audit logging

Set `AUDIT_LOG` to `stderr`, `stdout`, or a file path to record session
lifecycle events as JSON lines: `session_created`, `agent_joined` (with how
long the code waited), `join_rejected` (with reason: `rate_limited`,
`bad_code_format`, `bad_token`, `unknown_or_claimed_code`), `session_ended`
(with duration), and `client_lost`. Each record carries the peer IP (subject
to the `TRUST_PROXY_HEADERS` rules above). Session *recording* is not
implemented — this is a who/when/how-long trail, not a what-happened one.

## Client distribution

`/launch.ps1` downloads the client from the repository's rolling `latest` GitHub
release, which CI updates on every push to `main`. If you fork the project,
update the download URL in `launchScriptHandler` (`server/main.go`) and the
release workflow (`.github/workflows/release.yml`) to point at your fork, or the
one-liner will pull upstream binaries.

## Session lifecycle limits

- **Pending session TTL:** 10 minutes. A code with no agent is reclaimed.
- **Active session TTL:** 8 hours. A hard cap on any single session.
- **Pending client probe:** every 30 s the server pings a waiting client and
  reaps it if the ping fails.

## Configuration reference

All limits are tunable via environment variables. Invalid or non-positive
values fall back to the default (with a log line), so a typo can never
disable a safety limit.

### Server

| Variable              | Default    | Meaning                                             |
|-----------------------|------------|-----------------------------------------------------|
| `SERVER_ADDR`         | `:8080`    | Listen address                                      |
| `TRUST_PROXY_HEADERS` | unset      | Set to `1` behind a trusted reverse proxy (above)   |
| `PENDING_SESSION_TTL` | `10m`      | Lifetime of a code with no agent joined             |
| `ACTIVE_SESSION_TTL`  | `8h`       | Hard cap on a joined session                        |
| `JOIN_ATTEMPT_LIMIT`  | `8`        | Failed joins per IP per window before blocking      |
| `JOIN_ATTEMPT_WINDOW` | `1m`       | Window over which failed joins are counted          |
| `JOIN_ATTEMPT_BLOCK`  | `5m`       | How long an IP over the limit stays blocked         |
| `MAX_MESSAGE_BYTES`   | `10485760` | Per-message relay read limit (frames, input)        |
| `AGENT_TOKEN`         | unset      | Pre-shared secret agents must present to join (see [security.md](security.md)) |
| `AUDIT_LOG`           | unset      | Audit destination: `stderr`, `stdout`, or a file path; unset disables |

Durations use Go syntax: `30s`, `10m`, `8h`.

### Client (Windows)

| Variable              | Default | Range   | Meaning                        |
|-----------------------|---------|---------|--------------------------------|
| `REMOTEMASTER_FPS`    | `15`    | 1–60    | Capture/send frame rate        |
| `REMOTEMASTER_QUALITY`| `65`    | 1–100   | WebP encode quality per frame  |

Set these in the environment the client is launched from (out-of-range
values are clamped). Lower both on constrained links; raise FPS on a LAN.
