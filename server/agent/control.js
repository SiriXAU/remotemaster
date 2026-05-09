(function () {
  // control.js — captures mouse and keyboard events on the canvas and forwards
  // them to the relay as binary input messages for minimal latency and bandwidth.
  // Binary protocol (all values big-endian):
  //   mouse_move: [0x02][x:2][y:2]                    5 bytes
  //   mouse_down: [0x03][x:2][y:2][btn:1]             6 bytes
  //   mouse_up:   [0x04][x:2][y:2][btn:1]             6 bytes
  //   scroll:     [0x05][x:2][y:2][dx:i16:2][dy:i16:2] 9 bytes
  //   key_down:   [0x06][vk:2]                        3 bytes
  //   key_up:     [0x07][vk:2]                        3 bytes

  const BTN_CODES = { 0: 0, 1: 1, 2: 2 };

  function getCanvasPos(canvas, e) {
    const rect = canvas.getBoundingClientRect();
    const scaleX = canvas.width / rect.width;
    const scaleY = canvas.height / rect.height;
    return {
      x: Math.round((e.clientX - rect.left) * scaleX),
      y: Math.round((e.clientY - rect.top) * scaleY),
    };
  }

  function sendBinary(buf) {
    if (window._viewer) window._viewer.sendBinary(buf);
  }

  function encodeMouseMove(x, y) {
    const b = new ArrayBuffer(5);
    const d = new DataView(b);
    d.setUint8(0, 0x02);
    d.setUint16(1, x);
    d.setUint16(3, y);
    return b;
  }

  function encodeMouseDown(x, y, btn) {
    const b = new ArrayBuffer(6);
    const d = new DataView(b);
    d.setUint8(0, 0x03);
    d.setUint16(1, x);
    d.setUint16(3, y);
    d.setUint8(5, BTN_CODES[btn] != null ? BTN_CODES[btn] : 0);
    return b;
  }

  function encodeMouseUp(x, y, btn) {
    const b = new ArrayBuffer(6);
    const d = new DataView(b);
    d.setUint8(0, 0x04);
    d.setUint16(1, x);
    d.setUint16(3, y);
    d.setUint8(5, BTN_CODES[btn] != null ? BTN_CODES[btn] : 0);
    return b;
  }

  function encodeScroll(x, y, dx, dy) {
    const b = new ArrayBuffer(9);
    const d = new DataView(b);
    d.setUint8(0, 0x05);
    d.setUint16(1, x);
    d.setUint16(3, y);
    d.setInt16(5, dx);
    d.setInt16(7, dy);
    return b;
  }

  function encodeKey(vk, isDown) {
    const b = new ArrayBuffer(3);
    const d = new DataView(b);
    d.setUint8(0, isDown ? 0x06 : 0x07);
    d.setUint16(1, vk);
    return b;
  }

  // Throttle: max one event per type per frame (~30fps)
  let pendingMove = null;
  let moveScheduled = false;

  function flushMove() {
    if (pendingMove) {
      sendBinary(encodeMouseMove(pendingMove.x, pendingMove.y));
      pendingMove = null;
    }
    moveScheduled = false;
  }

  document.addEventListener('DOMContentLoaded', () => {
    const canvas = document.getElementById('canvas');
    if (!canvas) return;

    canvas.addEventListener('mousemove', (e) => {
      pendingMove = getCanvasPos(canvas, e);
      if (!moveScheduled) {
        moveScheduled = true;
        requestAnimationFrame(flushMove);
      }
    });

    canvas.addEventListener('mousedown', (e) => {
      e.preventDefault();
      const { x, y } = getCanvasPos(canvas, e);
      sendBinary(encodeMouseDown(x, y, e.button));
    });

    canvas.addEventListener('mouseup', (e) => {
      const { x, y } = getCanvasPos(canvas, e);
      sendBinary(encodeMouseUp(x, y, e.button));
    });

    canvas.addEventListener('wheel', (e) => {
      e.preventDefault();
      const { x, y } = getCanvasPos(canvas, e);
      sendBinary(encodeScroll(x, y, Math.round(e.deltaX / 40), Math.round(e.deltaY / 40)));
    }, { passive: false });

    canvas.addEventListener('contextmenu', (e) => e.preventDefault());

    // Keyboard events — capture while canvas area is in focus
    canvas.setAttribute('tabindex', '0');
    canvas.addEventListener('click', () => canvas.focus());

    const captureKey = (e) => {
      // Allow browser DevTools shortcuts to pass through
      if (e.ctrlKey && e.shiftKey && (e.key === 'I' || e.key === 'J')) return;
      e.preventDefault();
      sendBinary(encodeKey(e.keyCode, e.type === 'keydown'));
    };

    canvas.addEventListener('keydown', captureKey);
    canvas.addEventListener('keyup', captureKey);
  });
})();
