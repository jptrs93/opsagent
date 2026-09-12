import {
  createNixDockerDeployment,
  expectDeploymentOutput,
  expectPrepareOutput,
  resetNixBuildStore,
} from '../helpers/ui.js';

const HOSTILE_FLAKE = 'testexamples/hostilebuild/flake.nix';

// The build container cases pin down the containerized Nix build: the store
// lifecycle visible in the prepare log, what a derivation builder can and
// cannot reach, the two distinct failure reports (a non-image output and a
// build killed at the memory limit), and the operator store reset.
export const nixBuildContainerCases = [
  {
    id: 'nix-build-cold-start',
    title: 'first Nix build seeds the build image and store',
    description: 'The baseline build on worker-1 pulled the build image, seeded the store template, created the repository store, ran in a container and imported the image.',
    requires: ['nix-docker-baseline'],
    async run(ctx) {
      for (const text of [
        'pulled build image',
        'seeding store template for build image',
        'creating store for repository github.com/jptrs93/opsagent',
        'running Nix build in container',
        'image import complete',
      ]) {
        await expectPrepareOutput(ctx.page, 'nixdockerbuild1', text);
      }
    },
  },
  {
    id: 'nix-build-hostile',
    title: 'hostile build sees nothing of the node',
    description: 'A derivation that probes the build environment runs as a nixbld user without capabilities, cannot see agent state or the containerd socket, cannot write existing store paths, and reaches public hosts but not the cluster prefix, host loopback or metadata addresses.',
    requires: ['nix-docker-baseline'],
    async run(ctx) {
      await createNixDockerDeployment(ctx.page, {name: 'hostilebuild', flake: HOSTILE_FLAKE, env: {}, expectedEnv: {}});
      await expectDeploymentOutput(ctx.page, 'hostilebuild', [
        'hostilebuild probe=builder_uid result=nixbld',
        'hostilebuild probe=cap_eff result=0000000000000000',
        'hostilebuild probe=agent_data result=absent',
        'hostilebuild probe=agent_env result=absent',
        'hostilebuild probe=containerd_socket result=absent',
        'hostilebuild probe=machine_key result=absent',
        'hostilebuild probe=store_write result=readonly',
        'hostilebuild probe=dns_server result=present',
        'hostilebuild probe=cluster_tcp result=unreachable',
        'hostilebuild probe=metadata result=unreachable',
        'hostilebuild probe=host_loopback result=unreachable',
        'hostilebuild probe=public_https result=reachable',
      ]);
    },
  },
  {
    id: 'nix-build-not-image',
    title: 'non-image flake output fails preparation',
    description: 'A target whose output is not a nix2container image description is reported as such.',
    requires: ['nix-build-hostile'],
    async run(ctx) {
      await createNixDockerDeployment(ctx.page, {name: 'hostile-notimage', flake: HOSTILE_FLAKE, target: '.#notimage', env: {}, expectedEnv: {}, verifyLogs: false});
      await expectPrepareOutput(ctx.page, 'hostile-notimage', 'output is not a nix2container image');
    },
  },
  {
    id: 'nix-build-memory-limit',
    title: 'build killed at the memory limit is reported',
    description: 'A derivation that allocates without bound is killed by the build container cgroup and the prepare log names the memory limit.',
    requires: ['nix-build-not-image'],
    async run(ctx) {
      await createNixDockerDeployment(ctx.page, {name: 'hostile-membomb', flake: HOSTILE_FLAKE, target: '.#membomb', env: {}, expectedEnv: {}, verifyLogs: false});
      await expectPrepareOutput(ctx.page, 'hostile-membomb', 'build exceeded its memory limit');
    },
  },
  {
    id: 'nix-store-reset',
    title: 'operator store reset reseeds before the next build',
    description: 'Requesting a store reset from the deployment inspector makes the next build of the repository on the node reseed its store from the template.',
    requires: ['nix-build-memory-limit'],
    async run(ctx) {
      await resetNixBuildStore(ctx.page, {name: 'hostilebuild'});
      await createNixDockerDeployment(ctx.page, {name: 'hostile-after-reset', flake: HOSTILE_FLAKE, target: '.#notimage', env: {}, expectedEnv: {}, verifyLogs: false});
      await expectPrepareOutput(ctx.page, 'hostile-after-reset', 'has an operator reset outstanding; reseeding');
      await expectPrepareOutput(ctx.page, 'hostile-after-reset', 'store created in');
    },
  },
];
