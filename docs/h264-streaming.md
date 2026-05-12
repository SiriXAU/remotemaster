# H.264 Streaming Path

RemoteMaster currently streams full WebP frames over the existing WebSocket
relay. The next codec step is to keep the relay and input protocol, then swap
the frame producer and browser decoder to an H.264 stream.

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

## Client Implementation Steps

1. Add a `VideoEncoder` interface near the relay loop:
   `Configure(w, h, fps int)`, `Encode(frame)`, `ForceKeyFrame()`, `Close()`.
2. Keep WebP as a fallback encoder until WebCodecs support is confirmed by the
   viewer.
3. On Windows, prefer DXGI Desktop Duplication for capture. It avoids extra GDI
   copies and exposes dirty/move rectangles for future bitrate savings.
4. Feed frames to Media Foundation's H.264 encoder with low-latency mode, no
   B-frames, a short GOP, and constrained bitrate.
5. Send a `0x08` config when dimensions or codec configuration changes, then
   send each encoded access unit as a `0x09` chunk.
6. Add adaptive control after the first working encoder: lower FPS/bitrate when
   WebSocket buffered bytes, encoder queue depth, or `VideoDecoder.decodeQueueSize`
   stays high.

## Browser Implementation Status

`server/agent/viewer.js` already accepts `0x08` config messages and `0x09`
chunks through WebCodecs. The existing `0x01` WebP path remains active for
current clients and as a compatibility fallback.
