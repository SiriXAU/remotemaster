# Roadmap

Planned features and improvements for RemoteMaster, grouped by theme and roughly
ordered by value-to-effort within each. Items reference the code they touch so
they can be picked up directly. This is a living document — nothing here is a
commitment, and priorities will shift with real usage.

> **Implementation designs & task index:** the unbuilt items below have
> detailed, agent-ready designs (wire formats, integration points, tests,
> acceptance criteria) in [`docs/design/`](docs/design/README.md). Start there
> to pick up a task — it carries the task registry, protocol-tag registry, and
> suggested ordering.

## Streaming & performance

- ~~H.264 encode on the client~~ *(dropped)* — An FFmpeg-backed H.264 path
  was built and field-tested, then removed in favor of dirty-region WebP
  with adaptive quality: for desktop content WebP won on latency, text
  sharpness, and zero external dependencies. Revisit only if low-bandwidth
  WAN sessions become a priority.
- **DXGI Desktop Duplication capture** — Replace GDI `BitBlt`
  (`client/capture/capture_windows.go`) with Desktop Duplication. It avoids the
  per-frame GDI copy and, crucially, exposes dirty/move rectangles so only
  changed regions are captured and encoded — replacing the CPU-side frame
  diff and cutting the ~25 ms per-frame capture cost.
- **Adaptive bitrate / FPS** — Currently a fixed 15 fps at WebP quality 65
  (`client/relay/client.go`). Back off resolution, FPS, or bitrate when the
  WebSocket send buffer, encoder queue, or the browser's
  `VideoDecoder.decodeQueueSize` grows. Enables usable sessions on poor links.
- **Region/dirty-rect frame diffing** — Even before DXGI, the full-frame FNV
  hash could be replaced with tiled hashing so only changed tiles are re-encoded
  and sent, cutting bandwidth on mostly-static screens.

## Capabilities

- **Multi-monitor support** — Capture is hard-wired to the primary display via
  `GetSystemMetrics(SM_CXSCREEN/SM_CYSCREEN)`. Enumerate monitors, let the agent
  pick one (or see a virtual desktop spanning all), and carry a monitor id in the
  frame/config messages.
- **Clipboard sync** — Bidirectional text clipboard is a high-value, low-cost
  addition: two new control messages plus `SetClipboardData`/`navigator.clipboard`.
- **File transfer** — Chunked file push/pull over the existing binary channel,
  scoped to the session. Useful for delivering fixes to the remote machine.
- **Robust keyboard mapping** — `control.js` sends `KeyboardEvent.keyCode`, which
  is deprecated and only coincidentally lines up with Windows VK codes for the
  common set. Move to `event.code` → VK translation so non-US layouts and modifier
  combos (e.g. Ctrl+Alt+Del handling) behave correctly.
- **Session chat / annotations** — A lightweight text channel and on-screen
  pointer for the agent to guide the user.

## Security & trust

- **Explicit client-side consent** — Prompt the user in the client window to
  approve an incoming agent before control begins, and surface an always-visible
  "someone is controlling this machine" indicator. See
  [`docs/security.md`](docs/security.md).
- **One-time / expiring codes and agent auth** — Optional pre-shared token or
  short-lived signed code so a leaked 6-digit code alone is not enough.
- **End-to-end encryption** — Key exchange between client and agent so a
  compromised relay cannot read frames or inject input. Removes the "relay is
  fully trusted" assumption.
- **Audit logging** — Structured logs (and optional session recording) of who
  connected to which code, when, and for how long.

## Platform reach

- **macOS client** — Implement `capture_darwin.go` (`CGDisplayCreateImage` /
  ScreenCaptureKit) and `input_darwin.go` (`CGEventPost`) behind the existing
  `capture.Capturer` / `input.Injector` interfaces and `//go:build darwin` tags.
- **Linux client** — X11 (XShm) or PipeWire/portal capture and `uinput`/XTest
  injection, same interface pattern.
- **Signed Windows binaries** — Authenticode-sign the client EXE in CI so
  SmartScreen and AV do not flag the `launch.ps1` download.

## Operability

- **Metrics endpoint** — Prometheus `/metrics` (active sessions, join
  successes/failures, bytes relayed, frame rate) alongside the existing
  `/health`. See [`docs/deployment.md`](docs/deployment.md).
- **Horizontal scale** — Session state is in-process
  (`server/session/session.go`), so the relay is single-instance. A shared store
  (Redis) plus sticky or cross-node bridging would allow more than one replica.
- **Configurable limits** — Promote the compile-time TTLs, FPS, quality, and
  rate-limit parameters to environment variables so operators can tune them
  without rebuilding.

## Project hygiene

- **LICENSE** — No license file is present; add one to clarify usage terms.
- **CONTRIBUTING + SECURITY** — Contribution guide and a real security-reporting
  contact ([`docs/security.md`](docs/security.md) currently notes the gap).
- **Automated tests in CI** — `go test ./...` for both modules is not yet wired
  into the release workflow; add a test job. Unit tests exist for the session
  store, relay proto, and server helpers.
- **Version alignment** — `server/go.mod` pins `go 1.22` while `go.work`, the
  client module, and the Docker build target `go 1.25`; align them.
