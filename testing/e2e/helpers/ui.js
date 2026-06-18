import {expect} from '@playwright/test';

const LONG_UI_TIMEOUT = 15_000;
const OPTIONAL_VALIDATION_TIMEOUT = LONG_UI_TIMEOUT;
const VALIDATE_REQUEST_TIMEOUT = 5_000;
const LOG_OUTPUT_TIMEOUT = 120_000;
const LOG_OUTPUT_POLL_TIMEOUT = 1_500;
const RESTART_TIMEOUT = 120_000;
const UPGRADE_TIMEOUT = 180_000;
const RELEASE_OPTIONS_TIMEOUT = 60_000;
const STABLE_CHECK_DELAY = 200;

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
  await expect(page.getByRole('heading', {name: 'General settings'})).toBeVisible();
  const row = page.getByRole('row', {name: /GitHub token/});
  await row.getByRole('textbox').fill(token);
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
    const options = await deploymentSelect.locator('option').evaluateAll(options =>
      options.map(o => ({value: o.value, text: o.textContent || ''})),
    );
    return options.find(o => o.value && o.text.includes('opendeploy'))?.value || '';
  }, {message: 'expected opendeploy deployment option', timeout: LONG_UI_TIMEOUT}).not.toBe('');
  const deploymentValue = await deploymentSelect.locator('option').evaluateAll(options => {
    const match = options.find(o => o.value && (o.textContent || '').includes('opendeploy'));
    return match?.value || '';
  });
  await deploymentSelect.selectOption(deploymentValue);
  await page.getByTestId('logs-search-button').click();
  await expectOutputText(page, 'opendeploy starting primary');
}

export async function acceptFirstWaitingWorker(page, {workerName = 'worker-1'} = {}) {
  await byTestId(page, 'nav-cluster', page.getByText('Machines')).click();
  await expect(page.getByRole('heading', {name: 'Enrollment requests'})).toBeVisible();

  const requestRow = byTestId(page, 'enrollment-request-1', page.locator('tr').filter({hasText: '#1'}).filter({hasText: 'Accept'}));
  await expect(requestRow).toBeVisible({timeout: LONG_UI_TIMEOUT});
  await byTestId(requestRow, 'enrollment-worker-name-input', requestRow.getByRole('textbox')).fill(workerName);
  await byTestId(requestRow, 'enrollment-accept-button', requestRow.getByRole('button', {name: 'Accept'})).click();

  await expect(page.getByText('No pending enrollment requests.')).toBeVisible({timeout: LONG_UI_TIMEOUT});
  const workerRow = byTestId(page, `machine-row-${workerName}`, page.locator('tr').filter({hasText: workerName}).filter({hasText: 'secondary'}));
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
  expectDefaultDockerImage = false,
  verifyLogs = true,
} = {}) {
  await byTestId(page, 'nav-status', page.getByText('Deployments')).click();
  await byTestId(page, 'add-deployment-button', page.getByRole('button', {name: 'Add deployment'})).click();

  const dialog = byTestId(page, 'create-deployment-dialog', page.locator('.fixed.inset-0.z-50').filter({hasText: 'Deployment identity'}).last());
  await expect(dialog).toBeVisible();

  await byTestId(dialog, 'deployment-name-input', textField(dialog, 'Name')).fill(name);
  await byTestId(dialog, 'deployment-machine-select', selectField(dialog, 'Machine')).selectOption(machine);
  const sourceTypeSelect = byTestId(dialog, 'deployment-source-type-select', selectField(dialog, 'Source type'));
  if (expectDefaultDockerImage) {
    await expect(sourceTypeSelect).toHaveValue('containerImage');
  }
  await sourceTypeSelect.selectOption('nixDockerBuild');
  const validateRequests = trackRepoValidateRequests(page);
  const repoInput = byTestId(dialog, 'deployment-repo-input', textField(dialog, 'Repository'));
  const flakeInput = byTestId(dialog, 'deployment-flake-input', textField(dialog, 'Path to flake.nix'));

  try {
    await repoInput.fill(repo);
    await flakeInput.click();
    await validateRequests.expectCount(1, 'expected one validate call after setting repository URL');

    await flakeInput.fill(flake);
    await flakeInput.blur();
    await validateRequests.expectCount(2, 'expected a second validate call after setting flake path');

    await expectPathValidation(dialog);
    const commitSelect = selectField(dialog, 'Commit');
    await expect(commitSelect).not.toHaveValue('', {timeout: LONG_UI_TIMEOUT});

    const refreshButton = dialog.getByRole('button', {name: 'Refresh'});
    await expect(refreshButton).toBeEnabled();
    await refreshButton.click();
    await validateRequests.expectCount(3, 'expected a validate call after refreshing versions');
    await expectPathValidation(dialog);
    await expect(commitSelect).not.toHaveValue('', {timeout: LONG_UI_TIMEOUT});
    await expect(dialog.getByText('d is not a function')).toHaveCount(0);

    await validateRequests.expectStableCount(3, 'expected only the repo, flake, and refresh validate calls');
  } finally {
    validateRequests.stop();
  }

  await setDeploymentEnvVars(dialog, env);
  if (assetMount) await setDeploymentAssetMount(dialog, assetMount);
  await byTestId(dialog, 'create-deployment-submit', dialog.getByRole('button', {name: 'Create'})).click();

  let row = byTestId(page, `deployment-row-${name}`, page.locator('tr').filter({hasText: name}).filter({hasText: machine}));
  await expect(row).toBeVisible({timeout: LONG_UI_TIMEOUT});
  if (!verifyLogs) return;
  await openDeploymentLogsSearch(page, row);
  for (const [key, value] of Object.entries(expectedEnv || {})) {
    await expectOutputText(page, `nixdockerbuild1 env ${key}=${value}`);
  }
}

export async function createNixDockerCrasherDeployment(page, {
  name = 'nixdockercrasher',
  machine = 'worker-1',
} = {}) {
  await createNixDockerDeployment(page, {
    name,
    machine,
    flake: 'testexamples/nixdockercrasher/flake.nix',
    env: {},
    expectedEnv: {},
  });
  await expectDeploymentRestartCount(page, name, 3);
  await expectDeploymentOutput(page, name, [
    'nixdockercrasher wrote crash number=1',
    'msg="panic: nixdockercrasher panic crash count=1" fmt=unformatted',
    'nixdockercrasher wrote crash number=2',
    'msg="panic: nixdockercrasher panic crash count=2" fmt=unformatted',
    'nixdockercrasher wrote crash number=3',
    'msg="panic: nixdockercrasher panic crash count=3" fmt=unformatted',
    'nixdockercrasher crash count=3; staying alive',
  ]);
}

export async function upgradeOpenDeployAgents(page, {version = 'v0.0.136', workerName = 'worker-1'} = {}) {
  await upgradeOpenDeployAgent(page, {machine: workerName, version});
  await expectOpenDeployAgentVersion(page, {machine: workerName, version});
  await expectMachineConnected(page, workerName);
  await upgradeOpenDeployAgent(page, {machine: 'primary', version});
  await waitForHealthyApp(page);
  await page.reload();
  await expect(byTestId(page, 'add-deployment-button', page.getByRole('button', {name: 'Add deployment'}))).toBeVisible({timeout: LONG_UI_TIMEOUT});
  await expectOpenDeployAgentVersion(page, {machine: 'primary', version});
  await expectOpenDeployAgentVersion(page, {machine: workerName, version});
}

export async function createPostgresDeployment(page, {
  name = 'postgres18',
  machine = 'worker-1',
} = {}) {
  await createContainerImageDeployment(page, {
    name,
    machine,
    image: 'docker.io/library/postgres:18',
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

export async function createConfig(page, {name, value} = {}) {
  await byTestId(page, 'nav-secrets', page.getByText('Secrets / Configs')).click();
  await page.getByRole('button', {name: 'Add config'}).click();

  const row = page.locator('tbody tr').last();
  await row.locator('input').nth(0).fill(name);
  await row.locator('input').nth(1).fill(value);
  await row.getByRole('button', {name: 'Save'}).click();
  await expect(page.getByRole('row', {name: new RegExp(escapeRegExp(name))})).toBeVisible();
}

export async function createSecret(page, {name, value} = {}) {
  await byTestId(page, 'nav-secrets', page.getByText('Secrets / Configs')).click();
  await page.getByRole('button', {name: 'Add secret'}).click();

  const row = page.locator('tbody tr').last();
  await row.locator('input').nth(0).fill(name);
  await row.locator('input').nth(1).fill(value);
  await row.getByRole('button', {name: 'Save'}).click();
  await expect(page.getByRole('row', {name: new RegExp(escapeRegExp(name))})).toBeVisible();
}

async function createContainerImageDeployment(page, {
  name,
  machine,
  image,
  env = {},
  dataMountPath = '',
} = {}) {
  await byTestId(page, 'nav-status', page.getByText('Deployments')).click();
  await byTestId(page, 'add-deployment-button', page.getByRole('button', {name: 'Add deployment'})).click();

  const dialog = byTestId(page, 'create-deployment-dialog', page.locator('.fixed.inset-0.z-50').filter({hasText: 'Deployment identity'}).last());
  await expect(dialog).toBeVisible();

  await byTestId(dialog, 'deployment-name-input', textField(dialog, 'Name')).fill(name);
  await byTestId(dialog, 'deployment-machine-select', selectField(dialog, 'Machine')).selectOption(machine);
  await byTestId(dialog, 'deployment-source-type-select', selectField(dialog, 'Source type')).selectOption('containerImage');
  await byTestId(dialog, 'deployment-container-image-input', textField(dialog, 'Image')).fill(image);
  await setDeploymentEnvVars(dialog, env);
  if (dataMountPath) await setDeploymentDataMountPath(dialog, dataMountPath);
  await byTestId(dialog, 'create-deployment-submit', dialog.getByRole('button', {name: 'Create'})).click();

  const row = deploymentRow(page, {name, machine});
  await expect(row).toBeVisible({timeout: LONG_UI_TIMEOUT});
}

export async function createAsset(page, {key, content, format = 'text'} = {}) {
  await byTestId(page, 'nav-assets', page.getByText('Assets')).click();
  await expect(page.getByPlaceholder('Search assets')).toBeVisible();
  await page.getByRole('button', {name: 'Add asset'}).click();

  await page.getByPlaceholder('nginx.conf').fill(key);
  await page.getByPlaceholder('text').fill(format);
  await page.getByPlaceholder('Paste config file contents here').fill(content);
  await page.getByRole('button', {name: 'Save new version'}).click();
  await expect(page.getByRole('row', {name: new RegExp(escapeRegExp(key))})).toBeVisible();
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
  const row = deploymentRow(page, {name: 'opendeploy', machine});
  await expect(row).toContainText(version, {timeout: UPGRADE_TIMEOUT});
  await expect(row.getByTitle('View run output')).toContainText('Running', {timeout: UPGRADE_TIMEOUT});
}

async function expectMachineConnected(page, machine) {
  await byTestId(page, 'nav-cluster', page.getByText('Machines')).click();
  const row = byTestId(page, `machine-row-${machine}`, page.locator('tr').filter({hasText: machine}));
  await expect(row).toContainText('connected', {timeout: UPGRADE_TIMEOUT});
}

async function expectDeploymentRunning(page, name) {
  await byTestId(page, 'nav-status', page.getByText('Deployments')).click();
  const row = deploymentRow(page, {name});
  await expect(row.getByTitle('View run output')).toContainText('Running', {timeout: RESTART_TIMEOUT});
}

async function openDeploymentLogsSearch(page, row) {
  await row.getByTitle('View run output').click();
  await expect(byTestId(page, 'nav-logs', page.getByText('Logs'))).toBeVisible();
  await expect(page.getByTestId('logs-space-select')).toBeVisible();
  await expect(page.getByTestId('logs-deployment-select')).not.toHaveValue('');
  await expect(page.getByTestId('logs-output')).toBeVisible();
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
    await pane.getByRole('button', {name: '+ Add environment variable'}).click();
    const row = pane.locator('tbody tr').last();
    await row.locator('input').first().fill(key);

    const ref = envVarRef(value);
    if (ref.type !== 'value') {
      await row.locator('select').selectOption(ref.type);
      const picker = row.locator('input').nth(1);
      await picker.fill(ref.name);
      await picker.press('Enter');
    } else {
      await row.locator('input').nth(1).fill(ref.value);
    }
  }

  await expect(dialog.getByText(`${entries.length} environment variables`)).toBeVisible();
}

function envVarRef(value) {
  if (value && typeof value === 'object' && (value.type === 'secret' || value.type === 'config')) {
    return {type: value.type, name: value.name || ''};
  }
  return {type: 'value', value: String(value ?? '')};
}

async function setDeploymentDataMountPath(dialog, path) {
  await dialog.getByRole('button', {name: 'Click to manage'}).click();
  await expect(dialog.getByRole('heading', {name: 'Mounted volumes'})).toBeVisible();
  const pane = dialog.getByRole('heading', {name: 'Mounted volumes'}).locator('xpath=ancestor::div[contains(@class, "border-l")][1]');
  await field(pane, 'Container mount path').getByRole('textbox').fill(path);
}

async function setDeploymentAssetMount(dialog, {asset, path}) {
  await dialog.getByRole('button', {name: 'Click to mount assets'}).click();
  await expect(dialog.getByRole('heading', {name: 'Mounted assets'})).toBeVisible();
  const pane = dialog.getByRole('heading', {name: 'Mounted assets'}).locator('xpath=ancestor::div[contains(@class, "border-l")][1]');
  await field(pane, 'Asset').locator('select').selectOption(asset);
  await field(pane, 'Container path').getByRole('textbox').fill(path);
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
  const isValidateRequest = request => {
    const url = new URL(request.url());
    return request.method() === 'POST' && url.pathname === '/v1/repo/validate';
  };
  const handler = request => {
    if (isValidateRequest(request)) requests.push(request);
  };
  page.on('request', handler);

  return {
    async expectCount(count, message) {
      await expect.poll(() => requests.length, {message, timeout: VALIDATE_REQUEST_TIMEOUT}).toBe(count);
    },
    async expectStableCount(count, message) {
      await expect(requests, message).toHaveLength(count);
      await page.waitForTimeout(STABLE_CHECK_DELAY);
      await expect(requests, message).toHaveLength(count);
    },
    stop() {
      page.off('request', handler);
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
