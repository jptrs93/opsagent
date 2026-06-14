import {test} from '@playwright/test';
import {installVirtualAuthenticator} from '../helpers/webauthn.js';
import {
  acceptFirstWaitingWorker,
  bootstrapFirstUser,
  createAsset,
  createConfig,
  createNixDockerDeployment,
  createSecret,
  expectDeploymentOutput,
  signOutAndLoginWithPasskey,
} from '../helpers/ui.js';

test('bootstrap primary, enroll worker, and create Nix Docker deployment', async ({page}) => {
  await installVirtualAuthenticator(page);

  await bootstrapFirstUser(page);
  await signOutAndLoginWithPasskey(page);
  await acceptFirstWaitingWorker(page);
  await createNixDockerDeployment(page, {expectDefaultDockerImage: true});

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
      OPENDEPLOY_E2E_MESSAGE: '${c:e2e.config.message}',
      OPENDEPLOY_E2E_COLOR: '${s:e2e.secret.message}',
      OPENDEPLOY_E2E_CONFIG: '${c:e2e.config.message}',
      OPENDEPLOY_E2E_SECRET: '${s:e2e.secret.message}',
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
});
