(function () {
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

  codeLabel.textContent = 'Code: ' + code;

  if (!code) {
    window.location.href = '/';
    return;
  }

  let remoteW = 0, remoteH = 0;
  let ws = null;
  let reconnectDelay = 1000;
  let dead = false;

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

  function connect() {
    if (dead) return;
    const proto = location.protocol === 'https:' ? 'wss' : 'ws';
    ws = new WebSocket(`${proto}://${location.host}/ws/agent?code=${code}`);
    ws.binaryType = 'arraybuffer';
    setStatus('waiting', 'Connecting…');
    showOverlay('Connecting to session…');

    ws.onopen = () => {
      reconnectDelay = 1000;
      setStatus('waiting', 'Waiting for client…');
    };

    ws.onmessage = (e) => {
      // Binary message: frame data (type 0x01)
      if (e.data instanceof ArrayBuffer) {
        const dv = new DataView(e.data);
        if (dv.getUint8(0) !== 0x01) return;
        const w = dv.getUint32(1);
        const h = dv.getUint32(5);
        if (w !== remoteW || h !== remoteH) {
          remoteW = w;
          remoteH = h;
          canvas.width = remoteW;
          canvas.height = remoteH;
          scaleCanvas();
        }
        const webpData = new Uint8Array(e.data, 9);
        const blob = new Blob([webpData], { type: 'image/webp' });
        const url = URL.createObjectURL(blob);
        const img = new Image();
        img.onload = () => {
          ctx.drawImage(img, 0, 0);
          URL.revokeObjectURL(url);
          setStatus('connected', 'Connected');
          hideOverlay();
        };
        img.onerror = () => {
          URL.revokeObjectURL(url);
          setStatus('error', 'Frame decode error');
        };
        img.src = url;
        return;
      }

      // Text message: JSON control
      let m;
      try { m = JSON.parse(e.data); } catch { return; }

      switch (m.type) {
        case 'joined':
          setStatus('waiting', 'Waiting for first frame…');
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
      if (dead) return;
      setStatus('waiting', 'Reconnecting…');
      showOverlay('Connection lost. Reconnecting…');
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
