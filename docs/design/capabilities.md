# Design: Capabilities

Covers the roadmap's *Capabilities* theme that is not yet built:
**multi-monitor support**, **file transfer**, and **session chat /
annotations**. (Clipboard sync and robust keyboard mapping already shipped.)
Read the [design index](README.md) first for task IDs, the protocol-tag
registry, and the pickup workflow.

---

## RM-CAP-1 — Multi-monitor support

### Goal
Capture is hard-wired to the primary display via
`GetSystemMetrics(SM_CXSCREEN/SM_CYSCREEN)` in both
`client/capture/capture_windows.go` and `client/input/input_windows.go`. Let
the agent enumerate the client's monitors and switch which one it views, with
input coordinates mapped to the selected monitor.

### Approach
Per-monitor selection (not a spanning virtual desktop) is the smaller, more
useful first step. A spanning mode can come later behind the same messages.

### Monitor model
```go
// capture/capture.go
type Monitor struct {
    ID      int    // stable within a session
    Name    string // device name, e.g. \\.\DISPLAY1
    Primary bool
    Bounds  image.Rectangle // virtual-desktop coordinates
}

type MultiCapturer interface {
    Capturer
    Monitors() ([]Monitor, error)
    Select(id int) error // subsequent Capture() targets this monitor
}
```

- Windows: enumerate with `EnumDisplayMonitors` + `GetMonitorInfo`. GDI capture
  targets a monitor by `BitBlt`-ing from the virtual-desktop DC at the
  monitor's origin (`SM_XVIRTUALSCREEN` offsets); DXGI targets it by choosing
  the matching `IDXGIOutput`.
- Input injection must map remote coordinates to that monitor: `SendInput`
  absolute coordinates are in the **virtual desktop** 0..65535 space, so the
  injector needs the selected monitor's bounds (use `MOUSEEVENTF_VIRTUALDESK`).
  Update `WindowsInjector` to hold the active monitor bounds and offset/scale
  accordingly. This is the subtle part — get it wrong and clicks land on the
  wrong monitor.

### Wire format — JSON control messages
Monitor metadata is low-rate, so use JSON, not binary:

- server relays these verbatim like any other message.
- client → agent, on connect and on change:
  `{"type":"monitors","list":[{"id":0,"name":"\\\\.\\DISPLAY1","primary":true,"w":1920,"h":1080}], "active":0}`
- agent → client, to switch:
  `{"type":"select_monitor","id":1}`
- client re-emits `monitors` with the new `active` and starts sending that
  monitor's frames (send a full `0x01` keyframe immediately on switch).

The frame messages themselves stay as-is; the viewer already resizes to each
frame's width/height, so a monitor switch that changes resolution just works.

### Viewer changes
- Render a monitor picker (a `<select>` or button row) populated from the
  `monitors` message; send `select_monitor` on change.
- Reset the canvas on switch (dimensions come from the next frame).

### Testing
- Unit-test the coordinate mapping: remote (x,y) on monitor with bounds B →
  virtual-desktop absolute coords, for primary and non-primary, including
  negative origins (a monitor left of primary).
- Unit-test monitor-list JSON encode/decode.
- Manual: two-monitor box, switch, confirm the correct screen streams and
  clicks land on the right monitor.

### Acceptance criteria
- Agent sees all client monitors and can switch; the active one streams.
- Mouse/keyboard land on the selected monitor, including monitors with negative
  virtual-desktop origins.
- Single-monitor clients behave exactly as today.

### Effort
Medium–large (input coordinate mapping is fiddly and platform-specific).
Depends on nothing, but the input-mapping work overlaps DXGI monitor selection
— if RM-STREAM-1 is in flight, coordinate.

---

## RM-CAP-2 — File transfer

### Goal
Chunked file push/pull over the existing binary channel, scoped to the live
session, so an operator can deliver a fix to the remote machine (and optionally
pull a log back).

### Scope & safety (decide before coding)
- **Direction:** start with agent → client (push a file to the controlled
  machine). Pull (client → agent) is symmetric; add second.
- **Destination:** write to a fixed, session-scoped directory (e.g.
  `%USERPROFILE%\Downloads\RemoteMaster` or a configurable
  `REMOTEMASTER_TRANSFER_DIR`). Never let the sender choose an arbitrary path —
  strip everything but the base filename and sanitize it (no `..`, no drive
  letters, no separators). This is the main security concern; treat the
  filename as hostile.
- **Consent:** file transfer should be gated by the same trust surface as
  control. If RM-SEC-1 (consent prompt) lands, surface incoming transfers there
  too; until then, at minimum log every transfer to the audit channel and show
  it in the client window.
- **Size cap:** enforce a configurable max (`REMOTEMASTER_TRANSFER_MAX`,
  default e.g. 100 MiB) and reject oversize offers up front.

### Wire format — `0x11`/`0x12`/`0x13`
```
0x11 file offer   sender → receiver
  [type:1][transferId:u32][nameLen:u16][size:u64][name utf8]
0x12 file chunk   sender → receiver
  [type:1][transferId:u32][seq:u32][data]           (data ≤ chunk size)
0x13 file control receiver → sender / either
  [type:1][transferId:u32][code:1]                  code: 0=accept 1=reject 2=complete-ack 3=error
```

- Chunk size ~64 KiB, well under `maxMessageBytes`.
- Flow: sender emits `0x11` offer → receiver validates (size/name/consent) and
  replies `0x13 accept|reject` → sender streams `0x12` chunks in `seq` order →
  after the last chunk, receiver verifies total bytes and replies
  `0x13 complete-ack` (or `error`).
- `transferId` lets multiple transfers coexist and lets late chunks from an
  aborted transfer be dropped.

### Client implementation
- New `client/transfer` package: a receiver that assembles chunks to a temp
  file then atomically renames on success; a sender that reads a local file in
  chunks. Both are pure Go and unit-testable with an in-memory transport.
- Wire into `readPump`/write path in `client/relay/client.go` next to the
  clipboard handling: dispatch `0x11/0x12/0x13` to the transfer manager.
- Backpressure: interleave chunk sends with frames; don't block the capture
  loop. A simple approach is a bounded queue drained by the existing writer
  goroutine.

### Viewer implementation
- Push UI: a file input + drag-drop zone; read the `File` via `FileReader`/
  `arrayBuffer()`, chunk it, send `0x11` then `0x12`s, show a progress bar
  driven by `0x13`.
- Pull UI (later): request a path, receive chunks, assemble a `Blob`, trigger a
  download.

### Testing
- Unit-test filename sanitization exhaustively (the security-critical part):
  `..\\..\\evil`, `C:\\Windows\\x`, embedded NULs, unicode tricks, empty name.
- Unit-test the chunker/assembler round-trip over an in-memory pipe, including
  out-of-order and duplicate chunks, oversize rejection, and a mid-transfer
  abort.
- Manual: push a binary, checksum both ends.

### Acceptance criteria
- A file pushed from the viewer arrives byte-identical in the scoped directory.
- Malicious filenames cannot escape the transfer directory (proven by tests).
- Oversize transfers are rejected before any bytes are written.
- Transfers are logged/auditable and do not stall the video stream.

### Effort
Large. The protocol and Go side are testable in isolation; the security review
of path handling and the consent integration are the gating concerns.

---

## RM-CAP-3 — Session chat / annotations

### Goal
A lightweight text chat plus an on-screen pointer/annotation so the agent can
guide the user ("click here"). Two loosely related sub-features; chat is the
smaller, ship it first.

### Chat — JSON control messages
- `{"type":"chat","from":"agent|client","text":"...","ts":<unixMs>}` relayed
  verbatim. Cap text length (e.g. 4 KiB) on both ends.
- Client side: the native window (`client/ui/window_windows.go`) grows a small
  read-only chat log + an input box, or — simpler first cut — a toast/notice
  area that shows the latest agent message. The window is already a Win32
  message loop; adding a child edit control is the bulk of the work.
- Viewer side: a collapsible chat panel next to the canvas; send on Enter.

### Annotations — `0x0F` pointer
An ephemeral pointer/highlight the agent draws over the canvas, mirrored on the
client as an overlay so the user sees where the agent is pointing.

```
0x0F annotation  agent → client
  [type:1][kind:1][x:u16][y:u16][arg:u16]
  kind: 0=pointer(ping) 1=arrow 2=circle ; arg = radius/ttl as needed
```

- Coordinates are in remote pixel space (same mapping as input events).
- Client draws a transient overlay window (layered, click-through
  `WS_EX_LAYERED | WS_EX_TRANSPARENT`) at the mapped screen position; fades
  after a TTL. This is the involved part — a click-through topmost overlay per
  monitor.
- Viewer optionally echoes its own pointer locally for the agent's benefit
  (no wire needed; it already has the cursor).

Ship chat first; annotations (especially the client overlay window) are a
larger, separable follow-up.

### Testing
- Unit-test chat message encode/decode and length clamping.
- Unit-test annotation encode/decode and coordinate mapping.
- Manual: exchange chat both directions; confirm annotation appears at the
  right spot and fades.

### Acceptance criteria
- Text chat works both directions with length limits enforced.
- (Annotations) An agent ping shows a transient, click-through marker at the
  correct location on the client and does not block input beneath it.

### Effort
Chat: small–medium (mostly UI). Annotations: medium (the click-through overlay
window). Split into two tasks if picked up separately.
