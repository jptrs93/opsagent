import {
  createNixDockerDeployment,
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
      },
    });
  },
};
