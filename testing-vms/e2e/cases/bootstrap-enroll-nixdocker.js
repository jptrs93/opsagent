import {Buffer} from 'node:buffer';
import crypto from 'node:crypto';
import {caseDef as nixDockerBaselineCase} from './nix-docker-baseline.js';
import {caseDef as nixDockerVirtualNetworkCase} from './nix-docker-virtual-network.js';
import {hostRolloverCase, virtualPortForwardingCase, virtualRolloverCase} from './rollover-networking.js';
import {expectTLSPassthroughRoutes, tlsPassthroughCases} from './tls-passthrough.js';
import {installVirtualAuthenticator} from '../helpers/webauthn.js';
import {
  acceptWaitingWorker,
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
  deploymentRestartCount,
  expectDeploymentRestartCount,
  expectBackupStorageDisabled,
  expectDeploymentOutput,
  expectReferenceUsage,
  expectOpenDeployAgentVersion,
  expectOpenDeployNetVersion,
  expectOpenDeployLogs,
  expectDeploymentRunning,
  runBackupRestoreSetup,
  signOutAndLoginWithPasskey,
  uploadAsset,
  upgradeOpenDeployAgents,
  upgradeOpenDeployNet,
} from '../helpers/ui.js';

const PRE_MIGRATION_LARGE_ASSETS = [
  {key: 'e2e-large-asset-local.bin', path: '/tmp/e2e-large-asset-local.bin', seed: 17},
  {key: 'e2e-large-asset-migrated.bin', path: '/tmp/e2e-large-asset-migrated.bin', seed: 73},
  {key: 'e2e-large-asset-extra.bin', path: '/tmp/e2e-large-asset-extra.bin', seed: 131},
];
const POST_MIGRATION_LARGE_ASSET = {
  key: 'e2e-large-asset-s3.bin',
  path: '/tmp/e2e-large-asset-s3.bin',
  seed: 191,
};

function requiredEnv(name) {
  const value = process.env[name]?.trim();
  if (!value) throw new Error(`${name} is required`);
  return value;
}

function generateLargeAsset(asset) {
  const content = Buffer.allocUnsafe(11 * 1024 * 1024);
  for (let i = 0; i < content.length; i += 1) {
    content[i] = (i * 31 + asset.seed) % 251;
  }
  return {
    ...asset,
    content,
    sha256: crypto.createHash('sha256').update(content).digest('hex'),
  };
}

async function createLargeAssetDeployment(page, {name, asset}) {
  await createNixDockerDeployment(page, {
    name,
    flake: 'testexamples/largeassetverify/flake.nix',
    env: {
      OPENDEPLOY_E2E_ASSET_PATH: asset.path,
      OPENDEPLOY_E2E_ASSET_SHA256: asset.sha256,
    },
    expectedEnv: {},
    assetMount: {
      asset: asset.key,
      path: asset.path,
    },
  });
}

async function expectLargeAssetDeploymentOutput(page, {name, asset}) {
  await expectDeploymentOutput(page, name, [
    `largeassetverify asset read path=${asset.path} bytes=${asset.content.length}`,
    `largeassetverify asset sha256=${asset.sha256}`,
    'largeassetverify asset verified true',
  ]);
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
      await expectOpenDeployAgentVersion(ctx.page, {machine: 'primary', version: requiredEnv('OPD_INSTALL_VERSION')});
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
    description: 'Accepts worker-1 by its persistent secondary enrollment identity.',
    requires: ['passkey-login'],
    async run(ctx) {
      await acceptWaitingWorker(ctx.page, {
        machineID: requiredEnv('OPD_WORKER_1_MACHINE_ID'),
        workerName: 'worker-1',
      });
    },
  },
  {
    id: 'worker-2-enrolled',
    title: 'enroll second worker',
    description: 'Accepts worker-2 by its persistent secondary enrollment identity.',
    requires: ['worker-enrolled'],
    async run(ctx) {
      await acceptWaitingWorker(ctx.page, {
        machineID: requiredEnv('OPD_WORKER_2_MACHINE_ID'),
        workerName: 'worker-2',
        expectNoPending: true,
      });
    },
  },
  nixDockerBaselineCase,
  nixDockerVirtualNetworkCase,
  hostRolloverCase,
  virtualRolloverCase,
  virtualPortForwardingCase,
  ...tlsPassthroughCases,
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
    id: 'reference-usage-overlays-verified',
    title: 'verify reference usage overlays',
    description: 'Verifies config, secret, and asset usage overlays identify their consuming deployment.',
    requires: ['asset-backed-nix-docker-deployment'],
    async run(ctx) {
      const expected = {deploymentName: 'nixdockerbuild-assets'};
      await expectReferenceUsage(ctx.page, {resourceType: 'config', resourceName: 'e2e.config.message', ...expected});
      await expectReferenceUsage(ctx.page, {resourceType: 'secret', resourceName: 'e2e.secret.message', ...expected});
      await expectReferenceUsage(ctx.page, {resourceType: 'asset', resourceName: 'e2e-workload-asset.txt', ...expected});
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
    id: 'backup-storage-disabled',
    title: 'verify backup storage disabled',
    description: 'Confirms large assets initially use primary-local storage because Backup is disabled.',
    requires: ['worker-enrolled'],
    async run(ctx) {
      await expectBackupStorageDisabled(ctx.page);
    },
  },
  {
    id: 'pre-migration-large-assets-generated',
    title: 'generate pre-migration large assets',
    description: 'Generates three distinct deterministic large assets and records their SHA-256 digests.',
    requires: ['backup-storage-disabled'],
    async run(ctx) {
      ctx.preMigrationLargeAssets = PRE_MIGRATION_LARGE_ASSETS.map(generateLargeAsset);
    },
  },
  {
    id: 'pre-migration-large-assets-uploaded',
    title: 'upload pre-migration large assets',
    description: 'Uploads several large assets while Backup is disabled so they are stored locally.',
    requires: ['pre-migration-large-assets-generated'],
    async run(ctx) {
      for (const asset of ctx.preMigrationLargeAssets) {
        await uploadAsset(ctx.page, {
          key: asset.key,
          content: asset.content,
          fileName: asset.key,
        });
      }
    },
  },
  {
    id: 'local-large-asset-deployment-created',
    title: 'create local large asset deployment',
    description: 'Creates a deployment that consumes one large asset before S3 migration.',
    requires: ['pre-migration-large-assets-uploaded'],
    async run(ctx) {
      await createLargeAssetDeployment(ctx.page, {
        name: 'largeassetlocal',
        asset: ctx.preMigrationLargeAssets[0],
      });
    },
  },
  {
    id: 'local-large-asset-output-verified',
    title: 'verify local large asset output',
    description: 'Verifies the pre-migration deployment reads the locally stored asset.',
    requires: ['local-large-asset-deployment-created'],
    async run(ctx) {
      await expectLargeAssetDeploymentOutput(ctx.page, {
        name: 'largeassetlocal',
        asset: ctx.preMigrationLargeAssets[0],
      });
    },
  },
  {
    id: 'large-asset-storage-configured',
    title: 'configure large asset backup',
    description: 'Enables Backup and shared large-asset S3, then waits for all local assets to migrate.',
    requires: ['local-large-asset-output-verified'],
    async run(ctx) {
      await configureLargeAssetStorage(ctx.page);
    },
  },
  {
    id: 'migrated-large-asset-deployment-created',
    title: 'create migrated large asset deployment',
    description: 'Creates a fresh deployment using a pre-existing asset after its migration to S3.',
    requires: ['large-asset-storage-configured'],
    async run(ctx) {
      await createLargeAssetDeployment(ctx.page, {
        name: 'largeassetmigrated',
        asset: ctx.preMigrationLargeAssets[1],
      });
    },
  },
  {
    id: 'migrated-large-asset-output-verified',
    title: 'verify migrated large asset output',
    description: 'Verifies a fresh deployment can read the migrated asset from S3.',
    requires: ['migrated-large-asset-deployment-created'],
    async run(ctx) {
      await expectLargeAssetDeploymentOutput(ctx.page, {
        name: 'largeassetmigrated',
        asset: ctx.preMigrationLargeAssets[1],
      });
    },
  },
  {
    id: 'post-migration-large-asset-uploaded',
    title: 'upload post-migration large asset',
    description: 'Creates and uploads a new large asset while S3-backed storage is active.',
    requires: ['migrated-large-asset-output-verified'],
    async run(ctx) {
      ctx.postMigrationLargeAsset = generateLargeAsset(POST_MIGRATION_LARGE_ASSET);
      await uploadAsset(ctx.page, {
        key: ctx.postMigrationLargeAsset.key,
        content: ctx.postMigrationLargeAsset.content,
        fileName: ctx.postMigrationLargeAsset.key,
      });
    },
  },
  {
    id: 'post-migration-large-asset-deployment-created',
    title: 'create post-migration large asset deployment',
    description: 'Creates a deployment using the large asset uploaded directly to S3-backed storage.',
    requires: ['post-migration-large-asset-uploaded'],
    async run(ctx) {
      await createLargeAssetDeployment(ctx.page, {
        name: 'largeassets3',
        asset: ctx.postMigrationLargeAsset,
      });
    },
  },
  {
    id: 'post-migration-large-asset-output-verified',
    title: 'verify post-migration large asset output',
    description: 'Verifies the newly uploaded S3-backed asset is available to deployments.',
    requires: ['post-migration-large-asset-deployment-created'],
    async run(ctx) {
      await expectLargeAssetDeploymentOutput(ctx.page, {
        name: 'largeassets3',
        asset: ctx.postMigrationLargeAsset,
      });
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
    requires: ['nix-docker-virtual-network', 'virtual-network-rollover', 'virtual-port-forwarding', 'tls-ingress-route-restored'],
    async run(ctx) {
      const virtualWorkloads = [
        {name: 'nixdockerbuild-virtual', machine: 'worker-1'},
        {name: 'rollover-virtual', machine: 'worker-1'},
        {name: 'port-forward-virtual', machine: 'worker-1'},
        {name: 'tls-ingress-one', machine: 'worker-2'},
        {name: 'tls-ingress-two', machine: 'worker-2'},
        {name: 'tls-ingress-three', machine: 'worker-2'},
      ];
      const restartCounts = new Map();
      for (const workload of virtualWorkloads) {
        restartCounts.set(workload.name, await deploymentRestartCount(ctx.page, workload));
      }
      await upgradeOpenDeployAgents(ctx.page, {
        version: requiredEnv('OPD_UPGRADE_VERSION'),
        afterWorkerUpgrade: async workerName => {
          for (const workload of virtualWorkloads.filter(item => item.machine === workerName)) {
            await expectDeploymentRunning(ctx.page, workload);
            await expectDeploymentRestartCount(ctx.page, {...workload, count: restartCounts.get(workload.name)});
          }
          if (workerName === 'worker-2') await expectTLSPassthroughRoutes();
        },
        afterUpgrade: async () => {
          for (const machine of ['primary', 'worker-1', 'worker-2']) {
            await expectOpenDeployNetVersion(ctx.page, {machine, version: requiredEnv('OPD_INSTALL_VERSION')});
          }
          await expectTLSPassthroughRoutes();
        },
      });
    },
  },
  {
    id: 'postgres-deployment-created',
    title: 'create virtual-network postgres deployment',
    description: 'Creates and verifies a Postgres container image deployment on the virtual network.',
    requires: ['worker-enrolled', 'postgres-user-secret-created', 'postgres-password-secret-created'],
    async run(ctx) {
      await createPostgresDeployment(ctx.page);
    },
  },
  {
    id: 'postgres-client-deployment-created',
    title: 'verify cross-deployment virtual networking',
    description: 'Connects a virtual-network client to Postgres through internal DNS and verifies writes and reads.',
    requires: ['postgres-deployment-created'],
    async run(ctx) {
      await createPostgresClientDeployment(ctx.page);
    },
  },
  {
    id: 'postgres-address-client-deployment-created',
    title: 'verify direct virtual address reference',
    description: 'Connects a second virtual-network client to Postgres through an Address-typed environment variable.',
    requires: ['postgres-client-deployment-created'],
    async run(ctx) {
      await createPostgresClientDeployment(ctx.page, {
        name: 'postgresclient-address',
        postgresHost: {type: 'address', name: 'postgres18'},
      });
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
  {
    id: 'opendeploy-net-upgraded-on-worker-1',
    title: 'upgrade opendeploy-net on worker-1',
    description: 'Upgrades worker-1 netproxy and verifies its virtual-network containers remain running.',
    requires: ['postgres-address-client-deployment-created'],
    async run(ctx) {
      const virtualWorkloads = ['nixdockerbuild-virtual', 'rollover-virtual', 'port-forward-virtual', 'postgres18', 'postgresclient', 'postgresclient-address'];
      const restartCounts = new Map();
      for (const name of virtualWorkloads) {
        restartCounts.set(name, await deploymentRestartCount(ctx.page, {name, machine: 'worker-1'}));
      }
      await upgradeOpenDeployNet(ctx.page, {machine: 'worker-1', version: 'v1.0.1'});
      for (const name of virtualWorkloads) {
        await expectDeploymentRunning(ctx.page, {name, machine: 'worker-1'});
        await expectDeploymentRestartCount(ctx.page, {name, machine: 'worker-1', count: restartCounts.get(name)});
      }
    },
  },
];
