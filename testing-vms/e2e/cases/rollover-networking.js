import {
  createNixDockerDeployment,
  expectDeploymentOutput,
  expectHTTPText,
  NETWORKING_HOST,
  NETWORKING_VIRTUAL,
  PORT_FORWARD_TCP,
  updateNixDockerDeployment,
  UPGRADE_ROLLOVER,
} from '../helpers/ui.js';

const SECONDARY_HOST = process.env.OPD_SECONDARY_HOST || 'opendeploy-secondary';
const ROLLOVER_FLAKE = 'testexamples/rollover/flake.nix';

export const hostRolloverCase = {
  id: 'host-network-rollover',
  title: 'roll over host-network deployment',
  description: 'Verifies cooperative host-network rollover: candidate signals readiness before waiting for the old process to release the host port.',
  requires: ['worker-enrolled'],
  async run(ctx) {
    const name = 'rollover-host';
    const port = 18180;
    await createNixDockerDeployment(ctx.page, {
      name,
      flake: ROLLOVER_FLAKE,
      networkingMode: NETWORKING_HOST,
      env: {
        OPD_ROLLOVER_GENERATION: 'host-v1',
        OPD_ROLLOVER_ADDR: `:${port}`,
      },
      expectedEnv: {},
      verifyLogs: false,
    });
    await expectHTTPText(`http://${SECONDARY_HOST}:${port}/`, 'rollover generation=host-v1');

    await updateNixDockerDeployment(ctx.page, {
      name,
      env: {
        OPD_ROLLOVER_GENERATION: 'host-v2',
        OPD_ROLLOVER_ADDR: `:${port}`,
        OPD_ROLLOVER_READY_DELAY_MS: '500',
        OPD_ROLLOVER_BIND_BEFORE_READY: 'false',
      },
      upgradeStrategy: UPGRADE_ROLLOVER,
      readinessTimeoutSeconds: 30,
    });
    await expectDeploymentOutput(ctx.page, name, [
      'rollover readiness sent generation=host-v2',
      'rollover listen successful generation=host-v2',
    ]);
    await expectHTTPText(`http://${SECONDARY_HOST}:${port}/`, 'rollover generation=host-v2');
  },
};

export const virtualRolloverCase = {
  id: 'virtual-network-rollover',
  title: 'roll over virtual-network deployment',
  description: 'Verifies virtual-network rollover with host-port DNAT flipped to the candidate after readiness.',
  requires: ['worker-enrolled'],
  async run(ctx) {
    const name = 'rollover-virtual';
    const hostPort = 18181;
    await createNixDockerDeployment(ctx.page, {
      name,
      flake: ROLLOVER_FLAKE,
      networkingMode: NETWORKING_VIRTUAL,
      portForwarding: [{protocol: PORT_FORWARD_TCP, hostPort, containerPort: 8080}],
      env: {
        OPD_ROLLOVER_GENERATION: 'virtual-v1',
        OPD_ROLLOVER_ADDR: ':8080',
      },
      expectedEnv: {},
      verifyLogs: false,
    });
    await expectHTTPText(`http://${SECONDARY_HOST}:${hostPort}/`, 'rollover generation=virtual-v1');

    await updateNixDockerDeployment(ctx.page, {
      name,
      env: {
        OPD_ROLLOVER_GENERATION: 'virtual-v2',
        OPD_ROLLOVER_ADDR: ':8080',
        OPD_ROLLOVER_READY_DELAY_MS: '500',
      },
      upgradeStrategy: UPGRADE_ROLLOVER,
      readinessTimeoutSeconds: 30,
    });
    await expectDeploymentOutput(ctx.page, name, [
      'rollover readiness sent generation=virtual-v2',
      'rollover listen successful generation=virtual-v2',
    ]);
    await expectHTTPText(`http://${SECONDARY_HOST}:${hostPort}/`, 'rollover generation=virtual-v2');
  },
};

export const virtualPortForwardingCase = {
  id: 'virtual-port-forwarding',
  title: 'publish virtual deployment with port forwarding',
  description: 'Verifies virtual-mode TCP port forwarding from a machine host port to a container port.',
  requires: ['worker-enrolled'],
  async run(ctx) {
    const name = 'port-forward-virtual';
    const hostPort = 18182;
    await createNixDockerDeployment(ctx.page, {
      name,
      flake: ROLLOVER_FLAKE,
      networkingMode: NETWORKING_VIRTUAL,
      portForwarding: [{protocol: PORT_FORWARD_TCP, hostPort, containerPort: 8080}],
      env: {
        OPD_ROLLOVER_GENERATION: 'port-forward',
        OPD_ROLLOVER_ADDR: ':8080',
      },
      expectedEnv: {},
      verifyLogs: false,
    });
    await expectHTTPText(`http://${SECONDARY_HOST}:${hostPort}/`, 'rollover generation=port-forward');
  },
};
