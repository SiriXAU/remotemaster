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

2. The script downloads the client (a single ~7 MB EXE) into
   `%LOCALAPPDATA%\RemoteMaster` and starts it. Nothing is installed;
   deleting that folder removes everything.

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

The status line under the viewer shows the connection state; "Connected"
means frames are flowing.

## 3. Video: how it works

The client streams **dirty-region WebP**: each frame is diffed against the
previous one and only the changed rectangle is encoded (split across CPU
cores when large), with quality adapting per frame to hold the target frame
rate (default 25 fps). For desktop content this gives low latency and sharp
text, works in every browser, and needs no external dependencies.
Display-resolution changes mid-session are handled automatically — the
encoder is rebuilt at the new size.

You normally don't need to configure anything. When you do, set environment
variables **before launching the client EXE** (they are read at startup):

```powershell
# Example: lower the frame rate and quality cap for a slow link
$env:REMOTEMASTER_FPS = "10"
$env:REMOTEMASTER_QUALITY = "50"
& "$env:LOCALAPPDATA\RemoteMaster\remotemaster-client.exe"
```

### Client environment variables

| Variable | Default | Purpose |
| --- | --- | --- |
| `REMOTEMASTER_FPS` | `25` | Capture/encode frame rate, clamped to 1–60. |
| `REMOTEMASTER_QUALITY` | `65` | WebP quality cap (1–100). Quality adapts downward automatically (floor 30) during heavy motion to hold the frame rate. |

### Quick recipes

- **Slow or metered link:** `REMOTEMASTER_FPS=10` and
  `REMOTEMASTER_QUALITY=50` roughly halve bandwidth.
- **Crisper motion on a fast LAN:** raise `REMOTEMASTER_QUALITY` (e.g. `80`);
  the adaptive floor still protects the frame rate.

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

**Video looks blocky or blurry during motion.**
That's adaptive quality holding the frame rate; the picture sharpens as soon
as motion stops. Raise `REMOTEMASTER_QUALITY` (or lower `REMOTEMASTER_FPS`)
if you prefer crisper motion over smoothness.

**Clicks and keys do nothing in a specific app (e.g. Task Manager), but the
screen still updates.**
That app is running elevated, and Windows silently blocks input from
normal-privilege processes into elevated ones (UIPI). Both the client window
and the viewer status line show a warning while an elevated app has focus.
To control elevated apps, restart the client as administrator:

```powershell
Start-Process "$env:LOCALAPPDATA\RemoteMaster\remotemaster-client.exe" -Verb RunAs -WorkingDirectory "$env:LOCALAPPDATA\RemoteMaster"
```

**Everything is slow end-to-end.**
The relay copies every byte between the two sides, so its bandwidth and
latency matter. Host it near the participants and check
[`deployment.md`](deployment.md) for sizing notes.

## 5. Related documentation

- [`protocol.md`](protocol.md) — wire protocol reference (frame, region, and
  input messages).
- [`security.md`](security.md) — threat model; read before exposing a relay
  to the internet.
