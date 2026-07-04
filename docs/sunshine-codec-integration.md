# Sunshine Codec Integration Notes

Sunshine is the correct Moonlight-ecosystem reference for RemoteMaster's
sender-side video path. Moonlight receives and decodes streams; Sunshine captures
the host display and encodes it.

## What Sunshine Provides

The useful architecture is concentrated in these Sunshine areas:

| Sunshine area | What to borrow |
| --- | --- |
| `src/video.h` | Stream config, encoder capability model, encoded packet abstraction |
| `src/video.cpp` | Encoder registry, FFmpeg AVCodec session setup, encode loop, packet emission |
| `src/nvenc/` | Native NVENC path that returns encoded bytes, frame index, and IDR metadata |
| `src/platform/windows/display_*.cpp` | DXGI Desktop Duplication capture and D3D11 texture sharing |
| `src/platform/linux/vaapi.cpp` / `vulkan_encode.cpp` | Hardware-frame import and FFmpeg hardware encode patterns |
| `src/platform/macos/av_video.*` | VideoToolbox-style platform encoder reference |

The clean RemoteMaster mapping is:

| Sunshine concept | RemoteMaster concept |
| --- | --- |
| `video::config_t` | Encoder settings derived from env/config/session |
| `video::encode_session_t` | `wireVideoEncoder` implementation |
| `packet_raw_t::data()` | `0x09` video chunk payload |
| `packet_raw_t::is_idr()` | `0x09` chunk `flags & 0x01` |
| `packet_raw_t::frame_index()` | Chunk timestamp/frame counter basis |
| SPS/PPS/VPS replacement/injection | `0x08` config `description` or repeated Annex B parameter sets |

## Current RemoteMaster Integration

RemoteMaster now has a narrow encoder boundary in `client/relay/encoder.go`.
The existing WebP path implements it by returning one `0x01` WebP frame message.
The default `auto` mode now tries an FFmpeg H.264 implementation through the
same boundary and returns:

1. A `0x08` video config message when codec settings are initialized or changed.
2. A `0x09` video chunk for each encoded access unit.
3. `KeyFrame=true` on chunks that Sunshine would report as `idr`.

This keeps the relay opaque and avoids changing the browser viewer, which
already decodes `0x08`/`0x09` through WebCodecs. If FFmpeg or the selected
encoder is unavailable, RemoteMaster falls back to WebP. The FFmpeg path still
consumes CPU-side RGBA frames from the current GDI capturer; the
Sunshine-style upgrade is to replace that with DXGI capture and a hardware
encoder that avoids extra copies.

## Windows Encoder Recommendation

For the first optimized RemoteMaster encoder, copy Sunshine's shape rather than
its full dependency graph:

1. Start with FFmpeg AVCodec on Windows using `h264_mf`, `h264_qsv`, `h264_amf`,
   or `h264_nvenc` where available.
2. Feed the encoder a low-latency configuration:
   no B-frames, low-delay flags, CBR-ish bitrate, short or on-demand key-frame
   cadence, and repeated parameter sets before key frames.
3. Preserve Sunshine's packet metadata:
   encoded bytes, frame index, IDR/key-frame flag, capture timestamp.
4. Later replace GDI capture with a DXGI Desktop Duplication capture backend so
   captured frames can move toward GPU-backed encode paths.

Sunshine's native NVENC path is highly optimized, but it is also deeply tied to
NVIDIA's Video Codec SDK, D3D11/CUDA resources, and Sunshine's C++ platform
layer. It is a good reference for a future native Windows encoder, not a small
drop-in for the current Go client.

## Licensing

RemoteMaster is MIT licensed. Sunshine is GPL-3.0 licensed. Directly copying
Sunshine implementation code into RemoteMaster would likely require accepting
GPL obligations for the combined work. For now, keep this as clean-room design
guidance, or isolate any Sunshine-derived implementation behind an explicitly
chosen process/library boundary after a license decision.
