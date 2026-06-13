import {expect} from '@playwright/test';

export async function bootstrapFirstUser(page, {username = 'E2E Operator', password = 'opendeploy-setup'} = {}) {
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

export async function acceptFirstWaitingWorker(page, {workerName = 'worker-1'} = {}) {
  await byTestId(page, 'nav-cluster', page.getByText('Machines')).click();
  await expect(page.getByRole('heading', {name: 'Machines', exact: true})).toBeVisible();
  await expect(page.getByRole('heading', {name: 'Enrollment requests'})).toBeVisible();

  const requestRow = byTestId(page, 'enrollment-request-1', page.locator('tr').filter({hasText: '#1'}).filter({hasText: 'Accept'}));
  await expect(requestRow).toBeVisible({timeout: 30_000});
  await byTestId(requestRow, 'enrollment-worker-name-input', requestRow.getByRole('textbox')).fill(workerName);
  await byTestId(requestRow, 'enrollment-accept-button', requestRow.getByRole('button', {name: 'Accept'})).click();

  await expect(page.getByText('No pending enrollment requests.')).toBeVisible({timeout: 30_000});
  const workerRow = byTestId(page, `machine-row-${workerName}`, page.locator('tr').filter({hasText: workerName}).filter({hasText: 'secondary'}));
  await expect(workerRow).toContainText('connected', {timeout: 30_000});
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
} = {}) {
  await byTestId(page, 'nav-status', page.getByText('Deployments')).click();
  await byTestId(page, 'add-deployment-button', page.getByRole('button', {name: 'Add deployment'})).click();

  const dialog = byTestId(page, 'create-deployment-dialog', page.locator('.fixed.inset-0.z-50').filter({hasText: 'Deployment identity'}).last());
  await expect(dialog).toBeVisible();

  await byTestId(dialog, 'deployment-name-input', textField(dialog, 'Name')).fill(name);
  await byTestId(dialog, 'deployment-machine-select', selectField(dialog, 'Machine')).selectOption(machine);
  await byTestId(dialog, 'deployment-source-type-select', selectField(dialog, 'Source type')).selectOption('nixDockerBuild');
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

    if (await waitForOptionalPathValidation(dialog)) {
      const commitSelect = selectField(dialog, 'Commit');
      await expect(commitSelect).not.toHaveValue('');
    }

    await validateRequests.expectStableCount(2, 'expected only the repo and flake validate calls');
  } finally {
    validateRequests.stop();
  }

  await expect(dialog.getByText('Runs the prepared image with the container runner.')).toBeVisible();
  await setDeploymentEnvVars(dialog, env);
  await byTestId(dialog, 'create-deployment-submit', dialog.getByRole('button', {name: 'Create'})).click();

  const row = byTestId(page, `deployment-row-${name}`, page.locator('tr').filter({hasText: name}).filter({hasText: machine}));
  await expect(row).toBeVisible({timeout: 30_000});
  await expect(row.getByText('Running', {exact: true})).toBeVisible({timeout: 180_000});
  await row.getByText('Running', {exact: true}).click();
  await expect(page.getByRole('heading', {name: new RegExp(`^Output: .*${name}`)})).toBeVisible();
  await expect(page.getByText(`nixdockerbuild1 env OPENDEPLOY_E2E_MESSAGE=${env.OPENDEPLOY_E2E_MESSAGE}`)).toBeVisible({timeout: 30_000});
  await expect(page.getByText(`nixdockerbuild1 env OPENDEPLOY_E2E_COLOR=${env.OPENDEPLOY_E2E_COLOR}`)).toBeVisible();
}

async function waitForOptionalPathValidation(dialog) {
  const pathVerified = dialog.getByText('Path verified');
  const validationFailed = dialog.getByText(/Git repository not accessible|Unable to validate flake path|Flake path not found|Selected commit not found/);
  return Promise.race([
    pathVerified.waitFor({state: 'visible', timeout: 15_000}).then(() => true).catch(() => false),
    validationFailed.waitFor({state: 'visible', timeout: 15_000}).then(() => false).catch(() => false),
  ]);
}

function byTestId(root, testID, fallback) {
  return root.getByTestId(testID).or(fallback).first();
}

async function setDeploymentEnvVars(dialog, env) {
  const entries = Object.entries(env || {}).filter(([key]) => key);
  if (entries.length === 0) return;

  await byTestId(dialog, 'deployment-env-vars-toggle', dialog.getByRole('button', {name: 'View / edit'})).click();
  const text = entries.map(([key, value]) => `${key}=${value}`).join('\n');
  await byTestId(dialog, 'deployment-env-vars-textarea', dialog.getByRole('textbox')).fill(text);
  await expect(dialog.getByText(`${entries.length} environment variables`)).toBeVisible();
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
      await expect.poll(() => requests.length, {message, timeout: 10_000}).toBe(count);
    },
    async expectStableCount(count, message) {
      await expect(requests, message).toHaveLength(count);
      await page.waitForTimeout(500);
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
