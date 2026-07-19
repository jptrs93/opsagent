import {
  createNixDockerDeployment,
  expectDeploymentOutput,
  NETWORKING_VIRTUAL,
} from '../helpers/ui.js';

export const caseDef = {
  id: 'nix-docker-virtual-network',
  title: 'create virtual-network Nix Docker deployment',
  description: 'Creates a Nix Docker deployment using the phase-one virtual networking mode.',
  requires: ['bootstrap', 'worker-enrolled'],
  async run(ctx) {
    await createNixDockerDeployment(ctx.page, {
      name: 'nixdockerbuild-virtual',
      networkingMode: NETWORKING_VIRTUAL,
      env: {
        OPENDEPLOY_E2E_MESSAGE: 'hello-from-virtual-network',
        OPENDEPLOY_E2E_COLOR: 'purple',
        OPENDEPLOY_E2E_IPV4_EGRESS_URL: process.env.OPD_IPV4_EGRESS_URL || '',
      },
    });
    await expectDeploymentOutput(ctx.page, 'nixdockerbuild-virtual', [
      `nixdockerbuild1 ipv4 egress observed source=${process.env.OPD_IPV4_EGRESS_EXPECTED_SOURCE} status=200`,
    ]);
  },
};
