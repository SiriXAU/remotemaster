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
- **Key codes** (`vk`) are Windows virtual-key codes. The viewer maps the
  physical key (`KeyboardEvent.code`) through an explicit table
  (`CODE_TO_VK` in `control.js`) covering letters, digits, F1–F24, numpad,
  navigation, modifiers (with left/right distinction), and OEM punctuation,
  falling back to the legacy `keyCode` only for unmapped keys. The client
  sets `KEYEVENTF_EXTENDEDKEY` for keys in the extended range
  (`input.IsExtendedVK`) so e.g. arrow keys are not misread as numpad
  digits. On tab/canvas blur the viewer releases any held keys to avoid
  stuck modifiers on the remote machine.
- **`flags & 0x01`** on a video chunk marks an IDR/key frame. The viewer drops
  delta chunks until it has seen a key frame, and also skips deltas when the
  decoder's `decodeQueueSize` is backing up.
- **Encoded video codecs** use WebCodecs codec strings. The client validates
  outgoing `0x08` configs against the Moonlight-compatible families currently
  supported by the encoded path: `avc1`/`avc3` (H.264), `hvc1`/`hev1` (HEVC),
  and `av01` (AV1).
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

## Video path (encoded codecs)

The `0x08`/`0x09` messages are the forward-looking codec path. The browser
already decodes them through WebCodecs (`VideoDecoder` + `EncodedVideoChunk`).
The Windows client has the encode/decode helpers in `client/relay/proto.go`, a
Moonlight-compatible codec mask model in `client/relay/codec.go`, and an
FFmpeg-backed H.264 encoder behind `client/relay/encoder.go`.
`REMOTEMASTER_VIDEO_CODEC=auto` is the default: it tries H.264 first and falls
back to the `0x01` WebP path when FFmpeg or the selected encoder is not
available. Set `REMOTEMASTER_VIDEO_CODEC=webp` to force the legacy path on
browsers without WebCodecs.

The codec masks mirror `moonlight-common-c` so the eventual encoder can choose
families and profile features without changing the wire format:

| Mask | Meaning |
| --- | --- |
| `0x000F` | H.264 family |
| `0x0F00` | HEVC family |
| `0xF000` | AV1 family |
| `0xAA00` | 10-bit formats |
| `0xCC04` | YUV 4:4:4 formats |

See [`h264-streaming.md`](h264-streaming.md) for the first encoder plan.
See [`moonlight-codec-integration.md`](moonlight-codec-integration.md) for the
Moonlight source review and why the actual encoder/decoder still needs to be
provided by platform codec APIs.
