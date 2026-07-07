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

async function step(name, fn) {
  return test.step(name, fn);
}

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
  await step('install virtual authenticator', () => installVirtualAuthenticator(page));

  await step('bootstrap first user', () => bootstrapFirstUser(page));
  await step('sign out and login with passkey', () => signOutAndLoginWithPasskey(page));
  await step('configure github token', () => configureGithubToken(page, process.env.OPENDEPLOY_GITHUB_TOKEN || ''));
  await step('verify primary version', () => expectOpenDeployAgentVersion(page, {machine: 'primary', version: process.env.OPD_INSTALL_VERSION || 'v0.0.257'}));
  await step('verify opendeploy logs', () => expectOpenDeployLogs(page));
  await step('enroll worker', () => acceptFirstWaitingWorker(page));
  await step('create baseline nix docker deployment', async () => {
    await createNixDockerDeployment(page, {expectDefaultDockerImage: true});
    await expectPrepareOutput(page, 'nixdockerbuild1', 'checking out repo');
  });

  if (process.env.OPENDEPLOY_GITHUB_TOKEN) {
    await step('create private github deployment', () => createNixDockerDeployment(page, {
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
    }));
  }

  await step('create config', () => createConfig(page, {
    name: 'e2e.config.message',
    value: 'hello-from-config-page',
  }));
  await step('create secret', () => createSecret(page, {
    name: 'e2e.secret.message',
    value: 'hello-from-secret-page',
  }));
  await step('create small asset', () => createAsset(page, {
    key: 'e2e-workload-asset.txt',
    content: 'hello-from-asset-page',
  }));

  await step('create asset-backed nix docker deployment', () => createNixDockerDeployment(page, {
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
  }));
  await step('verify asset-backed deployment output', () => expectDeploymentOutput(page, 'nixdockerbuild-assets', [
    'nixdockerbuild1 asset file opendeploy-e2e-asset.txt',
    'nixdockerbuild1 asset content opendeploy-e2e-asset.txt=hello-from-asset-page',
  ]));

  await step('configure large asset storage', () => configureLargeAssetStorage(page));
  const largeAsset = await step('generate large asset', async () => generateLargeAsset());
  await step('upload large asset', () => uploadAsset(page, {
    key: LARGE_ASSET_KEY,
    content: largeAsset.content,
    fileName: LARGE_ASSET_KEY,
  }));
  await step('create large asset verification deployment', () => createNixDockerDeployment(page, {
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
  }));
  await step('verify large asset output', () => expectDeploymentOutput(page, 'largeassetverify', [
    `largeassetverify asset read path=${LARGE_ASSET_PATH} bytes=${largeAsset.content.length}`,
    `largeassetverify asset sha256=${largeAsset.sha256}`,
    'largeassetverify asset verified true',
  ]));

  await step('create crashing nix docker deployment', () => createNixDockerCrasherDeployment(page));
  await step('create postgres user secret', () => createSecret(page, {
    name: 'postgres',
    value: 'postgres',
  }));
  await step('create postgres password secret', () => createSecret(page, {
    name: 'postgrespass',
    value: 'postgrespass',
  }));
  await step('upgrade opendeploy agents', () => upgradeOpenDeployAgents(page, {version: process.env.OPD_UPGRADE_VERSION || 'v0.0.257'}));
  await step('create postgres deployment', () => createPostgresDeployment(page));
  await step('create postgres client deployment', () => createPostgresClientDeployment(page));

  if (process.env.OPD_BACKUP_RESTORE === 'true') {
    await step('prepare backup restore state', () => runBackupRestoreSetup(page));
  }
});
