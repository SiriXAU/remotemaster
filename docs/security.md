# Security Model

RemoteMaster grants full mouse/keyboard control of a machine to whoever holds a
6-digit session code. Treat it accordingly: it is a "read the code to someone
you trust for the duration of a call" tool, not a hardened multi-tenant remote
access service. This document describes what the current implementation defends
against and what it deliberately does not.

## Trust model

- The **client** trusts whoever is given the 6-digit code. There is currently no
  in-client prompt to approve a specific agent — as soon as an agent joins,
  control begins. An explicit consent prompt is on the [roadmap](../ROADMAP.md).
- The **relay** is fully trusted: it sees every frame and keystroke in plaintext.
  End-to-end encryption between client and agent is not implemented; confidentiality
  depends on running your own relay and putting TLS in front of it.
- The **agent** trusts the relay to route it to the right client by code.

## What the server defends against

- **Brute-forcing the code space.** `/ws/agent` join attempts are rate-limited
  per client IP (`newAttemptLimiter(8, 1m, 5m)` in `server/main.go`): more than
  8 *failed* joins in a minute blocks that IP for 5 minutes. Only failures count,
  so a legitimate viewer reconnecting to its own live session is never locked
  out. The 6-digit space is 900,000 codes; the limiter makes an online scan
  impractical, but see "Known limitations" below.
- **Code prediction.** Codes come from `crypto/rand`, not `math/rand`, and are
  allocated under a lock so two clients cannot be issued the same code.
- **Cross-site WebSocket hijacking.** WebSocket upgrades are rejected unless the
  `Origin` header host matches the request host (`allowWebSocketOrigin`). A
  missing `Origin` (non-browser client) is allowed, which is what the native
  client relies on.
- **Host-header injection into `launch.ps1`.** The generated PowerShell embeds
  the request host in a string that users pipe into `iex`. The handler rejects
  any host that is not a plain `host[:port]` (`isValidHost`) so a spoofed `Host`
  header cannot break out of the string and inject commands.
- **Untrusted forwarded headers.** `X-Forwarded-Proto/Host/For` are ignored
  unless `TRUST_PROXY_HEADERS=1`. This prevents a direct-connecting attacker from
  spoofing the client IP (to bypass the rate limiter) or the host (to poison the
  script) when no proxy is in front. See [deployment.md](deployment.md).
- **Resource exhaustion.** Pending sessions expire after 10 minutes, active ones
  after 8 hours, and pending clients are pinged every 30 s and reaped on failure,
  so dead connections and unclaimed codes do not accumulate. The rate-limiter map
  is garbage-collected on a timer.
- **Oversized frames vs. tiny read limits.** The read limit is disabled at accept
  time and set to 10 MiB during the bridge, chosen deliberately rather than left
  at the 32 KiB default (which would silently kill frames) or unbounded (a memory
  DoS).

## Known limitations / non-goals (today)

- **No transport encryption by default.** Without a TLS-terminating proxy,
  frames and keystrokes are plaintext on the wire. Always deploy behind TLS for
  anything beyond a trusted LAN.
- **No agent authentication.** Anyone who obtains a live code — by shoulder-surfing,
  social engineering, or interception on an unencrypted link — can take over.
- **No end-to-end encryption.** A compromised or malicious relay can observe and
  inject input.
- **Rate limiting is per source IP and in-memory.** An attacker with many source
  IPs (a botnet) faces weaker limits, and limits are not shared across multiple
  relay instances.
- **No audit logging or session recording.** The server logs connect/disconnect
  events only.
- **Single-instance only.** Session state is in process memory.

## Recommendations for operators

1. Always run behind TLS (`docs/deployment.md`), and set `TRUST_PROXY_HEADERS=1`
   only when a proxy you control overwrites the forwarded headers.
2. Keep sessions short; read codes out of band and start the agent promptly.
3. Restrict who can reach the agent UI (VPN, IP allowlist, or an auth proxy) if
   you need more than code-based gating.
4. If you fork, re-point the `launch.ps1` download URL at your own release so
   clients do not fetch upstream binaries (`docs/deployment.md`).

## Reporting

See [`SECURITY.md`](../SECURITY.md) at the repository root. In short: use
GitHub's private vulnerability reporting (Security tab → Report a
vulnerability) and do not post exploit details in public issues.
