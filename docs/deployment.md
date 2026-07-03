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
Docker image already wires this into a `HEALTHCHECK`. There are no metrics
endpoints yet; observability is on the [roadmap](../ROADMAP.md).

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

These are compile-time constants in `server/session/session.go`.
