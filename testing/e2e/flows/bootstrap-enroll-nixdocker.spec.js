import {test} from '@playwright/test';
import {Buffer} from 'node:buffer';
import crypto from 'node:crypto';
import {installVirtualAuthenticator} from '../helpers/webauthn.js';
import {
  acceptFirstWaitingWorker,
  bootstrapFirstUser,
  configureLargeAssetStorage,
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
  runBackupRestoreSetup,
  signOutAndLoginWithPasskey,
  uploadAsset,
  upgradeOpenDeployAgents,
} from '../helpers/ui.js';

const LARGE_ASSET_KEY = 'e2e-large-asset.bin';
const LARGE_ASSET_PATH = '/tmp/opendeploy-e2e-large-asset.bin';

function generateLargeAsset() {
  const content = Buffer.allocUnsafe(11 * 1024 * 1024);
  for (let i = 0; i < content.length; i += 1) {
    content[i] = (i * 31 + 17) % 251;
  }
  return {
    content,
    sha256: crypto.createHash('sha256').update(content).digest('hex'),
  };
}

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

  await configureLargeAssetStorage(page);
  const largeAsset = generateLargeAsset();
  console.log(`[opendeploy-e2e] large asset ${LARGE_ASSET_KEY} sha256=${largeAsset.sha256}`);
  await uploadAsset(page, {
    key: LARGE_ASSET_KEY,
    content: largeAsset.content,
    fileName: LARGE_ASSET_KEY,
  });
  await createNixDockerDeployment(page, {
    name: 'largeassetverify',
    flake: 'testexamples/largeassetverify/flake.nix',
    env: {
      OPENDEPLOY_E2E_ASSET_PATH: LARGE_ASSET_PATH,
      OPENDEPLOY_E2E_ASSET_SHA256: largeAsset.sha256,
    },
    expectedEnv: {},
    assetMount: {
      asset: LARGE_ASSET_KEY,
      path: LARGE_ASSET_PATH,
    },
  });
  await expectDeploymentOutput(page, 'largeassetverify', [
    `largeassetverify asset read path=${LARGE_ASSET_PATH} bytes=${largeAsset.content.length}`,
    `largeassetverify asset sha256=${largeAsset.sha256}`,
    'largeassetverify asset verified true',
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
  await upgradeOpenDeployAgents(page, {version: process.env.OPD_UPGRADE_VERSION || 'v0.0.173'});
  await createPostgresDeployment(page);
  await createPostgresClientDeployment(page);

  if (process.env.OPD_BACKUP_RESTORE === 'true') {
    await runBackupRestoreSetup(page);
  }
});
