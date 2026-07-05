# Design: Streaming & Performance

Covers the roadmap's remaining *Streaming & performance* work beyond the
shipped dirty-region WebP baseline: **DXGI Desktop Duplication capture** and
**adaptive FPS / resolution**. Read the [design index](README.md) first for
the task IDs, protocol-tag registry, and pickup workflow.

Current baseline (for reference):

- Capture: GDI `BitBlt` of the primary display, whole frame every tick
  (`client/capture/capture_windows.go`).
- Change detection: full-frame FNV-1a hash skips identical frames; changed
  frames are diffed against the previous frame and emitted as dirty-region
  WebP (`0x0C`) when possible.
- Encode/send: WebP quality cap 65, up to 25 fps, with per-frame quality
  adaptation and periodic full-frame (`0x01`) refresh.

---

## RM-STREAM-1 — DXGI Desktop Duplication capture

### Goal
Replace per-frame GDI `BitBlt` + `GetDIBits` with the DXGI Desktop Duplication
API (`IDXGIOutputDuplication`). This removes a full CPU-side copy per frame,
delivers frames as GPU textures, and — the real prize — exposes **dirty and
move rectangles** so downstream stages only touch changed regions. It is the
natural capture source for replacing the current CPU-side pixel diff.

### Interface
Keep the existing `capture.Capturer` interface intact so the WebP path is
unaffected. Add an optional richer capability that dirty-rect-aware consumers
can probe for:

```go
// capture/capture.go
type Frame struct {
    Image      image.Image        // full frame (may reuse a buffer, as today)
    Dirty      []image.Rectangle  // changed regions since the previous frame
    Moved      []MoveRect         // scroll/move hints from the compositor
    Accumulated bool              // true if dirty info was lost (treat as full)
}

type MoveRect struct{ Src image.Point; Dst image.Rectangle }

// Implemented by capturers that can report changed regions. Consumers type-
// assert for it and fall back to full-frame handling when absent.
type DirtyCapturer interface {
    Capturer
    CaptureFrame() (*Frame, error)
}
```

### Windows implementation notes
- **Prefer a small C shim over hand-driven COM.** CI already cross-compiles the
  client with mingw + cgo (for WebP), and mingw-w64 ships `d3d11.h` /
  `dxgi1_2.h`. Driving the COM vtables from pure Go via `syscall` is possible
  but verbose and error-prone; a thin C shim is the more maintainable path:
  ```
  client/capture/dxgi_windows.go   // Go wrapper, implements the interfaces below
  client/capture/dxgi_shim.c       // C: init / acquire / release / teardown
  client/capture/dxgi_shim.h
  ```
  The shim owns the COM lifetime: `D3D11CreateDevice` (with BGRA support),
  `IDXGIOutput1::DuplicateOutput`, a `D3D11_USAGE_STAGING` CPU-read texture, and
  one `AcquireFrame(timeoutMs, out *FrameInfo)` call that does the whole
  acquire → copy-to-staging → map → release cycle and hands Go a pixel pointer,
  row pitch, and the dirty/move rects. Keeping acquire+release inside a single
  shim call means Go can't hold the frame wrong (`AcquireNextFrame` must be
  paired with `ReleaseFrame` before the next acquire).
- If you deliberately avoid cgo here, the alternative is the pure-Go
  `syscall`-vtable approach — budget significantly more time and test COM
  reference counting carefully.
- **Pitch ≠ width×4:** the staging texture's mapped rows are padded; copy row by
  row into the `NRGBA` buffer, converting `DXGI_FORMAT_B8G8R8A8_UNORM` (BGRA) →
  RGBA with the same swap pattern as `capture_windows.go`.
- **Rotated outputs** (`DXGI_MODE_ROTATION_*`): either rotate during copy-out or
  explicitly fall back to GDI in v1 — document whichever you pick in code.
- `AcquireNextFrame` returns a `DXGI_OUTDUPL_FRAME_INFO` plus the desktop
  image as an `ID3D11Texture2D`. Read `GetFrameDirtyRects` and
  `GetFrameMoveRects` for the change regions.
- To get pixels to the CPU (WebP path), copy the texture into a
  `D3D11_USAGE_STAGING` texture and `Map` it.
- **Timeout semantics:** `AcquireNextFrame` with a timeout returning
  `DXGI_ERROR_WAIT_TIMEOUT` means "no change" — this is the DXGI-native
  equivalent of today's hash-equal skip, and is cheaper (no capture, no hash).
- **Loss recovery:** `DXGI_ERROR_ACCESS_LOST` (mode change, secure desktop,
  full-screen app takeover, laptop GPU switch) requires tearing down and
  re-creating the duplication. Handle it by returning a sentinel so the caller
  rebuilds — mirror how `GDICapturer.Capture` calls `release()` on `BitBlt`
  failure today.
- **UAC / secure desktop:** Desktop Duplication cannot capture the secure
  desktop (UAC prompt, Ctrl+Alt+Del, lock screen). The stream will freeze on
  the last frame there. Document it; do not try to work around it.

### Selection & fallback
- Add `REMOTEMASTER_CAPTURE=dxgi|gdi|auto` (default `auto`). `auto` tries DXGI
  and falls back to GDI on construction error, logging which path is live.
- GDI stays the default-safe fallback for RDP sessions and old GPUs where
  duplication is unavailable.

### Testing
- Pure-Go unit tests for any rect-merge / accumulation helper (e.g. clamping
  dirty rects to bounds, coalescing overlapping rects).
- Manual: run on a real Windows box, confirm frame parity with GDI (capture the
  same screen both ways, compare), confirm `WAIT_TIMEOUT` idles CPU on a static
  screen, and confirm recovery across a resolution change and a UAC prompt.
- CI can only compile-check (`GOOS=windows` build); note that in the PR.

### Acceptance criteria
- `REMOTEMASTER_CAPTURE=dxgi` produces a visually identical stream to GDI.
- Static screen holds CPU near idle (no per-tick capture).
- Resolution change and secure-desktop transition recover without a crash or a
  permanently black stream.
- GDI path unchanged and still the fallback.

### Effort
Large. This is the single most involved remaining item because of the COM
surface. Consider landing it in two PRs: (1) DXGI capture returning full frames
only (drop-in for GDI), (2) dirty/move rect reporting wired into the existing
dirty-region WebP encoder.

---

## RM-STREAM-3 — Adaptive FPS / resolution

### Goal
Keep sessions usable on poor links by backing off frame rate and optionally
resolution when the pipeline shows backpressure, then recovering when it
clears. WebP quality already adapts per frame; this task adds a session-level
controller for cadence and scale.

### Signals
Prefer signals already on the sending side — no new round-trips:

1. **WebSocket send backpressure.** The strongest local signal. `conn.Write`
   blocking or the buffered amount growing means the link can't keep up.
   `nhooyr.io/websocket` writes synchronously; measure the time `conn.Write`
   takes and treat a rising trend as congestion. (If we later expose buffered
   bytes, use that directly.)
2. **Encoder/capture queue depth.** If the capture loop can't keep its cadence
   because encode+send is slow, that's congestion.
3. **Viewer-reported render lag (optional, richer).** Add a JSON control
   message `{"type":"stat","paintMs":N,"rttMs":M}` sent from the viewer a few
   times a second. The client feeds it into the controller.

### Controller
A small AIMD-style controller in the client (new
`client/relay/adaptive.go`):

- Maintain a "quality level" enum mapping to concrete (fps, quality[, scale])
  tuples, e.g. L0 = 25fps/q65/1.0 … L4 = 8fps/q45/0.75.
- On sustained congestion (send time over a threshold for N ticks, or viewer
  paint latency rising): step down one level (multiplicative back-off).
- On sustained clear headroom: step up one level slowly (additive increase).
- Clamp to the env-configured ceiling (`REMOTEMASTER_FPS` / `_QUALITY` become
  the *maximum*, not a fixed value). Add `REMOTEMASTER_ADAPTIVE=1|0`
  (default on) to disable.
- When resolution scaling is used, downscale the captured image before encode;
  the frame header already carries width/height, so the viewer rescales for
  free via `scaleCanvas`.

### Client integration
- `captureLoop` asks the controller for the current `(fps, quality, scale)`
  before each encode and reconfigures its ticker when fps changes.
- Feed measured send duration into the controller after each `conn.Write`.

### Viewer integration (optional stat channel)
- Emit the `stat` control message on a timer; guard it behind feature
  detection so old clients ignore an unknown type (they already `switch` and
  drop unknown JSON types — safe).

### Testing
- Unit-test the controller as a pure function: sequences of (sendDuration,
  paintMs) inputs → expected level transitions, including clamp at ceiling
  and floor, and hysteresis (no oscillation on a single spike).
- Manual: throttle the link (e.g. `clumsy`/`tc`) and confirm fps/quality drop
  and recover; confirm a fast LAN sits at the ceiling.

### Acceptance criteria
- On a throttled link the session degrades gracefully (lower fps/quality)
  instead of stalling; recovers when the link clears.
- On a fast link it holds the configured ceiling.
- Controller has no oscillation on transient spikes (hysteresis proven by
  unit tests).
- `REMOTEMASTER_ADAPTIVE=0` restores fixed behavior.

### Effort
Medium. The controller is pure Go and fully unit-testable; the integration
surface (measuring send time, optional stat channel) is small.

### Dependencies / ordering
Independent of DXGI. Adaptive decisions should treat a dirty-region frame's
total encoded bytes and send duration as the load signal.
