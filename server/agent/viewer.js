(function () {
  const MSG_WEBP_FRAME = 0x01;
  const MSG_VIDEO_CONFIG = 0x08;
  const MSG_VIDEO_CHUNK = 0x09;
  const MSG_CLIPBOARD = 0x0a;
  const MSG_VIDEO_UNSUPPORTED = 0x0b;
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
  let videoDecoder = null;
  let videoConfigured = false;
  let videoWaitingForKey = true;
  let activeVideoCodec = '';
  // Cache of the most recent 0x08 video-config message, keyed to the decoded
  // VideoDecoderConfig + codec family. Used to transparently rebuild the
  // decoder after a runtime error, since the server only sends 0x08 once per
  // connection and never re-sends it on its own.
  let lastVideoConfig = null;

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
    setStatus('connected', 'Connected');
    hideOverlay();
  }

  // Closes the current decoder, if any, tolerating decoders that are already
  // in the 'closed' state (e.g. the WebCodecs spec auto-closes a decoder
  // right before firing its error callback, so close() there would throw).
  function closeVideoDecoder() {
    if (!videoDecoder) return;
    try {
      if (videoDecoder.state !== 'closed') videoDecoder.close();
    } catch (err) {
      // Already closed or closing; nothing to do.
    }
    videoDecoder = null;
  }

  function codecFamily(codec) {
    const lower = codec.toLowerCase();
    if (lower.startsWith('avc1.') || lower.startsWith('avc3.')) return 'H.264';
    if (lower.startsWith('hvc1.') || lower.startsWith('hev1.')) return 'HEVC';
    if (lower.startsWith('av01.')) return 'AV1';
    return '';
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
      case MSG_VIDEO_CONFIG:
        configureVideo(buffer);
        break;
      case MSG_VIDEO_CHUNK:
        decodeVideoChunk(buffer);
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
      data: buffer.slice(9),
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
      data: buffer.slice(17),
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

    const blob = new Blob([frame.data], { type: 'image/webp' });
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

  // Tells the client this browser cannot decode the advertised video codec,
  // so it should switch to WebP frames. Sent at most once per connection.
  let videoUnsupportedSent = false;
  function requestWebPFallback(reason) {
    console.warn('video codec unsupported, requesting WebP fallback:', reason);
    setStatus('waiting', 'Video codec unsupported here; switching to WebP...');
    if (videoUnsupportedSent) return;
    if (ws && ws.readyState === WebSocket.OPEN) {
      ws.send(new Uint8Array([MSG_VIDEO_UNSUPPORTED]));
      videoUnsupportedSent = true;
    }
  }

  function configureVideo(buffer) {
    if (!('VideoDecoder' in window) || !('EncodedVideoChunk' in window)) {
      requestWebPFallback('WebCodecs is not available in this browser');
      return;
    }
    if (buffer.byteLength < 12) return;

    const dv = new DataView(buffer);
    const codecLen = dv.getUint8(1);
    const w = dv.getUint32(2);
    const h = dv.getUint32(6);
    const descLen = dv.getUint16(10);
    const codecOffset = 12;
    const descOffset = codecOffset + codecLen;
    if (buffer.byteLength < descOffset + descLen) return;

    const codec = textDecoder.decode(new Uint8Array(buffer, codecOffset, codecLen));
    const family = codecFamily(codec);
    if (!family) {
      requestWebPFallback(`unrecognized codec string ${codec}`);
      return;
    }
    const config = {
      codec,
      codedWidth: w,
      codedHeight: h,
      optimizeForLatency: true,
    };
    if (descLen > 0) {
      config.description = new Uint8Array(buffer, descOffset, descLen);
    }

    lastVideoConfig = { codec, config, family };
    setRemoteSize(w, h);
    buildVideoDecoder(codec, config, family);
  }

  // (Re)creates the VideoDecoder from a decoded config. Used both for the
  // initial 0x08 config message and to recover from a decoder runtime error
  // without waiting for the server to resend a config it only sends once.
  function buildVideoDecoder(codec, config, family) {
    closeVideoDecoder();
    videoConfigured = false;
    videoWaitingForKey = true;
    activeVideoCodec = codec;

    videoDecoder = new VideoDecoder({
      output: (frame) => {
        const w = frame.displayWidth || frame.codedWidth || remoteW;
        const h = frame.displayHeight || frame.codedHeight || remoteH;
        setRemoteSize(w, h);
        ctx.drawImage(frame, 0, 0, remoteW, remoteH);
        frame.close();
        markFrameDrawn();
      },
      error: (err) => {
        console.error('video decode error', err);
        recoverVideoDecoder();
      },
    });

    const supportCheck = VideoDecoder.isConfigSupported
      ? VideoDecoder.isConfigSupported(config)
      : Promise.resolve({ supported: true, config });
    const decoder = videoDecoder;

    supportCheck
      .then((result) => {
        if (videoDecoder !== decoder) return;
        if (!result.supported) throw new Error(`unsupported ${family} config`);
        decoder.configure(result.config);
        videoConfigured = true;
      })
      .catch((err) => {
        console.error('video config error', err);
        if (videoDecoder === decoder) activeVideoCodec = '';
        requestWebPFallback(`${family} config rejected: ${err}`);
      });
  }

  // Rebuilds the decoder from the cached config after a runtime decode
  // error. The rebuilt decoder starts back in "waiting for keyframe" mode,
  // so corrupted decoder state is discarded and playback resumes cleanly
  // from the next IDR/keyframe chunk. If no config has ever been received,
  // this leaves the (unrecoverable) error state exactly as before.
  function recoverVideoDecoder() {
    closeVideoDecoder();
    videoConfigured = false;
    videoWaitingForKey = true;
    activeVideoCodec = '';

    if (!lastVideoConfig) {
      setStatus('error', 'Video decode error');
      return;
    }

    setStatus('waiting', 'Recovering video decoder...');
    buildVideoDecoder(lastVideoConfig.codec, lastVideoConfig.config, lastVideoConfig.family);
  }

  function decodeVideoChunk(buffer) {
    if (!videoDecoder || !videoConfigured || buffer.byteLength < 14) return;

    const dv = new DataView(buffer);
    const flags = dv.getUint8(1);
    const isKey = (flags & 0x01) !== 0;
    const timestamp = Number(dv.getBigUint64(2));
    const duration = dv.getUint32(10);

    if (videoWaitingForKey && !isKey) return;
    if (isKey) videoWaitingForKey = false;
    if (!isKey && videoDecoder.decodeQueueSize > 2) return;

    const chunk = {
      type: isKey ? 'key' : 'delta',
      timestamp,
      data: new Uint8Array(buffer, 14),
    };
    if (duration > 0) chunk.duration = duration;

    try {
      videoDecoder.decode(new EncodedVideoChunk(chunk));
    } catch (err) {
      console.error('video chunk error', err);
      recoverVideoDecoder();
    }
  }

  function connect() {
    if (dead) return;
    const proto = location.protocol === 'https:' ? 'wss' : 'ws';
    const tokenParam = token ? `&token=${encodeURIComponent(token)}` : '';
    ws = new WebSocket(`${proto}://${location.host}/ws/agent?code=${code}${tokenParam}`);
    ws.binaryType = 'arraybuffer';
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
      closeVideoDecoder();
      videoConfigured = false;
      videoWaitingForKey = true;
      activeVideoCodec = '';
      videoUnsupportedSent = false;
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
