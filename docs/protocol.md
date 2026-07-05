# Wire Protocol Reference

RemoteMaster uses a single WebSocket connection per participant. The relay
server (`server/relay/relay.go`) is codec-agnostic: it copies every message
verbatim between the paired client and agent connections. All meaning lives in
the endpoints — the Windows client (`client/relay/`) and the browser viewer
(`server/agent/viewer.js` + `control.js`).

Two message categories share each socket:

- **Text (JSON)** — session control (setup, notifications, teardown).
- **Binary** — video frames and input events. Byte 0 is a message-type tag.

All multi-byte binary fields are **big-endian**.

## Endpoints

| Path | Who | Notes |
| --- | --- | --- |
| `GET /` | agent | Code-entry page and client download instructions. |
| `GET /viewer.html?code=XXXXXX` | agent | Live session viewer. |
| `GET /launch.ps1` | client host | PowerShell bootstrap (download + run client). |
| `GET /health` | ops | `{"status":"ok","time":"<RFC3339>"}`. |
| `WS /ws/client` | client | Registers a session, receives a code. |
| `WS /ws/agent?code=XXXXXX` | agent | Joins an existing session by code. |

## JSON control messages

Shape: `{ "type": string, "code"?: string, "msg"?: string }`.

| `type` | Direction | Meaning |
| --- | --- | --- |
| `registered` | server → client | Session created; `code` is the 6-digit code. |
| `joined` | server → agent | Agent successfully attached to the session. |
| `agent_connected` | server → client | An agent joined; start the capture loop. |
| `agent_disconnected` | server → either | The peer left; session ending. |
| `disconnect` | server → either | Session torn down. |
| `error` | server → agent | `msg` explains the failure (bad/claimed code, etc.). |

The client starts capturing only after `agent_connected`, and the viewer drops
back to a "waiting" state on `agent_disconnected`/`disconnect`.

## Binary messages

Byte 0 is the type tag.

| Tag | Name | Direction | Payload |
| --- | --- | --- | --- |
| `0x01` | WebP frame | client → agent | `[type:1][w:u32][h:u32][webp bytes]` |
| `0x02` | mouse move | agent → client | `[type:1][x:u16][y:u16]` |
| `0x03` | mouse down | agent → client | `[type:1][x:u16][y:u16][btn:1]` |
| `0x04` | mouse up | agent → client | `[type:1][x:u16][y:u16][btn:1]` |
| `0x05` | scroll | agent → client | `[type:1][x:u16][y:u16][dx:i16][dy:i16]` |
| `0x06` | key down | agent → client | `[type:1][vk:u16]` |
| `0x07` | key up | agent → client | `[type:1][vk:u16]` |
| `0x0A` | clipboard text | both | `[type:1][utf8 bytes]` |
| `0x0C` | WebP region | client → agent | `[type:1][x:u32][y:u32][w:u32][h:u32][webp bytes]` |

Notes:

- **Coordinates** (`x`, `y`) are in the remote screen's pixel space. The viewer
  maps canvas-local cursor positions back to remote pixels before sending
  (`control.js` `getCanvasPos`).
- **Button codes**: `0` left, `1` middle, `2` right.
- **Scroll** `dx`/`dy` are signed line deltas (the viewer divides wheel pixel
  deltas by 40).
- **Key codes** (`vk`) are Windows virtual-key codes. The viewer maps the
  physical key (`KeyboardEvent.code`) through an explicit table
  (`CODE_TO_VK` in `control.js`) covering letters, digits, F1–F24, numpad,
  navigation, modifiers (with left/right distinction), and OEM punctuation,
  falling back to the legacy `keyCode` only for unmapped keys. The client
  sets `KEYEVENTF_EXTENDEDKEY` for keys in the extended range
  (`input.IsExtendedVK`) so e.g. arrow keys are not misread as numpad
  digits. On tab/canvas blur the viewer releases any held keys to avoid
  stuck modifiers on the remote machine.
- **WebP regions** (`0x0C`) patch a sub-rectangle of the last full `0x01`
  frame at `(x, y)`. Unlike full frames they must not be skipped by the
  viewer — each one is a delta against the current canvas. The client sends
  a fresh full frame at least every 30 s (and opportunistically ~10 s, on an
  idle tick) as self-healing.
- **Clipboard** payloads are UTF-8 with `\n` line endings (the Windows client
  converts to/from CRLF at the OS boundary) and are capped at 256 KiB in both
  directions. Both endpoints prime a baseline on the first observation and
  only sync *changes*, so clipboard contents that predate the session are
  never transmitted; each side records text it installs to avoid echo loops.
  The browser side polls `navigator.clipboard.readText()` while focused
  (requires clipboard permission on a secure context); the client polls the
  Win32 clipboard once per second during an active session.

## Frame path (default: dirty-region WebP)

The client captures the primary display (compositing the mouse cursor into
the frame), hashes the raw pixels (FNV-1a) to skip unchanged frames, and
diffs each changed frame against the previous one to find the dirty bounding
box. Small changes go out as `0x0C` region patches; large regions are split
into horizontal strips and encoded in parallel. WebP quality adapts each
frame (floor 30, cap `REMOTEMASTER_QUALITY`) so encode time stays inside the
per-frame budget at the target rate (default 25 fps), and a full `0x01`
frame is re-sent periodically as self-healing. The viewer decodes with
`createImageBitmap` (falling back to an `<img>` blob) and draws to a
`<canvas>`; region patches are queued and never dropped.
