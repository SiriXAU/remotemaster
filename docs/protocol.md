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
| `0x08` | video config | client → agent | `[type:1][codecLen:1][w:u32][h:u32][descLen:u16][codec][description]` |
| `0x09` | video chunk | client → agent | `[type:1][flags:1][timestamp:u64][duration:u32][payload]` |
| `0x0A` | clipboard text | both | `[type:1][utf8 bytes]` |

Notes:

- **Coordinates** (`x`, `y`) are in the remote screen's pixel space. The viewer
  maps canvas-local cursor positions back to remote pixels before sending
  (`control.js` `getCanvasPos`).
- **Button codes**: `0` left, `1` middle, `2` right.
- **Scroll** `dx`/`dy` are signed line deltas (the viewer divides wheel pixel
  deltas by 40).
- **Key codes** (`vk`) are Windows virtual-key codes. The browser currently
  sends `KeyboardEvent.keyCode`, which lines up with VK codes for the common
  set; see [ROADMAP.md](../ROADMAP.md) for the planned move to a stable mapping.
- **`flags & 0x01`** on a video chunk marks an IDR/key frame. The viewer drops
  delta chunks until it has seen a key frame, and also skips deltas when the
  decoder's `decodeQueueSize` is backing up.
- **Clipboard** payloads are UTF-8 with `\n` line endings (the Windows client
  converts to/from CRLF at the OS boundary) and are capped at 256 KiB in both
  directions. Both endpoints prime a baseline on the first observation and
  only sync *changes*, so clipboard contents that predate the session are
  never transmitted; each side records text it installs to avoid echo loops.
  The browser side polls `navigator.clipboard.readText()` while focused
  (requires clipboard permission on a secure context); the client polls the
  Win32 clipboard once per second during an active session.

## Frame path (current)

The client captures the primary display, hashes the raw pixels (FNV-1a) to skip
unchanged frames, WebP-encodes changed frames at quality 65, and sends them as
`0x01` at up to 15 fps. The viewer decodes each frame with `createImageBitmap`
(falling back to an `<img>` blob) and draws to a `<canvas>`.

## Video path (H.264, staged)

The `0x08`/`0x09` messages are the forward-looking codec path. The browser
already decodes them through WebCodecs (`VideoDecoder` + `EncodedVideoChunk`);
the Windows client has the encode/decode helpers in `client/relay/proto.go` but
does not yet produce H.264. The `0x01` WebP path stays as the default and as a
fallback for browsers without WebCodecs. See
[`h264-streaming.md`](h264-streaming.md) for the encoder plan.
