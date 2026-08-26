import {
  createNixDockerDeployment,
  expectDeploymentOutput,
  expectHTTPBlocked,
  expectHTTPText,
  expectPortForwardAllowDiagnostics,
  NETWORKING_VIRTUAL,
  PORT_FORWARD_TCP,
  setPortForwardAllowList,
} from '../helpers/ui.js';

const SECONDARY_HOST = process.env.OPD_SECONDARY_HOST || 'opendeploy-secondary';
const NAME = 'port-forward-filtered';
const HOST_PORT = 18183;
const URL = `http://${SECONDARY_HOST}:${HOST_PORT}/`;
const BLOCKED_ALLOW = ['203.0.113.7', '2001:db8::/64'];
// The filtered port is tunneled via the primary VM, so the secondary's
// prerouting filter sees the primary's source address; allowing exactly that
// /32 exercises real prefix matching. Fall back to allow-any when the
// harness does not export the client address.
const CLIENT_IP = process.env.OPD_FILTERED_PORT_CLIENT_IP?.trim();
const MATCHING_ALLOW = CLIENT_IP ? [CLIENT_IP] : ['0.0.0.0/0', '::/0'];

export const portForwardIpFilterCases = [
  {
    id: 'port-forward-ip-filter-blocked',
    title: 'create port forward with non-matching allow list',
    description: 'Creates a virtual deployment whose forwarded port allows only unroutable test addresses and verifies external traffic cannot reach it.',
    requires: ['worker-enrolled'],
    async run(ctx) {
      await createNixDockerDeployment(ctx.page, {
        name: NAME,
        flake: 'testexamples/rollover/flake.nix',
        networkingMode: NETWORKING_VIRTUAL,
        portForwarding: [{
          protocol: PORT_FORWARD_TCP,
          hostPort: HOST_PORT,
          containerPort: 8080,
          allow: BLOCKED_ALLOW.join(', '),
        }],
        env: {
          OPD_ROLLOVER_GENERATION: 'ip-filter',
          OPD_ROLLOVER_ADDR: ':8080',
        },
        expectedEnv: {},
        verifyLogs: false,
      });
      await expectDeploymentOutput(ctx.page, NAME, ['rollover listen successful generation=ip-filter']);
      await expectHTTPBlocked(URL);
    },
  },
  {
    id: 'port-forward-ip-filter-allowed',
    title: 'widen port forward allow list to admit the test client',
    description: 'Confirms the created allow list survived the HCL round trip, replaces it with one matching the test client, and verifies forwarding resumes.',
    requires: ['port-forward-ip-filter-blocked'],
    async run(ctx) {
      await setPortForwardAllowList(ctx.page, {
        name: NAME,
        allow: MATCHING_ALLOW,
        expectRendered: BLOCKED_ALLOW,
      });
      await expectHTTPText(URL, 'rollover generation=ip-filter');
    },
  },
  {
    id: 'port-forward-ip-filter-diagnostics',
    title: 'reject invalid allow entries in the editor',
    description: 'Verifies the HCL editor flags malformed allow values on the port forward without saving them.',
    requires: ['port-forward-ip-filter-allowed'],
    async run(ctx) {
      await expectPortForwardAllowDiagnostics(ctx.page, {
        name: NAME,
        checks: [
          {allow: ['office'], diagnostic: 'Allow entries must be quoted IP addresses or CIDR prefixes.'},
          {allow: ['203.0.113.7/33'], diagnostic: 'Allow entries must be quoted IP addresses or CIDR prefixes.'},
        ],
      });
    },
  },
];
