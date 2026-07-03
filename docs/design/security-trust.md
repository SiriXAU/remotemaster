# Design: Security & Trust

Covers the roadmap's *Security & trust* theme that is not yet built:
**explicit client-side consent + control indicator**, **one-time / expiring
signed codes**, and **end-to-end encryption**. (The optional pre-shared
`AGENT_TOKEN` half of "agent auth" already shipped.) Read the
[design index](README.md) first for task IDs and the pickup workflow, and
[`../security.md`](../security.md) for the current threat model these items
extend.

---

## RM-SEC-1 — Explicit client-side consent + control indicator

### Goal
Today control begins the instant an agent joins — the person at the client
machine has no say and no persistent signal that they are being controlled.
Add (a) an approval prompt before control starts and (b) an always-visible
"someone is controlling this machine" indicator while a session is active.
This is the highest-value trust item and depends on nothing.

### Consent flow
The client already waits for `agent_connected` before it starts the capture
loop (`client/relay/client.go` `connect`). Insert consent there:

1. On `agent_connected`, instead of immediately capturing, the client shows a
   modal prompt in its window: "Allow remote control? [Allow] [Deny]", ideally
   with the agent's IP (the server can include it in the `agent_connected`
   message — it already knows `clientIP(r)` for the agent).
2. On **Allow**, start capture as today and send
   `{"type":"consent","granted":true}` so the viewer leaves its "waiting"
   state.
3. On **Deny** or a timeout (e.g. 30 s), send
   `{"type":"consent","granted":false}`, tear the session down, and audit it.
4. Add `REMOTEMASTER_AUTO_CONSENT=1` to preserve today's unattended behavior
   for kiosk/self-support setups (documented as reducing safety).

### Control indicator
While a session is active, keep a conspicuous, always-on-top signal:

- Simplest: repurpose the existing status window to show a red "🔴 Remote
  control active — <agent ip>" banner and keep it topmost
  (`SetWindowPos` `HWND_TOPMOST`).
- Better: a small persistent overlay (layered click-through window, reused from
  the RM-CAP-3 annotation overlay if that lands) in a screen corner so it shows
  even when the status window is minimized.
- On session end, revert the indicator.

### Server changes
- Include the agent's IP in the `agent_connected` control message.
- Relay `consent` verbatim (no new server logic needed beyond that field).
- Audit `consent_granted` / `consent_denied` (extends RM's existing
  `server/audit`).

### Viewer changes
- After `agent_connected`, show "Waiting for the user to approve…"; reveal the
  canvas only on `consent granted`; show a clear message and stop on denial.

### Testing
- Unit-test the client consent state machine (granted / denied / timeout →
  correct message + capture start/no-start) with the UI abstracted behind a
  small interface so it runs without Win32.
- Manual: approve and deny from the client; confirm the indicator is visible
  and topmost throughout; confirm auto-consent env restores old behavior.

### Acceptance criteria
- No frames are captured or sent before the user approves (verify the capture
  loop does not start pre-consent).
- Denial or timeout ends the session cleanly and is audited.
- An always-visible active-control indicator is present for the whole session
  and removed at the end.
- `REMOTEMASTER_AUTO_CONSENT=1` reproduces today's behavior.

### Effort
Medium. The state machine is unit-testable; the Win32 prompt/indicator and the
viewer states are the manual-test surface.

---

## RM-SEC-2 — One-time / expiring signed codes

### Goal
Reduce the value of a leaked 6-digit code by making codes **short-lived and
single-use at the crypto level**, independent of the in-memory session TTLs.
Complements the already-shipped `AGENT_TOKEN`. Two layers, pick per deployment:

1. **Server-enforced single use (already mostly true):** a code is claimable
   once (`store.Join` refuses a second agent). Tighten by also expiring the
   *pending* code aggressively and optionally issuing codes only on demand.
2. **Signed, self-expiring codes (new):** the code the agent types carries a
   server HMAC + expiry so the server can reject stale/forged codes without
   even a store lookup, and so codes can be distributed out-of-band with a hard
   deadline.

### Signed-code design
- Keep the human-facing 6 digits for UX, but bind them server-side to a signed
  token. Two options:
  - **(a) Longer opaque code:** issue `NNNNNN-XXXX` where `XXXX` is a short
    base32 of `HMAC(secret, NNNNNN || expiry)` truncated; the agent enters
    both. Server recomputes and checks expiry + MAC in constant time before any
    store work. Rejections feed the existing rate limiter and audit.
  - **(b) Side-channel token:** keep 6 digits for the store key but require a
    signed `?ticket=` (JWT-like: `base64(payload).base64(hmac)`),
    `payload = {code, exp}`. This is closer to how `AGENT_TOKEN` already
    threads through the UI, so less UI churn.
- Config: `CODE_SIGNING_SECRET` (enables signing), `CODE_TTL` (e.g. 2m). When
  unset, behavior is exactly today's.
- Store the secret only in the relay; never send it to clients. The *client*
  doesn't need it — the server signs at registration and the agent just relays
  the opaque tail it was given out of band.

### Server changes
- At `/ws/client` registration, compute and return the signed tail alongside
  `code` in the `registered` message; the client shows `NNNNNN-XXXX` (or the
  ticket) in its window for the user to read out.
- At `/ws/agent`, verify signature + expiry before `store.Join`; on failure,
  `Fail(ip)` + audit `join_rejected` reason `expired_or_bad_signature`.

### Testing
- Unit-test signing/verification: valid, expired, tampered payload, wrong
  secret, constant-time compare. Pure Go, no network.
- Unit-test that unset secret preserves the current plain-6-digit path.

### Acceptance criteria
- With signing enabled, a code past `CODE_TTL` is rejected without a store hit
  and is audited.
- A forged/tampered code is rejected in constant time.
- With signing disabled, behavior is byte-for-byte today's.

### Effort
Medium. Crypto is small and fully unit-testable; the UI plumbing of the extra
tail is the integration cost.

### Note on E2E
Signed codes authenticate the *join*, not the media. They do not protect
against a malicious relay — that's RM-SEC-3.

---

## RM-SEC-3 — End-to-end encryption

### Goal
Remove the "relay is fully trusted" assumption: encrypt frames and input
between client and agent so a compromised or curious relay can neither read the
screen nor inject input. The relay keeps doing opaque byte-shoveling; it just
can no longer understand the bytes.

### Key exchange
The two endpoints never speak directly, so the exchange rides the relay but
must be relay-opaque and MITM-resistant:

- Use an authenticated ECDH: X25519 for the exchange, then an AEAD
  (ChaCha20-Poly1305 or AES-GCM) for the channel.
- **MITM binding (the hard part):** a relay in the middle can swap public keys.
  Bind the exchange to a **short authentication string (SAS)** derived from the
  handshake transcript hash, and display it on **both** the client window and
  the viewer. The human operator (already reading the 6-digit code out of band)
  compares the SAS words/number over the same trusted channel. Mismatch ⇒
  abort. This is the WebRTC/Signal "safety number" pattern and is the only way
  to defeat a MITM relay without a PKI.
- Alternatively, if `AGENT_TOKEN`/signed codes (RM-SEC-2) are in use, mix the
  shared secret into the handshake (SPAKE2-style PAKE) so a MITM without the
  secret cannot complete it — this removes the manual SAS compare. Prefer PAKE
  when a pre-shared secret exists; fall back to SAS when it doesn't.

### Wire format
- New control messages for the handshake:
  `{"type":"e2e_hello","pub":"base64","suite":"x25519-chacha20poly1305"}`
  both directions, relayed verbatim.
- Once established, wrap **every** binary payload:
  `0x10 e2e envelope [type:1][nonce:12][ciphertext(+tag)]`. The plaintext is a
  normal binary message (`0x01` frame, `0x02..` input, `0x0A` clipboard, …), so
  encryption is a transparent layer over the existing protocol.
- Nonce discipline: per-direction counter, never reused under one key; rekey or
  cap session length before wraparound.

### Endpoint changes
- **Client (Go):** `crypto/ecdh` + `golang.org/x/crypto/chacha20poly1305`
  (already an indirect dep candidate). Wrap the writer/reader in
  `client/relay` so `captureLoop`/`readPump` are unaware of encryption.
- **Viewer (browser):** WebCrypto (`crypto.subtle`) does X25519
  (`deriveBits`) and AES-GCM natively; ChaCha20 is not in WebCrypto, so if the
  browser must interop, choose **AES-GCM** for the suite. Confirm this drives
  the suite selection above.

### Compatibility
- Opt-in via `REMOTEMASTER_E2E=require|prefer|off`. `prefer` negotiates E2E
  when both sides support it and falls back to plaintext; `require` refuses a
  plaintext session. Default `off` until interop is proven, then `prefer`.

### Testing
- Unit-test the Go handshake + AEAD wrap/unwrap round-trip, nonce
  monotonicity, and tamper rejection.
- Cross-implementation test vectors shared with the JS side (fixed keys →
  known ciphertext) so browser and client agree.
- Manual: verify SAS matches on both ends for a good session and differs under
  a simulated key swap.

### Acceptance criteria
- With E2E on, the relay sees only `0x10` envelopes (verify by logging relayed
  tags) and cannot recover frames or input.
- A key-substitution MITM is detectable (SAS mismatch) or impossible (PAKE).
- Nonce reuse is structurally prevented; tamper is rejected by the AEAD.
- `off` is byte-for-byte today's behavior.

### Effort
Large, and the highest-risk item to get right. Sequence it **after** RM-SEC-2
(reuse the shared secret for PAKE) and ideally after RM-SEC-1 (the human is
already comparing an out-of-band value, a natural home for the SAS). Do not
hand-roll primitives; use vetted libraries on both sides and pin test vectors.
