import assert from 'node:assert/strict';
import { readFile } from 'node:fs/promises';
import test from 'node:test';
import vm from 'node:vm';

const viewerSource = await readFile(new URL('../agent/viewer.js', import.meta.url), 'utf8');

function loadViewer() {
  const decoded = [];
  let socket;

  class FakeBlob {
    constructor(parts, options = {}) {
      this.parts = parts;
      this.type = options.type || '';
    }

    async arrayBuffer() {
      const size = this.parts.reduce((total, part) => total + part.byteLength, 0);
      const bytes = new Uint8Array(size);
      let offset = 0;
      for (const part of this.parts) {
        bytes.set(new Uint8Array(part.buffer, part.byteOffset, part.byteLength), offset);
        offset += part.byteLength;
      }
      return bytes.buffer;
    }
  }

  const classList = { add() {}, remove() {} };
  const elements = new Map();
  const element = (id) => {
    if (!elements.has(id)) {
      elements.set(id, {
        addEventListener() {},
        classList,
        className: '',
        clientHeight: 600,
        clientWidth: 800,
        getContext: () => ({ drawImage() {} }),
        style: {},
        textContent: '',
      });
    }
    return elements.get(id);
  };

  class FakeWebSocket {
    static OPEN = 1;

    constructor() {
      this.readyState = FakeWebSocket.OPEN;
      socket = this;
    }

    close() {}
    send() {}
  }

  const location = {
    host: 'example.test',
    protocol: 'https:',
    search: '?code=123456',
  };
  const window = {
    addEventListener() {},
    createImageBitmap(blob) {
      decoded.push(blob);
      return new Promise(() => {});
    },
    location,
  };
  const document = {
    getElementById: element,
    hasFocus: () => false,
  };

  const context = vm.createContext({
    ArrayBuffer,
    Blob: FakeBlob,
    DataView,
    Image: class {},
    JSON,
    Math,
    TextDecoder,
    TextEncoder,
    URL,
    URLSearchParams,
    Uint8Array,
    WebSocket: FakeWebSocket,
    createImageBitmap: window.createImageBitmap,
    document,
    encodeURIComponent,
    location,
    navigator: {},
    setInterval() {},
    setTimeout() {},
    window,
  });
  vm.runInContext(viewerSource, context, { filename: 'viewer.js' });
  return { decoded, socket };
}

function fullFrame(payload) {
  const buffer = new ArrayBuffer(9 + payload.length);
  const view = new DataView(buffer);
  view.setUint8(0, 0x01);
  view.setUint32(1, 1920);
  view.setUint32(5, 1080);
  new Uint8Array(buffer, 9).set(payload);
  return buffer;
}

function regionFrame(payload) {
  const buffer = new ArrayBuffer(17 + payload.length);
  const view = new DataView(buffer);
  view.setUint8(0, 0x0c);
  view.setUint32(1, 10);
  view.setUint32(5, 20);
  view.setUint32(9, 30);
  view.setUint32(13, 40);
  new Uint8Array(buffer, 17).set(payload);
  return buffer;
}

for (const [name, makeFrame] of [
  ['full frame', fullFrame],
  ['region frame', regionFrame],
]) {
  test(`${name} reaches the decoder without slicing its ArrayBuffer`, async () => {
    const { decoded, socket } = loadViewer();
    const payload = Uint8Array.of(0x52, 0x49, 0x46, 0x46, 0xaa, 0xbb);
    const buffer = makeFrame(payload);
    let slices = 0;
    const originalSlice = buffer.slice;
    buffer.slice = function (...args) {
      slices++;
      return originalSlice.apply(this, args);
    };
    socket.onmessage({ data: buffer });

    assert.equal(slices, 0);
    assert.equal(decoded.length, 1);
    assert.equal(decoded[0].type, 'image/webp');
    assert.deepEqual(
      new Uint8Array(await decoded[0].arrayBuffer()),
      payload,
    );
  });
}
