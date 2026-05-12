(function () {
  const MSG_WEBP_FRAME = 0x01;
  const MSG_VIDEO_CONFIG = 0x08;
  const MSG_VIDEO_CHUNK = 0x09;

  const params = new URLSearchParams(window.location.search);
  const code = params.get('code') || '';

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

  codeLabel.textContent = 'Code: ' + code;

  if (!code) {
    window.location.href = '/';
    return;
  }

  let remoteW = 0, remoteH = 0;
  let ws = null;
  let reconnectDelay = 1000;
  let dead = false;

  let pendingImageFrame = null;
  let imageDecoding = false;
  let videoDecoder = null;
  let videoConfigured = false;
  let videoWaitingForKey = true;

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
      case MSG_VIDEO_CONFIG:
        configureVideo(buffer);
        break;
      case MSG_VIDEO_CHUNK:
        decodeVideoChunk(buffer);
        break;
    }
  }

  function enqueueImageFrame(buffer) {
    if (buffer.byteLength < 9) return;
    const dv = new DataView(buffer);
    pendingImageFrame = {
      w: dv.getUint32(1),
      h: dv.getUint32(5),
      data: buffer.slice(9),
    };
    if (!imageDecoding) decodeNextImageFrame();
  }

  function decodeNextImageFrame() {
    const frame = pendingImageFrame;
    if (!frame) return;

    pendingImageFrame = null;
    imageDecoding = true;

    const blob = new Blob([frame.data], { type: 'image/webp' });
    if (window.createImageBitmap) {
      createImageBitmap(blob)
        .then((bitmap) => {
          setRemoteSize(frame.w, frame.h);
          ctx.drawImage(bitmap, 0, 0);
          bitmap.close();
          markFrameDrawn();
          imageDecoding = false;
          if (pendingImageFrame) decodeNextImageFrame();
        })
        .catch(onImageDecodeError);
      return;
    }

    const url = URL.createObjectURL(blob);
    const img = new Image();
    img.onload = () => {
      URL.revokeObjectURL(url);
      setRemoteSize(frame.w, frame.h);
      ctx.drawImage(img, 0, 0);
      markFrameDrawn();
      imageDecoding = false;
      if (pendingImageFrame) decodeNextImageFrame();
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
    if (pendingImageFrame) decodeNextImageFrame();
  }

  function configureVideo(buffer) {
    if (!('VideoDecoder' in window) || !('EncodedVideoChunk' in window)) {
      setStatus('error', 'H.264 is not supported by this browser');
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
    const config = {
      codec,
      codedWidth: w,
      codedHeight: h,
      optimizeForLatency: true,
    };
    if (descLen > 0) {
      config.description = new Uint8Array(buffer, descOffset, descLen);
    }

    if (videoDecoder) {
      videoDecoder.close();
      videoDecoder = null;
    }
    videoConfigured = false;
    videoWaitingForKey = true;
    setRemoteSize(w, h);

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
        videoConfigured = false;
        videoWaitingForKey = true;
        setStatus('error', 'Video decode error');
      },
    });

    const supportCheck = VideoDecoder.isConfigSupported
      ? VideoDecoder.isConfigSupported(config)
      : Promise.resolve({ supported: true, config });
    const decoder = videoDecoder;

    supportCheck
      .then((result) => {
        if (videoDecoder !== decoder) return;
        if (!result.supported) throw new Error('unsupported H.264 config');
        decoder.configure(result.config);
        videoConfigured = true;
      })
      .catch((err) => {
        console.error('video config error', err);
        setStatus('error', 'H.264 config error');
      });
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
      videoWaitingForKey = true;
      setStatus('error', 'Video decode error');
    }
  }

  function connect() {
    if (dead) return;
    const proto = location.protocol === 'https:' ? 'wss' : 'ws';
    ws = new WebSocket(`${proto}://${location.host}/ws/agent?code=${code}`);
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
          setStatus('error', 'Session ended');
          showOverlay('The remote session has ended.');
          ws.close();
          break;
      }
    };

    ws.onerror = () => setStatus('error', 'Connection error');

    ws.onclose = () => {
      if (videoDecoder) {
        videoDecoder.close();
        videoDecoder = null;
      }
      videoConfigured = false;
      videoWaitingForKey = true;

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
