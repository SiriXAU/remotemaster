# Design: Operability — Horizontal Scale

Covers the remaining *Operability* roadmap item: **horizontal scale**.
(Metrics and configurable limits already shipped.) Read the
[design index](README.md) first for task IDs and the pickup workflow.

---

## RM-OPS-1 — Horizontal scale (multi-replica relay)

### Goal
Session state lives in process memory (`server/session/session.go`
`Store.sessions` map), so the relay is single-instance: a second replica has no
idea about codes registered on the first, and a client and agent that land on
different replicas can never bridge. Enable running more than one relay behind a
load balancer.

### The core problem
A session bridge needs the **client WebSocket and the agent WebSocket held by
the same process** — `relay.Bridge` copies bytes between two live
`*websocket.Conn`s. A shared store of metadata is necessary but **not
sufficient**: if the two sockets terminate on different replicas, something has
to carry bytes between those replicas. So the design has two layers.

### Layer 1 — shared session directory (necessary)
Replace/augment the in-memory map with a shared store keyed by code:

- **Redis** (roadmap's suggestion): `SET code -> {ownerNode, state, createdAt}`
  with a TTL matching `PENDING_SESSION_TTL`. `Create` writes; `Join` does an
  atomic claim (`SET NX` on an `agentNode` field or a Lua CAS) so only one agent
  can win a code across all replicas — the multi-node version of today's
  single-agent guarantee.
- Abstract behind a `session.Backend` interface so the in-memory store stays the
  default and the zero-dependency path:
  ```go
  type Backend interface {
      Create(code string, meta Meta) error
      ClaimAgent(code, node string) (Meta, bool, error) // atomic
      Get(code string) (Meta, bool, error)
      Remove(code string) error
  }
  ```
- `REMOTEMASTER_SESSION_BACKEND=memory|redis`, `REDIS_URL=...`. Unset ⇒ memory,
  exactly today.

### Layer 2 — cross-node bridging (the hard half)
Once the directory is shared, handle the client and agent being on different
replicas. Two viable strategies:

- **(A) Sticky routing (simpler).** Make the load balancer route both
  participants of a code to the same replica. The client registers and gets a
  code from node N; the agent must reach node N. Options: encode the node in the
  code/URL (`?node=` or a subdomain) so the LB/ingress pins it, or use
  consistent-hashing on the code at an L7 proxy. No inter-node data path needed —
  the store is only a directory + claim. **Recommended first step.**
- **(B) Inter-node relay (fully elastic).** If sockets land on different nodes,
  bridge over a backplane: the owning node subscribes to a Redis Pub/Sub (or
  NATS) channel per code and the other node forwards frames/input onto it.
  This doubles hops and adds the backplane's throughput as a bottleneck for
  high-bitrate video — measure before committing. Only needed if sticky routing
  is impossible in the target environment.

Prefer (A). Document (B) as the escape hatch for environments that can't do
sticky L7 routing.

### Server changes
- Introduce `session.Backend`; make `Store` the `memory` implementation of it.
- `main.go` selects the backend from env; the handlers call the interface.
- The `expireLoop` becomes a no-op for Redis (TTLs handle expiry); keep it for
  memory.
- `/metrics` gauges (`sessions_pending/active`) become per-replica; add a note
  that operators should aggregate across replicas in Prometheus (a `sum by`).
  Cross-node totals would need the store to expose counts — optional.

### Failure & correctness concerns
- **Split brain / claim races:** the agent claim MUST be atomic in Redis
  (`SET NX` / Lua) — this is the multi-node analogue of the locked
  check-then-insert in `Store.Join`. Unit-test the CAS semantics against a Redis
  test double or `miniredis`.
- **Node death:** if the owning replica dies mid-session, both sockets drop and
  the client reconnects (its existing back-off) to get a fresh code — acceptable.
  The Redis entry expires via TTL.
- **TTL alignment:** Redis key TTL must track `PENDING_SESSION_TTL`/
  `ACTIVE_SESSION_TTL`; refresh the TTL on join.

### Testing
- Unit-test the `Backend` interface against both the memory impl and `miniredis`
  (in-process Redis fake) — same test suite, two backends, proving the atomic
  claim and TTL behavior.
- Integration: two relay processes + one Redis + sticky routing; confirm a
  client on N1 and agent routed to N1 bridge, and that an agent hitting N2 for a
  code owned by N1 is redirected (strategy A) rather than silently failing.

### Acceptance criteria
- `REMOTEMASTER_SESSION_BACKEND=memory` (default) is byte-for-byte today's
  behavior with zero new dependencies.
- With Redis + ≥2 replicas + sticky routing, sessions established across the
  fleet work; a code can be claimed by exactly one agent fleet-wide.
- No regression in the single-instance path; the memory store still passes its
  existing tests.

### Effort
Large, and partly an infrastructure/deployment design as much as code. Land it
in stages: (1) extract the `Backend` interface with the memory impl (pure
refactor, no behavior change, fully covered by existing tests); (2) add the
Redis backend + atomic claim behind env selection; (3) document sticky routing
in `docs/deployment.md` and add the compose example. Stage 1 is a safe,
valuable refactor even if Redis never lands.
