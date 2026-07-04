# Moonlight Codec Integration Notes

RemoteMaster can reuse Moonlight's codec model and elementary-stream shape, but
`moonlight-common-c` is not itself a video encoder or hardware decoder.

What `moonlight-common-c` provides:

- codec family/profile constants for H.264, HEVC, and AV1
- RTSP/SDP negotiation for GameStream/Sunshine sessions
- RTP video/audio packet queueing, ordering, and FEC recovery
- Annex B video decode-unit assembly for H.264 and HEVC
- callback boundaries where the embedding app supplies the real decoder

What it does not provide:

- screen capture
- H.264, HEVC, or AV1 encoding
- software video decoding
- Windows Media Foundation, NVENC, VideoToolbox, MediaCodec, or WebCodecs
  decoder implementations

Moonlight's `DecoderRendererSubmitDecodeUnit` callback receives Annex B
elementary-stream data and hands it to a platform decoder. For RemoteMaster,
that maps cleanly to the existing `0x09` video chunk payload: send one encoded
access unit per chunk, mark IDR frames with `flags & 0x01`, and repeat codec
parameter sets before key frames when the encoder produces Annex B.

## Recommended RemoteMaster Path

1. Keep the relay opaque. The current WebSocket bridge should continue copying
   bytes without codec awareness.
2. Encode on the Windows client with a platform encoder, starting with Media
   Foundation H.264 for broad browser support.
3. Emit WebCodecs-compatible config strings in `0x08`:
   `avc1`/`avc3` first, then `hvc1`/`hev1` or `av01` later.
4. Send encoded access units as `0x09` chunks using Moonlight's key-frame and
   decode-unit conventions.
5. Keep browser decode in WebCodecs. If RemoteMaster later gains a native
   viewer, that viewer can use the same decode-unit boundary to feed a native
   hardware decoder.

Importing `moonlight-common-c` directly only becomes useful if RemoteMaster also
adopts Moonlight's RTP/FEC transport or connects to Sunshine/GFE as a Moonlight
client. It will not replace the WebP encoder by itself.

## Broader Moonlight Source Review

The rest of the `moonlight-stream` organization follows the same client-side
split:

- `moonlight-qt` wires `moonlight-common-c` decode units into an
  `IVideoDecoder` interface, then uses FFmpeg plus platform renderers such as
  D3D11VA/DXVA2 on Windows, VideoToolbox on macOS, VAAPI/VDPAU/Vulkan on Linux,
  and SDL/software fallback paths. This is highly optimized receive-side code.
- `moonlight-android` bridges decode units from C/JNI into Java and configures
  Android `MediaCodec` decoders. Its codec helper explicitly skips encoders
  when selecting MediaCodec entries.
- `moonlight-ios` is the same architectural category: a client-side receiver
  that feeds platform decode/render APIs.

Those paths are useful if RemoteMaster grows a native viewer, but they do not
solve RemoteMaster's sender-side problem. RemoteMaster's Windows client needs to
capture the desktop and encode it before sending it over the relay.

For sender-side encoding, the closest Moonlight ecosystem source is Sunshine,
not Moonlight. Sunshine is the Moonlight host and contains the relevant
capture/encode architecture: NVENC, AMF, QuickSync, Media Foundation, VAAPI,
VideoToolbox, Vulkan Video, and software encoder paths. See
[`sunshine-codec-integration.md`](sunshine-codec-integration.md) for the
RemoteMaster-specific mapping.

## Practical Integration Direction

For RemoteMaster, the strongest path is:

1. Keep the browser viewer's WebCodecs decode path for now.
2. Implement a Windows sender encoder in the client, starting with Media
   Foundation H.264 or FFmpeg/libavcodec with a hardware encoder backend.
3. Use Moonlight's decode-unit conventions for payload boundaries: one encoded
   access unit per `0x09`, IDR marked in the flags, SPS/PPS repeated before IDR
   for Annex B H.264.
4. Borrow Moonlight Qt's decoder choices only if adding a native agent/viewer.
5. Borrow Sunshine's host-side encoder architecture if the goal is optimized
   screen capture plus video encoding.
