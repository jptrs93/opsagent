import {
  createNixDockerDeployment,
  expectPrepareOutput,
} from '../helpers/ui.js';

export const caseDef = {
  id: 'nix-docker-baseline',
  title: 'create baseline Nix Docker deployment',
  description: 'Creates the baseline Nix Docker deployment and verifies prepare output is available.',
  requires: ['bootstrap', 'worker-enrolled'],
  async run(ctx) {
    await createNixDockerDeployment(ctx.page, {expectDefaultDockerImage: true});
    await expectPrepareOutput(ctx.page, 'nixdockerbuild1', 'checking out repo');
  },
};
