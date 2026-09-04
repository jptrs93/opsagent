import {Buffer} from 'node:buffer';
import {
  createNixDockerDeployment,
  createSecret,
  expectDeploymentIngressWarnings,
  expectHttpsRoutesDiagnostic,
  expectNodeHostAddresses,
  httpsRouteBlock,
  NETWORKING_VIRTUAL,
  readDeploymentHclText,
  setDeploymentHttpsRoutes,
} from '../helpers/ui.js';
import {expectHTTPSEcho, expectHTTPSIngressUnavailable, ingressCertificateBundle} from '../helpers/httpsClient.js';

// Ingress listen selectors decide which (node, host address) pairs a route is
// published on. The cases run on worker-1, whose 443 publish set they own
// entirely: on worker-2 every other HTTPS and passthrough route publishes
// 443 on every address, and DNAT is per (address, port), not per hostname,
// so a restriction there could never be observed. The worker-1 tunnel dials
// the VM's own address, which is what a literal selector names.

const FLAKE = 'testexamples/httpecho/flake.nix';
const LISTEN_HOST = 'listen.ingress.opendeploy.test';
const CERT_SECRET = 'e2e.tls.ingress.listen';
const NAME = 'listen-echo';
const WORKER = 'worker-1';
const PRIMARY_LOADGEN = 'loadgen-primary';

function requiredEnv(name) {
  const value = process.env[name];
  if (!value) throw new Error(`${name} is required`);
  return value;
}

const worker1Address = () => requiredEnv('OPD_WORKER_1_INGRESS_ADDRESS');
const primaryAddress = () => requiredEnv('OPD_FILTERED_PORT_CLIENT_IP');
const cert = () => `secret("global", "${CERT_SECRET}", 1)`;
const route = (listen = []) => httpsRouteBlock({hostname: LISTEN_HOST, containerPort: 8080, cert: cert(), listen});

async function expectListenRouteServes() {
  await expectHTTPSEcho(LISTEN_HOST, '/hello', {backend: 'listen', path: '/hello'}, {
    certificateBundle: ingressCertificateBundle('listen'),
    node: WORKER,
  });
}

export const ingressListenCases = [
  {
    id: 'ingress-listen-inventory-visible',
    title: 'verify node host address inventory',
    description: 'Confirms the Machines page lists the host addresses each node reported, which is what listen selectors expand against.',
    requires: ['https-routes-restored', 'nix-docker-virtual-network', 'metrics-loadgen-primary-deployed'],
    async run(ctx) {
      await expectNodeHostAddresses(ctx.page, {machine: WORKER, contains: [worker1Address()]});
      await expectNodeHostAddresses(ctx.page, {machine: 'primary', contains: [primaryAddress()]});
    },
  },
  {
    id: 'ingress-listen-deployment-created',
    title: 'create listen echo deployment',
    description: 'Creates a virtual-network HTTP backend on worker-1 and confirms 443 there is dark before any route exists.',
    requires: ['ingress-listen-inventory-visible'],
    async run(ctx) {
      await createSecret(ctx.page, {name: CERT_SECRET, value: Buffer.from(ingressCertificateBundle('listen'), 'base64').toString('utf8')});
      await createNixDockerDeployment(ctx.page, {
        name: NAME,
        machine: WORKER,
        flake: FLAKE,
        networkingMode: NETWORKING_VIRTUAL,
        env: {OPENDEPLOY_HTTP_BACKEND: 'listen'},
        expectedEnv: {},
      });
      await expectHTTPSIngressUnavailable(LISTEN_HOST, {node: WORKER, timeout: 30_000});
    },
  },
  {
    id: 'ingress-listen-literal-serves',
    title: 'verify a literal listen address serves',
    description: 'Publishes the route on worker-1 at its own address only, confirms it serves there, and that the HCL round-trips the selector.',
    requires: ['ingress-listen-deployment-created'],
    async run(ctx) {
      await setDeploymentHttpsRoutes(ctx.page, {
        name: NAME,
        machine: WORKER,
        routes: [route([{node: `node("${WORKER}")`, address: JSON.stringify(worker1Address())}])],
      });
      await expectListenRouteServes();
      const text = await readDeploymentHclText(ctx.page, {name: NAME, machine: WORKER});
      const listenBlock = new RegExp(`listen \\{\\s*node = node\\("${WORKER}"\\)\\s*address = ${JSON.stringify(worker1Address()).replace(/[.]/g, '\\.')}\\s*\\}`);
      if (!listenBlock.test(text)) {
        throw new Error(`rendered HCL lost the listen selector:\n${text}`);
      }
      // A literal selector never widens onto host-mode deployments, so no warning.
      await expectDeploymentIngressWarnings(ctx.page, {name: NAME, machine: WORKER, patterns: []});
    },
  },
  {
    id: 'ingress-listen-unmatched-literal-dark',
    title: 'verify an unmatched listen address publishes nothing',
    description: 'Restricts the route to an address worker-1 does not have and confirms 443 at the node address is no longer forwarded to netproxy.',
    requires: ['ingress-listen-literal-serves'],
    async run(ctx) {
      await setDeploymentHttpsRoutes(ctx.page, {
        name: NAME,
        machine: WORKER,
        routes: [route([{address: '"198.51.100.7"'}])],
      });
      await expectHTTPSIngressUnavailable(LISTEN_HOST, {node: WORKER, timeout: 60_000});
    },
  },
  {
    id: 'ingress-listen-default-warns-host-mode',
    title: 'verify the default listen serves and warns about host-mode neighbours',
    description: 'Drops the selector so the route publishes on every address of worker-1, confirms it serves, and that the host-mode deployments on that node are named in a warning.',
    requires: ['ingress-listen-unmatched-literal-dark'],
    async run(ctx) {
      await setDeploymentHttpsRoutes(ctx.page, {name: NAME, machine: WORKER, routes: [route()]});
      await expectListenRouteServes();
      await expectDeploymentIngressWarnings(ctx.page, {name: NAME, machine: WORKER, patterns: [/host-mode deployment/]});
    },
  },
  {
    id: 'ingress-listen-cross-node-rejected',
    title: 'verify a listen node other than the hosting node is rejected',
    description: 'Ingress is served by the deployment\'s own node until netproxy can dial remote backends: naming another node in listen is refused in the editor (and by the API).',
    requires: ['ingress-listen-default-warns-host-mode'],
    async run(ctx) {
      await expectHttpsRoutesDiagnostic(ctx.page, {
        name: NAME,
        machine: WORKER,
        routes: [route([{node: 'node("worker-2")'}])],
        diagnostic: /Ingress is served by the deployment's own node/,
      });
    },
  },
  {
    id: 'ingress-listen-overlap-rejected',
    title: 'verify overlapping selectors on one hostname are rejected',
    description: 'Confirms a second deployment on worker-1 cannot claim the same hostname at an address the first route already publishes on.',
    requires: ['ingress-listen-default-warns-host-mode'],
    async run(ctx) {
      await setDeploymentHttpsRoutes(ctx.page, {
        name: 'nixdockerbuild-virtual',
        machine: WORKER,
        routes: [route([{address: JSON.stringify(worker1Address())}])],
        expectError: /already claimed by another deployment/,
      });
    },
  },
  {
    id: 'ingress-listen-primary-reservation',
    title: 'verify the primary Web UI listener is a reserved claim',
    description: 'A literal listen on the primary address is rejected naming the Web UI; the default listen is accepted, reports the 443 exclusion, and leaves the Web UI reachable.',
    requires: ['ingress-listen-overlap-rejected'],
    async run(ctx) {
      await setDeploymentHttpsRoutes(ctx.page, {
        name: PRIMARY_LOADGEN,
        machine: 'primary',
        routes: [route([{address: JSON.stringify(primaryAddress())}])],
        expectError: /reserved by the primary Web UI/,
      });
      await setDeploymentHttpsRoutes(ctx.page, {name: PRIMARY_LOADGEN, machine: 'primary', routes: [route()]});
      await expectDeploymentIngressWarnings(ctx.page, {name: PRIMARY_LOADGEN, machine: 'primary', patterns: [/reserved by the primary Web UI/]});
      await setDeploymentHttpsRoutes(ctx.page, {name: PRIMARY_LOADGEN, machine: 'primary', routes: []});
      await expectDeploymentIngressWarnings(ctx.page, {name: PRIMARY_LOADGEN, machine: 'primary', patterns: []});
    },
  },
];
