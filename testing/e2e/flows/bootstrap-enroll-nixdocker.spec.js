import {test} from '@playwright/test';
import {installVirtualAuthenticator} from '../helpers/webauthn.js';
import {
  acceptFirstWaitingWorker,
  bootstrapFirstUser,
  configureGithubToken,
  createAsset,
  createNixDockerCrasherDeployment,
  createConfig,
  createNixDockerDeployment,
  createPostgresClientDeployment,
  createPostgresDeployment,
  createSecret,
  expectDeploymentOutput,
  expectOpenDeployAgentVersion,
  expectOpenDeployLogs,
  expectPrepareOutput,
  signOutAndLoginWithPasskey,
  upgradeOpenDeployAgents,
} from '../helpers/ui.js';

test('bootstrap primary, enroll worker, and create Nix Docker deployment', async ({page}) => {
  await installVirtualAuthenticator(page);

  await bootstrapFirstUser(page);
  await signOutAndLoginWithPasskey(page);
  await configureGithubToken(page, process.env.OPENDEPLOY_GITHUB_TOKEN || '');
  await expectOpenDeployAgentVersion(page, {machine: 'primary', version: process.env.OPD_INSTALL_VERSION || 'v0.0.140'});
  await expectOpenDeployLogs(page);
  await acceptFirstWaitingWorker(page);
  await createNixDockerDeployment(page, {expectDefaultDockerImage: true});
  await expectPrepareOutput(page, 'nixdockerbuild1', 'checking out repo');

  if (process.env.OPENDEPLOY_GITHUB_TOKEN) {
    await createNixDockerDeployment(page, {
      name: 'jnotes-primary',
      machine: 'primary',
      repo: 'github.com/jptrs93/jnotes',
      flake: 'flake.nix',
      env: {
        JNOTES_BIND_PORT: '8081',
        JNOTES_LOCAL_DEV: 'true',
      },
      expectedEnv: {},
      verifyLogs: false,
    });
  }

  await createConfig(page, {
    name: 'e2e.config.message',
    value: 'hello-from-config-page',
  });
  await createSecret(page, {
    name: 'e2e.secret.message',
    value: 'hello-from-secret-page',
  });
  await createAsset(page, {
    key: 'e2e-workload-asset.txt',
    content: 'hello-from-asset-page',
  });

  await createNixDockerDeployment(page, {
    name: 'nixdockerbuild-assets',
    env: {
      OPENDEPLOY_E2E_MESSAGE: {type: 'config', name: 'e2e.config.message'},
      OPENDEPLOY_E2E_COLOR: {type: 'secret', name: 'e2e.secret.message'},
      OPENDEPLOY_E2E_CONFIG: {type: 'config', name: 'e2e.config.message'},
      OPENDEPLOY_E2E_SECRET: {type: 'secret', name: 'e2e.secret.message'},
    },
    expectedEnv: {
      OPENDEPLOY_E2E_MESSAGE: 'hello-from-config-page',
      OPENDEPLOY_E2E_COLOR: 'hello-from-secret-page',
    },
    assetMount: {
      asset: 'e2e-workload-asset.txt',
      path: '/tmp/opendeploy-e2e-asset.txt',
    },
  });
  await expectDeploymentOutput(page, 'nixdockerbuild-assets', [
    'nixdockerbuild1 asset file opendeploy-e2e-asset.txt',
    'nixdockerbuild1 asset content opendeploy-e2e-asset.txt=hello-from-asset-page',
  ]);

  await createNixDockerCrasherDeployment(page);
  await createSecret(page, {
    name: 'postgres',
    value: 'postgres',
  });
  await createSecret(page, {
    name: 'postgrespass',
    value: 'postgrespass',
  });
  await upgradeOpenDeployAgents(page, {version: 'v0.0.158'});
  await createPostgresDeployment(page);
  await createPostgresClientDeployment(page);
});
