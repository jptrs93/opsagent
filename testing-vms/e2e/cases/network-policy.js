import {
  createNetworkPolicy,
  createNixDockerDeployment,
  createSpaceViaUI,
  deleteNetworkPolicy,
  deploymentOutputOccurrenceCount,
  expectDeploymentOutput,
  expectDeploymentOutputOccurrences,
  NETWORKING_VIRTUAL,
} from '../helpers/ui.js';

const NETPOL_SPACE = 'e2e-netpol';
const SERVER = 'netpol-echo';
const PROBE = 'netpol-probe';
const SERVER_PORT = '8080';

const probeUrl = (spaceId) => `http://${SERVER}.space-${spaceId}.internal:${SERVER_PORT}/hello`;

export const networkPolicyCases = [
  {
    id: 'network-policy-space-created',
    title: 'create the network policy test space',
    description: 'Creates a dedicated space so cross-space traffic hits the default deny boundary.',
    requires: ['worker-2-enrolled'],
    async run(ctx) {
      ctx.netpolSpaceId = await createSpaceViaUI(ctx.page, NETPOL_SPACE);
    },
  },
  {
    id: 'network-policy-server-created',
    title: 'create an HTTP server in the isolated space',
    description: 'Creates a virtual-network httpecho deployment inside the dedicated space on worker-2.',
    requires: ['network-policy-space-created'],
    async run(ctx) {
      await createNixDockerDeployment(ctx.page, {
        name: SERVER,
        machine: 'worker-2',
        space: NETPOL_SPACE,
        flake: 'testexamples/httpecho/flake.nix',
        networkingMode: NETWORKING_VIRTUAL,
        env: {OPENDEPLOY_HTTP_BACKEND: 'netpol'},
        expectedEnv: {},
      });
    },
  },
  {
    id: 'network-policy-cross-space-denied',
    title: 'verify cross-space traffic is denied by default',
    description: 'A global-space probe on the same node repeatedly requests the server over the virtual network and observes the default deny boundary.',
    requires: ['network-policy-server-created'],
    async run(ctx) {
      await createNixDockerDeployment(ctx.page, {
        name: PROBE,
        machine: 'worker-2',
        networkingMode: NETWORKING_VIRTUAL,
        env: {OPENDEPLOY_E2E_PROBE_URL: probeUrl(ctx.netpolSpaceId)},
        expectedEnv: {},
      });
      await expectDeploymentOutput(ctx.page, PROBE, [
        `nixdockerbuild1 probe url=${probeUrl(ctx.netpolSpaceId)} result=error`,
      ]);
    },
  },
  {
    id: 'network-policy-allow-applied',
    title: 'allow the cross-space flow with an override policy',
    description: 'Creates an allow rule (source space global, destination the server deployment, tcp/8080) on the Network page and observes the probe succeed without restarting either workload.',
    requires: ['network-policy-cross-space-denied'],
    async run(ctx) {
      ctx.netpolPolicyId = await createNetworkPolicy(ctx.page, {
        sourceKind: 'space',
        source: 'global',
        destinationKind: 'deployment',
        destination: `${SERVER} (space ${ctx.netpolSpaceId})`,
        ports: `tcp/${SERVER_PORT}`,
      });
      await expectDeploymentOutput(ctx.page, PROBE, [
        `nixdockerbuild1 probe url=${probeUrl(ctx.netpolSpaceId)} result=ok status=200`,
      ]);
    },
  },
  {
    id: 'network-policy-revoked',
    title: 'verify removal re-imposes the default deny',
    description: 'Deletes the override rule and observes fresh probe connections fail again.',
    requires: ['network-policy-allow-applied'],
    async run(ctx) {
      const deniedLine = `nixdockerbuild1 probe url=${probeUrl(ctx.netpolSpaceId)} result=error`;
      const denied = await deploymentOutputOccurrenceCount(ctx.page, PROBE, deniedLine);
      await deleteNetworkPolicy(ctx.page, ctx.netpolPolicyId);
      await expectDeploymentOutputOccurrences(ctx.page, PROBE, deniedLine, denied + 2);
    },
  },
];
