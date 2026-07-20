import {
  createNixDockerDeployment,
  createSecret,
  expectTLSIngress,
  expectTLSIngressUnavailable,
  NETWORKING_VIRTUAL,
  updateNixDockerDeployment,
} from '../helpers/ui.js';

const FLake = 'testexamples/tlspassthrough/flake.nix';
const routes = [
  {id: 'one', hostname: 'one.ingress.opendeploy.test'},
  {id: 'two', hostname: 'two.ingress.opendeploy.test'},
  {id: 'three', hostname: 'three.ingress.opendeploy.test'},
];

function certificateBundle(route) {
  const value = process.env[`OPD_TLS_INGRESS_CERT_${route.id.toUpperCase()}_B64`];
  if (!value) throw new Error(`missing certificate for ${route.hostname}`);
  return value;
}

function deploymentName(route) {
  return `tls-ingress-${route.id}`;
}

function ingress(route) {
  return [{hostname: route.hostname, containerPort: 8443}];
}

async function expectRoute(route) {
  await expectTLSIngress(route.hostname, {
    backend: route.id,
    certificateBundle: certificateBundle(route),
  });
}

export async function expectTLSPassthroughRoutes() {
  for (const route of routes) await expectRoute(route);
}

export const tlsPassthroughCases = [
  {
    id: 'tls-ingress-certificates-created',
    title: 'create TLS ingress certificates',
    description: 'Stores one distinct CA-signed TLS certificate bundle for each ingress backend.',
    requires: ['worker-2-enrolled'],
    async run(ctx) {
      for (const route of routes) {
        await createSecret(ctx.page, {
          name: `e2e.tls.ingress.${route.id}`,
          value: certificateBundle(route),
        });
      }
    },
  },
  {
    id: 'tls-ingress-deployments-created',
    title: 'create virtual HTTPS deployments',
    description: 'Creates three virtual-network HTTPS backends on worker-2 before configuring ingress.',
    requires: ['tls-ingress-certificates-created'],
    async run(ctx) {
      for (const route of routes) {
        await createNixDockerDeployment(ctx.page, {
          name: deploymentName(route),
          machine: 'worker-2',
          flake: FLake,
          networkingMode: NETWORKING_VIRTUAL,
          env: {
            OPENDEPLOY_TLS_BACKEND: route.id,
            OPENDEPLOY_TLS_BUNDLE_B64: {type: 'secret', name: `e2e.tls.ingress.${route.id}`},
          },
          expectedEnv: {},
        });
      }
    },
  },
  {
    id: 'tls-ingress-unconfigured',
    title: 'verify HTTPS is unavailable without ingress',
    description: 'Confirms virtual backends do not receive external TLS traffic until a route is configured.',
    requires: ['tls-ingress-deployments-created'],
    async run() {
      for (const route of routes) await expectTLSIngressUnavailable(route.hostname, {timeout: 30_000});
    },
  },
  {
    id: 'tls-ingress-routes-added',
    title: 'add TLS passthrough routes',
    description: 'Adds three exact-SNI routes sharing worker-2 host port 443.',
    requires: ['tls-ingress-deployments-created'],
    async run(ctx) {
      for (const route of routes) {
        await updateNixDockerDeployment(ctx.page, {name: deploymentName(route), machine: 'worker-2', ingress: ingress(route)});
      }
    },
  },
  {
    id: 'tls-ingress-routes-verified',
    title: 'verify TLS passthrough routes',
    description: 'Verifies hostname trust, certificate identity, SNI, and response routing for all three HTTPS backends.',
    requires: ['tls-ingress-routes-added'],
    async run() {
      await expectTLSPassthroughRoutes();
      await expectTLSIngressUnavailable('unknown.ingress.opendeploy.test', {timeout: 30_000});
    },
  },
  {
    id: 'tls-ingress-route-removed',
    title: 'remove one TLS passthrough route',
    description: 'Removes the middle route while leaving the other hostnames served from the shared listener.',
    requires: ['tls-ingress-routes-verified'],
    async run(ctx) {
      await updateNixDockerDeployment(ctx.page, {name: deploymentName(routes[1]), machine: 'worker-2', ingress: []});
    },
  },
  {
    id: 'tls-ingress-route-removal-verified',
    title: 'verify removed TLS route fails closed',
    description: 'Confirms the removed SNI fails while the remaining shared-port routes continue serving their own certificates.',
    requires: ['tls-ingress-route-removed'],
    async run() {
      await expectTLSIngressUnavailable(routes[1].hostname);
      await expectRoute(routes[0]);
      await expectRoute(routes[2]);
    },
  },
  {
    id: 'tls-ingress-route-restored',
    title: 'restore TLS passthrough route',
    description: 'Re-adds the removed route and confirms all three HTTPS backends recover.',
    requires: ['tls-ingress-route-removal-verified'],
    async run(ctx) {
      await updateNixDockerDeployment(ctx.page, {name: deploymentName(routes[1]), machine: 'worker-2', ingress: ingress(routes[1])});
      await expectTLSPassthroughRoutes();
    },
  },
];
