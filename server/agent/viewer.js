(function () {
  const MSG_WEBP_FRAME = 0x01;
  const MSG_CLIPBOARD = 0x0a;
  const MSG_WEBP_REGION = 0x0c;
  const MAX_CLIPBOARD_BYTES = 256 * 1024;

  const params = new URLSearchParams(window.location.search);
  const code = params.get('code') || '';
  const token = params.get('token') || '';

  const canvas = document.getElementById('canvas');
  const ctx = canvas.getContext('2d');
  const overlay = document.getElementById('overlay');
  const overlayMsg = document.getElementById('overlayMsg');
  const statusDot = document.getElementById('statusDot');
  const statusText = document.getElementById('statusText');
  const codeLabel = document.getElementById('codeLabel');
  const disconnectBtn = document.getElementById('disconnectBtn');
  const viewport = document.getElementById('viewport');
  const textDecoder = new TextDecoder();
  const textEncoder = new TextEncoder();

  codeLabel.textContent = 'Code: ' + code;

  if (!code) {
    window.location.href = '/';
    return;
  }

  let remoteW = 0, remoteH = 0;
  let ws = null;
  let reconnectDelay = 1000;
  let dead = false;

  let imageQueue = [];
  let discardRegionsUntilFull = false;
  let imageDecoding = false;
  let notice = '';

  function setStatus(state, text) {
    statusDot.className = state;
    statusText.textContent = text;
  }

  function showOverlay(msg) {
    overlay.classList.remove('hidden');
    overlayMsg.textContent = msg;
  }

  function hideOverlay() {
    overlay.classList.add('hidden');
  }

  function setRemoteSize(w, h) {
    if (!w || !h || (w === remoteW && h === remoteH)) return;
    remoteW = w;
    remoteH = h;
    canvas.width = remoteW;
    canvas.height = remoteH;
    scaleCanvas();
  }

  function markFrameDrawn() {
    if (notice) {
      setStatus('waiting', notice);
    } else {
      setStatus('connected', 'Connected');
    }
    hideOverlay();
  }

  function scaleCanvas() {
    if (!remoteW || !remoteH) return;
    const vw = viewport.clientWidth;
    const vh = viewport.clientHeight;
    const scale = Math.min(vw / remoteW, vh / remoteH);
    canvas.style.width = Math.floor(remoteW * scale) + 'px';
    canvas.style.height = Math.floor(remoteH * scale) + 'px';
  }

  window.addEventListener('resize', scaleCanvas);

  // Expose for control.js
  window._viewer = {
    getRemoteSize: () => ({ w: remoteW, h: remoteH }),
    getScale: () => {
      if (!remoteW) return 1;
      return parseFloat(canvas.style.width) / remoteW;
    },
    sendMsg: (obj) => {
      if (ws && ws.readyState === WebSocket.OPEN) {
        ws.send(JSON.stringify(obj));
      }
    },
    sendBinary: (buf) => {
      if (ws && ws.readyState === WebSocket.OPEN) {
        ws.send(buf);
      }
    },
  };

  function handleBinaryMessage(buffer) {
    if (!buffer || buffer.byteLength < 1) return;
    const type = new DataView(buffer).getUint8(0);
    switch (type) {
      case MSG_WEBP_FRAME:
        enqueueImageFrame(buffer);
        break;
      case MSG_WEBP_REGION:
        enqueueRegionFrame(buffer);
        break;
      case MSG_CLIPBOARD:
        receiveClipboard(buffer);
        break;
    }
  }

  // --- Clipboard sync -------------------------------------------------------
  // Remote copies arrive as 0x0A messages and are written to the local
  // clipboard. Local copies are detected by polling readText() while the tab
  // has focus (the Clipboard API has no change event) and sent as 0x0A.
  // lastClipboard === null means "not primed yet": the first successful read
  // only records a baseline, so clipboard contents that predate the session
  // are never shipped to the remote machine.
  let lastClipboard = null;

  function receiveClipboard(buffer) {
    const text = textDecoder.decode(new Uint8Array(buffer, 1));
    lastClipboard = text;
    if (navigator.clipboard && navigator.clipboard.writeText) {
      navigator.clipboard.writeText(text).catch(() => {});
    }
  }

  function pollClipboard() {
    if (!document.hasFocus()) return;
    if (!ws || ws.readyState !== WebSocket.OPEN) return;
    if (!navigator.clipboard || !navigator.clipboard.readText) return;

    navigator.clipboard.readText().then((text) => {
      if (text === lastClipboard) return;
      const priming = lastClipboard === null;
      lastClipboard = text;
      if (priming) return;

      const utf8 = textEncoder.encode(text);
      if (utf8.length > MAX_CLIPBOARD_BYTES) return;
      const buf = new Uint8Array(1 + utf8.length);
      buf[0] = MSG_CLIPBOARD;
      buf.set(utf8, 1);
      window._viewer.sendBinary(buf.buffer);
    }).catch(() => {}); // permission denied or unfocused — try again later
  }

  setInterval(pollClipboard, 2000);

  function enqueueImageFrame(buffer) {
    if (buffer.byteLength < 9) return;
    const dv = new DataView(buffer);
    // A full frame supersedes everything queued before it — dropping stale
    // full frames and any older region deltas is safe because the new frame
    // repaints the entire canvas.
    imageQueue.length = 0;
    imageQueue.push({
      full: true,
      w: dv.getUint32(1),
      h: dv.getUint32(5),
      buffer,
      payloadOffset: 9,
    });
    discardRegionsUntilFull = false;
    if (!imageDecoding) decodeNextImageFrame();
  }

  function enqueueRegionFrame(buffer) {
    if (buffer.byteLength < 17) return;
    // Region deltas cannot be dropped individually — each one patches the
    // canvas, so skipping one leaves a stale rectangle until the client's
    // next periodic full refresh. If decode falls impossibly far behind,
    // drop the whole backlog and wait for that refresh instead.
    if (discardRegionsUntilFull) return;
    if (imageQueue.length > 120) {
      imageQueue.length = 0;
      discardRegionsUntilFull = true;
      return;
    }
    const dv = new DataView(buffer);
    imageQueue.push({
      full: false,
      x: dv.getUint32(1),
      y: dv.getUint32(5),
      w: dv.getUint32(9),
      h: dv.getUint32(13),
      buffer,
      payloadOffset: 17,
    });
    if (!imageDecoding) decodeNextImageFrame();
  }

  function decodeNextImageFrame() {
    const frame = imageQueue.shift();
    if (!frame) return;

    imageDecoding = true;

    const drawFrame = (source) => {
      if (frame.full) {
        setRemoteSize(frame.w, frame.h);
        ctx.drawImage(source, 0, 0);
      } else {
        ctx.drawImage(source, frame.x, frame.y);
      }
      markFrameDrawn();
      imageDecoding = false;
      if (imageQueue.length) decodeNextImageFrame();
    };

    const payload = new Uint8Array(frame.buffer, frame.payloadOffset);
    const blob = new Blob([payload], { type: 'image/webp' });
    if (window.createImageBitmap) {
      createImageBitmap(blob)
        .then((bitmap) => {
          drawFrame(bitmap);
          bitmap.close();
        })
        .catch(onImageDecodeError);
      return;
    }

    const url = URL.createObjectURL(blob);
    const img = new Image();
    img.onload = () => {
      URL.revokeObjectURL(url);
      drawFrame(img);
    };
    img.onerror = () => {
      URL.revokeObjectURL(url);
      onImageDecodeError();
    };
    img.src = url;
  }

  function onImageDecodeError() {
    imageDecoding = false;
    setStatus('error', 'Frame decode error');
    if (imageQueue.length) decodeNextImageFrame();
  }

  function connect() {
    if (dead) return;
    const proto = location.protocol === 'https:' ? 'wss' : 'ws';
    const tokenParam = token ? `&token=${encodeURIComponent(token)}` : '';
    ws = new WebSocket(`${proto}://${location.host}/ws/agent?code=${code}${tokenParam}`);
    ws.binaryType = 'arraybuffer';
    notice = '';
    setStatus('waiting', 'Connecting...');
    showOverlay('Connecting to session...');

    ws.onopen = () => {
      reconnectDelay = 1000;
      setStatus('waiting', 'Waiting for client...');
    };

    ws.onmessage = (e) => {
      if (e.data instanceof ArrayBuffer) {
        handleBinaryMessage(e.data);
        return;
      }

      let m;
      try { m = JSON.parse(e.data); } catch { return; }

      switch (m.type) {
        case 'joined':
          setStatus('waiting', 'Waiting for first frame...');
          break;

        case 'notice':
          // Client-side warning (e.g. an elevated app has focus and Windows
          // is dropping our input). Sticky: shown until cleared by an empty
          // notice, surviving the per-frame "Connected" status refresh.
          notice = m.msg || '';
          setStatus(notice ? 'waiting' : 'connected', notice || 'Connected');
          break;

        case 'error':
          dead = true;
          setStatus('error', m.msg || 'Error');
          showOverlay(m.msg || 'Session error');
          break;

        case 'agent_disconnected':
        case 'disconnect':
          // The session is over; mark it dead so onclose does not reconnect
          // into a code the server has already removed.
          dead = true;
          setStatus('error', 'Session ended');
          showOverlay('The remote session has ended.');
          ws.close();
          break;
      }
    };

    ws.onerror = () => setStatus('error', 'Connection error');

    ws.onclose = () => {
      imageQueue.length = 0;
      discardRegionsUntilFull = false;

      if (dead) return;
      setStatus('waiting', 'Reconnecting...');
      showOverlay('Connection lost. Reconnecting...');
      setTimeout(connect, reconnectDelay);
      reconnectDelay = Math.min(reconnectDelay * 2, 16000);
    };
  }

  disconnectBtn.addEventListener('click', () => {
    dead = true;
    if (ws) ws.close();
    setStatus('error', 'Disconnected');
    showOverlay('Session disconnected.');
    setTimeout(() => { window.location.href = '/'; }, 1500);
  });

  connect();
})();
