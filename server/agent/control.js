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

  // KeyboardEvent.code (physical key) → Windows virtual-key code.
  // event.code is layout-independent and non-deprecated, unlike keyCode,
  // so modifier combos and non-US layouts map to the key the user actually
  // pressed; the remote machine's own keyboard layout then applies.
  const CODE_TO_VK = {
    // Letters (VK 0x41..0x5A)
    KeyA: 0x41, KeyB: 0x42, KeyC: 0x43, KeyD: 0x44, KeyE: 0x45, KeyF: 0x46,
    KeyG: 0x47, KeyH: 0x48, KeyI: 0x49, KeyJ: 0x4a, KeyK: 0x4b, KeyL: 0x4c,
    KeyM: 0x4d, KeyN: 0x4e, KeyO: 0x4f, KeyP: 0x50, KeyQ: 0x51, KeyR: 0x52,
    KeyS: 0x53, KeyT: 0x54, KeyU: 0x55, KeyV: 0x56, KeyW: 0x57, KeyX: 0x58,
    KeyY: 0x59, KeyZ: 0x5a,
    // Top-row digits (VK 0x30..0x39)
    Digit0: 0x30, Digit1: 0x31, Digit2: 0x32, Digit3: 0x33, Digit4: 0x34,
    Digit5: 0x35, Digit6: 0x36, Digit7: 0x37, Digit8: 0x38, Digit9: 0x39,
    // Function keys (VK 0x70..0x87)
    F1: 0x70, F2: 0x71, F3: 0x72, F4: 0x73, F5: 0x74, F6: 0x75, F7: 0x76,
    F8: 0x77, F9: 0x78, F10: 0x79, F11: 0x7a, F12: 0x7b, F13: 0x7c, F14: 0x7d,
    F15: 0x7e, F16: 0x7f, F17: 0x80, F18: 0x81, F19: 0x82, F20: 0x83,
    F21: 0x84, F22: 0x85, F23: 0x86, F24: 0x87,
    // Controls
    Escape: 0x1b, Tab: 0x09, CapsLock: 0x14, Enter: 0x0d, Backspace: 0x08,
    Space: 0x20, ContextMenu: 0x5d,
    ShiftLeft: 0xa0, ShiftRight: 0xa1,
    ControlLeft: 0xa2, ControlRight: 0xa3,
    AltLeft: 0xa4, AltRight: 0xa5,
    MetaLeft: 0x5b, MetaRight: 0x5c,
    // Navigation
    Insert: 0x2d, Delete: 0x2e, Home: 0x24, End: 0x23,
    PageUp: 0x21, PageDown: 0x22,
    ArrowLeft: 0x25, ArrowUp: 0x26, ArrowRight: 0x27, ArrowDown: 0x28,
    // Locks / system
    PrintScreen: 0x2c, ScrollLock: 0x91, Pause: 0x13, NumLock: 0x90,
    // Numpad
    Numpad0: 0x60, Numpad1: 0x61, Numpad2: 0x62, Numpad3: 0x63, Numpad4: 0x64,
    Numpad5: 0x65, Numpad6: 0x66, Numpad7: 0x67, Numpad8: 0x68, Numpad9: 0x69,
    NumpadMultiply: 0x6a, NumpadAdd: 0x6b, NumpadComma: 0x6c,
    NumpadSubtract: 0x6d, NumpadDecimal: 0x6e, NumpadDivide: 0x6f,
    NumpadEnter: 0x0d, NumpadEqual: 0xbb,
    // OEM punctuation (US-layout VK assignments; the remote layout decides
    // the produced character)
    Semicolon: 0xba, Equal: 0xbb, Comma: 0xbc, Minus: 0xbd, Period: 0xbe,
    Slash: 0xbf, Backquote: 0xc0, BracketLeft: 0xdb, Backslash: 0xdc,
    BracketRight: 0xdd, Quote: 0xde,
    // Extra intl keys
    IntlBackslash: 0xe2, IntlRo: 0xc1,
  };

  function vkFromEvent(e) {
    const vk = CODE_TO_VK[e.code];
    if (vk) return vk;
    // Unknown physical key: fall back to the legacy numeric code, which
    // coincides with VK values for most of what's left.
    return e.keyCode || 0;
  }

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

    // Track held keys so they can be released if the tab loses focus mid-press
    // (otherwise the remote machine is left with a stuck modifier).
    const heldVKs = new Set();

    const captureKey = (e) => {
      // Allow browser DevTools shortcuts to pass through
      if (e.ctrlKey && e.shiftKey && (e.key === 'I' || e.key === 'J')) return;
      e.preventDefault();
      const vk = vkFromEvent(e);
      if (!vk) return;
      const isDown = e.type === 'keydown';
      if (isDown) heldVKs.add(vk);
      else heldVKs.delete(vk);
      sendBinary(encodeKey(vk, isDown));
    };

    canvas.addEventListener('keydown', captureKey);
    canvas.addEventListener('keyup', captureKey);

    const releaseHeldKeys = () => {
      heldVKs.forEach((vk) => sendBinary(encodeKey(vk, false)));
      heldVKs.clear();
    };
    window.addEventListener('blur', releaseHeldKeys);
    canvas.addEventListener('blur', releaseHeldKeys);
  });
})();
