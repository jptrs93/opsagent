// Minimal RFC 6455 WebSocket client used to drive the wsecho testexample
// through the terminating HTTPS ingress. Frames are hand-encoded so tests
// control masking, close codes, and abrupt disconnects precisely.
import {Buffer} from 'node:buffer';
import crypto from 'node:crypto';
import https from 'node:https';
import {ingressCA, ingressTarget} from './httpsClient.js';

const WEBSOCKET_GUID = '258EAFA5-E914-47DA-95CA-C5AB0DC85B11';
const OP_TIMEOUT = 30_000;

const OPCODE = {text: 0x1, binary: 0x2, close: 0x8, ping: 0x9, pong: 0xa};
const OPCODE_NAMES = {0x1: 'text', 0x2: 'binary', 0x8: 'close', 0x9: 'ping', 0xa: 'pong'};

// Client-to-server frames must be masked per RFC 6455.
function encodeClientFrame(opcode, payload) {
  const mask = crypto.randomBytes(4);
  const masked = Buffer.from(payload);
  for (let i = 0; i < masked.length; i++) masked[i] ^= mask[i % 4];
  const parts = [];
  const length = masked.length;
  if (length < 126) {
    parts.push(Buffer.from([0x80 | opcode, 0x80 | length]));
  } else if (length <= 0xffff) {
    const header = Buffer.alloc(4);
    header[0] = 0x80 | opcode;
    header[1] = 0x80 | 126;
    header.writeUInt16BE(length, 2);
    parts.push(header);
  } else {
    const header = Buffer.alloc(10);
    header[0] = 0x80 | opcode;
    header[1] = 0x80 | 127;
    header.writeBigUInt64BE(BigInt(length), 2);
    parts.push(header);
  }
  parts.push(mask, masked);
  return Buffer.concat(parts);
}

class WsFrameParser {
  constructor() {
    this.buffer = Buffer.alloc(0);
  }

  push(chunk) {
    this.buffer = Buffer.concat([this.buffer, chunk]);
    const frames = [];
    for (;;) {
      if (this.buffer.length < 2) return frames;
      const opcode = this.buffer[0] & 0x0f;
      const masked = (this.buffer[1] & 0x80) !== 0;
      let length = this.buffer[1] & 0x7f;
      let offset = 2;
      if (length === 126) {
        if (this.buffer.length < 4) return frames;
        length = this.buffer.readUInt16BE(2);
        offset = 4;
      } else if (length === 127) {
        if (this.buffer.length < 10) return frames;
        length = Number(this.buffer.readBigUInt64BE(2));
        offset = 10;
      }
      const maskLength = masked ? 4 : 0;
      if (this.buffer.length < offset + maskLength + length) return frames;
      let payload = this.buffer.subarray(offset + maskLength, offset + maskLength + length);
      if (masked) {
        const mask = this.buffer.subarray(offset, offset + 4);
        payload = Buffer.from(payload);
        for (let i = 0; i < payload.length; i++) payload[i] ^= mask[i % 4];
      }
      frames.push({opcode, payload: Buffer.from(payload)});
      this.buffer = this.buffer.subarray(offset + maskLength + length);
    }
  }
}

export function connectWebSocket(hostname, path) {
  const target = ingressTarget();
  const ca = ingressCA();
  const key = crypto.randomBytes(16).toString('base64');
  const expectedAccept = crypto.createHash('sha1').update(key + WEBSOCKET_GUID).digest('base64');
  return new Promise((resolve, reject) => {
    const request = https.request({
      host: target.host,
      port: target.httpsPort,
      path,
      method: 'GET',
      servername: hostname,
      headers: {
        host: hostname,
        connection: 'Upgrade',
        upgrade: 'websocket',
        'sec-websocket-key': key,
        'sec-websocket-version': '13',
      },
      ca,
      rejectUnauthorized: true,
      agent: false,
    });
    const timer = setTimeout(() => {
      request.destroy(new Error(`websocket upgrade to ${hostname}${path} timed out`));
      reject(new Error(`websocket upgrade to ${hostname}${path} timed out`));
    }, OP_TIMEOUT);
    request.on('upgrade', (response, socket) => {
      clearTimeout(timer);
      if (response.headers['sec-websocket-accept'] !== expectedAccept) {
        socket.destroy();
        reject(new Error('invalid Sec-WebSocket-Accept'));
        return;
      }
      resolve(wrapSocket(hostname, path, socket));
    });
    request.on('response', response => {
      clearTimeout(timer);
      reject(new Error(`expected 101 upgrade for ${hostname}${path}, got HTTP ${response.statusCode}`));
    });
    request.on('error', error => {
      clearTimeout(timer);
      reject(error);
    });
    request.end();
  });
}

function wrapSocket(hostname, path, socket) {
  const parser = new WsFrameParser();
  const events = [];
  const waiters = [];
  const messageTimes = [];
  const emit = event => {
    const waiter = waiters.shift();
    if (waiter) waiter(event);
    else events.push(event);
  };
  socket.on('data', chunk => {
    for (const {opcode, payload} of parser.push(chunk)) {
      messageTimes.push(Date.now());
      if (opcode === OPCODE.close) {
        const code = payload.length >= 2 ? payload.readUInt16BE(0) : 0;
        emit({type: 'close', code, reason: payload.subarray(2).toString('utf8')});
      } else {
        emit({type: OPCODE_NAMES[opcode] || `opcode-${opcode}`, payload});
      }
    }
  });
  socket.on('end', () => emit({type: 'socket-end'}));
  socket.on('error', error => emit({type: 'socket-error', error}));

  return {
    messageTimes,
    sendText: text => socket.write(encodeClientFrame(OPCODE.text, Buffer.from(text, 'utf8'))),
    sendBinary: payload => socket.write(encodeClientFrame(OPCODE.binary, payload)),
    sendPing: payload => socket.write(encodeClientFrame(OPCODE.ping, payload)),
    sendClose: (code, reason = '') => {
      const payload = Buffer.alloc(2 + Buffer.byteLength(reason));
      payload.writeUInt16BE(code, 0);
      payload.write(reason, 2);
      socket.write(encodeClientFrame(OPCODE.close, payload));
    },
    destroy: () => socket.destroy(),
    next: (timeout = OP_TIMEOUT) => {
      if (events.length) return Promise.resolve(events.shift());
      return new Promise((resolve, reject) => {
        const timer = setTimeout(() => {
          const index = waiters.indexOf(waiter);
          if (index >= 0) waiters.splice(index, 1);
          reject(new Error(`timed out waiting for websocket event on ${hostname}${path}`));
        }, timeout);
        const waiter = event => {
          clearTimeout(timer);
          resolve(event);
        };
        waiters.push(waiter);
      });
    },
  };
}
