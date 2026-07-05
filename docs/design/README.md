# RemoteMaster Design Docs & Agent TODO Index

This directory holds the implementation designs for the remaining
[ROADMAP.md](../../ROADMAP.md) features, plus the task registry an agent (human
or AI) uses to pick one up and ship it. Each task is scoped to land as its own
PR with tests, the way the first roadmap batch did.

The already-shipped items (project hygiene, configurable limits, `/metrics`,
`AGENT_TOKEN`, audit logging, clipboard sync, keyboard mapping, dirty-region
WebP streaming, and adaptive WebP quality) are **not** listed here — see the
git history and the user-facing docs.

## How to pick up a task

1. **Claim** a task from the [registry](#task-registry) whose dependencies are
   all `done`. Prefer low-effort, high-value, unblocked tasks (see the
   [suggested order](#suggested-order)).
2. **Read** the linked design section end to end. Each has: goal, wire-format
   changes, integration points (with file references), implementation steps,
   testing, acceptance criteria, and an effort estimate.
3. **Check the [protocol tag registry](#binary-protocol-tag-registry)** before
   using any new binary message tag, so parallel work doesn't collide. If you
   consume a tag, strike it from "free" in your PR.
4. **Build & test both modules** (`server/` and `client/`) — `go vet ./...` and
   `go test ./...` in each. For Windows/macOS/Linux client code, at least
   compile for the target `GOOS` (mingw for windows/amd64 is already wired in
   CI); note anything that needs real-hardware manual verification in the PR.
5. **Keep the relay and protocol backward-compatible.** The relay is
   codec-agnostic and copies bytes verbatim; unknown JSON `type`s and unknown
   binary tags are already ignored by both endpoints, so additive messages are
   safe. Don't repurpose an existing tag.
6. **One feature per PR**, with a design-doc reference in the description and
   docs (`docs/protocol.md`, `docs/deployment.md`, `docs/security.md`) updated
   alongside the code.

## Task registry

Task IDs are stable; reference them in branches, commits, and PRs (e.g.
`RM-CAP-2`). "Effort" is rough: S <= ~1 focused PR, M = 1–2, L = multi-PR.

| ID | Feature | Theme | Effort | Depends on | Design |
|----|---------|-------|--------|-----------|--------|
| RM-STREAM-1 | DXGI Desktop Duplication capture | Streaming | L | — | [streaming-performance](streaming-performance.md#rm-stream-1--dxgi-desktop-duplication-capture) |
| RM-STREAM-3 | Adaptive FPS / resolution | Streaming | M | — | [streaming-performance](streaming-performance.md#rm-stream-3--adaptive-fps--resolution) |
| RM-CAP-1 | Multi-monitor support | Capabilities | M–L | — | [capabilities](capabilities.md#rm-cap-1--multi-monitor-support) |
| RM-CAP-2 | File transfer | Capabilities | L | SEC-1 (consent, ideal) | [capabilities](capabilities.md#rm-cap-2--file-transfer) |
| RM-CAP-3 | Session chat / annotations | Capabilities | S–M | — | [capabilities](capabilities.md#rm-cap-3--session-chat--annotations) |
| RM-SEC-1 | Client consent + control indicator | Security | M | — | [security-trust](security-trust.md#rm-sec-1--explicit-client-side-consent--control-indicator) |
| RM-SEC-2 | One-time / expiring signed codes | Security | M | — | [security-trust](security-trust.md#rm-sec-2--one-time--expiring-signed-codes) |
| RM-SEC-3 | End-to-end encryption | Security | L | SEC-2 (PAKE), SEC-1 (SAS) | [security-trust](security-trust.md#rm-sec-3--end-to-end-encryption) |
| RM-PLAT-1 | macOS client | Platform | L | — (main/ui refactor) | [platform-reach](platform-reach.md#rm-plat-1--macos-client) |
| RM-PLAT-2 | Linux client (X11) | Platform | L | — | [platform-reach](platform-reach.md#rm-plat-2--linux-client) |
| RM-PLAT-2b | Linux client (Wayland/PipeWire) | Platform | L | PLAT-2 | [platform-reach](platform-reach.md#rm-plat-2--linux-client) |
| RM-PLAT-3 | Signed Windows binaries | Platform | S–M | cert procurement | [platform-reach](platform-reach.md#rm-plat-3--signed-windows-binaries) |
| RM-OPS-1 | Horizontal scale (multi-replica) | Operability | L | — | [operability-scale](operability-scale.md#rm-ops-1--horizontal-scale-multi-replica-relay) |

### Status

All tasks are **`todo`**. This table is the source of truth; a PR that lands a
task should flip its row (add a ✅ and the PR number) so the next agent sees
current state. Nothing here is claimed yet.

## Suggested order

Value-to-effort, respecting dependencies. Independent tracks can run in
parallel (they touch different files):

1. **RM-SEC-1 (consent + indicator)** — highest trust value, no deps, and it
   unblocks safe file transfer.
2. **RM-CAP-3 (chat)** — small, self-contained, useful; annotations as a
   follow-up.
3. **RM-STREAM-3 (adaptive FPS / resolution)** — pure-Go controller, big UX
   win on bad links.
4. **RM-SEC-2 (expiring codes)** — small crypto, sets up SEC-3.
5. **RM-OPS-1 stage 1 (Backend interface refactor)** — safe refactor, no
   behavior change, unlocks Redis later.
6. **RM-CAP-1 (multi-monitor)** and **RM-PLAT-3 (signing)** — independent.
7. **RM-STREAM-1 (DXGI)** — the big remaining streaming track.
8. **RM-CAP-2 (file transfer)** — after SEC-1.
9. **RM-PLAT-1/2 (macOS/Linux)** — parallel platform track; start with the
    portable `main`/`ui` refactor called out in PLAT-1.
10. **RM-SEC-3 (E2E)** — last of the security track; highest risk, reuses
    SEC-1's out-of-band channel and SEC-2's shared secret.

## Binary protocol tag registry

Byte 0 of every binary WebSocket message. Keep this table authoritative;
**claim the next free tag in your PR** and update `docs/protocol.md` in the same
change. All multi-byte fields are big-endian. The relay copies every message
verbatim, so tags are shared across both directions — do not reuse a number.

| Tag | Name | Status | Direction | Defined in |
|-----|------|--------|-----------|-----------|
| `0x01` | WebP frame | **shipped** | client → agent | `docs/protocol.md` |
| `0x02`–`0x07` | input events (move/down/up/scroll/key↓/key↑) | **shipped** | agent → client | `docs/protocol.md` |
| `0x08`–`0x09` | reserved: removed encoded-video path | **reserved** | client → agent | `client/relay/proto.go` |
| `0x0A` | clipboard text | **shipped** | both | `docs/protocol.md` |
| `0x0B` | reserved: removed encoded-video path | **reserved** | client → agent | `client/relay/proto.go` |
| `0x0C` | WebP region | **shipped** | client → agent | `docs/protocol.md` |
| `0x0D`–`0x0E` | **free** | — | — | — |
| `0x0F` | annotation / pointer | *proposed* RM-CAP-3 | agent → client | [capabilities](capabilities.md#annotations--0x0f-pointer) |
| `0x10` | E2E envelope (wraps any tag) | *proposed* RM-SEC-3 | both | [security-trust](security-trust.md#wire-format) |
| `0x11` | file offer | *proposed* RM-CAP-2 | sender → receiver | [capabilities](capabilities.md#wire-format--0x110x120x13) |
| `0x12` | file chunk | *proposed* RM-CAP-2 | sender → receiver | [capabilities](capabilities.md#wire-format--0x110x120x13) |
| `0x13` | file control | *proposed* RM-CAP-2 | receiver → sender | [capabilities](capabilities.md#wire-format--0x110x120x13) |
| `0x14`+ | **free** | — | — | — |

> Proposed tags are reserved by their design doc but not yet implemented. If you
> implement a different feature first and need a tag, take `0x14`+ rather than a
> reserved one, and update this table.

### JSON control message types

Session control rides the same sockets as JSON text frames
(`{"type": ...}`). Shipped types are in `docs/protocol.md`
(`registered`, `joined`, `agent_connected`, `agent_disconnected`, `disconnect`,
`error`). Proposed additions, all relayed verbatim and safely ignored by
endpoints that don't know them:

| type | Task | Purpose |
|------|------|---------|
| `monitors`, `select_monitor` | RM-CAP-1 | monitor list / switch |
| `chat` | RM-CAP-3 | text chat both directions |
| `consent` | RM-SEC-1 | client approval result |
| `stat` | RM-STREAM-3 | viewer decode-queue / RTT feedback |
| `e2e_hello` | RM-SEC-3 | key-exchange handshake |

## Conventions recap

- Interfaces are the platform/backend seams: `capture.Capturer`,
  `input.Injector`, `clipboard.Clipboard`, and the proposed `session.Backend`.
  Add capabilities as **optional** extension interfaces consumers type-assert
  for (e.g. `DirtyCapturer`, `MultiCapturer`) so existing implementations keep
  compiling.
- Env vars gate every new operational behavior and default to today's; see the
  reference table in `docs/deployment.md` and follow the same
  fall-back-to-default-on-bad-value rule (`server/env.go`,
  `client/relay/env.go`).
- New logic ships with pure-Go unit tests; platform code that can't run in CI
  is at least compiled for its `GOOS` and its portable helpers (mapping tables,
  math, protocol codec) are unit-tested.
