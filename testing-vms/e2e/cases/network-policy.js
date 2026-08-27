import {
  createNetworkPolicy,
  createNixDockerDeployment,
  createSpaceViaUI,
  deleteDeployment,
  deleteNetworkPolicy,
  deploymentIdByName,
  expectDeploymentNetworkPolicies,
  expectNetworkPolicyDangling,
  spaceIdByName,
  stopDeployment,
  updateNetworkPolicy,
  NETWORKING_VIRTUAL,
} from '../helpers/ui.js';
import {
  expectProbeAllowed,
  expectProbeDenied,
  FLAKE,
  internalUrl,
  targets,
} from '../helpers/netprobe.js';

// Same-node coverage of the logical policy boundary: the default rules
// (same-space, global-space destination, DNS), the shape of an override
// (source and destination peers, ports, protocol), the edit and delete paths,
// and the fail-closed behaviour of a rule anchored to a deleted entity.
//
// Every deny assertion is paired with a positive control on the same path:
// `netpol-peer` reaches the server that `netpol-probe` cannot, so a denial can
// never be satisfied by a broken space, a server that never started, or DNS
// that stopped resolving.

export const NETPOL_SPACE = 'e2e-netpol';
export const SERVER = 'netpol-server';
export const PEER = 'netpol-peer';
export const PROBE = 'netpol-probe';
export const PROBE_B = 'netpol-probe-b';
const GHOST = 'netpol-ghost';
const WORKER = 'worker-2';

export const SERVER_PORT = 8080;
const ALT_PORT = 8081;
const UDP_PORT = 9000;

export const serverDeploymentLabel = (ctx) => `${SERVER} (space ${ctx.netpolSpaceId})`;
const serverUrl = (ctx, port, path = '/') => internalUrl({deployment: SERVER, spaceId: ctx.netpolSpaceId, port, path});

export const networkPolicyCases = [
  {
    id: 'network-policy-space-created',
    title: 'create the network policy test space',
    description: 'Creates a dedicated space so cross-space traffic hits the default deny boundary.',
    requires: ['worker-2-enrolled'],
    async run(ctx) {
      ctx.netpolSpaceId = await createSpaceViaUI(ctx.page, NETPOL_SPACE);
      ctx.globalSpaceId = await spaceIdByName(ctx.page, 'global');
    },
  },
  {
    id: 'network-policy-server-created',
    title: 'create the isolated-space server',
    description: 'Runs a netprobe server on tcp/8080, tcp/8081 and udp/9000 inside the dedicated space, probing IPv4 egress from there.',
    requires: ['network-policy-space-created'],
    async run(ctx) {
      const egressUrl = process.env.OPD_IPV4_EGRESS_URL || '';
      await createNixDockerDeployment(ctx.page, {
        name: SERVER,
        machine: WORKER,
        space: NETPOL_SPACE,
        flake: FLAKE,
        networkingMode: NETWORKING_VIRTUAL,
        env: {
          NETPROBE_NAME: SERVER,
          NETPROBE_LISTEN: `tcp/${SERVER_PORT},tcp/${ALT_PORT},udp/${UDP_PORT}`,
          ...(egressUrl ? {NETPROBE_TARGETS: targets({egress: egressUrl})} : {}),
        },
        expectedEnv: {},
      });
      ctx.netpolServerId = await deploymentIdByName(ctx.page, SERVER);
    },
  },
  {
    id: 'network-policy-cross-space-denied',
    title: 'verify cross-space traffic is denied by default',
    description: 'A global-space probe repeatedly requests the isolated-space server and observes the default deny boundary as connect timeouts with no successes.',
    requires: ['network-policy-server-created'],
    async run(ctx) {
      await createNixDockerDeployment(ctx.page, {
        name: PROBE,
        machine: WORKER,
        flake: FLAKE,
        networkingMode: NETWORKING_VIRTUAL,
        env: {
          NETPROBE_NAME: PROBE,
          NETPROBE_LISTEN: `tcp/${SERVER_PORT}`,
          NETPROBE_TARGETS: targets({
            p8080: serverUrl(ctx, SERVER_PORT),
            p8081: serverUrl(ctx, ALT_PORT),
          }),
          NETPROBE_UDP_TARGETS: targets({u9000: `${SERVER}.space-${ctx.netpolSpaceId}.internal:${UDP_PORT}`}),
        },
        expectedEnv: {},
      });
      await expectProbeDenied(ctx.page, {deployment: PROBE, label: 'p8080'});
    },
  },
  {
    id: 'network-policy-dns-allowed-cross-space',
    title: 'verify DNS crosses the boundary while the payload does not',
    description: 'The denied probe still resolves the server name: the netproxy DNS default rule allows udp/53 and tcp/53 from any space, so a drop shows up as a connect timeout after a successful lookup.',
    requires: ['network-policy-cross-space-denied'],
    async run(ctx) {
      // expectProbeDenied asserts stage=dns result=ok alongside the connect
      // timeout, so the same probe proves both halves at once — the pairing is
      // what makes it evidence about the boundary rather than about DNS.
      await expectProbeDenied(ctx.page, {deployment: PROBE, label: 'p8081'});
    },
  },
  {
    id: 'network-policy-same-space-allowed',
    title: 'verify same-space traffic is allowed and global-space destinations are open',
    description: 'A peer inside the dedicated space reaches the server with no policy (same-space default), and reaches a global-space deployment from outside global (global-space destination default).',
    requires: ['network-policy-cross-space-denied'],
    async run(ctx) {
      await createNixDockerDeployment(ctx.page, {
        name: PEER,
        machine: WORKER,
        space: NETPOL_SPACE,
        flake: FLAKE,
        networkingMode: NETWORKING_VIRTUAL,
        env: {
          NETPROBE_NAME: PEER,
          NETPROBE_TARGETS: targets({
            same: serverUrl(ctx, SERVER_PORT),
            global: internalUrl({deployment: PROBE, spaceId: ctx.globalSpaceId, port: SERVER_PORT}),
          }),
        },
        expectedEnv: {},
      });
      await expectProbeAllowed(ctx.page, {deployment: PEER, label: 'same'});
      await expectProbeAllowed(ctx.page, {deployment: PEER, label: 'global'});
    },
  },
  {
    id: 'network-policy-egress-open',
    title: 'verify egress from an isolated space stays open',
    description: 'The boundary is destination-side only: the isolated-space server still reaches the machine-external IPv4 egress endpoint.',
    requires: ['network-policy-server-created'],
    when() {
      return Boolean(process.env.OPD_IPV4_EGRESS_URL);
    },
    async run(ctx) {
      await expectProbeAllowed(ctx.page, {deployment: SERVER, label: 'egress'});
    },
  },
  {
    id: 'network-policy-allow-applied',
    title: 'allow the cross-space flow with an override policy',
    description: 'Creates an allow rule (source space global, destination the server deployment, tcp/8080) and observes the probe succeed without restarting either workload, with the rule visible on the deployment inspector.',
    requires: ['network-policy-cross-space-denied'],
    async run(ctx) {
      ctx.netpolPolicyId = await createNetworkPolicy(ctx.page, {
        sourceKind: 'space',
        source: 'global',
        destinationKind: 'deployment',
        destination: serverDeploymentLabel(ctx),
        ports: `tcp/${SERVER_PORT}`,
      });
      await expectProbeAllowed(ctx.page, {deployment: PROBE, label: 'p8080'});
      await expectDeploymentNetworkPolicies(ctx.page, {
        name: SERVER,
        machine: WORKER,
        present: [`space global → ${SERVER}`],
      });
    },
  },
  {
    id: 'network-policy-port-scoped',
    title: 'verify the override applies only to the allowed port and protocol',
    description: 'With tcp/8080 allowed, the same source is still denied on tcp/8081 and on udp/9000 — the allow is a port match, not a blanket accept.',
    requires: ['network-policy-allow-applied'],
    async run(ctx) {
      await expectProbeDenied(ctx.page, {deployment: PROBE, label: 'p8081'});
      // The UDP probe reports a response timeout rather than a connect one:
      // UDP has no handshake, so the drop surfaces on the read.
      await expectProbeDenied(ctx.page, {deployment: PROBE, label: 'u9000', expectDns: false, stage: 'response'});
    },
  },
  {
    id: 'network-policy-port-range-widened',
    title: 'widen the override to a port range',
    description: 'Edits the rule to tcp/8080-8081 and observes the second port open without recreating the rule or the workloads.',
    requires: ['network-policy-port-scoped'],
    async run(ctx) {
      await updateNetworkPolicy(ctx.page, {
        id: ctx.netpolPolicyId,
        sourceKind: 'space',
        source: 'global',
        destinationKind: 'deployment',
        destination: serverDeploymentLabel(ctx),
        ports: `tcp/${SERVER_PORT}-${ALT_PORT}`,
      });
      await expectProbeAllowed(ctx.page, {deployment: PROBE, label: 'p8081'});
      await expectProbeAllowed(ctx.page, {deployment: PROBE, label: 'p8080'});
    },
  },
  {
    id: 'network-policy-protocol-scoped',
    title: 'verify protocol scoping with a udp-only override',
    description: 'Edits the rule to udp/9000: the UDP probe succeeds and both TCP probes fall back to the default deny.',
    requires: ['network-policy-port-range-widened'],
    async run(ctx) {
      await updateNetworkPolicy(ctx.page, {
        id: ctx.netpolPolicyId,
        sourceKind: 'space',
        source: 'global',
        destinationKind: 'deployment',
        destination: serverDeploymentLabel(ctx),
        ports: `udp/${UDP_PORT}`,
      });
      await expectProbeAllowed(ctx.page, {deployment: PROBE, label: 'u9000'});
      await expectProbeDenied(ctx.page, {deployment: PROBE, label: 'p8080'});
      await expectProbeDenied(ctx.page, {deployment: PROBE, label: 'p8081'});
    },
  },
  {
    id: 'network-policy-source-deployment-scoped',
    title: 'narrow the override source to one deployment',
    description: 'A second global-space probe stays denied while the named source deployment is allowed, proving the source compiles to that deployment prefix rather than its whole space.',
    requires: ['network-policy-protocol-scoped'],
    async run(ctx) {
      await createNixDockerDeployment(ctx.page, {
        name: PROBE_B,
        machine: WORKER,
        flake: FLAKE,
        networkingMode: NETWORKING_VIRTUAL,
        env: {
          NETPROBE_NAME: PROBE_B,
          NETPROBE_TARGETS: targets({p8080: serverUrl(ctx, SERVER_PORT)}),
        },
        expectedEnv: {},
      });
      await expectProbeDenied(ctx.page, {deployment: PROBE_B, label: 'p8080'});

      await updateNetworkPolicy(ctx.page, {
        id: ctx.netpolPolicyId,
        sourceKind: 'deployment',
        source: `${PROBE} (space ${ctx.globalSpaceId})`,
        destinationKind: 'deployment',
        destination: serverDeploymentLabel(ctx),
        ports: `tcp/${SERVER_PORT}`,
      });
      await expectProbeAllowed(ctx.page, {deployment: PROBE, label: 'p8080'});
      await expectProbeDenied(ctx.page, {deployment: PROBE_B, label: 'p8080'});
    },
  },
  {
    id: 'network-policy-space-destination',
    title: 'target a whole space with one override',
    description: 'A rule whose destination is the space, not a deployment, opens every deployment in it to the named source.',
    requires: ['network-policy-source-deployment-scoped'],
    async run(ctx) {
      await updateNetworkPolicy(ctx.page, {
        id: ctx.netpolPolicyId,
        sourceKind: 'space',
        source: 'global',
        destinationKind: 'space',
        destination: NETPOL_SPACE,
        ports: `tcp/${SERVER_PORT}`,
      });
      await expectProbeAllowed(ctx.page, {deployment: PROBE, label: 'p8080'});
      await expectProbeAllowed(ctx.page, {deployment: PROBE_B, label: 'p8080'});
      await expectProbeAllowed(ctx.page, {deployment: PEER, label: 'same'});
    },
  },
  {
    id: 'network-policy-revoked',
    title: 'verify removal re-imposes the default deny',
    description: 'Deletes the override rule and observes fresh probe connections fail again, with the rule gone from the deployment inspector.',
    requires: ['network-policy-space-destination'],
    async run(ctx) {
      await deleteNetworkPolicy(ctx.page, ctx.netpolPolicyId);
      ctx.netpolPolicyId = null;
      await expectProbeDenied(ctx.page, {deployment: PROBE, label: 'p8080'});
      await expectProbeDenied(ctx.page, {deployment: PROBE_B, label: 'p8080'});
      await expectDeploymentNetworkPolicies(ctx.page, {
        name: SERVER,
        machine: WORKER,
        absent: [`space global → ${SERVER}`],
      });
      // The same-space default is untouched by an override coming and going.
      await expectProbeAllowed(ctx.page, {deployment: PEER, label: 'same'});
    },
  },
  {
    id: 'network-policy-dangling-fails-closed',
    title: 'verify a rule anchored to a deleted deployment opens nothing',
    description: 'Deleting a rule\'s source deployment leaves the rule unresolvable: the FE flags it as dangling and the boundary stays closed, because ids are never recycled.',
    requires: ['network-policy-revoked'],
    async run(ctx) {
      await createNixDockerDeployment(ctx.page, {
        name: GHOST,
        machine: WORKER,
        flake: FLAKE,
        networkingMode: NETWORKING_VIRTUAL,
        env: {NETPROBE_NAME: GHOST},
        expectedEnv: {},
      });
      const policyId = await createNetworkPolicy(ctx.page, {
        sourceKind: 'deployment',
        source: `${GHOST} (space ${ctx.globalSpaceId})`,
        destinationKind: 'deployment',
        destination: serverDeploymentLabel(ctx),
        ports: `tcp/${SERVER_PORT}`,
      });
      // The inspector only offers Delete once the deployment is stopped.
      await stopDeployment(ctx.page, {name: GHOST, machine: WORKER});
      await deleteDeployment(ctx.page, {name: GHOST, machine: WORKER});
      await expectNetworkPolicyDangling(ctx.page, policyId);
      await expectProbeDenied(ctx.page, {deployment: PROBE, label: 'p8080'});
      await deleteNetworkPolicy(ctx.page, policyId);
    },
  },
  {
    id: 'network-policy-same-space-rejected',
    title: 'reject a rule that is redundant with the same-space default',
    description: 'A rule whose source and destination resolve to the same space is rejected on write, so no operator can believe an unenforced restriction is in place.',
    requires: ['network-policy-revoked'],
    async run(ctx) {
      await createNetworkPolicy(ctx.page, {
        sourceKind: 'space',
        source: NETPOL_SPACE,
        destinationKind: 'deployment',
        destination: serverDeploymentLabel(ctx),
        ports: `tcp/${SERVER_PORT}`,
        expectError: /Same-space traffic is always allowed/,
      });
    },
  },
];
