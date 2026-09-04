import {expect} from '@playwright/test';
import {Buffer} from 'node:buffer';
import {
  createNetworkPolicy,
  createSecret,
  createSpaceViaUI,
  deleteNetworkPolicy,
  deploymentIdByName,
  expectDeploymentOutput,
  expectHTTPText,
  expectNetworkPolicyCount,
  moveDeploymentToSpace,
  readDeploymentOutputMatch,
  httpsRouteBlock,
  setDeploymentHttpsRoutes,
  updateNixDockerDeployment,
  writeNetworkPolicyState,
  UPGRADE_ROLLOVER,
} from '../helpers/ui.js';
import {
  expectProbeAllowed,
  expectProbeDenied,
  expectProbeNotDenied,
  expectStreamAdvanced,
  inboundAddressPattern,
  internalUrl,
  probeDeniedCount,
  streamTickCount,
} from '../helpers/netprobe.js';
import {ingressCertificateBundle, requestHTTPSIngress} from '../helpers/httpsClient.js';
import {NETPOL_SPACE, PEER, PROBE, PROBE_B, SERVER, SERVER_PORT, serverDeploymentLabel} from './network-policy.js';
import {REMOTE_PEER_HOST_PORT, REMOTE_PEER_NAME} from './network-policy-cross-node.js';

// How the boundary interacts with the rest of the system: traffic that is not
// workload-to-workload (external DNAT, terminated ingress), a rollover under
// an active rule, a peer that moves space, and the flow-scoped meaning of a
// revoke.

const SOURCE_SPACE = 'e2e-netpol-src';
const INGRESS_HOST = 'netpol.ingress.opendeploy.test';
const INGRESS_CERT_SECRET = 'e2e.tls.ingress.netpol';
const SECONDARY_HOST = process.env.OPD_SECONDARY_HOST || 'opendeploy-secondary';
const WORKER = 'worker-2';

export const networkPolicyInteractionCases = [
  {
    id: 'network-policy-port-forward-external-open',
    title: 'verify published ports still reach an isolated-space workload',
    description: 'Externally DNATed traffic is governed by port forwarding, not by workload policy: a non-cluster source falls through the cluster-prefix drop and reaches a deployment in the dedicated space.',
    requires: ['network-policy-cross-node-same-space-allowed'],
    async run() {
      await expectHTTPText(`http://${SECONDARY_HOST}:${REMOTE_PEER_HOST_PORT}/`, `netprobe=${REMOTE_PEER_NAME}`);
    },
  },
  {
    id: 'network-policy-ingress-into-isolated-space',
    title: 'verify terminated ingress reaches an isolated-space backend',
    description: 'The netproxy runs in the system space and dials the backend across a space boundary: an HTTPS route into the dedicated space works with no override rule, which is the only end-to-end exercise of the system-space default.',
    requires: ['network-policy-server-created', 'https-ingress-certificates-created'],
    async run(ctx) {
      await createSecret(ctx.page, {
        name: INGRESS_CERT_SECRET,
        value: Buffer.from(ingressCertificateBundle('netpol'), 'base64').toString('utf8'),
      });
      await setDeploymentHttpsRoutes(ctx.page, {
        name: SERVER,
        machine: WORKER,
        routes: [httpsRouteBlock({hostname: INGRESS_HOST, containerPort: SERVER_PORT, cert: `secret("global", "${INGRESS_CERT_SECRET}", 1)`})],
      });
      await expect.poll(async () => {
        try {
          return (await requestHTTPSIngress(INGRESS_HOST, {path: '/'})).body;
        } catch {
          return '';
        }
      }, {message: `expected terminated ingress for ${INGRESS_HOST} to reach the isolated-space backend`, timeout: 180_000})
        .toContain(`netprobe=${SERVER}`);
    },
  },
  {
    id: 'network-policy-rollover-continuity',
    title: 'verify policy survives a rollover of the destination',
    description: 'During a rollover the old and candidate attachments share the stable inbound address and each hold their own outbound address; the destination chain is shared per deployment, so an allowed peer must not see a single denial across the promotion.',
    requires: ['network-policy-revoked'],
    async run(ctx) {
      const policyId = await createNetworkPolicy(ctx.page, {
        sourceKind: 'space',
        source: 'global',
        destinationKind: 'deployment',
        destination: serverDeploymentLabel(ctx),
        ports: `tcp/${SERVER_PORT}`,
      });
      await expectProbeAllowed(ctx.page, {deployment: PROBE, label: 'p8080'});

      // Counts first: the window has to span the promotion, so it opens before
      // the update is submitted and closes only once the candidate is serving.
      const deniedBefore = {
        peer: await probeDeniedCount(ctx.page, {deployment: PEER, label: 'same'}),
        probe: await probeDeniedCount(ctx.page, {deployment: PROBE, label: 'p8080'}),
      };

      await updateNixDockerDeployment(ctx.page, {
        name: SERVER,
        machine: WORKER,
        env: {NETPROBE_NAME: `${SERVER}-v2`},
        upgradeStrategy: UPGRADE_ROLLOVER,
        readinessTimeoutSeconds: 120,
      });
      // The candidate's own startup line marks the promotion; traffic flowing
      // afterwards is going through the flipped inbound route.
      await expectDeploymentOutput(ctx.page, SERVER, [`netprobe start name=${SERVER}-v2`]);
      await expectProbeAllowed(ctx.page, {deployment: PEER, label: 'same'});
      await expectProbeAllowed(ctx.page, {deployment: PROBE, label: 'p8080'});

      // A connection can legitimately be reset by the promotion; a policy
      // denial — a silent drop, reported as a connect timeout — cannot.
      await expectProbeNotDenied(ctx.page, {deployment: PEER, label: 'same', since: deniedBefore.peer});
      await expectProbeNotDenied(ctx.page, {deployment: PROBE, label: 'p8080', since: deniedBefore.probe});

      await deleteNetworkPolicy(ctx.page, policyId);
      await expectProbeDenied(ctx.page, {deployment: PROBE, label: 'p8080'});
    },
  },
  {
    id: 'network-policy-follows-space-move',
    title: 'verify a deployment-anchored rule follows a space move',
    description: 'A rule anchored to the source deployment keeps allowing it after it moves to another space, because a deployment peer stores the id alone and its space resolves at render time. A space-anchored rule would stop applying instead.',
    requires: ['network-policy-revoked'],
    async run(ctx) {
      const policyId = await createNetworkPolicy(ctx.page, {
        sourceKind: 'deployment',
        source: `${PROBE_B} (space ${ctx.globalSpaceId})`,
        destinationKind: 'deployment',
        destination: serverDeploymentLabel(ctx),
        ports: `tcp/${SERVER_PORT}`,
      });
      await expectProbeAllowed(ctx.page, {deployment: PROBE_B, label: 'p8080'});

      ctx.netpolSourceSpaceId = await createSpaceViaUI(ctx.page, SOURCE_SPACE);
      await moveDeploymentToSpace(ctx.page, {name: PROBE_B, machine: WORKER, space: SOURCE_SPACE});

      // The move replaces the placement — new space, new addresses, new DNS
      // name — so this asserts fresh successes after the replacement rather
      // than uninterrupted ones: the rule following the move is the claim, not
      // zero downtime across a security-domain migration.
      await expectProbeAllowed(ctx.page, {deployment: PROBE_B, label: 'p8080'});
      // Nothing else was opened: the global-space probe is still denied.
      await expectProbeDenied(ctx.page, {deployment: PROBE, label: 'p8080'});
      await deleteNetworkPolicy(ctx.page, policyId);
      await expectProbeDenied(ctx.page, {deployment: PROBE_B, label: 'p8080'});
    },
  },
  {
    id: 'network-policy-established-survives-revoke',
    title: 'verify an established connection survives a revoke',
    description: 'Enforcement is stateful: deleting an allow rule stops new connections but leaves conntrack-established ones flowing, the same flow-scoped semantics as removing a port forward.',
    requires: ['network-policy-revoked'],
    async run(ctx) {
      const policyId = await createNetworkPolicy(ctx.page, {
        sourceKind: 'space',
        source: 'global',
        destinationKind: 'deployment',
        destination: serverDeploymentLabel(ctx),
        ports: `tcp/${SERVER_PORT}`,
      });
      await updateNixDockerDeployment(ctx.page, {
        name: PROBE,
        machine: WORKER,
        env: {
          NETPROBE_STREAM_TARGET: `sse=${internalUrl({deployment: SERVER, spaceId: ctx.netpolSpaceId, port: SERVER_PORT, path: '/sse'})}`,
        },
      });
      await expectProbeAllowed(ctx.page, {deployment: PROBE, label: 'p8080'});
      // The stream target only takes effect on the restart the update above
      // triggers, and p8080 succeeding says nothing about the SSE connection
      // having been made yet. Wait for the first tick rather than reading the
      // count bare, which races the restart.
      await expectStreamAdvanced(ctx.page, {deployment: PROBE, label: 'sse', from: 0, ticks: 1});
      const ticksBefore = await streamTickCount(ctx.page, {deployment: PROBE, label: 'sse'});
      expect(ticksBefore, 'expected the stream to be established before the revoke').toBeGreaterThan(0);

      await deleteNetworkPolicy(ctx.page, policyId);
      await expectProbeDenied(ctx.page, {deployment: PROBE, label: 'p8080'});
      // New connections are denied, so further ticks can only come from the
      // connection that was already established when the rule was deleted.
      await expectStreamAdvanced(ctx.page, {deployment: PROBE, label: 'sse', from: ticksBefore, ticks: 5});
    },
  },
  {
    id: 'network-policy-kernel-check-state',
    title: 'record workload ids for the kernel checks',
    description: 'Writes the policy workloads\' deployment ids for the post-flow kernel checks, which address workloads by id and cannot authenticate to the API, and leaves the cluster with no override rules so the default deny is what those checks measure.',
    requires: ['network-policy-revoked'],
    async run(ctx) {
      await expectNetworkPolicyCount(ctx.page, 0);
      const serverId = ctx.netpolServerId ?? await deploymentIdByName(ctx.page, SERVER);
      const state = {
        NETPOL_SERVER_ID: serverId,
        NETPOL_PEER_ID: await deploymentIdByName(ctx.page, PEER),
        NETPOL_PROBE_ID: await deploymentIdByName(ctx.page, PROBE),
        NETPOL_SERVER_PORT: SERVER_PORT,
        NETPOL_SERVER_ADDRESS: ctx.netpolServerAddress
          ?? await readDeploymentOutputMatch(ctx.page, SERVER, inboundAddressPattern(SERVER)),
        NETPOL_SPACE_NAME: NETPOL_SPACE,
      };
      writeNetworkPolicyState(state);
    },
  },
];
