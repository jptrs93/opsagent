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
  await expect(page.getByText('Enrollment requests')).toBeVisible();

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
} = {}) {
  await byTestId(page, 'nav-status', page.getByText('Deployments')).click();
  await byTestId(page, 'add-deployment-button', page.getByRole('button', {name: 'Add deployment'})).click();

  const dialog = byTestId(page, 'create-deployment-dialog', page.locator('.fixed.inset-0.z-50').filter({hasText: 'Deployment identity'}).last());
  await expect(dialog).toBeVisible();

  await byTestId(dialog, 'deployment-name-input', textField(dialog, 'Name')).fill(name);
  await byTestId(dialog, 'deployment-machine-select', selectField(dialog, 'Machine')).selectOption(machine);
  await byTestId(dialog, 'deployment-source-type-select', selectField(dialog, 'Source type')).selectOption('nixDockerBuild');
  await byTestId(dialog, 'deployment-repo-input', textField(dialog, 'Repository')).fill(repo);
  await byTestId(dialog, 'deployment-flake-input', textField(dialog, 'Flake')).fill(flake);
  await expect(dialog.getByText('Runs the prepared image with the container runner.')).toBeVisible();
  await byTestId(dialog, 'create-deployment-submit', dialog.getByRole('button', {name: 'Create'})).click();

  const row = byTestId(page, `deployment-row-${name}`, page.locator('tr').filter({hasText: name}).filter({hasText: machine}));
  await expect(row).toBeVisible({timeout: 30_000});
  await expect(row).toContainText('none');
}

function byTestId(root, testID, fallback) {
  return root.getByTestId(testID).or(fallback).first();
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
