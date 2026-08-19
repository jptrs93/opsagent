import {expect} from '@playwright/test';
import {Buffer} from 'node:buffer';
import crypto from 'node:crypto';
import http from 'node:http';
import http2 from 'node:http2';
import https from 'node:https';
import tls from 'node:tls';

const INGRESS_READY_TIMEOUT = 180_000;
const REQUEST_TIMEOUT = 15_000;
const UNAVAILABLE_PROBE_TIMEOUT = 3_000;

export function ingressTarget() {
  return {
    host: process.env.OPD_TLS_INGRESS_HOST || 'host.docker.internal',
    httpsPort: Number(process.env.OPD_TLS_INGRESS_PORT || '18443'),
    httpPort: Number(process.env.OPD_HTTP_INGRESS_PORT || '18980'),
  };
}

export function ingressCA() {
  const ca = Buffer.from(process.env.OPD_TLS_INGRESS_CA_B64 || '', 'base64');
  if (ca.length === 0) throw new Error('OPD_TLS_INGRESS_CA_B64 is required');
  return ca;
}

export function ingressCertificateBundle(id) {
  const value = process.env[`OPD_TLS_INGRESS_CERT_${id.toUpperCase()}_B64`];
  if (!value) throw new Error(`missing ingress certificate bundle for ${id}`);
  return value;
}

export function requestHTTPSIngress(hostname, {
  path = '/',
  method = 'GET',
  headers = {},
  body = null,
  servername = hostname,
} = {}) {
  const target = ingressTarget();
  const ca = ingressCA();
  return new Promise((resolve, reject) => {
    let fingerprint;
    const chunkTimes = [];
    const req = https.request({
      host: target.host,
      port: target.httpsPort,
      path,
      method,
      servername,
      headers: {host: hostname, ...headers},
      ca,
      rejectUnauthorized: true,
      agent: false,
    }, response => {
      let responseBody = '';
      response.setEncoding('utf8');
      response.on('data', chunk => {
        responseBody += chunk;
        chunkTimes.push(Date.now());
      });
      response.on('end', () => {
        resolve({
          status: response.statusCode,
          headers: response.headers,
          body: responseBody,
          fingerprint,
          chunkTimes,
        });
      });
    });
    req.on('socket', socket => socket.on('secureConnect', () => {
      fingerprint = socket.getPeerCertificate().fingerprint256;
    }));
    req.setTimeout(REQUEST_TIMEOUT, () => req.destroy(new Error(`HTTPS ingress request to ${hostname}${path} timed out`)));
    req.on('error', reject);
    if (body) req.write(body);
    req.end();
  });
}

export function parseEchoBody(body) {
  const fields = {};
  for (const line of String(body || '').split('\n')) {
    const separator = line.indexOf('=');
    if (separator > 0) fields[line.slice(0, separator)] = line.slice(separator + 1);
  }
  return fields;
}

export async function expectHTTPSEcho(hostname, path, expectedFields, {
  certificateBundle,
  timeout = INGRESS_READY_TIMEOUT,
} = {}) {
  const expectedFingerprint = certificateBundle
    ? new crypto.X509Certificate(Buffer.from(certificateBundle, 'base64')).fingerprint256
    : null;
  await expect.poll(async () => {
    try {
      const response = await requestHTTPSIngress(hostname, {path});
      if (response.status !== 200) return {status: response.status};
      const result = {status: 200, ...pick(parseEchoBody(response.body), Object.keys(expectedFields))};
      if (expectedFingerprint) result.fingerprint = response.fingerprint;
      return result;
    } catch (error) {
      return {error: String(error?.message || error)};
    }
  }, {message: `expected HTTPS echo from ${hostname}${path}`, timeout}).toEqual({
    status: 200,
    ...expectedFields,
    ...(expectedFingerprint ? {fingerprint: expectedFingerprint} : {}),
  });
}

function pick(fields, keys) {
  const result = {};
  for (const key of keys) result[key] = fields[key];
  return result;
}

export async function expectHTTPSStatus(hostname, path, expectedStatus, {
  method = 'GET',
  headers = {},
  body = null,
  servername,
  timeout = INGRESS_READY_TIMEOUT,
} = {}) {
  await expect.poll(async () => {
    try {
      const response = await requestHTTPSIngress(hostname, {path, method, headers, body, servername});
      return response.status;
    } catch (error) {
      return String(error?.message || error);
    }
  }, {message: `expected HTTPS ${method} ${hostname}${path} to return ${expectedStatus}`, timeout}).toBe(expectedStatus);
}

// probeTLSIngressClosed makes one TLS connection attempt with the given SNI
// and classifies the outcome. The healthy fail-closed path always answers
// fast: either the worker refuses the connection outright (no DNAT rule,
// surfaced through the ssh tunnel as an immediate close of the forwarded
// connection) or netproxy reads the ClientHello and closes on an unrouted
// SNI. Two outcomes must never read as "correctly unavailable": a local
// ECONNREFUSED before the TCP connect completes (the ssh tunnel listener
// itself is down) and a timeout (packets silently dropped somewhere between
// the tunnel and netproxy — the signature of an ingress blackhole, worth
// capturing network state from the worker before restarting anything).
export function probeTLSIngressClosed(hostname) {
  const target = ingressTarget();
  const ca = ingressCA();
  return new Promise(resolve => {
    let done = false;
    let tcpConnected = false;
    const socket = tls.connect({
      host: target.host,
      port: target.httpsPort,
      servername: hostname,
      ca,
      rejectUnauthorized: true,
    });
    const finish = outcome => {
      if (done) return;
      done = true;
      clearTimeout(timer);
      socket.destroy();
      resolve(outcome);
    };
    const timer = setTimeout(() => finish({kind: 'timeout', detail: tcpConnected ? 'connected, TLS handshake stalled' : 'TCP connect stalled'}), UNAVAILABLE_PROBE_TIMEOUT);
    socket.on('connect', () => { tcpConnected = true; });
    socket.on('secureConnect', () => finish({kind: 'established', detail: 'TLS handshake completed'}));
    socket.on('error', error => {
      const code = error?.code || '';
      if (!tcpConnected && code === 'ECONNREFUSED') {
        finish({kind: 'tunnel-refused', detail: String(error?.message || error)});
        return;
      }
      if (['ECONNRESET', 'ECONNREFUSED', 'ECONNABORTED', 'EPIPE'].includes(code) ||
          String(error?.message || '').includes('disconnected before secure TLS connection')) {
        finish({kind: 'rejected', detail: String(error?.message || error)});
        return;
      }
      // Anything else (certificate errors, protocol errors) means a TLS
      // server answered, so the route is not closed.
      finish({kind: 'tls-responded', detail: String(error?.message || error)});
    });
    socket.on('close', () => finish({kind: 'rejected', detail: 'connection closed before TLS was established'}));
  });
}

export async function expectHTTPSIngressUnavailable(hostname, {timeout = INGRESS_READY_TIMEOUT} = {}) {
  await expectTLSProbeRejected(hostname, `expected HTTPS ingress for ${hostname} to fail closed`, timeout);
}

// expectTLSProbeRejected polls until a probe reports the remote side
// rejecting the connection. A tunnel failure or a silent packet drop aborts
// the poll immediately with a diagnosis instead of masquerading as a pass
// (blackholes) or burning the poll budget (dead tunnel).
export async function expectTLSProbeRejected(hostname, message, timeout) {
  await expect.poll(async () => {
    const probe = await probeTLSIngressClosed(hostname);
    if (probe.kind === 'timeout') {
      throw new Error(`TLS probe for ${hostname} timed out after ${UNAVAILABLE_PROBE_TIMEOUT}ms (${probe.detail}): ` +
        'the ingress path is silently dropping packets instead of refusing — capture nft/conntrack/route state from the worker before restarting anything');
    }
    if (probe.kind === 'tunnel-refused') {
      throw new Error(`TLS probe for ${hostname} was refused by the local tunnel listener (${probe.detail}): ` +
        'the ssh ingress tunnel is down, not the product ingress');
    }
    return probe.kind;
  }, {message, timeout}).toBe('rejected');
}

// Proves the response body was streamed rather than buffered: the backend emits
// 20 SSE events spread over ~5s, so a streaming path shows a wide spread
// between the first and last chunk arrival while full buffering delivers
// everything at once.
export async function expectSSEStreaming(hostname, path, {timeout = INGRESS_READY_TIMEOUT} = {}) {
  await expect.poll(async () => {
    try {
      const response = await requestHTTPSIngress(hostname, {path, headers: {accept: 'text/event-stream'}});
      if (response.status !== 200) return {status: response.status};
      const events = response.body.split('\n\n').filter(part => part.startsWith('data: '));
      const spreadMs = response.chunkTimes.length > 1
        ? response.chunkTimes[response.chunkTimes.length - 1] - response.chunkTimes[0]
        : 0;
      return {status: 200, events: events.length, streamed: spreadMs > 2_000};
    } catch (error) {
      return {error: String(error?.message || error)};
    }
  }, {message: `expected streamed SSE from ${hostname}${path}`, timeout}).toEqual({status: 200, events: 20, streamed: true});
}

export function requestUpgradeEcho(hostname, path, lines) {
  const target = ingressTarget();
  const ca = ingressCA();
  return new Promise((resolve, reject) => {
    const req = https.request({
      host: target.host,
      port: target.httpsPort,
      path,
      method: 'GET',
      servername: hostname,
      headers: {host: hostname, connection: 'Upgrade', upgrade: 'opendeploy-echo'},
      ca,
      rejectUnauthorized: true,
      agent: false,
    });
    const timer = setTimeout(() => {
      req.destroy(new Error(`upgrade request to ${hostname}${path} timed out`));
      reject(new Error(`upgrade request to ${hostname}${path} timed out`));
    }, REQUEST_TIMEOUT);
    req.on('upgrade', (response, socket) => {
      const received = [];
      let buffered = '';
      socket.setEncoding('utf8');
      socket.on('data', chunk => {
        buffered += chunk;
        let newline = buffered.indexOf('\n');
        while (newline >= 0) {
          received.push(buffered.slice(0, newline));
          buffered = buffered.slice(newline + 1);
          newline = buffered.indexOf('\n');
        }
        if (received.length >= lines.length) {
          clearTimeout(timer);
          socket.destroy();
          resolve({status: response.statusCode, upgrade: response.headers.upgrade, received});
        }
      });
      socket.on('error', error => {
        clearTimeout(timer);
        reject(error);
      });
      socket.write(lines.join('\n') + '\n');
    });
    req.on('response', response => {
      clearTimeout(timer);
      reject(new Error(`expected 101 upgrade for ${hostname}${path}, got HTTP ${response.statusCode}`));
    });
    req.on('error', error => {
      clearTimeout(timer);
      reject(error);
    });
    req.end();
  });
}

export async function expectUpgradeEcho(hostname, path, backend, {timeout = INGRESS_READY_TIMEOUT} = {}) {
  const lines = ['first message', 'second message'];
  await expect.poll(async () => {
    try {
      return await requestUpgradeEcho(hostname, path, lines);
    } catch (error) {
      return {error: String(error?.message || error)};
    }
  }, {message: `expected upgrade echo via ${hostname}${path}`, timeout}).toEqual({
    status: 101,
    upgrade: 'opendeploy-echo',
    received: lines.map(line => `echo:${line} backend=${backend}`),
  });
}

export function requestHTTPIngress(hostname, path) {
  const target = ingressTarget();
  return new Promise((resolve, reject) => {
    const req = http.request({
      host: target.host,
      port: target.httpPort,
      path,
      method: 'GET',
      headers: {host: hostname},
      agent: false,
    }, response => {
      let body = '';
      response.setEncoding('utf8');
      response.on('data', chunk => { body += chunk; });
      response.on('end', () => resolve({status: response.statusCode, headers: response.headers, body}));
    });
    req.setTimeout(REQUEST_TIMEOUT, () => req.destroy(new Error(`HTTP ingress request to ${hostname}${path} timed out`)));
    req.on('error', reject);
    req.end();
  });
}

export async function expectHTTPRedirect(hostname, path, {timeout = INGRESS_READY_TIMEOUT} = {}) {
  await expect.poll(async () => {
    try {
      const response = await requestHTTPIngress(hostname, path);
      return {status: response.status, location: response.headers.location};
    } catch (error) {
      return {error: String(error?.message || error)};
    }
  }, {message: `expected HTTP redirect for ${hostname}${path}`, timeout}).toEqual({
    status: 301,
    location: `https://${hostname}${path}`,
  });
}

export function requestH2Ingress(hostname, path) {
  const target = ingressTarget();
  const ca = ingressCA();
  return new Promise((resolve, reject) => {
    const session = http2.connect(`https://${hostname}`, {
      host: target.host,
      port: target.httpsPort,
      servername: hostname,
      ca,
      rejectUnauthorized: true,
    });
    const fail = error => {
      session.destroy();
      reject(error);
    };
    const timer = setTimeout(() => fail(new Error(`h2 request to ${hostname}${path} timed out`)), REQUEST_TIMEOUT);
    session.on('error', error => {
      clearTimeout(timer);
      fail(error);
    });
    session.on('connect', () => {
      const alpn = session.socket instanceof tls.TLSSocket ? session.socket.alpnProtocol : '';
      const req = session.request({':path': path});
      let status = 0;
      let body = '';
      req.setEncoding('utf8');
      req.on('response', headers => { status = headers[':status']; });
      req.on('data', chunk => { body += chunk; });
      req.on('end', () => {
        clearTimeout(timer);
        session.close();
        resolve({status, body, alpn});
      });
      req.on('error', error => {
        clearTimeout(timer);
        fail(error);
      });
      req.end();
    });
  });
}

export async function expectH2Ingress(hostname, path, expectedFields, {timeout = INGRESS_READY_TIMEOUT} = {}) {
  await expect.poll(async () => {
    try {
      const response = await requestH2Ingress(hostname, path);
      if (response.status !== 200) return {status: response.status, alpn: response.alpn};
      return {status: 200, alpn: response.alpn, ...pick(parseEchoBody(response.body), Object.keys(expectedFields))};
    } catch (error) {
      return {error: String(error?.message || error)};
    }
  }, {message: `expected h2 response from ${hostname}${path}`, timeout}).toEqual({
    status: 200,
    alpn: 'h2',
    ...expectedFields,
  });
}
