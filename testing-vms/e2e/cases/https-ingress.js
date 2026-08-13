import {expect} from '@playwright/test';
import {Buffer} from 'node:buffer';
import {
  createNixDockerDeployment,
  createSecret,
  expectDeploymentHttpsIngressRows,
  expectDeploymentRunning,
  NETWORKING_VIRTUAL,
  setDeploymentHttpsRoutes,
  updateNixDockerDeployment,
} from '../helpers/ui.js';
import {
  expectH2Ingress,
  expectHTTPRedirect,
  expectHTTPSEcho,
  expectHTTPSIngressUnavailable,
  expectHTTPSStatus,
  expectSSEStreaming,
  expectUpgradeEcho,
  ingressCertificateBundle,
  requestHTTPIngress,
  requestHTTPSIngress,
} from '../helpers/httpsClient.js';
import {expectTLSPassthroughRoutes} from './tls-passthrough.js';

const FLAKE = 'testexamples/httpecho/flake.nix';
const WEB_HOST = 'web.ingress.opendeploy.test';
const API_HOST = 'api.ingress.opendeploy.test';
const WEB_CERT_SECRET = 'e2e.tls.ingress.web';
const API_CERT_SECRET = 'e2e.tls.ingress.api';

const webCert = () => `secret("${WEB_CERT_SECRET}", { version = 1 })`;
const apiCert = () => `secret("${API_CERT_SECRET}", { version = 1 })`;

const rootRoutes = () => [
  `https("${WEB_HOST}", 8080, { cert = ${webCert()} })`,
];
const apiRoutes = () => [
  `https("${WEB_HOST}", 8080, { path_prefix = "/api", strip_prefix = true, cert = ${webCert()} })`,
  `https("${API_HOST}", 8080, { path_prefix = "/svc", max_request_body_bytes = 1024, flush_interval_ms = 50, cert = ${apiCert()} })`,
];

export async function expectHTTPSIngressRoutes() {
  await expectHTTPSEcho(WEB_HOST, '/hello', {backend: 'root', path: '/hello'}, {
    certificateBundle: ingressCertificateBundle('web'),
  });
  await expectHTTPSEcho(WEB_HOST, '/api/hello', {backend: 'api', path: '/hello', 'x-forwarded-prefix': '/api'});
  await expectHTTPSEcho(API_HOST, '/svc/hello', {backend: 'api', path: '/svc/hello'}, {
    certificateBundle: ingressCertificateBundle('api'),
  });
}

export const httpsIngressCases = [
  {
    id: 'https-ingress-certificates-created',
    title: 'create HTTPS ingress certificate secrets',
    description: 'Stores CA-signed TLS certificate bundles used as BYO-cert sources for terminating HTTPS routes.',
    requires: ['worker-2-enrolled', 'tls-ingress-route-restored'],
    async run(ctx) {
      // HTTPS cert secrets hold the combined PEM directly; the *_B64 env vars
      // are base64 of that PEM (the passthrough app decodes them itself).
      await createSecret(ctx.page, {name: WEB_CERT_SECRET, value: Buffer.from(ingressCertificateBundle('web'), 'base64').toString('utf8')});
      await createSecret(ctx.page, {name: API_CERT_SECRET, value: Buffer.from(ingressCertificateBundle('api'), 'base64').toString('utf8')});
    },
  },
  {
    id: 'https-echo-deployments-created',
    title: 'create HTTPS echo deployments',
    description: 'Creates two virtual-network HTTP backends on worker-2 that echo routing details.',
    requires: ['https-ingress-certificates-created'],
    async run(ctx) {
      for (const backend of ['root', 'api']) {
        await createNixDockerDeployment(ctx.page, {
          name: `https-echo-${backend}`,
          machine: 'worker-2',
          flake: FLAKE,
          networkingMode: NETWORKING_VIRTUAL,
          env: {OPENDEPLOY_HTTP_BACKEND: backend},
          expectedEnv: {},
        });
      }
    },
  },
  {
    id: 'https-ingress-unconfigured',
    title: 'verify termination is unavailable without routes',
    description: 'Confirms the terminating hostnames fail closed before any HTTPS route exists.',
    requires: ['https-echo-deployments-created'],
    async run() {
      await expectHTTPSIngressUnavailable(WEB_HOST, {timeout: 30_000});
      await expectHTTPSIngressUnavailable(API_HOST, {timeout: 30_000});
    },
  },
  {
    id: 'https-routes-added',
    title: 'add HTTPS termination routes',
    description: 'Adds terminating routes through the HCL editor: a root route plus path-composed and prefixed routes.',
    requires: ['https-ingress-unconfigured'],
    async run(ctx) {
      await setDeploymentHttpsRoutes(ctx.page, {name: 'https-echo-root', routes: rootRoutes()});
      await setDeploymentHttpsRoutes(ctx.page, {name: 'https-echo-api', routes: apiRoutes()});
    },
  },
  {
    id: 'https-root-route-verified',
    title: 'verify root HTTPS route',
    description: 'Verifies certificate identity, routing, preserved Host, query, and X-Forwarded headers.',
    requires: ['https-routes-added'],
    async run() {
      await expectHTTPSEcho(WEB_HOST, '/hello?x=1', {
        backend: 'root',
        host: WEB_HOST,
        path: '/hello',
        query: 'x=1',
        'x-forwarded-proto': 'https',
        'x-forwarded-prefix': '',
        'has-x-forwarded-for': 'true',
      }, {certificateBundle: ingressCertificateBundle('web')});
    },
  },
  {
    id: 'https-path-composition-verified',
    title: 'verify path composition across deployments',
    description: 'Verifies longest-prefix routing, strip_prefix rewriting, and segment-boundary matching on a shared hostname.',
    requires: ['https-root-route-verified'],
    async run() {
      await expectHTTPSEcho(WEB_HOST, '/api/hello', {backend: 'api', path: '/hello', 'x-forwarded-prefix': '/api'});
      await expectHTTPSEcho(WEB_HOST, '/api', {backend: 'api', path: '/'});
      await expectHTTPSEcho(WEB_HOST, '/apix', {backend: 'root', path: '/apix', 'x-forwarded-prefix': ''});
    },
  },
  {
    id: 'https-svc-route-verified',
    title: 'verify prefixed route without strip',
    description: 'Verifies a non-stripping prefix route serves its own certificate and unmatched paths fail with 404.',
    requires: ['https-routes-added'],
    async run() {
      await expectHTTPSEcho(API_HOST, '/svc/hello', {backend: 'api', path: '/svc/hello', 'x-forwarded-prefix': ''}, {
        certificateBundle: ingressCertificateBundle('api'),
      });
      await expectHTTPSStatus(API_HOST, '/other', 404, {timeout: 30_000});
      await expectHTTPSStatus(API_HOST, '/svcx', 404, {timeout: 30_000});
    },
  },
  {
    id: 'https-negative-routing-verified',
    title: 'verify misdirected and unknown requests fail closed',
    description: 'Confirms unknown authorities on a terminated connection get 421 and unknown SNI never completes a handshake.',
    requires: ['https-root-route-verified'],
    async run() {
      await expectHTTPSStatus('unknown.example.test', '/', 421, {servername: WEB_HOST, timeout: 30_000});
      await expectHTTPSStatus('one.ingress.opendeploy.test', '/', 421, {servername: WEB_HOST, timeout: 30_000});
      await expectHTTPSIngressUnavailable('unknown.ingress.opendeploy.test', {timeout: 30_000});
    },
  },
  {
    id: 'https-max-body-enforced',
    title: 'verify request body limit',
    description: 'Confirms max_request_body_bytes allows bodies under the limit and rejects oversized bodies with 413.',
    requires: ['https-svc-route-verified'],
    async run() {
      const small = Buffer.alloc(512, 'a');
      const response = await requestHTTPSIngress(API_HOST, {path: '/svc/upload', method: 'POST', body: small});
      expect(response.status).toBe(200);
      expect(response.body).toContain('received=512');
      await expectHTTPSStatus(API_HOST, '/svc/upload', 413, {
        method: 'POST',
        body: Buffer.alloc(4096, 'a'),
        timeout: 30_000,
      });
    },
  },
  {
    id: 'https-sse-streaming-verified',
    title: 'verify server-sent events stream',
    description: 'Confirms SSE responses are flushed incrementally through the proxy rather than buffered.',
    requires: ['https-root-route-verified'],
    async run() {
      await expectSSEStreaming(WEB_HOST, '/sse');
    },
  },
  {
    id: 'https-upgrade-echo-verified',
    title: 'verify connection upgrade proxying',
    description: 'Confirms a 101 protocol upgrade is proxied end-to-end with bidirectional streaming.',
    requires: ['https-root-route-verified'],
    async run() {
      await expectUpgradeEcho(WEB_HOST, '/echo-upgrade', 'root');
    },
  },
  {
    id: 'https-http-redirect-verified',
    title: 'verify port 80 redirect',
    description: 'Confirms plain HTTP requests are redirected to HTTPS and unknown ACME challenge tokens return 404.',
    requires: ['https-root-route-verified'],
    async run() {
      await expectHTTPRedirect(WEB_HOST, '/hello?x=1');
      const challenge = await requestHTTPIngress(WEB_HOST, '/.well-known/acme-challenge/unknown-token');
      expect(challenge.status).toBe(404);
    },
  },
  {
    id: 'https-h2-edge-verified',
    title: 'verify HTTP/2 at the edge',
    description: 'Confirms the terminating listener negotiates h2 via ALPN and routes h2 requests by authority.',
    requires: ['https-root-route-verified'],
    async run() {
      await expectH2Ingress(WEB_HOST, '/h2check', {backend: 'root', path: '/h2check', proto: 'HTTP/1.1'});
    },
  },
  {
    id: 'https-passthrough-coexistence-verified',
    title: 'verify passthrough and termination share port 443',
    description: 'Confirms the existing TLS passthrough routes still relay while termination hosts are served from the same listener.',
    requires: ['https-root-route-verified'],
    async run() {
      await expectTLSPassthroughRoutes();
      await expectHTTPSIngressRoutes();
    },
  },
  {
    id: 'https-form-roundtrip-preserved',
    title: 'verify form updates preserve HTTPS routes',
    description: 'Confirms the structured form shows read-only HTTPS rows and a form-driven update leaves the routes serving.',
    requires: ['https-path-composition-verified', 'https-svc-route-verified'],
    async run(ctx) {
      await expectDeploymentHttpsIngressRows(ctx.page, {name: 'https-echo-api', count: 2});
      await updateNixDockerDeployment(ctx.page, {
        name: 'https-echo-api',
        machine: 'worker-2',
        env: {OPENDEPLOY_E2E_TOUCH: 'form-roundtrip'},
      });
      await expectDeploymentRunning(ctx.page, {name: 'https-echo-api', machine: 'worker-2'});
      await expectHTTPSIngressRoutes();
    },
  },
  {
    id: 'https-duplicate-route-rejected',
    title: 'verify duplicate route rejection',
    description: 'Confirms claiming a (hostname, prefix) pair already owned by another deployment is rejected.',
    requires: ['https-form-roundtrip-preserved'],
    async run(ctx) {
      await setDeploymentHttpsRoutes(ctx.page, {
        name: 'https-echo-root',
        routes: [
          ...rootRoutes(),
          `https("${WEB_HOST}", 8080, { path_prefix = "/api", strip_prefix = true, cert = ${webCert()} })`,
        ],
        expectError: /already claimed by another deployment/,
      });
    },
  },
  {
    id: 'https-cert-coverage-rejected',
    title: 'verify certificate coverage rejection',
    description: 'Confirms a cert secret that does not cover the route hostname is rejected at save time.',
    requires: ['https-form-roundtrip-preserved'],
    async run(ctx) {
      await setDeploymentHttpsRoutes(ctx.page, {
        name: 'https-echo-root',
        routes: [`https("${WEB_HOST}", 8080, { cert = ${apiCert()} })`],
        expectError: /does not cover web\.ingress\.opendeploy\.test/,
      });
    },
  },
  {
    id: 'https-cert-mismatch-rejected',
    title: 'verify per-hostname certificate consistency',
    description: 'Confirms mixing ACME and secret certificate sources for one hostname on a node is rejected.',
    requires: ['https-form-roundtrip-preserved'],
    async run(ctx) {
      await setDeploymentHttpsRoutes(ctx.page, {
        name: 'https-echo-root',
        routes: [
          ...rootRoutes(),
          `https("${WEB_HOST}", 8080, { path_prefix = "/other", cert = acme() })`,
        ],
        expectError: /certSource for web\.ingress\.opendeploy\.test must match/,
      });
    },
  },
  {
    id: 'https-route-removed-fallback-verified',
    title: 'verify removed route falls back by prefix',
    description: 'Removes the /api route and confirms the shared hostname falls back to the root route without stripping.',
    requires: ['https-duplicate-route-rejected', 'https-cert-coverage-rejected', 'https-cert-mismatch-rejected'],
    async run(ctx) {
      await setDeploymentHttpsRoutes(ctx.page, {name: 'https-echo-api', routes: [apiRoutes()[1]]});
      await expectHTTPSEcho(WEB_HOST, '/api/hello', {backend: 'root', path: '/api/hello', 'x-forwarded-prefix': ''});
      await expectHTTPSEcho(API_HOST, '/svc/hello', {backend: 'api', path: '/svc/hello'});
    },
  },
  {
    id: 'https-routes-restored',
    title: 'restore HTTPS routes',
    description: 'Re-adds the removed route and confirms all terminating routes recover.',
    requires: ['https-route-removed-fallback-verified'],
    async run(ctx) {
      await setDeploymentHttpsRoutes(ctx.page, {name: 'https-echo-api', routes: apiRoutes()});
      await expectHTTPSIngressRoutes();
    },
  },
];
