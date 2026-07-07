import {Buffer} from 'node:buffer';
import crypto from 'node:crypto';
import {caseDef as nixDockerBaselineCase} from './nix-docker-baseline.js';
import {caseDef as nixDockerVirtualNetworkCase} from './nix-docker-virtual-network.js';
import {hostRolloverCase, virtualPortForwardingCase, virtualRolloverCase} from './rollover-networking.js';
import {installVirtualAuthenticator} from '../helpers/webauthn.js';
import {
  acceptFirstWaitingWorker,
  bootstrapFirstUser,
  configureLargeAssetStorage,
  configureGithubToken,
  createAsset,
  createConfig,
  createNixDockerCrasherDeployment,
  createNixDockerDeployment,
  createPostgresClientDeployment,
  createPostgresDeployment,
  createSecret,
  expectDeploymentOutput,
  expectOpenDeployAgentVersion,
  expectOpenDeployLogs,
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

export const orderedCases = [
  {
    id: 'virtual-authenticator-installed',
    title: 'install virtual authenticator',
    description: 'Installs a Chromium virtual WebAuthn authenticator for passkey registration and login.',
    requires: [],
    async run(ctx) {
      await installVirtualAuthenticator(ctx.page);
    },
  },
  {
    id: 'bootstrap',
    title: 'bootstrap first user',
    description: 'Bootstraps the first operator account and registers its passkey.',
    requires: ['virtual-authenticator-installed'],
    async run(ctx) {
      await bootstrapFirstUser(ctx.page);
    },
  },
  {
    id: 'passkey-login',
    title: 'sign out and login with passkey',
    description: 'Verifies the registered passkey can authenticate a fresh session.',
    requires: ['bootstrap'],
    async run(ctx) {
      await signOutAndLoginWithPasskey(ctx.page);
    },
  },
  {
    id: 'github-token-configured',
    title: 'configure github token',
    description: 'Configures the GitHub token secret when OPENDEPLOY_GITHUB_TOKEN is available.',
    requires: ['passkey-login'],
    async run(ctx) {
      await configureGithubToken(ctx.page, process.env.OPENDEPLOY_GITHUB_TOKEN || '');
    },
  },
  {
    id: 'primary-version-verified',
    title: 'verify primary version',
    description: 'Verifies the primary OpenDeploy deployment is running the expected install version.',
    requires: ['passkey-login'],
    async run(ctx) {
      await expectOpenDeployAgentVersion(ctx.page, {machine: 'primary', version: process.env.OPD_INSTALL_VERSION || 'v0.0.258'});
    },
  },
  {
    id: 'opendeploy-logs-verified',
    title: 'verify opendeploy logs',
    description: 'Checks that the built-in OpenDeploy deployment emits logs in the UI.',
    requires: ['passkey-login'],
    async run(ctx) {
      await expectOpenDeployLogs(ctx.page);
    },
  },
  {
    id: 'worker-enrolled',
    title: 'enroll worker',
    description: 'Accepts the first waiting secondary enrollment request as worker-1.',
    requires: ['passkey-login'],
    async run(ctx) {
      await acceptFirstWaitingWorker(ctx.page);
    },
  },
  nixDockerBaselineCase,
  nixDockerVirtualNetworkCase,
  hostRolloverCase,
  virtualRolloverCase,
  virtualPortForwardingCase,
  {
    id: 'private-github-deployment',
    title: 'create private github deployment',
    description: 'Optionally verifies a private GitHub deployment when a token is available.',
    requires: ['github-token-configured', 'primary-version-verified'],
    when() {
      return Boolean(process.env.OPENDEPLOY_GITHUB_TOKEN);
    },
    async run(ctx) {
      await createNixDockerDeployment(ctx.page, {
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
    },
  },
  {
    id: 'config-created',
    title: 'create config',
    description: 'Creates a config value used by the asset-backed deployment.',
    requires: ['passkey-login'],
    async run(ctx) {
      await createConfig(ctx.page, {
        name: 'e2e.config.message',
        value: 'hello-from-config-page',
      });
    },
  },
  {
    id: 'secret-created',
    title: 'create secret',
    description: 'Creates a secret value used by the asset-backed deployment.',
    requires: ['passkey-login'],
    async run(ctx) {
      await createSecret(ctx.page, {
        name: 'e2e.secret.message',
        value: 'hello-from-secret-page',
      });
    },
  },
  {
    id: 'small-asset-created',
    title: 'create small asset',
    description: 'Creates a small text asset used by the asset-backed deployment.',
    requires: ['passkey-login'],
    async run(ctx) {
      await createAsset(ctx.page, {
        key: 'e2e-workload-asset.txt',
        content: 'hello-from-asset-page',
      });
    },
  },
  {
    id: 'asset-backed-nix-docker-deployment',
    title: 'create asset-backed nix docker deployment',
    description: 'Creates a Nix Docker deployment wired to config, secret, and asset inputs.',
    requires: ['worker-enrolled', 'config-created', 'secret-created', 'small-asset-created'],
    async run(ctx) {
      await createNixDockerDeployment(ctx.page, {
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
    },
  },
  {
    id: 'asset-backed-output-verified',
    title: 'verify asset-backed deployment output',
    description: 'Verifies the asset-backed deployment read the mounted asset content.',
    requires: ['asset-backed-nix-docker-deployment'],
    async run(ctx) {
      await expectDeploymentOutput(ctx.page, 'nixdockerbuild-assets', [
        'nixdockerbuild1 asset file opendeploy-e2e-asset.txt',
        'nixdockerbuild1 asset content opendeploy-e2e-asset.txt=hello-from-asset-page',
      ]);
    },
  },
  {
    id: 'large-asset-storage-configured',
    title: 'configure large asset storage',
    description: 'Creates object storage and configures large asset S3 settings.',
    requires: ['worker-enrolled'],
    async run(ctx) {
      await configureLargeAssetStorage(ctx.page);
    },
  },
  {
    id: 'large-asset-generated',
    title: 'generate large asset',
    description: 'Generates deterministic large binary content and records its SHA-256.',
    requires: ['large-asset-storage-configured'],
    async run(ctx) {
      ctx.largeAsset = generateLargeAsset();
    },
  },
  {
    id: 'large-asset-uploaded',
    title: 'upload large asset',
    description: 'Uploads the generated large binary asset through the assets UI.',
    requires: ['large-asset-generated'],
    async run(ctx) {
      await uploadAsset(ctx.page, {
        key: LARGE_ASSET_KEY,
        content: ctx.largeAsset.content,
        fileName: LARGE_ASSET_KEY,
      });
    },
  },
  {
    id: 'large-asset-verification-deployment',
    title: 'create large asset verification deployment',
    description: 'Creates a deployment that reads the externally stored large asset and checks its digest.',
    requires: ['large-asset-uploaded'],
    async run(ctx) {
      await createNixDockerDeployment(ctx.page, {
        name: 'largeassetverify',
        flake: 'testexamples/largeassetverify/flake.nix',
        env: {
          OPENDEPLOY_E2E_ASSET_PATH: LARGE_ASSET_PATH,
          OPENDEPLOY_E2E_ASSET_SHA256: ctx.largeAsset.sha256,
        },
        expectedEnv: {},
        assetMount: {
          asset: LARGE_ASSET_KEY,
          path: LARGE_ASSET_PATH,
        },
      });
    },
  },
  {
    id: 'large-asset-output-verified',
    title: 'verify large asset output',
    description: 'Verifies the large asset deployment read the expected bytes and SHA-256.',
    requires: ['large-asset-verification-deployment'],
    async run(ctx) {
      await expectDeploymentOutput(ctx.page, 'largeassetverify', [
        `largeassetverify asset read path=${LARGE_ASSET_PATH} bytes=${ctx.largeAsset.content.length}`,
        `largeassetverify asset sha256=${ctx.largeAsset.sha256}`,
        'largeassetverify asset verified true',
      ]);
    },
  },
  {
    id: 'crashing-nix-docker-deployment',
    title: 'create crashing nix docker deployment',
    description: 'Creates a crashing deployment and verifies restart/output behavior.',
    requires: ['worker-enrolled'],
    async run(ctx) {
      await createNixDockerCrasherDeployment(ctx.page);
    },
  },
  {
    id: 'postgres-user-secret-created',
    title: 'create postgres user secret',
    description: 'Creates the Postgres username secret.',
    requires: ['passkey-login'],
    async run(ctx) {
      await createSecret(ctx.page, {
        name: 'postgres',
        value: 'postgres',
      });
    },
  },
  {
    id: 'postgres-password-secret-created',
    title: 'create postgres password secret',
    description: 'Creates the Postgres password secret.',
    requires: ['passkey-login'],
    async run(ctx) {
      await createSecret(ctx.page, {
        name: 'postgrespass',
        value: 'postgrespass',
      });
    },
  },
  {
    id: 'opendeploy-agents-upgraded',
    title: 'upgrade opendeploy agents',
    description: 'Upgrades worker and primary OpenDeploy agents to the expected upgrade version.',
    requires: ['worker-enrolled'],
    async run(ctx) {
      await upgradeOpenDeployAgents(ctx.page, {version: process.env.OPD_UPGRADE_VERSION || 'v0.0.258'});
    },
  },
  {
    id: 'postgres-deployment-created',
    title: 'create postgres deployment',
    description: 'Creates and verifies the Postgres container image deployment.',
    requires: ['worker-enrolled', 'postgres-user-secret-created', 'postgres-password-secret-created'],
    async run(ctx) {
      await createPostgresDeployment(ctx.page);
    },
  },
  {
    id: 'postgres-client-deployment-created',
    title: 'create postgres client deployment',
    description: 'Creates a Nix Docker Postgres client deployment and verifies rows are readable.',
    requires: ['postgres-deployment-created'],
    async run(ctx) {
      await createPostgresClientDeployment(ctx.page);
    },
  },
  {
    id: 'backup-restore-state-prepared',
    title: 'prepare backup restore state',
    description: 'Optionally writes backup/restore state for the restore harness extension.',
    requires: ['large-asset-storage-configured'],
    when() {
      return process.env.OPD_BACKUP_RESTORE === 'true';
    },
    async run(ctx) {
      await runBackupRestoreSetup(ctx.page);
    },
  },
];
