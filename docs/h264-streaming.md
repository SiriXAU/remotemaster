# H.264 Streaming Path

RemoteMaster keeps the existing WebSocket relay and input protocol, but the
default video path now tries FFmpeg-backed H.264 before falling back to WebP.
The protocol layer models the Moonlight codec families (H.264, HEVC, AV1), but
H.264 remains the first encoder target because it has the broadest browser and
Windows Media Foundation support. See
[`moonlight-codec-integration.md`](moonlight-codec-integration.md) for the
boundary between reusable Moonlight stream handling, Moonlight client decoder
code, and Sunshine-style host encoder work. See
[`sunshine-codec-integration.md`](sunshine-codec-integration.md) for the
sender-side encoder map.

## Wire Format

The relay treats all binary messages as opaque payloads. The client and viewer
share these video message types:

- `0x08` video config:
  `[type:1][codecLen:1][w:u32][h:u32][descriptionLen:u16][codec][description]`
- `0x09` video chunk:
  `[type:1][flags:1][timestamp:u64][duration:u32][payload]`

`flags & 0x01` marks an IDR/key frame. The viewer drops delta chunks until it
has seen a key frame.

For H.264, start with Annex B access units and repeat SPS/PPS before each IDR.
If an encoder exposes AVC configuration bytes instead, put them in the config
`description` field and use an `avc1.*` codec string accepted by WebCodecs.
Later HEVC and AV1 encoders can reuse the same `0x08`/`0x09` messages with
`hvc1`/`hev1` or `av01` codec strings.

## Client Implementation Steps

1. Exercise the default `REMOTEMASTER_VIDEO_CODEC=auto` path. It tries FFmpeg
   H.264 first, consumes the current RGBA capture frames, emits a WebCodecs
   `0x08` config, and packetizes Annex B access units as `0x09` chunks.
2. Move from the default `libx264` encoder to a hardware FFmpeg encoder on
   target machines, for example `h264_mf`, `h264_nvenc`, `h264_qsv`, or
   `h264_amf`.
   RemoteMaster sets an explicit high-quality bitrate by default; tune
   `REMOTEMASTER_VIDEO_BITRATE_KBPS` for slower links.
3. Keep WebP as the fallback encoder for missing FFmpeg, older browsers without
   WebCodecs, or encoder startup/runtime failures.
4. On Windows, prefer DXGI Desktop Duplication for capture. It avoids extra GDI
   copies and exposes dirty/move rectangles for future bitrate savings.
5. Feed frames to Media Foundation's H.264 encoder with low-latency mode, no
   B-frames, a short GOP, and constrained bitrate.
6. Send a `0x08` config when dimensions or codec configuration changes, then
   send each encoded access unit as a `0x09` chunk.
7. Add adaptive control after the first working encoder: lower FPS/bitrate when
   WebSocket buffered bytes, encoder queue depth, or `VideoDecoder.decodeQueueSize`
   stays high.

Sunshine is the better ecosystem reference for the encoder half. Moonlight Qt
and Android are still valuable references for decoder-side pacing, hardware
capability selection, and key-frame recovery behavior.

## Browser Implementation Status

`server/agent/viewer.js` already accepts `0x08` config messages and `0x09`
chunks through WebCodecs. The existing `0x01` WebP path remains active for
current clients and as a compatibility fallback.
