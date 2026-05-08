(function () {
  // control.js — captures mouse and keyboard events on the canvas and forwards
  // them to the relay as JSON input messages.
  // Must load before viewer.js so the canvas element exists.

  const BUTTONS = ['left', 'middle', 'right'];

  function getCanvasPos(canvas, e) {
    const rect = canvas.getBoundingClientRect();
    const scaleX = canvas.width / rect.width;
    const scaleY = canvas.height / rect.height;
    return {
      x: Math.round((e.clientX - rect.left) * scaleX),
      y: Math.round((e.clientY - rect.top) * scaleY),
    };
  }

  function send(obj) {
    if (window._viewer) window._viewer.sendMsg(obj);
  }

  document.addEventListener('DOMContentLoaded', () => {
    const canvas = document.getElementById('canvas');
    if (!canvas) return;

    // Mouse events
    canvas.addEventListener('mousemove', e => {
      const { x, y } = getCanvasPos(canvas, e);
      send({ type: 'mouse_move', x, y });
    });

    canvas.addEventListener('mousedown', e => {
      e.preventDefault();
      const { x, y } = getCanvasPos(canvas, e);
      send({ type: 'mouse_down', x, y, btn: BUTTONS[e.button] || 'left' });
    });

    canvas.addEventListener('mouseup', e => {
      const { x, y } = getCanvasPos(canvas, e);
      send({ type: 'mouse_up', x, y, btn: BUTTONS[e.button] || 'left' });
    });

    canvas.addEventListener('wheel', e => {
      e.preventDefault();
      const { x, y } = getCanvasPos(canvas, e);
      send({ type: 'scroll', x, y, dx: Math.round(e.deltaX / 40), dy: Math.round(e.deltaY / 40) });
    }, { passive: false });

    canvas.addEventListener('contextmenu', e => e.preventDefault());

    // Keyboard events — capture while canvas area is in focus
    canvas.setAttribute('tabindex', '0');
    canvas.addEventListener('click', () => canvas.focus());

    const captureKey = e => {
      // Allow browser DevTools shortcuts to pass through
      if (e.ctrlKey && e.shiftKey && (e.key === 'I' || e.key === 'J')) return;
      e.preventDefault();
      send({ type: e.type === 'keydown' ? 'key_down' : 'key_up', vk: e.keyCode, key: e.key });
    };

    canvas.addEventListener('keydown', captureKey);
    canvas.addEventListener('keyup', captureKey);
  });
})();
