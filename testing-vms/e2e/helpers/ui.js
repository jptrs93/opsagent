import {expect, test} from '@playwright/test';
import {Buffer} from 'node:buffer';
import crypto from 'node:crypto';
import dns from 'node:dns';
import fs from 'node:fs';
import https from 'node:https';
import path from 'node:path';

const LONG_UI_TIMEOUT = 15_000;
const OPTIONAL_VALIDATION_TIMEOUT = LONG_UI_TIMEOUT;
const VALIDATE_REQUEST_TIMEOUT = 5_000;
const LOG_OUTPUT_TIMEOUT = 120_000;
const LOG_OUTPUT_POLL_TIMEOUT = 1_500;
const DEPLOYMENT_RUNNING_TIMEOUT = 180_000;
const PREPARATION_TIMEOUT = 180_000;
const RUNNER_START_TIMEOUT = 60_000;
const RESTART_TIMEOUT = 120_000;
const UPGRADE_TIMEOUT = 180_000;
const RELEASE_OPTIONS_TIMEOUT = 60_000;
const BACKUP_RESTORE_TIMEOUT = 120_000;
const ASSET_UPLOAD_TIMEOUT = 120_000;
const MINIO_BUCKET_SETUP_DELAY = 8_000;
const STABLE_CHECK_DELAY = 200;

const BACKUP_RESTORE_DEFAULTS = {
  minioDeploymentName: 'minio-backup',
  minioImage: process.env.OPD_MINIO_IMAGE || 'docker.io/bitnamilegacy/minio:latest',
  minioRootUserSecret: 'minio-root-user',
  minioRootPasswordSecret: 'minio-root-password',
  minioRootUser: 'opendeploy',
  minioRootPassword: 'opendeploy-minio-password',
  bucket: 'opendeploy-e2e-backup',
  path: 'opendeploy/e2e-primary',
  largeAssetPath: 'opendeploy/e2e-assets',
  region: 'us-east-1',
  endpoint: process.env.OPD_BACKEND_S3_ENDPOINT || 'http://opendeploy-secondary:9000',
  statePath: process.env.OPD_BACKUP_RESTORE_STATE || '/e2e/test-results/backup-restore.env',
};

export const NETWORKING_HOST = '2';
export const NETWORKING_VIRTUAL = '1';
export const PORT_FORWARD_TCP = '1';
export const PORT_FORWARD_UDP = '2';
export const UPGRADE_RECREATE = '1';
export const UPGRADE_ROLLOVER = '2';

let e2eObjectStorageReady = false;

async function step(name, fn) {
  return test.step(name, fn);
}

export async function bootstrapFirstUser(page, {username = 'E2E Operator', password = process.env.OPD_SETUP_PASSWORD || 'opendeploy-setup'} = {}) {
  await page.goto('/bootstrap');
  await expect(page.getByRole('heading', {name: 'First time setup'})).toBeVisible();
  await byTestId(page, 'bootstrap-username-input', page.getByPlaceholder('Your name')).fill(username);
  await byTestId(page, 'bootstrap-password-input', page.getByPlaceholder('Master password')).fill(password);
  await byTestId(page, 'bootstrap-authenticate-button', page.getByRole('button', {name: 'Authenticate'})).click();
  await expect(page.getByText('Authenticated. Now register a passkey for future logins.')).toBeVisible();
  await byTestId(page, 'bootstrap-register-passkey-button', page.getByRole('button', {name: 'Register passkey'})).click();
  await expect(byTestId(page, 'add-deployment-button', page.getByRole('button', {name: 'Add deployment'}))).toBeVisible();
}

export async function signOutAndLoginWithPasskey(page) {
  await byTestId(page, 'nav-sign-out', page.getByText('Sign out')).click();
  const loginButton = byTestId(page, 'login-passkey-button', page.getByRole('button', {name: 'Sign in with passkey'}));
  await expect(loginButton).toBeVisible();
  await loginButton.click();
  await expect(byTestId(page, 'add-deployment-button', page.getByRole('button', {name: 'Add deployment'}))).toBeVisible();
}

export async function configureGithubToken(page, token) {
  if (!token) return;
  await byTestId(page, 'nav-settings', page.getByText('Settings')).click();
  const row = page.getByRole('row', {name: /GitHub token/});
  await expect(row).toBeVisible({timeout: LONG_UI_TIMEOUT});
  await row.getByRole('button', {name: 'Create secret'}).click();

  const dialog = page.locator('.fixed').filter({hasText: 'Create a secret, then reference it from this setting.'});
  await expect(dialog).toBeVisible();
  await dialog.getByLabel('Secret name').fill('opendeploy.config.github_token');
  await dialog.getByLabel('Secret value').fill(token);
  await dialog.getByRole('button', {name: 'Create secret'}).click();

  await expect(dialog).toBeHidden({timeout: LONG_UI_TIMEOUT});
  await expect(page.getByText('Unsaved changes')).toBeVisible({timeout: LONG_UI_TIMEOUT});
  await page.getByRole('button', {name: 'Save changes'}).click();
  await expect(page.getByText('Unsaved changes')).toBeHidden({timeout: LONG_UI_TIMEOUT});
}

export async function expectOpenDeployLogs(page) {
  await byTestId(page, 'nav-logs', page.getByText('Logs')).click();
  const spaceSelect = page.getByTestId('logs-space-select');
  await expect(spaceSelect).toBeVisible();
  await expect.poll(async () => {
    return await spaceSelect.locator('option').evaluateAll(options => options.map(o => o.value));
  }, {message: 'expected default space option', timeout: LONG_UI_TIMEOUT}).toContain('0');
  await spaceSelect.selectOption('0');

  const deploymentSelect = page.getByTestId('logs-deployment-select');
  await expect.poll(async () => {
    const options = await deploymentSelect.locator('option').evaluateAll(optionNodes =>
      optionNodes.map(o => ({value: o.value, text: o.textContent || ''})),
    );
    return options.find(o => o.value && o.text.trim().endsWith(' / opendeploy'))?.value || '';
  }, {message: 'expected opendeploy deployment option', timeout: LONG_UI_TIMEOUT}).not.toBe('');
  const deploymentValue = await deploymentSelect.locator('option').evaluateAll(options => {
    const match = options.find(o => o.value && (o.textContent || '').trim().endsWith(' / opendeploy'));
    return match?.value || '';
  });
  await deploymentSelect.selectOption(deploymentValue);
  await page.getByTestId('logs-search-button').click();
  await expectOutputText(page, 'opendeploy starting primary');
}

export async function acceptWaitingWorker(page, {machineID, workerName, expectNoPending = false} = {}) {
  if (!machineID) throw new Error('worker machine ID is required');
  if (!workerName) throw new Error('worker name is required');
  await byTestId(page, 'nav-cluster', page.getByText('Machines')).click();
  await expect(page.getByRole('heading', {name: 'Enrollment requests'})).toBeVisible();

  const requestRow = page.locator('tr').filter({hasText: machineID}).filter({hasText: 'Accept'});
  await expect(requestRow).toBeVisible({timeout: LONG_UI_TIMEOUT});
  await byTestId(requestRow, 'enrollment-worker-name-input', requestRow.getByRole('textbox')).fill(workerName);
  await byTestId(requestRow, 'enrollment-accept-button', requestRow.getByRole('button', {name: 'Accept'})).click();

  await expect(requestRow).toBeHidden({timeout: LONG_UI_TIMEOUT});
  if (expectNoPending) await expect(page.getByText('No pending enrollment requests.')).toBeVisible({timeout: LONG_UI_TIMEOUT});
  const workerRow = (await clusterMachineRow(page, workerName)).filter({hasText: 'secondary'});
  await expect(workerRow).toContainText('connected', {timeout: LONG_UI_TIMEOUT});
}

export async function createNixDockerDeployment(page, {
  name = 'nixdockerbuild1',
  machine = 'worker-1',
  repo = 'github.com/jptrs93/opsagent',
  flake = 'testexamples/nixdockerbuild1/flake.nix',
  env = {
    OPENDEPLOY_E2E_MESSAGE: 'hello-from-playwright',
    OPENDEPLOY_E2E_COLOR: 'blue',
  },
  expectedEnv = env,
  assetMount,
  portForwarding = [],
  ingress,
  upgradeStrategy = UPGRADE_RECREATE,
  readinessTimeoutSeconds = 600,
  expectDefaultDockerImage = false,
  verifyLogs = true,
  networkingMode = NETWORKING_HOST,
} = {}) {
  await step(`open deployment dialog ${name}`, async () => {
    await byTestId(page, 'nav-status', page.getByText('Deployments')).click();
    await byTestId(page, 'add-deployment-button', page.getByRole('button', {name: 'Add deployment'})).click();
  });

  const dialog = byTestId(page, 'create-deployment-dialog', page.locator('.fixed.inset-0.z-50').filter({hasText: 'Deployment identity'}).last());
  await expect(dialog).toBeVisible();

  await step(`fill deployment identity ${name}`, async () => {
    await byTestId(dialog, 'deployment-name-input', textField(dialog, 'Name')).fill(name);
    await selectDeploymentNode(dialog, machine);
    await setDeploymentNetworkingMode(dialog, networkingMode);
  });
  const sourceTypeSelect = byTestId(dialog, 'deployment-source-type-select', selectField(dialog, 'Source type'));
  if (expectDefaultDockerImage) {
    await expect(sourceTypeSelect).toHaveValue('containerImage');
  }
  await step(`select nix docker source ${name}`, () => sourceTypeSelect.selectOption('nixDockerBuild'));
  const validateRequests = trackRepoValidateRequests(page);
  const repoInput = byTestId(dialog, 'deployment-repo-input', textField(dialog, 'Repository'));
  const flakeInput = byTestId(dialog, 'deployment-flake-input', textField(dialog, 'Path to flake.nix'));

  try {
    await step(`validate repository ${name}`, async () => {
      await repoInput.fill(repo);
      await flakeInput.click();
      await validateRequests.expectCount(1, 'expected one validate call after setting repository URL');
      await validateRequests.expectResponseCount(1, 'expected the repository validation response');
    });

    await step(`validate flake path ${name}`, async () => {
      await flakeInput.fill(flake);
      await flakeInput.blur();
      await validateRequests.expectCount(2, 'expected a second validate call after setting flake path');
      await validateRequests.expectResponseCount(2, 'expected the flake validation response');
      await expectPathValidation(dialog);
    });

    const commitSelect = selectField(dialog, 'Commit');
    await step(`wait for commit options ${name}`, () => expect(commitSelect).not.toHaveValue('', {timeout: LONG_UI_TIMEOUT}));

    await step(`refresh source versions ${name}`, async () => {
      const refreshButton = dialog.getByRole('button', {name: 'Refresh'});
      await expect(refreshButton).toBeEnabled();
      await refreshButton.click();
      await validateRequests.expectCount(3, 'expected a validate call after refreshing versions');
      await validateRequests.expectResponseCount(3, 'expected the refreshed version response');
      await expectPathValidation(dialog);
      await expect(commitSelect).not.toHaveValue('', {timeout: LONG_UI_TIMEOUT});
      await expect(dialog.getByText('d is not a function')).toHaveCount(0);
    });

    await step(`verify source validation settled ${name}`, () => validateRequests.expectStableCount(3, 'expected only the repo, flake, and refresh validate calls'));
  } finally {
    validateRequests.stop();
  }

  await step(`configure deployment inputs ${name}`, async () => {
    await setDeploymentUpgradeStrategy(dialog, {strategy: upgradeStrategy, readinessTimeoutSeconds});
    await setDeploymentPortForwarding(dialog, portForwarding);
    if (ingress !== undefined) await setDeploymentIngress(dialog, ingress);
    await setDeploymentEnvVars(dialog, env);
    if (assetMount) await setDeploymentAssetMount(dialog, assetMount);
  });
  await step(`submit deployment ${name}`, async () => {
    const submit = byTestId(dialog, 'create-deployment-submit', dialog.getByRole('button', {name: 'Create'}));
    await expect(dialog.getByTestId('create-validation-reason')).toHaveCount(0);
    await expect(submit).toBeEnabled({timeout: LONG_UI_TIMEOUT});
    const createResponse = page.waitForResponse(response => {
      const request = response.request();
      return request.method() === 'POST' && new URL(request.url()).pathname === '/v1/deployment/create';
    }, {timeout: LONG_UI_TIMEOUT});
    await submit.click();
    expect((await createResponse).ok()).toBe(true);
    await expect(dialog).toBeHidden({timeout: LONG_UI_TIMEOUT});
  });

  let row = byTestId(page, `deployment-row-${name}`, page.locator('tr').filter({hasText: name}).filter({hasText: machine}));
  await step(`wait for deployment row ${name}`, () => expect(row).toBeVisible({timeout: LONG_UI_TIMEOUT}));
  if (!verifyLogs) return;
  await step(`wait for deployment running ${name}`, () => expectDeploymentRunning(page, name));
  row = byTestId(page, `deployment-row-${name}`, page.locator('tr').filter({hasText: name}).filter({hasText: machine}));
  await step(`open deployment logs ${name}`, () => openDeploymentLogsSearch(page, row));
  for (const [key, value] of Object.entries(expectedEnv || {})) {
    await step(`wait for output ${name} ${key}`, () => expectOutputText(page, `nixdockerbuild1 env ${key}=${value}`));
  }
}

export async function updateNixDockerDeployment(page, {
  name,
  machine = 'worker-1',
  env = {},
  portForwarding = [],
  ingress,
  upgradeStrategy = UPGRADE_RECREATE,
  readinessTimeoutSeconds = 600,
} = {}) {
  await step(`open update dialog ${name}`, async () => {
    await byTestId(page, 'nav-status', page.getByText('Deployments')).click();
    const row = deploymentRow(page, {name, machine});
    await expect(row).toBeVisible({timeout: LONG_UI_TIMEOUT});
    await row.getByRole('button', {name: 'Update'}).click();
  });

  const dialog = page.locator('.fixed.inset-0.z-50').filter({hasText: 'Update deployment'}).last();
  await expect(dialog).toBeVisible();

  await step(`configure update ${name}`, async () => {
    await setDeploymentUpgradeStrategy(dialog, {strategy: upgradeStrategy, readinessTimeoutSeconds});
    await setDeploymentPortForwarding(dialog, portForwarding);
    if (ingress !== undefined) await setDeploymentIngress(dialog, ingress);
    await setDeploymentEnvVars(dialog, env);
  });

  await step(`submit update ${name}`, async () => {
    const submit = dialog.getByRole('button', {name: 'Update deployment'});
    await expect(submit).toBeEnabled({timeout: LONG_UI_TIMEOUT});
    await submit.click();
    await expect(dialog).toBeHidden({timeout: LONG_UI_TIMEOUT});
  });
}

export async function createNixDockerCrasherDeployment(page, {
  name = 'nixdockercrasher',
  machine = 'worker-1',
} = {}) {
  await step(`create crasher deployment ${name}`, () => createNixDockerDeployment(page, {
    name,
    machine,
    flake: 'testexamples/nixdockercrasher/flake.nix',
    env: {},
    expectedEnv: {},
  }));
  await step(`wait for crasher restarts ${name}`, () => expectDeploymentRestartCount(page, name, 3));
  await step(`verify crasher output ${name}`, () => expectDeploymentOutput(page, name, [
    'nixdockercrasher wrote crash number=1',
    'panic: nixdockercrasher panic crash count=1',
    'nixdockercrasher wrote crash number=2',
    'panic: nixdockercrasher panic crash count=2',
    'nixdockercrasher wrote crash number=3',
    'panic: nixdockercrasher panic crash count=3',
    'nixdockercrasher crash count=3; staying alive',
  ]));
}

export async function upgradeOpenDeployAgents(page, {version, workerNames = ['worker-1', 'worker-2']} = {}) {
  if (!version) throw new Error('upgrade version is required');
  for (const workerName of workerNames) {
    await upgradeOpenDeployAgent(page, {machine: workerName, version});
    await expectOpenDeployAgentVersion(page, {machine: workerName, version});
    await expectMachineConnected(page, workerName);
  }
  await upgradeOpenDeployAgent(page, {machine: 'primary', version});
  await waitForHealthyApp(page);
  await waitForLoadableApp(page);
  await expectAuthenticatedDeploymentsPage(page);
  await expectOpenDeployAgentVersion(page, {machine: 'primary', version});
  for (const workerName of workerNames) await expectOpenDeployAgentVersion(page, {machine: workerName, version});
}

async function waitForLoadableApp(page) {
  const appReady = byTestId(page, 'nav-status', page.getByText('Deployments'))
    .or(byTestId(page, 'login-passkey-button', page.getByRole('button', {name: 'Sign in with passkey'})))
    .first();
  await expect.poll(async () => {
    try {
      const response = await page.goto('/', {waitUntil: 'domcontentloaded', timeout: 5_000});
      return Boolean(response?.ok() && await appReady.isVisible());
    } catch {
      return false;
    }
  }, {message: 'expected OpenDeploy web UI to become loadable', timeout: UPGRADE_TIMEOUT}).toBe(true);
}

async function expectAuthenticatedDeploymentsPage(page) {
  const addButton = byTestId(page, 'add-deployment-button', page.getByRole('button', {name: 'Add deployment'}));
  try {
    await expect(addButton).toBeVisible({timeout: LONG_UI_TIMEOUT});
    return;
  } catch (err) {
    const loginButton = byTestId(page, 'login-passkey-button', page.getByRole('button', {name: 'Sign in with passkey'}));
    if (!(await loginButton.isVisible().catch(() => false))) throw err;
    await loginButton.click();
    await expect(addButton).toBeVisible({timeout: LONG_UI_TIMEOUT});
  }
}

export async function createPostgresDeployment(page, {
  name = 'postgres18',
  machine = 'worker-1',
} = {}) {
  await createContainerImageDeployment(page, {
    name,
    machine,
    image: process.env.OPD_POSTGRES_IMAGE || 'docker.io/library/postgres:18',
    env: {
      POSTGRES_USER: {type: 'secret', name: 'postgres'},
      POSTGRES_PASSWORD: {type: 'secret', name: 'postgrespass'},
      POSTGRES_DB: 'postgres',
    },
    dataMountPath: '/var/lib/postgresql',
  });
  await expectDeploymentRunning(page, name);
  await expectDeploymentOutput(page, name, ['database system is ready to accept connections']);
}

export async function createPostgresClientDeployment(page, {
  name = 'postgresclient',
  machine = 'worker-1',
} = {}) {
  await createNixDockerDeployment(page, {
    name,
    machine,
    flake: 'testexamples/postgresclient/flake.nix',
    env: {
      PGHOST: '127.0.0.1',
      PGPORT: '5432',
      PGUSER: {type: 'secret', name: 'postgres'},
      PGPASSWORD: {type: 'secret', name: 'postgrespass'},
      PGDATABASE: 'postgres',
    },
    expectedEnv: {},
  });
  await expectDeploymentOutput(page, name, [
    'msg="postgresclient row" id=1 name=alpha',
    'msg="postgresclient row" id=2 name=bravo',
    'msg="postgresclient row" id=3 name=charlie',
    'msg="postgresclient verified rows" count=3',
  ]);
}

export async function runBackupRestoreSetup(page, opts = {}) {
  const cfg = {...BACKUP_RESTORE_DEFAULTS, ...opts};

  await ensureE2EObjectStorage(page, cfg);

  await configureBackupSettings(page, cfg);
  const recoveryCode = await generateRecoveryCode(page);
  writeBackupRestoreState(cfg, recoveryCode);
  await waitForBackupReplicationInSync(page);
}

async function waitForBackupReplicationInSync(page) {
  await step('wait for backup replication in sync', async () => {
    await byTestId(page, 'nav-cluster', page.getByText('Machines')).click();
    const status = byTestId(page, 'backup-replication-status', page.getByText(/in sync|syncing|error|not configured|not running/));
    await expect(status).toContainText('in sync', {timeout: BACKUP_RESTORE_TIMEOUT});
  });
}

export async function configureLargeAssetStorage(page, opts = {}) {
  const cfg = {...BACKUP_RESTORE_DEFAULTS, ...opts};

  await step('ensure object storage deployment', () => ensureE2EObjectStorage(page, cfg));
  await step('open settings for large assets', async () => {
    await byTestId(page, 'nav-settings', page.getByText('Settings')).click();
    await expect(settingRow(page, 'Backup enabled')).toBeVisible({timeout: LONG_UI_TIMEOUT});
  });
  await step('enable backup storage', async () => {
    await setSettingBool(page, 'Backup enabled', true);
    await expect(settingRow(page, 'Large asset S3 path')).toBeVisible({timeout: LONG_UI_TIMEOUT});
    await expect(settingRow(page, 'Use separate large assets S3')).toBeVisible({timeout: LONG_UI_TIMEOUT});
    await expect(settingRow(page, 'Large asset S3 access key ID')).toBeHidden();
  });
  await step('fill shared backup and large asset S3 settings', async () => {
    await setSettingText(page, 'Backup S3 access key ID', cfg.minioRootUser);
    await setSettingSecret(page, 'Backup S3 secret access key', cfg.minioRootPasswordSecret);
    await setSettingText(page, 'Backup S3 bucket', cfg.bucket);
    await setSettingText(page, 'Backup S3 path', cfg.path);
    await setSettingText(page, 'Backup S3 region', cfg.region);
    await setSettingText(page, 'Backup S3 endpoint', cfg.endpoint);
    await setSettingText(page, 'Large asset S3 path', cfg.largeAssetPath);
  });
  await step('save large asset S3 settings', async () => {
    await page.getByRole('button', {name: 'Save changes'}).click();
    await expect(page.getByText('Unsaved changes')).toBeHidden({timeout: LONG_UI_TIMEOUT});
  });
  await waitForBackupReplicationInSync(page);
}

export async function expectBackupStorageDisabled(page) {
  await step('verify backup storage is disabled', async () => {
    await byTestId(page, 'nav-cluster', page.getByText('Machines')).click();
    const status = byTestId(page, 'backup-replication-status', page.getByText(/not configured|not running|syncing|in sync|error/));
    await expect(status).toContainText('not configured', {timeout: LONG_UI_TIMEOUT});
  });
}

async function ensureE2EObjectStorage(page, cfg) {
  if (e2eObjectStorageReady) return;

  await step('create minio root user secret', () => createSecret(page, {
    name: cfg.minioRootUserSecret,
    value: cfg.minioRootUser,
  }));
  await step('create minio root password secret', () => createSecret(page, {
    name: cfg.minioRootPasswordSecret,
    value: cfg.minioRootPassword,
  }));
  await step('create minio deployment', () => createContainerImageDeployment(page, {
    name: cfg.minioDeploymentName,
    machine: 'worker-1',
    image: cfg.minioImage,
    env: {
      MINIO_ROOT_USER: {type: 'secret', name: cfg.minioRootUserSecret},
      MINIO_ROOT_PASSWORD: {type: 'secret', name: cfg.minioRootPasswordSecret},
      MINIO_DEFAULT_BUCKETS: cfg.bucket,
    },
  }));
  await step('wait for minio deployment running', () => expectDeploymentRunning(page, cfg.minioDeploymentName));
  // The Docker Playwright runner only needs primary UI access. We can enhance
  // this later with a primary-side readiness API if deployment-running + grace
  // delay is not enough.
  await step('wait for minio bucket setup grace period', () => page.waitForTimeout(MINIO_BUCKET_SETUP_DELAY));
  e2eObjectStorageReady = true;
}

export async function createConfig(page, {name, value} = {}) {
  await byTestId(page, 'nav-secrets', page.getByText('Secrets / Configs')).click();
  await page.getByRole('button', {name: 'Add config'}).click();

  const row = editableSecretConfigRow(page, 'Config');
  await row.locator('input').nth(0).fill(name);
  await row.locator('input').nth(1).fill(value);
  await row.getByRole('button', {name: 'Save'}).click();
  await expect(row.getByRole('button', {name: 'Save'})).toBeHidden({timeout: LONG_UI_TIMEOUT});
  await expect(page.getByRole('row', {name: new RegExp(escapeRegExp(name))})).toBeVisible();
}

export async function createSecret(page, {name, value} = {}) {
  await byTestId(page, 'nav-secrets', page.getByText('Secrets / Configs')).click();
  await page.getByRole('button', {name: 'Add secret'}).click();

  const row = editableSecretConfigRow(page, 'Secret');
  await row.locator('input').nth(0).fill(name);
  await row.locator('input').nth(1).fill(value);
  await row.getByRole('button', {name: 'Save'}).click();
  await expect(row.getByRole('button', {name: 'Save'})).toBeHidden({timeout: LONG_UI_TIMEOUT});
  await expect(page.getByRole('row', {name: new RegExp(escapeRegExp(name))})).toBeVisible();
}

export async function expectReferenceUsage(page, {
  resourceType,
  resourceName,
  deploymentName,
  space = 'default',
  machine = 'worker-1',
} = {}) {
  const navKey = resourceType === 'asset' ? 'assets' : 'secrets';
  const navLabel = resourceType === 'asset' ? 'Assets' : 'Secrets / Configs';
  await byTestId(page, `nav-${navKey}`, page.getByText(navLabel)).click();

  const usageButton = page.getByRole('button', {name: `Show usage for ${resourceType} ${resourceName}`, exact: true});
  await expect(usageButton).toHaveText('1', {timeout: LONG_UI_TIMEOUT});
  await usageButton.click();

  const overlay = byTestId(page, 'reference-usage-overlay', page.locator('.fixed.inset-0.z-50').filter({hasText: 'In use by'}));
  await expect(overlay).toBeVisible();
  const deploymentRow = overlay.locator('[data-testid^="reference-usage-deployment-"]').filter({hasText: deploymentName});
  await expect(deploymentRow).toContainText(space);
  await expect(deploymentRow).toContainText(deploymentName);
  await expect(deploymentRow).toContainText(machine);
  await overlay.getByRole('button', {name: 'Close'}).click();
  await expect(overlay).toBeHidden();
}

function editableSecretConfigRow(page, typeLabel) {
  return page.locator('tbody tr')
    .filter({hasText: typeLabel})
    .filter({has: page.getByRole('button', {name: 'Save'})})
    .first();
}

async function configureBackupSettings(page, cfg) {
  await byTestId(page, 'nav-settings', page.getByText('Settings')).click();
  await expect(settingRow(page, 'Backup enabled')).toBeVisible({timeout: LONG_UI_TIMEOUT});
  await setSettingBool(page, 'Backup enabled', true);
  await expect(settingRow(page, 'Backup S3 access key ID')).toBeVisible({timeout: LONG_UI_TIMEOUT});

  await setSettingText(page, 'Backup S3 access key ID', cfg.minioRootUser);
  await setSettingSecret(page, 'Backup S3 secret access key', cfg.minioRootPasswordSecret);
  await setSettingText(page, 'Backup S3 bucket', cfg.bucket);
  await setSettingText(page, 'Backup S3 path', cfg.path);
  await setSettingText(page, 'Backup S3 region', cfg.region);
  await setSettingText(page, 'Backup S3 endpoint', cfg.endpoint);

  const saveButton = page.getByRole('button', {name: 'Save changes'});
  if (await saveButton.isVisible()) {
    await saveButton.click();
    await expect(page.getByText('Unsaved changes')).toBeHidden({timeout: LONG_UI_TIMEOUT});
  }
}

async function generateRecoveryCode(page) {
  await byTestId(page, 'nav-settings', page.getByText('Settings')).click();
  const button = page.getByRole('button', {name: /Generate recovery code|Regenerate recovery code/});
  await expect(button).toBeVisible({timeout: LONG_UI_TIMEOUT});
  await button.click();

  const card = page.locator('.card').filter({hasText: 'Save your recovery code'}).last();
  await expect(card).toBeVisible({timeout: LONG_UI_TIMEOUT});
  const code = ((await card.locator('pre').textContent()) || '').trim();
  expect(code).not.toBe('');
  await card.getByRole('button', {name: "I've saved it"}).click();
  await expect(card).toBeHidden({timeout: LONG_UI_TIMEOUT});
  return code;
}

async function setSettingText(page, label, value) {
  const row = settingRow(page, label);
  await row.getByRole('textbox').fill(value);
}

async function setSettingSecret(page, label, secretName) {
  const row = settingRow(page, label);
  const picker = row.getByRole('textbox');
  await picker.fill(secretName);
  await expect(row.locator('li').filter({hasText: secretName}).first()).toBeVisible({timeout: LONG_UI_TIMEOUT});
  await picker.press('Enter');
  await expect(picker).toHaveValue(versionedReferenceValue(secretName), {timeout: LONG_UI_TIMEOUT});
}

async function setSettingBool(page, label, enabled) {
  const row = settingRow(page, label);
  const checkbox = row.locator('input[type="checkbox"]');
  if (await checkbox.isChecked() !== enabled) {
    await checkbox.setChecked(enabled, {force: true});
  }
  if (enabled) await expect(checkbox).toBeChecked({timeout: LONG_UI_TIMEOUT});
  else await expect(checkbox).not.toBeChecked({timeout: LONG_UI_TIMEOUT});
}

function settingRow(page, label) {
  return page.getByText(label, {exact: true})
    .locator('xpath=ancestor::div[contains(@class, "sm:flex-row")][1]');
}

function versionedReferenceValue(name) {
  const escapedName = name.replace(/[.*+?^${}()|[\]\\]/g, '\\$&');
  return new RegExp(`^${escapedName}( v\\d+)?$`);
}

async function waitForHTTPReady(url, label) {
  await expect.poll(async () => {
    try {
      const res = await fetch(url);
      return res.ok;
    } catch {
      return false;
    }
  }, {message: `expected ${label} to become ready`, timeout: BACKUP_RESTORE_TIMEOUT}).toBe(true);
}

function writeBackupRestoreState(cfg, recoveryCode) {
  fs.mkdirSync(path.dirname(cfg.statePath), {recursive: true});
  const values = {
    OPD_RESTORE_S3_ACCESS_KEY_ID: cfg.minioRootUser,
    OPD_RESTORE_S3_SECRET_ACCESS_KEY: cfg.minioRootPassword,
    OPD_RESTORE_S3_BUCKET: cfg.bucket,
    OPD_RESTORE_S3_PATH: cfg.path,
    OPD_RESTORE_S3_REGION: cfg.region,
    OPD_RESTORE_S3_ENDPOINT: cfg.endpoint,
    OPD_RESTORE_RECOVERY_CODE: recoveryCode,
  };
  const args = [
    '--restore-backup true',
    `--restore-s3-access-key-id ${shellQuote(values.OPD_RESTORE_S3_ACCESS_KEY_ID)}`,
    `--restore-s3-secret-access-key ${shellQuote(values.OPD_RESTORE_S3_SECRET_ACCESS_KEY)}`,
    `--restore-s3-bucket ${shellQuote(values.OPD_RESTORE_S3_BUCKET)}`,
    `--restore-s3-path ${shellQuote(values.OPD_RESTORE_S3_PATH)}`,
    `--restore-s3-region ${shellQuote(values.OPD_RESTORE_S3_REGION)}`,
    `--restore-s3-endpoint ${shellQuote(values.OPD_RESTORE_S3_ENDPOINT)}`,
    `--recovery-code ${shellQuote(values.OPD_RESTORE_RECOVERY_CODE)}`,
  ].join(' ');
  const lines = [
    '# Generated by the optional OPD_BACKUP_RESTORE E2E extension.',
    ...Object.entries(values).map(([key, value]) => `${key}=${shellQuote(value)}`),
    `OPD_RESTORE_INSTALL_ARGS=${shellQuote(args)}`,
    '',
  ];
  fs.writeFileSync(cfg.statePath, lines.join('\n'));
  console.log(`[opendeploy-e2e] backup restore args: ${args}`);
}

function shellQuote(value) {
  return `'${String(value).replaceAll("'", `'\\''`)}'`;
}

async function createContainerImageDeployment(page, {
  name,
  machine,
  image,
  env = {},
  dataMountPath = '',
  networkingMode = NETWORKING_HOST,
  portForwarding = [],
} = {}) {
  await step(`open container deployment dialog ${name}`, async () => {
    await byTestId(page, 'nav-status', page.getByText('Deployments')).click();
    await byTestId(page, 'add-deployment-button', page.getByRole('button', {name: 'Add deployment'})).click();
  });

  const dialog = byTestId(page, 'create-deployment-dialog', page.locator('.fixed.inset-0.z-50').filter({hasText: 'Deployment identity'}).last());
  await expect(dialog).toBeVisible();

  await step(`fill container deployment ${name}`, async () => {
    await byTestId(dialog, 'deployment-name-input', textField(dialog, 'Name')).fill(name);
    await selectDeploymentNode(dialog, machine);
    await setDeploymentNetworkingMode(dialog, networkingMode);
    await setDeploymentPortForwarding(dialog, portForwarding);
    await byTestId(dialog, 'deployment-source-type-select', selectField(dialog, 'Source type')).selectOption('containerImage');
    await byTestId(dialog, 'deployment-container-image-input', textField(dialog, 'Image')).fill(image);
    await setDeploymentEnvVars(dialog, env);
    if (dataMountPath) await setDeploymentDataMountPath(dialog, dataMountPath);
  });
  const submit = byTestId(dialog, 'create-deployment-submit', dialog.getByRole('button', {name: 'Create'}));
  await step(`submit container deployment ${name}`, async () => {
    await expect(submit).toBeEnabled({timeout: LONG_UI_TIMEOUT});
    await submit.click();
  });

  const row = deploymentRow(page, {name, machine});
  await step(`wait for container deployment row ${name}`, () => expect(row).toBeVisible({timeout: LONG_UI_TIMEOUT}));
}

export async function createAsset(page, {key, content} = {}) {
  await byTestId(page, 'nav-assets', page.getByText('Assets')).click();
  await expect(page.getByPlaceholder('Search assets')).toBeVisible();
  await page.getByRole('button', {name: 'Add asset'}).click();

  await page.getByPlaceholder('nginx.conf').fill(key);
  await page.getByPlaceholder('Paste config file contents here').fill(content);
  await page.getByRole('button', {name: 'Save new version'}).click();
  await expect(page.getByRole('row', {name: new RegExp(escapeRegExp(key))})).toBeVisible();
}

export async function uploadAsset(page, {key, content, fileName = key} = {}) {
  await byTestId(page, 'nav-assets', page.getByText('Assets')).click();
  await expect(page.getByPlaceholder('Search assets')).toBeVisible();
  const fileChooserPromise = page.waitForEvent('filechooser');
  await page.getByRole('button', {name: 'Upload asset'}).click();

  const fileChooser = await fileChooserPromise;
  await fileChooser.setFiles({
    name: fileName,
    mimeType: 'application/octet-stream',
    buffer: Buffer.from(content),
  });
  const assetRow = page.getByRole('row', {name: new RegExp(escapeRegExp(fileName))});
  await expect(assetRow).toBeVisible({timeout: ASSET_UPLOAD_TIMEOUT});
  const overlay = page.locator('.fixed.inset-0.z-50').filter({hasText: 'Upload successful. Asset created.'}).last();
  if (await overlay.isVisible().catch(() => false)) {
    await overlay.getByRole('button', {name: 'Close'}).click();
    await expect(overlay).toBeHidden({timeout: LONG_UI_TIMEOUT});
  }
  await assetRow.click();
  await expect(page.getByText(/[0-9.]+ (B|KB|MB|GB|TB) large asset/)).toBeVisible({timeout: LONG_UI_TIMEOUT});
}

export async function expectDeploymentOutput(page, name, expectedLines) {
  await byTestId(page, 'nav-status', page.getByText('Deployments')).click();
  const row = byTestId(page, `deployment-row-${name}`, page.locator('tr').filter({hasText: name}));
  await expect(row).toBeVisible({timeout: LONG_UI_TIMEOUT});
  await openDeploymentLogsSearch(page, row);
  for (const line of expectedLines) {
    await expectOutputText(page, line);
  }
}

export async function expectHTTPText(url, expectedText, {timeout = DEPLOYMENT_RUNNING_TIMEOUT} = {}) {
  await expect.poll(async () => {
    try {
      const response = await fetch(url, {cache: 'no-store'});
      if (!response.ok) return '';
      return await response.text();
    } catch {
      return '';
    }
  }, {message: `expected ${url} to contain ${expectedText}`, timeout}).toContain(expectedText);
}

export async function expectTLSIngress(hostname, {backend, certificateBundle, timeout = DEPLOYMENT_RUNNING_TIMEOUT} = {}) {
  if (!backend || !certificateBundle) throw new Error('TLS ingress backend and certificate bundle are required');
  const expectedBody = `backend=${backend}\nsni=${hostname}\n`;
  const expectedFingerprint = new crypto.X509Certificate(Buffer.from(certificateBundle, 'base64')).fingerprint256;
  await expect.poll(async () => {
    try {
      return (await requestTLSIngress(hostname)).body;
    } catch {
      return '';
    }
  }, {message: `expected TLS ingress for ${hostname}`, timeout}).toBe(expectedBody);
  const result = await requestTLSIngress(hostname);
  expect(result.fingerprint).toBe(expectedFingerprint);
}

export async function expectTLSIngressUnavailable(hostname, {timeout = DEPLOYMENT_RUNNING_TIMEOUT} = {}) {
  await expect.poll(async () => {
    try {
      await requestTLSIngress(hostname);
      return false;
    } catch {
      return true;
    }
  }, {message: `expected TLS ingress for ${hostname} to fail closed`, timeout}).toBe(true);
}

function requestTLSIngress(hostname) {
  const tunnelHost = process.env.OPD_TLS_INGRESS_HOST || 'host.docker.internal';
  const tunnelPort = Number(process.env.OPD_TLS_INGRESS_PORT || '18443');
  const ca = Buffer.from(process.env.OPD_TLS_INGRESS_CA_B64 || '', 'base64');
  if (ca.length === 0) return Promise.reject(new Error('OPD_TLS_INGRESS_CA_B64 is required'));
  return new Promise((resolve, reject) => {
    const req = https.request({
      host: hostname,
      port: tunnelPort,
      path: '/',
      method: 'GET',
      servername: hostname,
      headers: {host: hostname},
      ca,
      rejectUnauthorized: true,
      lookup: (_name, _options, callback) => dns.lookup(tunnelHost, {family: 4}, callback),
    }, response => {
      let body = '';
      response.setEncoding('utf8');
      response.on('data', chunk => { body += chunk; });
      response.on('end', () => {
        if (response.statusCode !== 200) {
          reject(new Error(`TLS ingress ${hostname} returned HTTP ${response.statusCode}`));
          return;
        }
        resolve({body, fingerprint: response.socket.getPeerCertificate().fingerprint256});
      });
    });
    req.setTimeout(10_000, () => req.destroy(new Error(`TLS ingress ${hostname} timed out`)));
    req.on('error', reject);
    req.end();
  });
}

export async function expectPrepareOutput(page, name, expectedText) {
  await byTestId(page, 'nav-status', page.getByText('Deployments')).click();
  const row = byTestId(page, `deployment-row-${name}`, page.locator('tr').filter({hasText: name}));
  await expect(row).toBeVisible({timeout: LONG_UI_TIMEOUT});
  await row.getByTitle('View prepare output').click();
  await expect(page.getByTestId('prepare-output-overlay')).toBeVisible();
  await expect(page.getByTestId('prepare-output-text')).toContainText(expectedText, {timeout: LONG_UI_TIMEOUT});
  await page.getByRole('button', {name: 'Close'}).click();
  await expect(page.getByTestId('prepare-output-overlay')).toBeHidden();
}

export async function expectDeploymentRestartCount(page, name, count) {
  await byTestId(page, 'nav-status', page.getByText('Deployments')).click();
  const row = byTestId(page, `deployment-row-${name}`, page.locator('tr').filter({hasText: name}));
  await expect(row).toBeVisible({timeout: LONG_UI_TIMEOUT});
  await expect.poll(async () => {
    const text = await row.getByTestId(`deployment-restarts-${name}`).textContent();
    return Number.parseInt(text || '0', 10) || 0;
  }, {message: `expected ${name} to restart ${count} times`, timeout: RESTART_TIMEOUT}).toBe(count);
  await expect(row.getByTitle('View run output')).toContainText('Running', {timeout: LONG_UI_TIMEOUT});
}

async function upgradeOpenDeployAgent(page, {machine, version}) {
  await byTestId(page, 'nav-status', page.getByText('Deployments')).click();
  await showOpendeployDeployments(page);
  const row = deploymentRow(page, {name: 'opendeploy', machine});
  await expect(row).toBeVisible({timeout: LONG_UI_TIMEOUT});
  await row.getByRole('button', {name: 'Update'}).click();

  const dialog = page.locator('.fixed.inset-0.z-50').filter({hasText: 'Update deployment'}).last();
  await expect(dialog).toBeVisible();
  const releaseSelect = field(dialog, 'Release').locator('select');
  await expect.poll(async () => {
    return await releaseSelect.locator('option').evaluateAll(options => options.map(o => o.value));
  }, {message: `expected ${version} release option`, timeout: RELEASE_OPTIONS_TIMEOUT}).toContain(version);
  await releaseSelect.selectOption(version);
  await dialog.getByRole('button', {name: 'Update deployment'}).click();
  await expect(dialog).toBeHidden({timeout: LONG_UI_TIMEOUT});
}

export async function expectOpenDeployAgentVersion(page, {machine, version}) {
  await byTestId(page, 'nav-status', page.getByText('Deployments')).click();
  await showOpendeployDeployments(page);
  const row = deploymentRow(page, {name: 'opendeploy', machine});
  await expect(row).toContainText(version, {timeout: UPGRADE_TIMEOUT});
  await expect(row.getByTitle('View run output')).toContainText('Running', {timeout: UPGRADE_TIMEOUT});
}

async function expectMachineConnected(page, machine) {
  await byTestId(page, 'nav-cluster', page.getByText('Machines')).click();
  const row = await clusterMachineRow(page, machine, UPGRADE_TIMEOUT);
  await expect(row).toContainText('connected', {timeout: UPGRADE_TIMEOUT});
}

async function selectDeploymentNode(dialog, nodeName) {
  const select = byTestId(dialog, 'deployment-node-select', selectField(dialog, 'Node'));
  const option = select.locator('option').filter({hasText: nodeName});
  await expect(option).toHaveCount(1, {timeout: LONG_UI_TIMEOUT});
  await select.selectOption(await option.getAttribute('value'));
}

async function clusterMachineRow(page, machine, timeout = LONG_UI_TIMEOUT) {
  let match;
  await expect.poll(async () => {
    const rows = page.locator('tr').filter({has: page.getByRole('textbox', {name: /^(Machine|Node) name for /})});
    for (let i = 0; i < await rows.count(); i++) {
      const row = rows.nth(i);
      if (await row.getByRole('textbox').inputValue() === machine) {
        match = row;
        return true;
      }
    }
    return false;
  }, {message: `expected machine row for ${machine}`, timeout}).toBe(true);
  return match;
}

async function expectDeploymentRunning(page, name) {
  await byTestId(page, 'nav-status', page.getByText('Deployments')).click();
  const row = deploymentRow(page, {name});
  const prepareStatus = row.getByTestId(`deployment-prepare-status-${name}`);
  await expect(prepareStatus).toContainText(/\b(ready|failed)\b/, {timeout: PREPARATION_TIMEOUT});

  const prepareText = ((await prepareStatus.textContent()) || '').trim();
  if (/\bfailed\b/.test(prepareText)) {
    await prepareStatus.click();
    const output = page.getByTestId('prepare-output-text');
    await expect(output).toBeVisible({timeout: LONG_UI_TIMEOUT});
    const details = ((await output.textContent()) || '').trim();
    throw new Error(`deployment ${name} preparation failed${details ? `:\n${details}` : ''}`);
  }

  const runnerStatus = row.getByTestId(`deployment-runner-status-${name}`);
  await expect(runnerStatus).toHaveText(/^(Running|Crashed|Stopped)$/, {timeout: RUNNER_START_TIMEOUT});
  const runnerText = ((await runnerStatus.textContent()) || '').trim();
  if (runnerText !== 'Running') {
    throw new Error(`deployment ${name} runner entered ${runnerText}`);
  }
}

async function openDeploymentLogsSearch(page, row) {
  await row.getByTitle('View run output').click();
  await expect(byTestId(page, 'nav-logs', page.getByText('Logs'))).toBeVisible();
  await expect(page.getByTestId('logs-space-select')).toBeVisible();
  await expect(page.getByTestId('logs-deployment-select')).not.toHaveValue('');
  await expect(page.getByTestId('logs-output')).toBeVisible();
}

async function showOpendeployDeployments(page) {
  const toggle = page.getByRole('button', {name: 'Show opendeploy'});
  if (await toggle.count() === 0) return;
  if (await toggle.getAttribute('aria-pressed') !== 'true') {
    await toggle.click();
  }
}

function deploymentRow(page, {name, machine, space} = {}) {
  let row = page.locator('tr').filter({hasText: name});
  if (machine) row = row.filter({hasText: machine});
  if (space) row = row.filter({hasText: space});
  return row.first();
}

async function waitForHealthyApp(page) {
  await expect.poll(async () => {
    try {
      return await page.evaluate(async () => {
        try {
          const response = await fetch('/v1/healthz', {cache: 'no-store'});
          return response.ok;
        } catch {
          return false;
        }
      });
    } catch {
      return false;
    }
  }, {message: 'expected OpenDeploy web API to recover', timeout: UPGRADE_TIMEOUT}).toBe(true);
}

async function waitForOptionalPathValidation(dialog) {
  const pathVerified = dialog.getByText(/Path verified|Flake path '.+' exists/);
  const validationFailed = dialog.getByText(/Git repository not accessible|Unable to validate flake path|Flake path not found|Selected commit not found/);
  return Promise.race([
    pathVerified.waitFor({state: 'visible', timeout: OPTIONAL_VALIDATION_TIMEOUT}).then(() => true).catch(() => false),
    validationFailed.waitFor({state: 'visible', timeout: OPTIONAL_VALIDATION_TIMEOUT}).then(() => false).catch(() => false),
  ]);
}

async function expectPathValidation(dialog) {
  if (await waitForOptionalPathValidation(dialog)) return;
  await expect(dialog.getByText(/Path verified|Flake path '.+' exists/)).toBeVisible({timeout: 1});
}

function byTestId(root, testID, fallback) {
  return root.getByTestId(testID).or(fallback).first();
}

async function setDeploymentEnvVars(dialog, env) {
  const entries = Object.entries(env || {}).filter(([key]) => key);
  if (entries.length === 0) return;

  await byTestId(dialog, 'deployment-env-vars-toggle', dialog.getByRole('button', {name: 'View / edit'})).click();
  const pane = dialog.getByRole('heading', {name: 'Environment variables'})
    .locator('xpath=ancestor::div[contains(@class, "border-l")][1]');

  for (const [key, value] of entries) {
    let row = await existingEnvRow(pane, key);
    if (!row) {
      await pane.getByRole('button', {name: '+ Add environment variable'}).click();
      row = pane.locator('tbody tr').last();
      await row.locator('input').first().fill(key);
    }

    const ref = envVarRef(value);
    if (ref.type !== 'value') {
      await row.locator('select').selectOption(ref.type);
      await selectEnvReference(row, ref.name);
    } else {
      await row.locator('input').nth(1).fill(ref.value);
    }
  }

  await pane.getByRole('button', {name: 'Close'}).click();
  await expect(pane).toBeHidden({timeout: LONG_UI_TIMEOUT});
}

async function existingEnvRow(pane, key) {
  const rows = pane.locator('tbody tr');
  const count = await rows.count();
  for (let i = 0; i < count; i += 1) {
    const row = rows.nth(i);
    if (await row.locator('input').first().inputValue() === key) return row;
  }
  return null;
}

async function setDeploymentUpgradeStrategy(dialog, {strategy = UPGRADE_RECREATE, readinessTimeoutSeconds = 600} = {}) {
  if (String(strategy) === UPGRADE_RECREATE) return;
  await dialog.getByText(/^Upgrade strategy:/).locator('xpath=ancestor::div[contains(@class, "justify-between")][1]').getByRole('button').click();
  const pane = dialog.getByRole('heading', {name: 'Upgrade strategy'}).locator('xpath=ancestor::div[contains(@class, "border-l")][1]');
  await field(pane, 'Strategy').locator('select').selectOption(String(strategy));
  if (String(strategy) === UPGRADE_ROLLOVER) {
    await field(pane, 'Readiness timeout').getByRole('spinbutton').fill(String(readinessTimeoutSeconds));
  }
  await pane.getByTitle('Close').click();
  await expect(pane).toBeHidden({timeout: LONG_UI_TIMEOUT});
}

async function setDeploymentPortForwarding(dialog, portForwarding) {
  const rows = portForwarding || [];
  if (rows.length === 0) return;
  const pane = await openDeploymentNetworkingPane(dialog);
  const section = pane.getByText('Port forwarding', {exact: true}).locator('xpath=ancestor::div[contains(@class, "border")][1]');
  await expect(section).toBeVisible({timeout: LONG_UI_TIMEOUT});
  for (const rule of rows) {
    await section.getByRole('button', {name: 'Add port'}).click();
    const row = section.locator('tbody tr').last();
    await row.locator('select').selectOption(String(rule.protocol || PORT_FORWARD_TCP));
    await row.locator('input').nth(0).fill(String(rule.hostPort));
    await row.locator('input').nth(1).fill(String(rule.containerPort));
  }
  await pane.getByTitle('Close').click();
  await expect(pane).toBeHidden({timeout: LONG_UI_TIMEOUT});
}

async function setDeploymentIngress(dialog, ingress) {
  const routes = ingress || [];
  const pane = await openDeploymentNetworkingPane(dialog);
  const section = byTestId(pane, 'deployment-ingress-section', pane.getByText('Ingress', {exact: true}).locator('xpath=ancestor::div[contains(@class, "border")][1]'));
  await expect(section).toBeVisible({timeout: LONG_UI_TIMEOUT});
  while (await byTestId(section, 'deployment-ingress-row', section.locator('tbody tr')).count() > 0) {
    await byTestId(section, 'deployment-remove-ingress-route', section.getByTitle('Remove ingress route')).first().click();
  }
  for (const route of routes) {
    await byTestId(section, 'deployment-add-ingress-route', section.getByRole('button', {name: 'Add route'})).click();
    const row = byTestId(section, 'deployment-ingress-row', section.locator('tbody tr')).last();
    await byTestId(row, 'deployment-ingress-hostname-input', row.locator('input').nth(0)).fill(route.hostname);
    if (route.hostPort) await byTestId(row, 'deployment-ingress-host-port-input', row.locator('input').nth(1)).fill(String(route.hostPort));
    await byTestId(row, 'deployment-ingress-container-port-input', row.locator('input').nth(2)).fill(String(route.containerPort));
  }
  await pane.getByTitle('Close').click();
  await expect(pane).toBeHidden({timeout: LONG_UI_TIMEOUT});
}

async function setDeploymentNetworkingMode(dialog, networkingMode) {
  const pane = await openDeploymentNetworkingPane(dialog);
  await byTestId(pane, 'deployment-networking-mode-select', selectField(pane, 'Mode')).selectOption(String(networkingMode));
  await pane.getByTitle('Close').click();
  await expect(pane).toBeHidden({timeout: LONG_UI_TIMEOUT});
}

async function openDeploymentNetworkingPane(dialog) {
  const pane = dialog.getByRole('heading', {name: 'Networking'}).locator('xpath=ancestor::div[contains(@class, "border-l")][1]');
  if (await pane.isVisible().catch(() => false)) return pane;
  await dialog.getByText(/^Networking:/).locator('xpath=ancestor::div[contains(@class, "justify-between")][1]').getByRole('button').click();
  await expect(pane).toBeVisible({timeout: LONG_UI_TIMEOUT});
  return pane;
}

function envVarRef(value) {
  if (value && typeof value === 'object' && (value.type === 'secret' || value.type === 'config')) {
    return {type: value.type, name: value.name || ''};
  }
  return {type: 'value', value: String(value ?? '')};
}

async function selectEnvReference(row, name) {
  const picker = row.locator('input').nth(1);
  await picker.fill(name);
  await expect(row.locator('li').filter({hasText: name}).first()).toBeVisible({timeout: LONG_UI_TIMEOUT});
  await picker.press('Enter');
  await expect(picker).toHaveValue(versionedReferenceValue(name), {timeout: LONG_UI_TIMEOUT});
}

async function setDeploymentDataMountPath(dialog, mountPath) {
  await dialog.getByRole('button', {name: 'Click to manage'}).click();
  await expect(dialog.getByRole('heading', {name: 'Mounted volumes'})).toBeVisible();
  const pane = dialog.getByRole('heading', {name: 'Mounted volumes'}).locator('xpath=ancestor::div[contains(@class, "border-l")][1]');
  await field(pane, 'Container mount path').getByRole('textbox').fill(mountPath);
}

async function setDeploymentAssetMount(dialog, {asset, path: mountPath}) {
  await dialog.getByRole('button', {name: 'Click to mount assets'}).click();
  await expect(dialog.getByRole('heading', {name: 'Mounted assets'})).toBeVisible();
  const pane = dialog.getByRole('heading', {name: 'Mounted assets'}).locator('xpath=ancestor::div[contains(@class, "border-l")][1]');
  const assetSelect = field(pane, 'Asset').locator('select');
  const assetOption = assetSelect.locator('option').filter({hasText: asset}).first();
  await expect(assetOption).toBeAttached({timeout: LONG_UI_TIMEOUT});
  const assetValue = await assetOption.getAttribute('value');
  await assetSelect.selectOption(assetValue);
  await expect(assetSelect).toHaveValue(assetValue);
  const pathInput = field(pane, 'Container path').getByRole('textbox');
  await pathInput.fill(mountPath);
  await expect(pathInput).toHaveValue(mountPath);
  await expect(dialog.getByText('1 mounted asset')).toBeVisible();
}

async function expectOutputText(page, text) {
  const deadline = Date.now() + LOG_OUTPUT_TIMEOUT;
  while (Date.now() < deadline) {
    try {
      await expect(page.getByTestId('logs-output')).toContainText(text, {timeout: LOG_OUTPUT_POLL_TIMEOUT});
      return;
    } catch {
      await page.getByTestId('logs-search-button').click();
    }
  }
  await expect(page.getByTestId('logs-output')).toContainText(text, {timeout: 1});
}

function trackRepoValidateRequests(page) {
  const requests = [];
  const responses = [];
  const isValidateRequest = request => {
    const url = new URL(request.url());
    return request.method() === 'POST' && url.pathname === '/v1/repo/validate';
  };
  const handler = request => {
    if (isValidateRequest(request)) requests.push(request);
  };
  const responseHandler = response => {
    if (isValidateRequest(response.request())) responses.push(response);
  };
  page.on('request', handler);
  page.on('response', responseHandler);

  return {
    async expectCount(count, message) {
      await expect.poll(() => requests.length, {message, timeout: VALIDATE_REQUEST_TIMEOUT}).toBe(count);
    },
    async expectResponseCount(count, message) {
      await expect.poll(() => responses.length, {message, timeout: VALIDATE_REQUEST_TIMEOUT}).toBe(count);
    },
    async expectStableCount(count, message) {
      await expect(requests, message).toHaveLength(count);
      await expect(responses, message).toHaveLength(count);
      await page.waitForTimeout(STABLE_CHECK_DELAY);
      await expect(requests, message).toHaveLength(count);
      await expect(responses, message).toHaveLength(count);
    },
    stop() {
      page.off('request', handler);
      page.off('response', responseHandler);
    },
  };
}

function field(dialog, label) {
  return dialog.locator('label').filter({hasText: label}).first();
}

function textField(dialog, label) {
  return field(dialog, label).getByRole('textbox');
}

function selectField(dialog, label) {
  return field(dialog, label).locator('select');
}

function escapeRegExp(s) {
  return String(s).replace(/[.*+?^${}()|[\]\\]/g, '\\$&');
}
