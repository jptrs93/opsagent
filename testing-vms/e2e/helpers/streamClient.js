// Client for the protostream testexample's cleanproto streaming RPCs, driven
// through the terminating HTTPS ingress. Requests are hand-driven over raw
// node http1/http2 clients (rather than fetch) so tests control certificate
// trust, SNI, frame pacing, and mid-stream cancellation from the client end.
import {expect} from '@playwright/test';
import {Buffer} from 'node:buffer';
import http2 from 'node:http2';
import https from 'node:https';
import {ingressCA, ingressTarget} from './httpsClient.js';
import {
  decodeEchoReply,
  decodeStreamReport,
  decodeTick,
  decodeUploadSummary,
  encodeChunk,
  encodeEchoRequest,
  encodeStreamReportRequest,
  encodeTickRequest,
} from './protostreamgen/model.js';

const PROTO_CONTENT_TYPE = 'application/x-protobuf';
const STREAM_CONTENT_TYPE = 'application/protobuf-stream';
const OP_TIMEOUT = 30_000;

// uvarint length prefix, matching cleanproto's protobuf-stream framing.
export function encodeFrame(payload) {
  const header = [];
  let remaining = payload.length;
  while (remaining > 0x7f) {
    header.push((remaining & 0x7f) | 0x80);
    remaining = Math.floor(remaining / 128);
  }
  header.push(remaining);
  return Buffer.concat([Buffer.from(header), Buffer.from(payload)]);
}

class FrameParser {
  constructor() {
    this.buffer = Buffer.alloc(0);
  }

  // Returns complete frame payloads parsed from the accumulated bytes.
  push(chunk) {
    this.buffer = Buffer.concat([this.buffer, chunk]);
    const frames = [];
    for (;;) {
      let length = 0;
      let shift = 0;
      let headerLength = 0;
      for (let i = 0; i < this.buffer.length && i < 10; i++) {
        const byte = this.buffer[i];
        length |= (byte & 0x7f) << shift;
        shift += 7;
        if ((byte & 0x80) === 0) {
          headerLength = i + 1;
          break;
        }
      }
      if (headerLength === 0 || this.buffer.length < headerLength + length) return frames;
      frames.push(this.buffer.subarray(headerLength, headerLength + length));
      this.buffer = this.buffer.subarray(headerLength + length);
    }
  }

  get pendingBytes() {
    return this.buffer.length;
  }
}

// Normalized duplex POST over either the http/1.1 or h2 edge. Events queue
// as {frame}, {end}, or {error}; frames record their arrival time so tests
// can prove incremental delivery.
export function openDuplex(hostname, path, {edge = 'h1', contentType = PROTO_CONTENT_TYPE, framed = true} = {}) {
  const target = ingressTarget();
  const ca = ingressCA();
  const parser = new FrameParser();
  const events = [];
  const waiters = [];
  const frameTimes = [];
  let statusResolve;
  let statusReject;
  const status = new Promise((resolve, reject) => {
    statusResolve = resolve;
    statusReject = reject;
  });
  status.catch(() => {});
  const emit = event => {
    const waiter = waiters.shift();
    if (waiter) waiter(event);
    else events.push(event);
  };
  const onData = chunk => {
    // Unary responses are raw protobuf; framed responses are uvarint frames.
    for (const frame of framed ? parser.push(chunk) : [chunk]) {
      frameTimes.push(Date.now());
      emit({frame});
    }
  };
  const onEnd = () => emit(framed && parser.pendingBytes > 0 ? {error: new Error('stream truncated mid-frame')} : {end: true});
  const onError = error => {
    statusReject(error);
    emit({error});
  };

  const conn = {status, frameTimes};
  conn.next = (timeout = OP_TIMEOUT) => {
    if (events.length) return Promise.resolve(events.shift());
    return new Promise((resolve, reject) => {
      const timer = setTimeout(() => {
        const index = waiters.indexOf(waiter);
        if (index >= 0) waiters.splice(index, 1);
        reject(new Error(`timed out waiting for stream event on ${hostname}${path}`));
      }, timeout);
      const waiter = event => {
        clearTimeout(timer);
        resolve(event);
      };
      waiters.push(waiter);
    });
  };
  conn.nextFrame = async (timeout = OP_TIMEOUT) => {
    const event = await conn.next(timeout);
    if (event.error) throw event.error;
    if (event.end) throw new Error('unexpected end of stream');
    return event.frame;
  };

  if (edge === 'h2') {
    const session = http2.connect(`https://${hostname}`, {
      host: target.host,
      port: target.httpsPort,
      servername: hostname,
      ca,
      rejectUnauthorized: true,
    });
    session.on('error', onError);
    const stream = session.request({':method': 'POST', ':path': path, 'content-type': contentType});
    stream.on('response', headers => statusResolve(headers[':status']));
    stream.on('data', onData);
    stream.on('end', onEnd);
    stream.on('error', onError);
    conn.write = frame => stream.write(frame);
    conn.end = () => stream.end();
    conn.cancel = () => {
      // stream.close() would half-close cleanly with END_STREAM; destroy()
      // sends RST_STREAM so the abort is visible server-side.
      stream.destroy(new Error('canceled by test'));
      setImmediate(() => session.destroy());
    };
    conn.close = () => session.close();
    return conn;
  }

  const request = https.request({
    host: target.host,
    port: target.httpsPort,
    path,
    method: 'POST',
    servername: hostname,
    headers: {host: hostname, 'content-type': contentType},
    ca,
    rejectUnauthorized: true,
    agent: false,
  }, response => {
    statusResolve(response.statusCode);
    response.on('data', onData);
    response.on('end', onEnd);
    response.on('error', onError);
  });
  request.on('error', onError);
  conn.write = frame => request.write(frame);
  conn.end = () => request.end();
  conn.cancel = () => request.destroy(new Error('canceled by test'));
  conn.close = () => {};
  return conn;
}

async function collectFrames(conn, decode) {
  const frames = [];
  for (;;) {
    const event = await conn.next();
    if (event.error) return {frames, outcome: `error: ${event.error.message}`};
    if (event.end) return {frames, outcome: 'end'};
    frames.push(decode(bufferToArrayBuffer(event.frame)));
  }
}

function bufferToArrayBuffer(buffer) {
  return buffer.buffer.slice(buffer.byteOffset, buffer.byteOffset + buffer.byteLength);
}

export async function postStreamReport(hostname, streamId, {edge = 'h1'} = {}) {
  const conn = openDuplex(hostname, '/v1/stream-report', {edge, framed: false});
  try {
    conn.write(Buffer.from(encodeStreamReportRequest({streamId})));
    conn.end();
    const status = await conn.status;
    if (status !== 200) throw new Error(`stream-report status ${status}`);
    const raw = [];
    for (;;) {
      const event = await conn.next();
      if (event.error) throw event.error;
      if (event.end) break;
      raw.push(event.frame);
    }
    return decodeStreamReport(bufferToArrayBuffer(Buffer.concat(raw)));
  } finally {
    conn.close();
  }
}

export async function runServerStream(hostname, request, {edge = 'h1', cancelAfterFrames = 0} = {}) {
  const conn = openDuplex(hostname, '/v1/server-stream', {edge});
  conn.write(Buffer.from(encodeTickRequest(request)));
  conn.end();
  const status = await conn.status;
  if (status !== 200) {
    conn.close();
    return {status, ticks: [], outcome: `status ${status}`};
  }
  if (cancelAfterFrames > 0) {
    const ticks = [];
    for (let i = 0; i < cancelAfterFrames; i++) {
      ticks.push(decodeTick(bufferToArrayBuffer(await conn.nextFrame())));
    }
    conn.cancel();
    return {status, ticks, outcome: 'canceled', frameTimes: conn.frameTimes};
  }
  const {frames, outcome} = await collectFrames(conn, decodeTick);
  conn.close();
  return {status, ticks: frames, outcome, frameTimes: conn.frameTimes};
}

export async function runClientStream(hostname, {streamId, chunks, frameGapMs = 0, abortAfterFrames = 0, edge = 'h1'} = {}) {
  // The request body is framed but the single UploadSummary response is not.
  const conn = openDuplex(hostname, '/v1/client-stream', {edge, contentType: STREAM_CONTENT_TYPE, framed: false});
  let sent = 0;
  for (const data of chunks) {
    sent++;
    conn.write(encodeFrame(encodeChunk({streamId, seq: sent, data})));
    if (abortAfterFrames > 0 && sent >= abortAfterFrames) {
      // Give the proxy a beat to forward the flushed frames, then abort the
      // request without a clean end-of-stream.
      await sleep(150);
      conn.cancel();
      return {aborted: true, sent};
    }
    if (frameGapMs > 0) await sleep(frameGapMs);
  }
  conn.end();
  const status = await conn.status;
  const raw = [];
  for (;;) {
    const event = await conn.next();
    if (event.end || event.error) break;
    raw.push(event.frame);
  }
  conn.close();
  if (status !== 200) return {status, sent};
  return {status, sent, summary: decodeUploadSummary(bufferToArrayBuffer(Buffer.concat(raw)))};
}

// Ping-pong bidi conversation: each request frame is sent only after the echo
// of the previous one arrived, so a pass proves neither the request nor the
// response direction is buffered anywhere on the path.
export async function runBidi(hostname, {streamId, messages = 3, cancelAfterEchoes = 0, edge = 'h2'} = {}) {
  const conn = openDuplex(hostname, '/v1/bidi-stream', {edge, contentType: STREAM_CONTENT_TYPE});
  const echoes = [];
  for (let seq = 1; seq <= messages; seq++) {
    const closeStream = cancelAfterEchoes === 0 && seq === messages;
    conn.write(encodeFrame(encodeEchoRequest({streamId, seq, text: `msg-${seq}`, closeStream})));
    echoes.push(decodeEchoReply(bufferToArrayBuffer(await conn.nextFrame())));
    if (cancelAfterEchoes > 0 && seq >= cancelAfterEchoes) {
      conn.cancel();
      return {echoes, outcome: 'canceled'};
    }
  }
  // The final message asked the server to close its side; the response stream
  // must end while the request stream is still open.
  const event = await conn.next();
  conn.end();
  conn.close();
  return {echoes, outcome: event.end ? 'end' : `error: ${event.error?.message || 'frame after close'}`};
}

function sleep(ms) {
  return new Promise(resolve => setTimeout(resolve, ms));
}

// Polls the unary stream-report RPC until the route serves it, covering both
// route propagation and backend startup.
export async function expectStreamRouteReady(hostname, {edge = 'h1', timeout = 180_000} = {}) {
  await expect.poll(async () => {
    try {
      const report = await postStreamReport(hostname, 'readiness-probe', {edge});
      return typeof report.started === 'boolean' ? 'ready' : 'malformed report';
    } catch (error) {
      return String(error?.message || error);
    }
  }, {message: `expected protostream route on ${hostname} (${edge}) to serve`, timeout}).toBe('ready');
}

export async function waitStreamDone(hostname, streamId, {edge = 'h1', timeout = 30_000} = {}) {
  let report;
  await expect.poll(async () => {
    report = await postStreamReport(hostname, streamId, {edge});
    return report.done;
  }, {message: `expected stream ${streamId} to report done`, timeout}).toBe(true);
  return report;
}
