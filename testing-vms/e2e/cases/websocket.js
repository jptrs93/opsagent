import {expect} from '@playwright/test';
import {Buffer} from 'node:buffer';
import {
  createNixDockerDeployment,
  createSecret,
  NETWORKING_VIRTUAL,
  setDeploymentHttpsRoutes,
} from '../helpers/ui.js';
import {ingressCertificateBundle, parseEchoBody, requestHTTPSIngress} from '../helpers/httpsClient.js';
import {connectWebSocket} from '../helpers/wsClient.js';

// The wsecho testexample terminates real RFC 6455 WebSocket connections
// behind the HTTPS ingress. Its /state endpoint reports what the server
// observed per connection, so tests can verify close codes and disconnects
// propagate through the proxied upgrade tunnel.
const WS_HOST = 'ws.ingress.opendeploy.test';
const WS_CERT_SECRET = 'e2e.tls.ingress.ws';
const INGRESS_READY_TIMEOUT = 180_000;

async function fetchWsState(streamId) {
  const response = await requestHTTPSIngress(WS_HOST, {path: `/state?stream_id=${streamId}`});
  if (response.status !== 200) throw new Error(`state status ${response.status}`);
  return parseEchoBody(response.body);
}

async function expectWsState(streamId, expectedFields, {timeout = 30_000} = {}) {
  await expect.poll(async () => {
    try {
      const fields = await fetchWsState(streamId);
      const picked = {};
      for (const key of Object.keys(expectedFields)) picked[key] = fields[key];
      return picked;
    } catch (error) {
      return {error: String(error?.message || error)};
    }
  }, {message: `expected ws state for ${streamId}`, timeout}).toEqual(expectedFields);
}

export async function expectWebSocketRoute() {
  await expect.poll(async () => {
    try {
      return (await requestHTTPSIngress(WS_HOST, {path: '/state?stream_id=readiness-probe'})).status;
    } catch (error) {
      return String(error?.message || error);
    }
  }, {message: `expected wsecho route on ${WS_HOST} to serve`, timeout: INGRESS_READY_TIMEOUT}).toBe(200);
}

export const websocketCases = [
  {
    id: 'wsecho-certificate-created',
    title: 'create websocket certificate secret',
    description: 'Stores the CA-signed TLS bundle for the websocket hostname.',
    requires: ['worker-2-enrolled'],
    async run(ctx) {
      await createSecret(ctx.page, {name: WS_CERT_SECRET, value: Buffer.from(ingressCertificateBundle('ws'), 'base64').toString('utf8')});
    },
  },
  {
    id: 'wsecho-deployment-created',
    title: 'create websocket echo deployment',
    description: 'Creates the WebSocket echo backend on the worker-2 virtual network.',
    requires: ['wsecho-certificate-created'],
    async run(ctx) {
      await createNixDockerDeployment(ctx.page, {
        name: 'wsecho',
        machine: 'worker-2',
        flake: 'testexamples/wsecho/flake.nix',
        networkingMode: NETWORKING_VIRTUAL,
        env: {},
        expectedEnv: {},
      });
    },
  },
  {
    id: 'wsecho-route-added',
    title: 'add websocket HTTPS route',
    description: 'Adds the terminating route for the websocket hostname and waits for it to serve.',
    requires: ['wsecho-deployment-created'],
    async run(ctx) {
      await setDeploymentHttpsRoutes(ctx.page, {
        name: 'wsecho',
        routes: [`https("${WS_HOST}", 8080, { cert = secret("${WS_CERT_SECRET}", { version = 1 }) })`],
      });
      await expectWebSocketRoute();
    },
  },
  {
    id: 'websocket-echo-verified',
    title: 'verify websocket echo',
    description: 'Confirms text and binary frames round-trip through the proxied upgrade tunnel and a normal close handshake completes.',
    requires: ['wsecho-route-added'],
    async run() {
      const ws = await connectWebSocket(WS_HOST, '/ws/echo?stream_id=ws-echo');
      ws.sendText('hello through the proxy');
      let event = await ws.next();
      expect(event.type).toBe('text');
      expect(event.payload.toString()).toBe('hello through the proxy');
      const binary = Buffer.from([0, 1, 2, 250, 251, 252]);
      ws.sendBinary(binary);
      event = await ws.next();
      expect(event.type).toBe('binary');
      expect(Buffer.compare(event.payload, binary)).toBe(0);
      ws.sendClose(1000, 'bye');
      event = await ws.next();
      expect(event.type).toBe('close');
      expect(event.code).toBe(1000);
      ws.destroy();
    },
  },
  {
    id: 'websocket-large-message-verified',
    title: 'verify large websocket message',
    description: 'Confirms a 256KB message (64-bit length frames) survives the round trip in both directions.',
    requires: ['websocket-echo-verified'],
    async run() {
      const ws = await connectWebSocket(WS_HOST, '/ws/echo?stream_id=ws-large');
      const large = 'x'.repeat(256 * 1024);
      ws.sendText(large);
      const event = await ws.next();
      expect(event.type).toBe('text');
      expect(event.payload.length).toBe(large.length);
      expect(event.payload.toString()).toBe(large);
      ws.sendClose(1000, '');
      await ws.next();
      ws.destroy();
    },
  },
  {
    id: 'websocket-ping-pong-verified',
    title: 'verify websocket control frames',
    description: 'Confirms a client ping receives a pong with the same payload through the tunnel.',
    requires: ['websocket-echo-verified'],
    async run() {
      const ws = await connectWebSocket(WS_HOST, '/ws/echo?stream_id=ws-ping');
      ws.sendPing(Buffer.from('are-you-there'));
      const event = await ws.next();
      expect(event.type).toBe('pong');
      expect(event.payload.toString()).toBe('are-you-there');
      ws.sendClose(1000, '');
      await ws.next();
      ws.destroy();
    },
  },
  {
    id: 'websocket-server-close-verified',
    title: 'verify server-initiated close propagates',
    description: 'Confirms a close initiated by the backend reaches the client with its code and reason intact.',
    requires: ['websocket-echo-verified'],
    async run() {
      const ws = await connectWebSocket(WS_HOST, '/ws/echo?stream_id=ws-server-close');
      ws.sendText('close:4321:going away');
      const event = await ws.next();
      expect(event.type).toBe('close');
      expect(event.code).toBe(4321);
      expect(event.reason).toBe('going away');
      ws.sendClose(4321, 'going away');
      ws.destroy();
    },
  },
  {
    id: 'websocket-client-close-verified',
    title: 'verify client-initiated close propagates',
    description: 'Confirms the backend records the close code and reason the client sent.',
    requires: ['websocket-echo-verified'],
    async run() {
      const ws = await connectWebSocket(WS_HOST, '/ws/echo?stream_id=ws-client-close');
      ws.sendText('hello');
      await ws.next();
      ws.sendClose(4111, 'done here');
      const event = await ws.next();
      expect(event.type).toBe('close');
      expect(event.code).toBe(4111);
      ws.destroy();
      await expectWsState('ws-client-close', {
        'close-code': '4111',
        'close-reason': 'done here',
        result: 'closed',
      });
    },
  },
  {
    id: 'websocket-push-cancel-verified',
    title: 'verify abrupt client disconnect propagates',
    description: 'Confirms server pushes stream incrementally and an abrupt client disconnect promptly unblocks the backend.',
    requires: ['websocket-echo-verified'],
    async run() {
      const ws = await connectWebSocket(WS_HOST, '/ws/push?stream_id=ws-push-cancel&count=1000&interval_ms=50');
      for (let i = 0; i < 5; i++) {
        const event = await ws.next();
        expect(event.type).toBe('text');
      }
      // Pushes are spaced 50ms apart; buffered delivery would collapse the
      // arrival spread to near zero.
      expect(ws.messageTimes[ws.messageTimes.length - 1] - ws.messageTimes[0]).toBeGreaterThan(100);
      ws.destroy();
      await expectWsState('ws-push-cancel', {result: 'client-gone'});
    },
  },
  {
    id: 'websocket-push-completed-verified',
    title: 'verify server push completes',
    description: 'Confirms a bounded server push delivers every message and ends with a normal close.',
    requires: ['websocket-echo-verified'],
    async run() {
      const ws = await connectWebSocket(WS_HOST, '/ws/push?stream_id=ws-push-done&count=10&interval_ms=30');
      let texts = 0;
      for (;;) {
        const event = await ws.next();
        if (event.type === 'text') {
          texts++;
          continue;
        }
        expect(event.type).toBe('close');
        expect(event.code).toBe(1000);
        break;
      }
      expect(texts).toBe(10);
      ws.sendClose(1000, '');
      ws.destroy();
      await expectWsState('ws-push-done', {result: 'completed', 'messages-out': '10'});
    },
  },
];
