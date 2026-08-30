import {
  createNetworkPolicy,
  createNixDockerDeployment,
  deleteNetworkPolicy,
  readDeploymentOutputMatch,
  NETWORKING_VIRTUAL,
  PORT_FORWARD_TCP,
} from '../helpers/ui.js';
import {
  addressUrl,
  expectProbeAllowed,
  expectProbeBytes,
  expectProbeDenied,
  FLAKE,
  inboundAddressPattern,
  targets,
} from '../helpers/netprobe.js';
import {NETPOL_SPACE, SERVER, SERVER_PORT, serverDeploymentLabel} from './network-policy.js';

// Cross-node coverage. Policy binds to delivery into a local attachment, so a
// packet crossing a tunnel is evaluated once, on the destination node, after
// decapsulation — same-node and cross-node paths must behave identically. The
// primary participates on the same terms through its own in-process netmap
// applier, which nothing else in the suite exercises.
//
// Targets are literal addresses: `.internal` names resolve only on the node
// holding the deployment, and an address() env ref cannot cross a space
// boundary. The server reports its own stable inbound address in its output.

const REMOTE = 'netpol-remote';
const REMOTE_PEER = 'netpol-remote-peer';
const PRIMARY_PROBE = 'netpol-primary-probe';
const REMOTE_NODE = 'worker-1';
const BULK_BYTES = 2_000_000;
const FORWARD_HOST_PORT = 18184;

export const networkPolicyCrossNodeCases = [
  {
    id: 'network-policy-cross-node-same-space-allowed',
    title: 'verify same-space traffic is allowed across nodes',
    description: 'A peer in the dedicated space on the other worker reaches the server over the node tunnel with no policy, establishing that the cross-node path itself works before anything asserts a denial on it.',
    requires: ['network-policy-server-created'],
    async run(ctx) {
      ctx.netpolServerAddress = await readDeploymentOutputMatch(ctx.page, SERVER, inboundAddressPattern(SERVER));
      await createNixDockerDeployment(ctx.page, {
        name: REMOTE_PEER,
        machine: REMOTE_NODE,
        space: NETPOL_SPACE,
        flake: FLAKE,
        networkingMode: NETWORKING_VIRTUAL,
        portForwarding: [{protocol: PORT_FORWARD_TCP, hostPort: FORWARD_HOST_PORT, containerPort: SERVER_PORT}],
        env: {
          NETPROBE_NAME: REMOTE_PEER,
          NETPROBE_LISTEN: `tcp/${SERVER_PORT}`,
          NETPROBE_TARGETS: targets({
            xsame: addressUrl({address: ctx.netpolServerAddress, port: SERVER_PORT}),
          }),
        },
        expectedEnv: {},
      });
      await expectProbeAllowed(ctx.page, {deployment: REMOTE_PEER, label: 'xsame'});
    },
  },
  {
    id: 'network-policy-cross-node-denied',
    title: 'verify cross-space traffic is denied across nodes',
    description: 'A global-space probe on the other worker is denied by the destination node after decapsulation, exactly as the same-node probe is.',
    requires: ['network-policy-cross-node-same-space-allowed'],
    async run(ctx) {
      await createNixDockerDeployment(ctx.page, {
        name: REMOTE,
        machine: REMOTE_NODE,
        flake: FLAKE,
        networkingMode: NETWORKING_VIRTUAL,
        env: {
          NETPROBE_NAME: REMOTE,
          NETPROBE_TARGETS: targets({
            x8080: addressUrl({address: ctx.netpolServerAddress, port: SERVER_PORT}),
            bulk: addressUrl({address: ctx.netpolServerAddress, port: SERVER_PORT, path: `/bulk?bytes=${BULK_BYTES}`}),
          }),
        },
        expectedEnv: {},
      });
      await expectProbeDenied(ctx.page, {deployment: REMOTE, label: 'x8080'});
    },
  },
  {
    id: 'network-policy-cross-node-allowed',
    title: 'allow the cross-node flow with an override policy',
    description: 'The rule is distributed on the cluster network map and applied by the destination node: the remote probe succeeds without restarting either workload.',
    requires: ['network-policy-cross-node-denied'],
    async run(ctx) {
      ctx.netpolCrossNodePolicyId = await createNetworkPolicy(ctx.page, {
        sourceKind: 'space',
        source: 'global',
        destinationKind: 'deployment',
        destination: serverDeploymentLabel(ctx),
        ports: `tcp/${SERVER_PORT}`,
      });
      await expectProbeAllowed(ctx.page, {deployment: REMOTE, label: 'x8080'});
    },
  },
  {
    id: 'network-policy-cross-node-pmtu',
    title: 'verify a large cross-node response completes through the tunnel',
    description: 'A 2MB response crosses the node tunnel, whose MTU is below the local link: the exact byte count proves the ICMPv6 packet-too-big path survives the boundary as related traffic, since a lost one stalls rather than errors.',
    requires: ['network-policy-cross-node-allowed'],
    async run(ctx) {
      await expectProbeBytes(ctx.page, {deployment: REMOTE, label: 'bulk', bytes: BULK_BYTES});
    },
  },
  {
    id: 'network-policy-primary-node-enforced',
    title: 'verify the primary enforces policy like any other node',
    description: 'A probe on the primary reaches the worker-hosted server under the same override rule, exercising the primary\'s in-process network map applier rather than the worker session path.',
    requires: ['network-policy-cross-node-allowed'],
    async run(ctx) {
      await createNixDockerDeployment(ctx.page, {
        name: PRIMARY_PROBE,
        machine: 'primary',
        flake: FLAKE,
        networkingMode: NETWORKING_VIRTUAL,
        env: {
          NETPROBE_NAME: PRIMARY_PROBE,
          NETPROBE_TARGETS: targets({
            pri8080: addressUrl({address: ctx.netpolServerAddress, port: SERVER_PORT}),
          }),
        },
        expectedEnv: {},
      });
      await expectProbeAllowed(ctx.page, {deployment: PRIMARY_PROBE, label: 'pri8080'});
    },
  },
  {
    id: 'network-policy-cross-node-revoked',
    title: 'verify revoking the override re-denies every node',
    description: 'Deleting the rule republishes the map: fresh connections from both the remote worker and the primary fail again, while same-space traffic across the tunnel is untouched.',
    requires: ['network-policy-primary-node-enforced'],
    async run(ctx) {
      await deleteNetworkPolicy(ctx.page, ctx.netpolCrossNodePolicyId);
      ctx.netpolCrossNodePolicyId = null;
      await expectProbeDenied(ctx.page, {deployment: REMOTE, label: 'x8080'});
      await expectProbeDenied(ctx.page, {deployment: PRIMARY_PROBE, label: 'pri8080'});
      await expectProbeAllowed(ctx.page, {deployment: REMOTE_PEER, label: 'xsame'});
    },
  },
];

export const REMOTE_PEER_NAME = REMOTE_PEER;
export const REMOTE_PEER_HOST_PORT = FORWARD_HOST_PORT;
