# Design: Platform Reach

Covers the roadmap's *Platform reach* theme: a **macOS client**, a **Linux
client**, and **signed Windows binaries**. Read the [design index](README.md)
first for task IDs and the pickup workflow. The guiding constraint: all three
build behind the existing interfaces so the relay, protocol, and viewer never
change.

The client is already structured for this. `capture.Capturer` and
`input.Injector` are the platform seams; the Windows implementations live in
`*_windows.go` behind `//go:build windows`. A new OS means new files behind its
own build tag plus a portable `main` — see below.

---

## RM-PLAT-1 — macOS client

### Goal
Implement `capture_darwin.go` and `input_darwin.go` behind the existing
interfaces and `//go:build darwin`, so a Mac can be the controlled machine.

### Capture
- **ScreenCaptureKit** (`SCStream`, macOS 12.3+) is the modern, performant path
  and the right target; `CGDisplayCreateImage` is the simpler fallback for a
  first cut and older OSes.
- Both are Objective-C / CoreGraphics APIs, so this needs **cgo** with
  `-framework` linker flags (CoreGraphics, ScreenCaptureKit, CoreMedia). That
  matches the client module already using cgo for WebP.
- Return `*image.NRGBA` like the Windows capturer. `CGDisplayCreateImage`
  yields a `CGImage`; copy its pixels (BGRA, premultiplied) into NRGBA with the
  same swap loop pattern as `capture_windows.go`.
- **Permissions:** macOS requires Screen Recording permission (TCC). First run
  prompts; a headless/unattended deploy must be pre-authorized via MDM. Document
  this prominently — without it, capture silently returns black.

### Input
- **`CGEventPost`** for mouse and keyboard synthesis
  (`CGEventCreateMouseEvent`, `CGEventCreateKeyboardEvent`). Also cgo.
- **Key mapping:** the wire carries Windows VK codes (see `docs/protocol.md`).
  macOS needs a VK→CGKeyCode translation table — the inverse mapping problem
  RM already solved browser-side in `control.js`. Build a `vk_darwin.go` table
  mirroring `client/input/vk.go`.
- **Coordinates:** `CGEventCreateMouseEvent` takes global display points; map
  remote pixels to points accounting for Retina backing scale.
- **Permissions:** Accessibility (TCC) permission is required to post events;
  same MDM pre-authorization note as capture.

### main / wiring
- Extract the OS-neutral parts of `client/main.go` (URL resolution, relay wiring,
  signal handling) into a portable `main.go` (no build tag) and move the Win32
  window bits behind `//go:build windows`. macOS needs either a minimal Cocoa
  status window or a headless mode (`ui` becomes an interface with a no-op impl
  for a first cut).
- Clipboard: `NSPasteboard` behind the `clipboard.Clipboard` interface
  (`clipboard_darwin.go`) — optional for a first cut (nil is handled).

### Build / CI
- Cross-compiling cgo for darwin from Linux is painful; build macOS on a
  `macos-latest` GitHub runner instead. Add a matrix leg.
- Signing/notarization for distribution is a separate concern (analogous to
  RM-PLAT-3); at minimum produce an unsigned universal (arm64 + amd64) binary.

### Testing
- Pure-Go unit tests for the VK→CGKeyCode table and coordinate/Retina scaling
  math.
- Manual on real hardware for capture parity, input landing, and the TCC
  permission prompts.

### Acceptance criteria
- A Mac client registers a code and streams its display to the viewer.
- Mouse/keyboard from the viewer land correctly, including modifiers and arrows.
- Missing TCC permissions produce a clear log message, not a silent black
  screen.
- Windows/other builds are unaffected (portable `main` refactor is behaviour-
  preserving).

### Effort
Large. Two cgo/Obj-C surfaces plus a `main`/`ui` refactor and a new CI runner.

---

## RM-PLAT-2 — Linux client

### Goal
Implement Linux capture and input behind the same interfaces and
`//go:build linux`.

### The X11 vs Wayland split (decide first)
Linux has two display stacks and they need different code:

- **X11:** capture via `XShmGetImage` (MIT-SHM, fast) or `XGetImage`; input via
  **XTest** (`XTestFakeMotionEvent`, `XTestFakeButtonEvent`,
  `XTestFakeKeyEvent`). cgo against `libX11`/`libXtst`/`libXext`. This is the
  pragmatic first target — most remote-support Linux desktops are still X11 or
  XWayland-capable.
- **Wayland:** no global screen grab or input injection by design. Capture goes
  through the **PipeWire + xdg-desktop-portal** `ScreenCast` API (user picks a
  source via a portal dialog — a consent step baked into the platform); input
  via the `RemoteDesktop` portal or `uinput` (needs privileges). Much more
  involved; make it a second task (`RM-PLAT-2b`).

### Key mapping
- XTest takes **KeyCodes** (keyboard-specific), not KeySyms or VKs. Translate
  wire VK → X11 KeySym → KeyCode via `XKeysymToKeycode` at runtime. Build a
  `vk_linux.go` VK→KeySym table (mirroring `vk.go`); resolve KeySym→KeyCode
  against the running server since it's layout-dependent.

### Input coordinates / clipboard
- XTest motion is in root-window pixels; map directly from remote pixels for a
  single screen. Multi-monitor/RandR is a later concern (ties to RM-CAP-1).
- Clipboard: X11 selections (`CLIPBOARD` atom) behind `clipboard.Clipboard`
  (`clipboard_linux.go`), optional first cut.

### Build / CI
- cgo against X11 dev libraries; build on `ubuntu-latest` with
  `libx11-dev libxtst-dev libxext-dev` installed. Add a matrix leg.

### Testing
- Pure-Go unit tests for the VK→KeySym table.
- Manual under Xvfb for headless capture/inject smoke tests in CI is possible
  (start `Xvfb`, run a tiny inject→capture round-trip); real desktops for parity.

### Acceptance criteria
- An X11 Linux client streams its display and accepts input with correct key
  mapping.
- Clear, actionable error when run under a Wayland session without the portal
  path (until RM-PLAT-2b lands).
- Other platforms unaffected.

### Effort
Large for X11; Wayland/PipeWire (RM-PLAT-2b) is a separate large task.

---

## RM-PLAT-3 — Signed Windows binaries

### Goal
Authenticode-sign the client EXE in CI so SmartScreen/AV do not flag the
`launch.ps1` download, and users get a verified publisher instead of "Unknown".

### Approach
- **Certificate:** an OV/EV code-signing certificate (EV gives immediate
  SmartScreen reputation; OV builds reputation over time). This is an
  org/procurement decision and a hard prerequisite — note it in the PR;
  engineering can't proceed without the cert material.
- **Signing in CI:** after the build step in
  `.github/workflows/release.yml`, sign with `signtool` on a Windows runner (or
  `osslsigncode` on Linux for a software cert). Use a timestamp server
  (`/tr http://timestamp.digicert.com /td sha256`) so signatures outlive the
  cert's validity.
- **Secret handling:** store the cert as an encrypted GitHub secret (base64 PFX
  + password) or, preferred, use a cloud KMS/HSM-backed signer (Azure Trusted
  Signing, cloud HSM) so the private key never lands in CI. **Never commit key
  material**; the workflow only references secrets.
- **Verification:** add a post-sign `signtool verify /pa` gate so an unsigned
  or badly-signed artifact fails the release.

### Build-chain interaction
- The current release job cross-compiles with mingw on `ubuntu-latest`. Signing
  with `signtool` needs Windows; either add a Windows signing job that consumes
  the artifact, or use `osslsigncode` on the existing Linux job. Prefer a
  KMS-backed `osslsigncode`/Trusted Signing step to avoid a second runner.

### Testing
- CI: verify step must pass; a deliberately-unsigned artifact must fail the
  gate (test with a dry-run branch).
- Manual: download via `launch.ps1` on a clean Windows VM, confirm the
  publisher shows and SmartScreen does not block.

### Acceptance criteria
- Released EXEs are Authenticode-signed and timestamped.
- CI fails if signing/verification fails.
- No key material is present in the repo or logs.

### Effort
Small–medium engineering once the certificate exists; the cert procurement is
the real gate and is non-engineering.
