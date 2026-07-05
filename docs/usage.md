# Usage Guide

A step-by-step guide to running a RemoteMaster support session, tuning video
quality, and fixing the most common problems. For deploying the relay server
itself, see [`deployment.md`](deployment.md).

There are two people in every session:

- **The person being helped** — runs the client EXE on their Windows machine.
- **The support agent** — views and controls that machine from a browser.

## 1. For the person being helped

1. Open **PowerShell** (no admin rights needed) and paste the one-liner your
   support agent gives you. It looks like this:

   ```powershell
   irm https://yourdomain.com/launch.ps1 | iex
   ```

   The relay's home page shows this exact command pre-filled with its own
   address, so the agent can just read it out or send it in chat.

2. The script downloads the client (and FFmpeg, used for smoother H.264 video)
   into `%LOCALAPPDATA%\RemoteMaster` and starts it. Nothing is installed;
   deleting that folder removes everything.

   > If the FFmpeg download fails (offline mirror, blocked domain), the script
   > warns and continues — the session still works, just with WebP video
   > instead of H.264.

3. A small window appears showing a **6-digit code**. Read it to your support
   agent.

   | Display | Meaning |
   | --- | --- |
   | `123456` | Ready — share this code with your agent. |
   | `------` | Idle / waiting for a code from the relay. |
   | `NOCONN` | Can't reach the relay server — check your internet connection or the relay URL. |

4. When the session starts, the agent sees your screen and can control your
   mouse and keyboard. Click **End Session** in the client window at any time
   to cut them off instantly.

## 2. For the support agent

1. Open the relay in a browser: `https://yourdomain.com`.
2. Enter the 6-digit code the user reads to you.
3. The remote screen appears. Your mouse and keyboard input is forwarded while
   the viewer tab has focus.
4. Close the tab (or have the user click **End Session**) to end the session.
   Codes are single-use; a new session needs a new code.

The status line under the viewer tells you what the video pipeline is doing —
"Recovering video decoder..." means a transient decode error occurred and the
viewer is resyncing at the next keyframe on its own; no action needed.

## 3. Video codec: what happens by default

Out of the box the client streams **dirty-region WebP**: each frame is
diffed against the previous one and only the changed rectangle is encoded
(split across CPU cores when large), with quality adapting per frame to hold
the target frame rate (default 25 fps). For desktop content this gives the
lowest latency and the sharpest text, and it works in every browser.

Set `REMOTEMASTER_VIDEO_CODEC=h264` to use **H.264 via FFmpeg** instead —
worthwhile on slow links or for full-motion video, since it uses a fraction
of the bandwidth. If FFmpeg is missing, the encoder fails to start, or the
browser can't decode it, the client falls back to WebP automatically.
Display-resolution changes mid-session are handled in both paths — the
encoder is rebuilt at the new size.

You normally don't need to configure anything. When you do, set environment
variables **before launching the client EXE** (they are read at startup):

```powershell
# Example: force H.264 with NVIDIA hardware encoding at 8 Mbps
$env:REMOTEMASTER_VIDEO_CODEC = "h264"
$env:REMOTEMASTER_H264_ENCODER = "h264_nvenc"
$env:REMOTEMASTER_VIDEO_BITRATE_KBPS = "8000"
& "$env:LOCALAPPDATA\RemoteMaster\remotemaster-client.exe"
```

### Client environment variables

| Variable | Default | Purpose |
| --- | --- | --- |
| `REMOTEMASTER_VIDEO_CODEC` | `auto` | `auto`/`webp` use dirty-region WebP (recommended). `h264` / `ffmpeg-h264` use FFmpeg H.264 for low-bandwidth links (falls back to WebP on any failure, logged loudly). |
| `REMOTEMASTER_FFMPEG` | auto-detect | Full path to `ffmpeg.exe`. By default the client looks next to its own EXE, then on `PATH`. |
| `REMOTEMASTER_H264_ENCODER` | `h264_mf` (Windows), `libx264` elsewhere | FFmpeg encoder name. Hardware options: `h264_nvenc` (NVIDIA), `h264_qsv` (Intel), `h264_amf` (AMD). |
| `REMOTEMASTER_VIDEO_BITRATE_KBPS` | ~0.18 bits/pixel/frame, min 6000 | Target bitrate in kbps, clamped to 500–100000. Lower it for slow links, raise it for crisp text at high resolutions. |
| `REMOTEMASTER_VIDEO_CODEC_STRING` | `avc1.42E01F` | WebCodecs codec string sent to the viewer. Only change this if you change the encoder profile. |
| `REMOTEMASTER_FPS` | `25` | Capture/encode frame rate, clamped to 1–60. |
| `REMOTEMASTER_QUALITY` | `65` | WebP quality cap (1–100). Quality adapts downward automatically (floor 30) during heavy motion to hold the frame rate. |

### Quick recipes

- **Slow or metered link:** `REMOTEMASTER_VIDEO_BITRATE_KBPS=1500` and
  `REMOTEMASTER_FPS=10`.
- **Crisp text on a 4K display:** raise `REMOTEMASTER_VIDEO_BITRATE_KBPS`
  (e.g. `20000`).
- **Slow/metered WAN link:** `REMOTEMASTER_VIDEO_CODEC=h264` — H.264 uses a
  fraction of WebP's bandwidth for the same motion.
- **GPU is busy / driver issues on the H.264 path:**
  `REMOTEMASTER_H264_ENCODER=libx264` for software encoding.

### Diagnostics

The client writes `client.log` next to its EXE (`%LOCALAPPDATA%\RemoteMaster`
when installed by launch.ps1), including which encoder started and a
`pipeline:` line every 5 seconds with frame rate, per-stage timings, and the
current adaptive quality. Go runtime crashes land in `client-err.log`.

## 4. Troubleshooting

**The client shows `NOCONN`.**
The client can't reach the relay. Verify the relay URL (first CLI argument,
`server.txt` next to the EXE, or the baked-in default — in that order) and
that the machine can make outbound WebSocket connections to it.

**Video looks blocky or blurry.**
Raise `REMOTEMASTER_VIDEO_BITRATE_KBPS`. If the client fell back to WebP
(check its log output for `video encoder: webp`), fix the FFmpeg setup — see
next item.

**"h264 unavailable ... falling back to webp" in the client log.**
FFmpeg wasn't found or the encoder failed to start. Make sure `ffmpeg.exe`
sits next to the client EXE (the launch script puts it there), or point
`REMOTEMASTER_FFMPEG` at it. If a hardware encoder is the problem, try
`REMOTEMASTER_H264_ENCODER=libx264`.

**I set `REMOTEMASTER_VIDEO_CODEC=h264` but still get WebP.**
The client logs a single loud line explaining exactly why the explicit
request failed (missing FFmpeg, encoder startup error). Fix that cause; the
fallback keeps the session usable in the meantime.

**The viewer briefly shows "Recovering video decoder...".**
Normal self-healing: the browser decoder hit a transient error and rebuilt
itself, resuming at the next keyframe. If it happens constantly, lower the
bitrate or switch encoders.

**The launch script warns "FFmpeg download failed".**
The session still works over WebP. To get H.264, download FFmpeg manually
(e.g. from <https://www.gyan.dev/ffmpeg/builds/>) and drop `ffmpeg.exe` into
`%LOCALAPPDATA%\RemoteMaster`, then relaunch.

**Everything is slow end-to-end.**
The relay copies every byte between the two sides, so its bandwidth and
latency matter. Host it near the participants and check
[`deployment.md`](deployment.md) for sizing notes.

## 5. Related documentation

- [`protocol.md`](protocol.md) — wire protocol reference (0x08/0x09 video
  messages, input events).
- [`h264-streaming.md`](h264-streaming.md) — H.264 pipeline internals and
  roadmap.
- [`security.md`](security.md) — threat model; read before exposing a relay
  to the internet.
