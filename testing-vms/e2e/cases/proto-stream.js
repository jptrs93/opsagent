import {expect} from '@playwright/test';
import {Buffer} from 'node:buffer';
import {
  createNixDockerDeployment,
  createSecret,
  NETWORKING_VIRTUAL,
  httpsRouteBlock,
  setDeploymentHttpsRoutes,
} from '../helpers/ui.js';
import {ingressCertificateBundle} from '../helpers/httpsClient.js';
import {
  expectStreamRouteReady,
  runBidi,
  runClientStream,
  runServerStream,
  waitStreamDone,
} from '../helpers/streamClient.js';

// The protostream testexample serves cleanproto streaming RPCs (unary,
// server-stream, client-stream, bidi) behind two terminating hostnames: one
// proxied to the backend over h2c and one over HTTP/1.1. Bidirectional cases
// run only against the h2c hostname — interleaved reads and writes need
// HTTP/2 end to end (the Go HTTP/1.1 server drains the request body once a
// handler starts writing).
const STREAM_HOST = 'stream.ingress.opendeploy.test';
const STREAM_H1_HOST = 'streamh1.ingress.opendeploy.test';
const STREAM_CERT_SECRET = 'e2e.tls.ingress.stream';
const STREAM_H1_CERT_SECRET = 'e2e.tls.ingress.streamh1';

// Edge protocol per hostname: h2 toward the h2c-backed route, http/1.1
// toward the h1-backed route, so both edge stacks stay covered.
const HOSTS = [
  {hostname: STREAM_HOST, edge: 'h2'},
  {hostname: STREAM_H1_HOST, edge: 'h1'},
];

export async function expectProtoStreamRoutes() {
  for (const {hostname, edge} of HOSTS) {
    await expectStreamRouteReady(hostname, {edge});
  }
}

export const protoStreamCases = [
  {
    id: 'protostream-certificates-created',
    title: 'create protostream certificate secrets',
    description: 'Stores the CA-signed TLS bundles for the two protostream hostnames.',
    requires: ['worker-2-enrolled'],
    async run(ctx) {
      await createSecret(ctx.page, {name: STREAM_CERT_SECRET, value: Buffer.from(ingressCertificateBundle('stream'), 'base64').toString('utf8')});
      await createSecret(ctx.page, {name: STREAM_H1_CERT_SECRET, value: Buffer.from(ingressCertificateBundle('streamh1'), 'base64').toString('utf8')});
    },
  },
  {
    id: 'protostream-deployment-created',
    title: 'create protostream deployment',
    description: 'Creates the cleanproto streaming backend on the worker-2 virtual network.',
    requires: ['protostream-certificates-created'],
    async run(ctx) {
      await createNixDockerDeployment(ctx.page, {
        name: 'protostream',
        machine: 'worker-2',
        flake: 'testexamples/protostream/flake.nix',
        networkingMode: NETWORKING_VIRTUAL,
        env: {},
        expectedEnv: {},
      });
    },
  },
  {
    id: 'protostream-routes-added',
    title: 'add protostream HTTPS routes',
    description: 'Routes one hostname to the backend over h2c and one over HTTP/1.1, then waits for both to serve.',
    requires: ['protostream-deployment-created'],
    async run(ctx) {
      await setDeploymentHttpsRoutes(ctx.page, {
        name: 'protostream',
        routes: [
          httpsRouteBlock({hostname: STREAM_HOST, containerPort: 8080, backend: 'h2c', cert: `secret("global", "${STREAM_CERT_SECRET}", 1)`}),
          httpsRouteBlock({hostname: STREAM_H1_HOST, containerPort: 8080, cert: `secret("global", "${STREAM_H1_CERT_SECRET}", 1)`}),
        ],
      });
      await expectProtoStreamRoutes();
    },
  },
  {
    id: 'protostream-server-stream-verified',
    title: 'verify server streaming',
    description: 'Streams ticks through both hostnames and confirms ordered frames, incremental delivery, and a completed backend report.',
    requires: ['protostream-routes-added'],
    async run() {
      for (const {hostname, edge} of HOSTS) {
        const streamId = `ss-${edge}`;
        const {status, ticks, outcome, frameTimes} = await runServerStream(hostname, {
          streamId, count: 8, intervalMs: 100, payloadBytes: 512,
        }, {edge});
        expect(status).toBe(200);
        expect(outcome).toBe('end');
        expect(ticks.map(tick => tick.seq)).toEqual([1, 2, 3, 4, 5, 6, 7, 8]);
        expect(ticks[0].padding.length).toBe(512);
        // Ticks are spaced 100ms apart; buffered delivery would collapse the
        // arrival spread to near zero.
        expect(frameTimes[frameTimes.length - 1] - frameTimes[0]).toBeGreaterThan(400);
        const report = await waitStreamDone(hostname, streamId, {edge});
        expect(report.result).toBe('completed');
        expect(report.messagesOut).toBe(8);
      }
    },
  },
  {
    id: 'protostream-server-stream-cancel-verified',
    title: 'verify client cancel of a server stream',
    description: 'Cancels mid-stream from the client and confirms the backend handler observes the cancellation promptly.',
    requires: ['protostream-server-stream-verified'],
    async run() {
      for (const {hostname, edge} of HOSTS) {
        const streamId = `ss-cancel-${edge}`;
        const {ticks, outcome} = await runServerStream(hostname, {
          streamId, count: 1000, intervalMs: 50,
        }, {edge, cancelAfterFrames: 3});
        expect(outcome).toBe('canceled');
        expect(ticks.length).toBe(3);
        const report = await waitStreamDone(hostname, streamId, {edge});
        expect(report.result).toMatch(/context-canceled|write-failed/);
        expect(report.messagesOut).toBeLessThan(1000);
      }
    },
  },
  {
    id: 'protostream-server-abort-verified',
    title: 'verify server-side stream abort',
    description: 'Confirms a mid-stream backend failure stops the response after the delivered frames and the report records the abort.',
    requires: ['protostream-server-stream-verified'],
    async run() {
      // An uncompressed abort is indistinguishable from a clean end at the
      // HTTP layer (the handler just returns), so the delivered-frame count
      // plus the backend report carry the assertion.
      const {ticks} = await runServerStream(STREAM_H1_HOST, {
        streamId: 'ss-abort', count: 100, intervalMs: 20, failAfter: 4,
      }, {edge: 'h1'});
      expect(ticks.length).toBe(4);
      const report = await waitStreamDone(STREAM_H1_HOST, 'ss-abort', {edge: 'h1'});
      expect(report.result).toBe('aborted');
    },
  },
  {
    id: 'protostream-client-stream-verified',
    title: 'verify client streaming',
    description: 'Uploads paced frames through both hostnames and confirms the backend digest matches.',
    requires: ['protostream-routes-added'],
    async run() {
      for (const {hostname, edge} of HOSTS) {
        const streamId = `cs-${edge}`;
        const chunks = Array.from({length: 6}, (_, i) => Buffer.alloc(2048, i + 1));
        const {status, summary} = await runClientStream(hostname, {streamId, chunks, frameGapMs: 80, edge});
        expect(status).toBe(200);
        expect(summary.frames).toBe(6);
        expect(summary.totalBytes).toBe(6 * 2048);
        expect(summary.sha256).toHaveLength(64);
        const report = await waitStreamDone(hostname, streamId, {edge});
        expect(report.result).toBe('completed');
        expect(report.messagesIn).toBe(6);
      }
    },
  },
  {
    id: 'protostream-client-stream-abort-verified',
    title: 'verify client stream abort surfaces as an error',
    description: 'Aborts an upload mid-stream and confirms the backend sees a receive error rather than a clean end of stream.',
    requires: ['protostream-client-stream-verified'],
    async run() {
      // If the proxy converted the abort into a clean end of stream, a
      // partial upload would be indistinguishable from a complete one.
      for (const {hostname, edge} of HOSTS) {
        const streamId = `cs-abort-${edge}`;
        const chunks = Array.from({length: 10}, () => Buffer.alloc(512, 7));
        const {aborted, sent} = await runClientStream(hostname, {
          streamId, chunks, frameGapMs: 60, abortAfterFrames: 3, edge,
        });
        expect(aborted).toBe(true);
        expect(sent).toBe(3);
        const report = await waitStreamDone(hostname, streamId, {edge});
        expect(report.result).toMatch(/recv-error|context-canceled/);
      }
    },
  },
  {
    id: 'protostream-bidi-verified',
    title: 'verify bidirectional streaming',
    description: 'Runs an interleaved ping-pong conversation over h2 and confirms the server can end its side while the request stream stays open.',
    requires: ['protostream-routes-added'],
    async run() {
      // Each request frame is sent only after the previous echo arrived, so
      // a pass proves neither direction is buffered anywhere on the path.
      // The final message asks the server to half-close; the response end
      // must reach the client while the request stream is still open.
      const {echoes, outcome} = await runBidi(STREAM_HOST, {streamId: 'bidi-1', messages: 4});
      expect(outcome).toBe('end');
      expect(echoes.map(echo => echo.seq)).toEqual([1, 2, 3, 4]);
      expect(echoes.map(echo => echo.text)).toEqual(['msg-1', 'msg-2', 'msg-3', 'msg-4']);
      const report = await waitStreamDone(STREAM_HOST, 'bidi-1', {edge: 'h2'});
      expect(report.result).toBe('completed');
      expect(report.messagesIn).toBe(4);
      expect(report.messagesOut).toBe(4);
    },
  },
  {
    id: 'protostream-bidi-cancel-verified',
    title: 'verify client cancel of a bidi stream',
    description: 'Cancels a bidi conversation mid-flight and confirms the backend unblocks promptly having seen only the delivered messages.',
    requires: ['protostream-bidi-verified'],
    async run() {
      const {echoes, outcome} = await runBidi(STREAM_HOST, {
        streamId: 'bidi-cancel', messages: 10, cancelAfterEchoes: 2,
      });
      expect(outcome).toBe('canceled');
      expect(echoes.length).toBe(2);
      // A client cancel tears down both directions concurrently; the
      // request-side abort (RST CANCEL, surfacing as an error) races the
      // response-side close (RST NO_ERROR, which Go's h2 server maps to a
      // clean request EOF per RFC 9113 section 8.1). Either way the backend
      // must unblock promptly and must not see further messages.
      const report = await waitStreamDone(STREAM_HOST, 'bidi-cancel', {edge: 'h2'});
      expect(report.messagesIn).toBe(2);
      expect(report.messagesOut).toBe(2);
    },
  },
];
