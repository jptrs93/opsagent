import crypto from 'node:crypto';
import {
  createAsset,
  createContainerImageDeployment,
  createNixDockerDeployment,
  createSecret,
  deleteDeployment,
  deploymentOutputOccurrenceCount,
  expectDeploymentOutput,
  expectDeploymentOutputOccurrences,
  expectDeploymentRunning,
  NETWORKING_VIRTUAL,
  rotateSecret,
  stopDeployment,
  updateAsset,
  updateNixDockerDeployment,
} from '../helpers/ui.js';

const names = {
  minio: 'pgbackrest-minio',
  postgres: 'pgbackrest-postgres',
  restoredPostgres: 'pgbackrest-postgres-restored',
  client: 'pgbackrest-test-app',
  passwordSecret: 'pgbackrest-postgres-password',
  configAsset: 'pgbackrest-postgres.yaml',
};

const postgresUser = 'pgbackrest_app';
const postgresDatabase = 'pgbackrest_e2e';
const initialPassword = 'pgbackrest-initial-password';
const rotatedPassword = 'pgbackrest-rotated-password';
const minioAccessKey = 'pgbackrest-access-key';
const minioSecretKey = 'pgbackrest-secret-key';
const repositoryCipherPass = 'pgbackrest-repository-cipher-pass';
const bucket = 'pgbackrest-e2e';
const backupCompleted = 'backup command end: completed successfully';

const initialConfig = `settings:
  max_connections: 50
  shared_buffers: 32MB
hba:
  - host all all all scram-sha-256
initdb:
  postgres_user: ${postgresUser}
  postgres_password: \${POSTGRES_PASSWORD}
  postgres_db: ${postgresDatabase}
`;

const backupConfig = `${initialConfig}pgbackrest:
  enabled: true
  stanza: opendeploy-e2e
  repository_cipher_pass: \${PGBACKREST_REPOSITORY_CIPHER_PASS}
  s3:
    host: ${names.minio}.default.internal
    port: '9000'
    bucket: ${bucket}
    region: us-east-1
    uri_style: path
    verify_tls: false
    access_key: \${S3_ACCESS_KEY}
    secret_key: \${S3_SECRET_KEY}
  retention:
    full: 2
    archive: 2
  schedules:
    full: '* * * * *'
    differential: '0 0 1 1 *'
    check: '* * * * *'
  archive:
    push_queue_max: 1GiB
    timeout_seconds: 60
  process_max: 2
  initial_backup: true
  timezone: UTC
`;

const postgresEnv = (restore = false) => ({
  POSTGRES_PASSWORD: {type: 'secret', name: names.passwordSecret},
  S3_ACCESS_KEY: minioAccessKey,
  S3_SECRET_KEY: minioSecretKey,
  PGBACKREST_REPOSITORY_CIPHER_PASS: repositoryCipherPass,
  ...(restore ? {PGBACKREST_RESTORE_ENABLED: 'true'} : {}),
});

const clientEnv = ({host, write, expected, readOnly = false}) => ({
  PGHOST: `${host}.default.internal`,
  PGPORT: '5432',
  PGUSER: postgresUser,
  PGPASSWORD: {type: 'secret', name: names.passwordSecret},
  PGDATABASE: postgresDatabase,
  OPENDEPLOY_E2E_WRITE: write,
  OPENDEPLOY_E2E_EXPECT: expected.join(','),
  OPENDEPLOY_E2E_READ_ONLY: String(readOnly),
});

const createPostgres = async (page, {name, restore = false}) => {
  const image = process.env.OPD_DECLARATIVE_POSTGRES_IMAGE;
  if (!image) throw new Error('OPD_DECLARATIVE_POSTGRES_IMAGE is required');
  await createContainerImageDeployment(page, {
    name,
    machine: 'worker-1',
    image,
    env: postgresEnv(restore),
    dataMountPath: '/var/lib/postgresql',
    networkingMode: NETWORKING_VIRTUAL,
    assetMount: {
      asset: names.configAsset,
      path: '/etc/postgres-supervisor/config.yaml',
    },
  });
  await expectDeploymentRunning(page, name);
};

export const pgBackRestCases = [
  {
    id: 'pgbackrest-resources-created',
    title: 'create independent pgBackRest resources',
    description: 'Creates dedicated credentials, PostgreSQL YAML, and MinIO without reusing earlier E2E resources.',
    requires: [],
    async run(ctx) {
      await createSecret(ctx.page, {name: names.passwordSecret, value: initialPassword});
      await createAsset(ctx.page, {key: names.configAsset, content: initialConfig});
      await createContainerImageDeployment(ctx.page, {
        name: names.minio,
        machine: 'worker-1',
        image: process.env.OPD_MINIO_IMAGE,
        env: {
          MINIO_ROOT_USER: minioAccessKey,
          MINIO_ROOT_PASSWORD: minioSecretKey,
          MINIO_DEFAULT_BUCKETS: bucket,
        },
        networkingMode: NETWORKING_VIRTUAL,
      });
      await expectDeploymentRunning(ctx.page, names.minio);
      await ctx.page.waitForTimeout(8_000);
      await createPostgres(ctx.page, {name: names.postgres});
      await expectDeploymentOutput(ctx.page, names.postgres, ['database system is ready to accept connections']);
    },
  },
  {
    id: 'pgbackrest-initial-data-written',
    title: 'write initial PostgreSQL data',
    description: 'Starts the dedicated Go test app and writes the pre-backup value.',
    requires: ['pgbackrest-resources-created'],
    async run(ctx) {
      await createNixDockerDeployment(ctx.page, {
        name: names.client,
        machine: 'worker-1',
        flake: 'testexamples/postgresclient/flake.nix',
        networkingMode: NETWORKING_VIRTUAL,
        env: clientEnv({host: names.postgres, write: 'before-backup', expected: ['before-backup']}),
        expectedEnv: {},
      });
      await expectDeploymentOutput(ctx.page, names.client, [
        'msg="postgresclient wrote persistent value" value=before-backup',
        'msg="postgresclient persistent row" value=before-backup',
        'msg="postgresclient verified persistent rows" count=1',
      ]);
    },
  },
  {
    id: 'pgbackrest-enabled',
    title: 'enable pgBackRest on existing PostgreSQL data',
    description: 'Creates YAML v2 with MinIO pgBackRest settings and rolls the existing PostgreSQL deployment to it.',
    requires: ['pgbackrest-initial-data-written'],
    async run(ctx) {
      await updateAsset(ctx.page, {key: names.configAsset, content: backupConfig});
      await updateNixDockerDeployment(ctx.page, {
        name: names.postgres,
        machine: 'worker-1',
        assetMount: {asset: names.configAsset, path: '/etc/postgres-supervisor/config.yaml'},
      });
      await expectDeploymentRunning(ctx.page, names.postgres);
      await expectDeploymentOutputOccurrences(ctx.page, names.postgres, backupCompleted, 1);
    },
  },
  {
    id: 'pgbackrest-post-backup-data-replicated',
    title: 'write and back up post-configuration data',
    description: 'Restarts the Go app to write another value, then waits for a subsequent successful full backup.',
    requires: ['pgbackrest-enabled'],
    async run(ctx) {
      await updateNixDockerDeployment(ctx.page, {
        name: names.client,
        machine: 'worker-1',
        env: clientEnv({
          host: names.postgres,
          write: 'after-backup',
          expected: ['before-backup', 'after-backup'],
        }),
      });
      await expectDeploymentRunning(ctx.page, names.client);
      await expectDeploymentOutput(ctx.page, names.client, [
        'msg="postgresclient wrote persistent value" value=after-backup',
        'msg="postgresclient persistent row" value=before-backup',
        'msg="postgresclient persistent row" value=after-backup',
        'msg="postgresclient verified persistent rows" count=2',
      ]);
      const completedBackupsAfterWrite = await deploymentOutputOccurrenceCount(ctx.page, names.postgres, backupCompleted);
      await expectDeploymentOutputOccurrences(ctx.page, names.postgres, backupCompleted, completedBackupsAfterWrite + 1);
    },
  },
  {
    id: 'pgbackrest-original-postgres-deleted',
    title: 'stop and delete original PostgreSQL',
    description: 'Stops and deletes the original deployment only after the post-write backup has completed.',
    requires: ['pgbackrest-post-backup-data-replicated'],
    async run(ctx) {
      await stopDeployment(ctx.page, {name: names.postgres, machine: 'worker-1'});
      await deleteDeployment(ctx.page, {name: names.postgres, machine: 'worker-1'});
    },
  },
  {
    id: 'pgbackrest-postgres-restored',
    title: 'restore PostgreSQL into a fresh deployment',
    description: 'Creates a new deployment and empty volume, then restores the latest pgBackRest backup from MinIO.',
    requires: ['pgbackrest-original-postgres-deleted'],
    async run(ctx) {
      await createPostgres(ctx.page, {name: names.restoredPostgres, restore: true});
      await expectDeploymentOutput(ctx.page, names.restoredPostgres, [
        'restore command end: completed successfully',
        'database system is ready to accept connections',
      ]);
      await updateNixDockerDeployment(ctx.page, {
        name: names.client,
        machine: 'worker-1',
        env: clientEnv({
          host: names.restoredPostgres,
          write: 'after-backup',
          expected: ['before-backup', 'after-backup'],
          readOnly: true,
        }),
      });
      await expectDeploymentRunning(ctx.page, names.client);
      await expectDeploymentOutput(ctx.page, names.client, [
        `msg="postgresclient starting" host=${names.restoredPostgres}.default.internal`,
        'msg="postgresclient persistent row" value=before-backup',
        'msg="postgresclient persistent row" value=after-backup',
        'msg="postgresclient verified persistent rows" count=2',
      ]);

      // The restore gate must be disabled after the first successful start so
      // later config and secret rotations can restart the restored instance.
      await updateNixDockerDeployment(ctx.page, {
        name: names.restoredPostgres,
        machine: 'worker-1',
        env: {PGBACKREST_RESTORE_ENABLED: 'false'},
      });
      await expectDeploymentRunning(ctx.page, names.restoredPostgres);
    },
  },
  {
    id: 'pgbackrest-password-rotated',
    title: 'rotate PostgreSQL password across referencing deployments',
    description: 'Uses the update-referencing-deployments toggle and verifies PostgreSQL and the Go app restart with the new credential.',
    requires: ['pgbackrest-postgres-restored'],
    async run(ctx) {
      await rotateSecret(ctx.page, {
        name: names.passwordSecret,
        value: rotatedPassword,
        referencingDeployments: 2,
      });
      await expectDeploymentRunning(ctx.page, names.restoredPostgres);
      await expectDeploymentRunning(ctx.page, names.client);
      const fingerprint = crypto.createHash('sha256').update(rotatedPassword).digest('hex').slice(0, 12);
      await expectDeploymentOutput(ctx.page, names.client, [
        `msg="postgresclient connected credential" sha256=${fingerprint}`,
        'msg="postgresclient persistent row" value=before-backup',
        'msg="postgresclient persistent row" value=after-backup',
        'msg="postgresclient verified persistent rows" count=2',
      ]);
    },
  },
];
