import {expect, test} from '@playwright/test';
import {Buffer} from 'node:buffer';
import crypto from 'node:crypto';
import fs from 'node:fs';
import https from 'node:https';
import path from 'node:path';
import {expectTLSProbeRejected} from './httpsClient.js';

const LONG_UI_TIMEOUT = 15_000;
// Commit discovery fetches from the repo remote, and the first fetch against a
// fresh cluster is cold: measured at ~4.5s on the mock mirror, which does not
// support `--filter`, against 0.02s for every fetch after it. A 5s budget sat
// on that cliff and failed the first deployment of a run. The polls here exit
// as soon as the expected count is reached, so headroom costs nothing.
const VALIDATE_REQUEST_TIMEOUT = 30_000;
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
const PGBACKREST_TIMEOUT = 300_000;
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

  const dialog = page.getByTestId('create-secret-overlay').getByRole('dialog');
  await expect(dialog).toBeVisible();
  await dialog.getByLabel('Secret name').fill('opendeploy.config.github_token');
  await fillCodeEditor(dialog, 'Value for new secret', token);
  await dialog.getByRole('button', {name: 'Add secret'}).click();

  await expect(dialog).toBeHidden({timeout: LONG_UI_TIMEOUT});
  await expect(page.getByText('Unsaved changes')).toBeVisible({timeout: LONG_UI_TIMEOUT});
  await page.getByRole('button', {name: 'Save changes'}).click();
  await expect(page.getByText('Unsaved changes')).toBeHidden({timeout: LONG_UI_TIMEOUT});
}

export async function expectOpenDeployLogs(page) {
  await byTestId(page, 'nav-logs', page.getByText('Logs')).click();
  // The multi-select space filter defaults to every space visible; make sure
  // the system space is checked so the opendeploy deployments are listed.
  const spaceFilter = page.getByTestId('logs-space-filter');
  await expect(spaceFilter).toBeVisible();
  await spaceFilter.click();
  const systemRow = page.getByTestId('logs-space-filter-row-0');
  await expect(systemRow).toBeVisible({timeout: LONG_UI_TIMEOUT});
  if (await systemRow.getAttribute('aria-checked') !== 'true') await systemRow.click();
  await page.mouse.click(2, 2);
  await expect(systemRow).toBeHidden();

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
  await expectOutputText(page, 'opendeploy starting primary');

  // Filtered search: every matching record must carry the search phrase.
  await expectFilteredLogSearch(page, 'starting primary');
  // Same filter through an explicit quick range.
  await page.getByTestId('logs-time-button').click();
  await page.getByRole('button', {name: 'Last hour'}).click();
  await expectFilteredLogSearch(page, 'starting primary');
  await resetLogSearchFilters(page);

  const netproxyValue = await deploymentSelect.locator('option').evaluateAll(options => {
    const match = options.find(o => o.value && (o.textContent || '').trim().endsWith(' / opendeploy-net'));
    return match?.value || '';
  });
  expect(netproxyValue, 'expected opendeploy-net deployment option').not.toBe('');
  await deploymentSelect.selectOption(netproxyValue);
  await expectOutputText(page, 'starting opendeploy-net');
}

// The results table is virtualised, so full-result assertions read the page's
// __logsResult test hook rather than the DOM.
async function logsResultText(page) {
  return await page.evaluate(() =>
    (window.__logsResult?.records || []).map(r => r.msg).join('\n'));
}

// Clicking search only issues the request. The hook is cleared first so a
// later read cannot observe the previous search's records: the page drops
// stale responses by generation, so whatever repopulates it belongs to this
// search.
async function setLogsQuery(page, query) {
  const input = page.getByTestId('logs-query-input');
  await input.fill(query);
  await page.evaluate(() => { window.__logsResult = null; });
  await page.getByTestId('logs-search-button').click();
}

const quoteLogsPhrase = (text) => `"${text}"`;

async function expectFilteredLogSearch(page, searchStr) {
  await setLogsQuery(page, quoteLogsPhrase(searchStr));
  // Not expectOutputText: the pre-search view usually already contains the
  // phrase, so visible text is no evidence that this search has landed. Wait
  // on the hook itself. -1 means the response is still in flight; 0 means it
  // landed empty, so re-issue the search the way expectOutputText does in
  // case the records have not been ingested yet.
  await expect.poll(async () => {
    const count = await page.evaluate(() => window.__logsResult?.records?.length ?? -1);
    if (count === 0) {
      await page.evaluate(() => { window.__logsResult = null; });
      await page.getByTestId('logs-search-button').click();
    }
    return count;
  }, {
    message: `expected filtered log search for ${searchStr} to return records`,
    timeout: LOG_OUTPUT_TIMEOUT,
  }).toBeGreaterThan(0);
  const lines = (await logsResultText(page)).split('\n').filter(line => line.trim() !== '');
  expect(lines.length, 'expected filtered log search to return records').toBeGreaterThan(0);
  for (const line of lines) {
    expect(line.toLowerCase(), `expected every matching record to contain ${searchStr}`).toContain(searchStr.toLowerCase());
  }
}

async function resetLogSearchFilters(page) {
  await setLogsQuery(page, '');
}

export async function acceptWaitingWorker(page, {machineID, workerName, expectNoPending = false} = {}) {
  if (!machineID) throw new Error('worker machine ID is required');
  if (!workerName) throw new Error('worker name is required');
  await byTestId(page, 'nav-cluster', page.getByText('Machines')).click();
  await expect(page.getByRole('heading', {name: 'Enrollment requests'})).toBeVisible();

  const requestRow = page.locator('tr').filter({hasText: machineID}).filter({hasText: 'Accept'});
  await expect(requestRow).toBeVisible({timeout: LONG_UI_TIMEOUT});
  await byTestId(requestRow, 'enrollment-node-name-input', requestRow.getByRole('textbox')).fill(workerName);
  await byTestId(requestRow, 'enrollment-accept-button', requestRow.getByRole('button', {name: 'Accept'})).click();

  await expect(requestRow).toBeHidden({timeout: LONG_UI_TIMEOUT});
  if (expectNoPending) await expect(page.getByText('No pending enrollment requests.')).toBeVisible({timeout: LONG_UI_TIMEOUT});
  const workerRow = (await clusterMachineRow(page, workerName)).filter({hasText: 'secondary'});
  await expect(workerRow).toContainText('connected', {timeout: LONG_UI_TIMEOUT});
}

export async function createNixDockerDeployment(page, {
  name = 'nixdockerbuild1',
  machine = 'worker-1',
  space,
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

  const dialog = editorPanel(page, 'create-deployment-dialog');
  await expect(dialog).toBeVisible();
  await step(`open editor tab in UI mode ${name}`, async () => {
    await expect(page.locator('[data-testid^="deployments-tab-create-"]').last()).toHaveAttribute('aria-selected', 'true');
    await selectEditorMode(dialog, 'ui');
  });

  await step(`fill deployment identity ${name}`, async () => {
    await byTestId(dialog, 'deployment-name-input', textField(dialog, 'Name')).fill(name);
    if (space) {
      const spaceSelect = byTestId(dialog, 'deployment-space-select', selectField(dialog, 'Space'));
      const spaceOption = spaceSelect.locator('option').filter({hasText: space});
      await expect(spaceOption).toHaveCount(1, {timeout: LONG_UI_TIMEOUT});
      await spaceSelect.selectOption(await spaceOption.getAttribute('value'));
    }
    await selectDeploymentNode(dialog, machine);
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
    // Validation is human-triggered: typing the source issues nothing, the
    // footer's Validate lists the repository's branches and the commits of
    // main (two requests), and picking a commit checks the flake file at it.
    await step(`validate source ${name}`, async () => {
      await repoInput.fill(repo);
      await flakeInput.fill(flake);
      await flakeInput.blur();
      await expectSourceStatus(dialog, 'Source not validated');
      await validateRequests.expectStableCount(0, 'expected no validate requests before Validate is clicked');
      await expect(dialog.getByTestId('version-select-button')).toBeDisabled();
      await dialog.getByTestId('source-validate-button').click();
      await validateRequests.expectCount(2, 'expected repository and commit listing after Validate');
      await validateRequests.expectResponseCount(2, 'expected repository and commit listing responses');
      await expectSourceStatus(dialog, 'Source valid');
    });

    await step(`select version ${name}`, async () => {
      await selectDeploymentVersion(dialog, 0);
      await validateRequests.expectCount(3, 'expected the flake check at the selected commit');
      await validateRequests.expectResponseCount(3, 'expected the flake check response');
      await expectSourceStatus(dialog, 'Source valid');
      await expect(dialog.getByTestId('version-selection')).toBeVisible();
    });

    await step(`refresh source versions ${name}`, async () => {
      const refreshButton = dialog.getByTestId('version-refresh-button');
      await expect(refreshButton).toBeEnabled();
      await refreshButton.click();
      await validateRequests.expectCount(4, 'expected one listing request for refresh');
      await validateRequests.expectResponseCount(4, 'expected the refreshed listing response');
      await expect(refreshButton).toBeEnabled({timeout: LONG_UI_TIMEOUT});
      await expect(dialog.getByTestId('version-selection')).toBeVisible();
      await expectSourceStatus(dialog, 'Source valid');
      await expect(dialog.getByText('d is not a function')).toHaveCount(0);
    });

    await step(`verify source validation settled ${name}`, () => validateRequests.expectStableCount(4, 'expected listing and flake check requests to settle'));
  } finally {
    validateRequests.stop();
  }

  await step(`configure deployment inputs ${name}`, async () => {
    await setDeploymentNetworkingMode(dialog, networkingMode);
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
      return request.method() === 'POST' && new URL(request.url()).pathname === '/v1/deployments/create';
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
  assetMount,
  desiredRunning,
} = {}) {
  await step(`open update dialog ${name}`, async () => {
    await byTestId(page, 'nav-status', page.getByText('Deployments')).click();
    const row = deploymentRow(page, {name, machine});
    await expect(row).toBeVisible({timeout: LONG_UI_TIMEOUT});
    await row.getByRole('button', {name: 'Update'}).click();
  });

  const dialog = editorPanel(page, 'update-deployment-dialog');
  await expect(dialog).toBeVisible();
  await selectEditorMode(dialog, 'ui');

  await step(`configure update ${name}`, async () => {
    await setDeploymentUpgradeStrategy(dialog, {strategy: upgradeStrategy, readinessTimeoutSeconds});
    await setDeploymentPortForwarding(dialog, portForwarding);
    if (ingress !== undefined) await setDeploymentIngress(dialog, ingress);
    await setDeploymentEnvVars(dialog, env);
    if (assetMount) await setDeploymentAssetMount(dialog, assetMount);
    if (desiredRunning !== undefined) await setDeploymentDesiredRunning(dialog, desiredRunning);
  });

  await step(`submit update ${name}`, async () => {
    const submit = dialog.getByRole('button', {name: 'Update deployment'});
    await expect(submit).toBeEnabled({timeout: LONG_UI_TIMEOUT});
    const updateResponse = page.waitForResponse(response => {
      const request = response.request();
      return request.method() === 'POST' && new URL(request.url()).pathname === '/v2/deployments/update';
    }, {timeout: LONG_UI_TIMEOUT});
    await submit.click();
    expect((await updateResponse).ok()).toBe(true);
    await expect(dialog).toBeHidden({timeout: LONG_UI_TIMEOUT});
  });
}

export async function setDeploymentHttpsRoutes(page, {name, machine = 'worker-2', routes = [], expectError} = {}) {
  await step(`open update dialog ${name}`, async () => {
    await byTestId(page, 'nav-status', page.getByText('Deployments')).click();
    const row = deploymentRow(page, {name, machine});
    await expect(row).toBeVisible({timeout: LONG_UI_TIMEOUT});
    await row.getByRole('button', {name: 'Update'}).click();
  });

  const dialog = editorPanel(page, 'update-deployment-dialog');
  await expect(dialog).toBeVisible();

  const editor = dialog.getByTestId('deployment-hcl-editor').locator('.cm-content');
  await step(`open code editor ${name}`, async () => {
    await selectEditorMode(dialog, 'code');
    await expect(editor).toBeVisible({timeout: LONG_UI_TIMEOUT});
  });

  await step(`set HTTPS routes ${name}`, async () => {
    // Read/write through the CodeMirror view: innerText is layout-dependent
    // (soft-wrapped lines gain newlines) and long https(...) lines wrap.
    const text = await editor.evaluate(el => {
      const view = el.cmTile?.root?.view || el.cmView?.view;
      return view.state.doc.toString();
    });
    await editor.evaluate((el, next) => {
      const view = el.cmTile?.root?.view || el.cmView?.view;
      view.dispatch({changes: {from: 0, to: view.state.doc.length, insert: next}});
    }, withHttpsRoutes(text, routes));
    // The code widget reports no syntax or schema diagnostics once the
    // written text is accepted.
    await expect(dialog.getByText('0 diagnostics', {exact: true})).toBeVisible({timeout: LONG_UI_TIMEOUT});
  });

  const submit = dialog.getByRole('button', {name: 'Update deployment'});
  if (expectError) {
    await step(`expect rejected update ${name}`, async () => {
      await expect(submit).toBeEnabled({timeout: LONG_UI_TIMEOUT});
      const updateResponse = page.waitForResponse(response => {
        const request = response.request();
        return request.method() === 'POST' && new URL(request.url()).pathname === '/v2/deployments/update';
      }, {timeout: LONG_UI_TIMEOUT});
      await submit.click();
      expect((await updateResponse).ok()).toBe(false);
      await expect(dialog).toBeVisible();
      await expect(dialog.getByText(expectError)).toBeVisible({timeout: LONG_UI_TIMEOUT});
      await dialog.getByRole('button', {name: 'Cancel'}).click();
      await expect(dialog).toBeHidden({timeout: LONG_UI_TIMEOUT});
    });
    return;
  }

  await step(`submit HTTPS routes ${name}`, async () => {
    await expect(submit).toBeEnabled({timeout: LONG_UI_TIMEOUT});
    const updateResponse = page.waitForResponse(response => {
      const request = response.request();
      return request.method() === 'POST' && new URL(request.url()).pathname === '/v2/deployments/update';
    }, {timeout: LONG_UI_TIMEOUT});
    await submit.click();
    expect((await updateResponse).ok()).toBe(true);
    await expect(dialog).toBeHidden({timeout: LONG_UI_TIMEOUT});
  });
}

export function withHttpsRoutes(text, routes) {
  const lines = text.split('\n').filter(line => !/^\s*https\(/.test(line));
  const routeLines = routes.map(route => `      ${route},`);
  const openIndex = lines.findIndex(line => line.trim() === 'ingress = [');
  if (openIndex >= 0) {
    if (routes.length) {
      lines.splice(openIndex + 1, 0, ...routeLines);
    } else if (lines[openIndex + 1]?.trim() === ']') {
      const start = lines[openIndex - 1]?.trim() === '' ? openIndex - 1 : openIndex;
      lines.splice(start, openIndex + 2 - start);
    }
    return lines.join('\n');
  }
  if (!routes.length) return lines.join('\n');
  const modeIndex = lines.findIndex(line => /^\s*mode = /.test(line));
  if (modeIndex < 0) throw new Error('network mode attribute not found in deployment HCL');
  lines.splice(modeIndex + 1, 0, '', '    ingress = [', ...routeLines, '    ]');
  return lines.join('\n');
}

export function issuedTLSMountLine({containerPath, extraNames = [], caOnly = false} = {}) {
  const options = [];
  if (extraNames.length) options.push(`extra_names = [${extraNames.map(name => JSON.stringify(name)).join(', ')}]`);
  if (caOnly) options.push('ca_only = true');
  const source = options.length ? `issued_tls({ ${options.join(', ')} })` : 'issued_tls()';
  return `mount(${source}, ${JSON.stringify(containerPath)})`;
}

export function withIssuedTLSMounts(text, mountLines) {
  const lines = text.split('\n').filter(line => !/^\s*mount\(issued_tls/.test(line));
  if (!mountLines.length) return lines.join('\n');
  const openIndex = lines.findIndex(line => line.trim() === 'mounts = [');
  if (openIndex < 0) throw new Error('mounts block not found in deployment HCL');
  lines.splice(openIndex + 1, 0, ...mountLines.map(line => `      ${line},`));
  return lines.join('\n');
}

async function openDeploymentHclEditor(page, {name, machine}) {
  await step(`open update dialog ${name}`, async () => {
    await byTestId(page, 'nav-status', page.getByText('Deployments')).click();
    const row = deploymentRow(page, {name, machine});
    await expect(row).toBeVisible({timeout: LONG_UI_TIMEOUT});
    await row.getByRole('button', {name: 'Update'}).click();
  });

  const dialog = editorPanel(page, 'update-deployment-dialog');
  await expect(dialog).toBeVisible();

  const editor = dialog.getByTestId('deployment-hcl-editor').locator('.cm-content');
  await step(`open code editor ${name}`, async () => {
    await selectEditorMode(dialog, 'code');
    await expect(editor).toBeVisible({timeout: LONG_UI_TIMEOUT});
  });
  return {dialog, editor};
}

async function readDeploymentHcl(editor) {
  // Read/write through the CodeMirror view: innerText is layout-dependent
  // (soft-wrapped lines gain newlines) and long mount(...) lines wrap.
  return editor.evaluate(el => {
    const view = el.cmTile?.root?.view || el.cmView?.view;
    return view.state.doc.toString();
  });
}

async function writeDeploymentHcl(editor, next) {
  await editor.evaluate((el, text) => {
    const view = el.cmTile?.root?.view || el.cmView?.view;
    view.dispatch({changes: {from: 0, to: view.state.doc.length, insert: text}});
  }, next);
}

export async function setDeploymentIssuedTLSMount(page, {name, machine = 'worker-1', mount, expectError} = {}) {
  const {dialog, editor} = await openDeploymentHclEditor(page, {name, machine});

  await step(`set issued TLS mount ${name}`, async () => {
    const text = await readDeploymentHcl(editor);
    await writeDeploymentHcl(editor, withIssuedTLSMounts(text, mount ? [issuedTLSMountLine(mount)] : []));
    // The code widget reports no syntax or schema diagnostics once the
    // written text is accepted.
    await expect(dialog.getByText('0 diagnostics', {exact: true})).toBeVisible({timeout: LONG_UI_TIMEOUT});
  });

  const submit = dialog.getByRole('button', {name: 'Update deployment'});
  if (expectError) {
    await step(`expect rejected update ${name}`, async () => {
      await expect(submit).toBeEnabled({timeout: LONG_UI_TIMEOUT});
      const updateResponse = page.waitForResponse(response => {
        const request = response.request();
        return request.method() === 'POST' && new URL(request.url()).pathname === '/v2/deployments/update';
      }, {timeout: LONG_UI_TIMEOUT});
      await submit.click();
      expect((await updateResponse).ok()).toBe(false);
      await expect(dialog).toBeVisible();
      await expect(dialog.getByText(expectError).first()).toBeVisible({timeout: LONG_UI_TIMEOUT});
      await dialog.getByRole('button', {name: 'Cancel'}).click();
      await expect(dialog).toBeHidden({timeout: LONG_UI_TIMEOUT});
    });
    return;
  }

  await step(`submit issued TLS mount ${name}`, async () => {
    await expect(submit).toBeEnabled({timeout: LONG_UI_TIMEOUT});
    const updateResponse = page.waitForResponse(response => {
      const request = response.request();
      return request.method() === 'POST' && new URL(request.url()).pathname === '/v2/deployments/update';
    }, {timeout: LONG_UI_TIMEOUT});
    await submit.click();
    expect((await updateResponse).ok()).toBe(true);
    await expect(dialog).toBeHidden({timeout: LONG_UI_TIMEOUT});
  });
}

export async function expectIssuedTLSHclDiagnostics(page, {name, machine = 'worker-1', checks = []} = {}) {
  const {dialog, editor} = await openDeploymentHclEditor(page, {name, machine});
  const original = await readDeploymentHcl(editor);

  for (const {mounts, diagnostic} of checks) {
    await step(`expect diagnostic: ${diagnostic}`, async () => {
      await writeDeploymentHcl(editor, withIssuedTLSMounts(original, mounts));
      // The message renders both in the diagnostics list and as the submit
      // blocker paragraph, so assert on the first match.
      await expect(dialog.getByText(diagnostic).first()).toBeVisible({timeout: LONG_UI_TIMEOUT});
      await writeDeploymentHcl(editor, original);
      await expect(dialog.getByText(diagnostic)).toHaveCount(0, {timeout: LONG_UI_TIMEOUT});
    });
  }

  await step(`cancel update dialog ${name}`, async () => {
    await dialog.getByRole('button', {name: 'Cancel'}).click();
    await expect(dialog).toBeHidden({timeout: LONG_UI_TIMEOUT});
  });
}

export function withPortForwardAllow(text, allow) {
  const lines = text.split('\n');
  const index = lines.findIndex(line => line.includes('port_forward('));
  if (index < 0) throw new Error('port_forward route not found in deployment HCL');
  const match = lines[index].match(/^(\s*)port_forward\(("[^"]+"),\s*(\d+)(?:,\s*\{\s*(.*?)\s*\})?\)(,?)\s*$/);
  if (!match) throw new Error(`unrecognized port_forward line: ${lines[index].trim()}`);
  const [, indent, protocol, containerPort, optionsBody = '', comma] = match;
  const kept = optionsBody
    .replace(/allow\s*=\s*\[[^\]]*\]/, '')
    .split(',')
    .map(part => part.trim())
    .filter(Boolean);
  if (allow.length) kept.push(`allow = [${allow.map(entry => JSON.stringify(entry)).join(', ')}]`);
  const options = kept.length ? `, { ${kept.join(', ')} }` : '';
  lines[index] = `${indent}port_forward(${protocol}, ${containerPort}${options})${comma}`;
  return lines.join('\n');
}

export async function setPortForwardAllowList(page, {name, machine = 'worker-1', allow = [], expectRendered = []} = {}) {
  const {dialog, editor} = await openDeploymentHclEditor(page, {name, machine});

  await step(`set port forward allow list ${name}`, async () => {
    const text = await readDeploymentHcl(editor);
    for (const entry of expectRendered) {
      expect(text, `expected rendered HCL to contain allow entry ${entry}`).toContain(JSON.stringify(entry));
    }
    await writeDeploymentHcl(editor, withPortForwardAllow(text, allow));
    // The code widget reports no syntax or schema diagnostics once the
    // written text is accepted.
    await expect(dialog.getByText('0 diagnostics', {exact: true})).toBeVisible({timeout: LONG_UI_TIMEOUT});
  });

  await step(`submit port forward allow list ${name}`, async () => {
    const submit = dialog.getByRole('button', {name: 'Update deployment'});
    await expect(submit).toBeEnabled({timeout: LONG_UI_TIMEOUT});
    const updateResponse = page.waitForResponse(response => {
      const request = response.request();
      return request.method() === 'POST' && new URL(request.url()).pathname === '/v2/deployments/update';
    }, {timeout: LONG_UI_TIMEOUT});
    await submit.click();
    expect((await updateResponse).ok()).toBe(true);
    await expect(dialog).toBeHidden({timeout: LONG_UI_TIMEOUT});
  });
}

export async function expectPortForwardAllowDiagnostics(page, {name, machine = 'worker-1', checks = []} = {}) {
  const {dialog, editor} = await openDeploymentHclEditor(page, {name, machine});
  const original = await readDeploymentHcl(editor);

  for (const {allow, diagnostic} of checks) {
    await step(`expect diagnostic: ${diagnostic}`, async () => {
      await writeDeploymentHcl(editor, withPortForwardAllow(original, allow));
      await expect(dialog.getByText(diagnostic).first()).toBeVisible({timeout: LONG_UI_TIMEOUT});
      await writeDeploymentHcl(editor, original);
      await expect(dialog.getByText(diagnostic)).toHaveCount(0, {timeout: LONG_UI_TIMEOUT});
    });
  }

  await step(`cancel update dialog ${name}`, async () => {
    await dialog.getByRole('button', {name: 'Cancel'}).click();
    await expect(dialog).toBeHidden({timeout: LONG_UI_TIMEOUT});
  });
}

export async function expectDeploymentHttpsIngressRows(page, {name, machine = 'worker-2', count} = {}) {
  await byTestId(page, 'nav-status', page.getByText('Deployments')).click();
  const row = deploymentRow(page, {name, machine});
  await expect(row).toBeVisible({timeout: LONG_UI_TIMEOUT});
  await row.getByRole('button', {name: 'Update'}).click();
  const dialog = editorPanel(page, 'update-deployment-dialog');
  await expect(dialog).toBeVisible();
  await selectEditorMode(dialog, 'ui');
  const pane = await openDeploymentNetworkingPane(dialog);
  await expect(pane.getByTestId('deployment-https-ingress-row')).toHaveCount(count, {timeout: LONG_UI_TIMEOUT});
  await pane.getByTitle('Close').click();
  await dialog.getByRole('button', {name: 'Cancel'}).click();
  await expect(dialog).toBeHidden({timeout: LONG_UI_TIMEOUT});
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

export async function upgradeOpenDeployAgents(page, {
  version,
  workerNames = ['worker-1', 'worker-2'],
  afterWorkerUpgrade,
  afterUpgrade,
} = {}) {
  if (!version) throw new Error('upgrade version is required');
  for (const workerName of workerNames) {
    await upgradeOpenDeployAgent(page, {machine: workerName, version});
    await expectOpenDeployAgentVersion(page, {machine: workerName, version});
    await expectMachineConnected(page, workerName);
    if (afterWorkerUpgrade) await afterWorkerUpgrade(workerName);
  }
  await upgradeOpenDeployAgent(page, {machine: 'primary', version});
  await waitForHealthyApp(page);
  await waitForLoadableApp(page);
  await expectAuthenticatedDeploymentsPage(page);
  await expectOpenDeployAgentVersion(page, {machine: 'primary', version});
  for (const workerName of workerNames) await expectOpenDeployAgentVersion(page, {machine: workerName, version});
  if (afterUpgrade) await afterUpgrade();
}

export async function upgradeOpenDeployNet(page, {machine, version} = {}) {
  if (!machine) throw new Error('machine is required');
  if (!version) throw new Error('upgrade version is required');
  await upgradeOpenDeployDeployment(page, {name: 'opendeploy-net', machine, version});
  await expectOpenDeployDeploymentVersion(page, {name: 'opendeploy-net', machine, version});
}

// upgradeOpenDeployNetGroup upgrades every node's opendeploy-net deployment in
// a single overlay run with "Align versions" left on: the primary's dropdown
// drives all rows and the overlay rolls the whole group itself, secondaries
// first, primary last. The opendeploy agent group is deliberately untouched.
export async function upgradeOpenDeployNetGroup(page, {version} = {}) {
  if (!version) throw new Error('upgrade version is required');
  await byTestId(page, 'nav-status', page.getByText('Deployments')).click();
  await showOpendeployDeployments(page);
  const row = page.getByTestId('deployment-row-opendeploy-net');
  await expect(row).toBeVisible({timeout: LONG_UI_TIMEOUT});
  await row.getByRole('button', {name: 'Update'}).click();

  const dialog = editorPanel(page, 'update-deployment-dialog');
  await expect(dialog).toBeVisible();
  const primarySelect = dialog.getByTestId('deployment-target-version-opendeploy-net-primary');
  await expect.poll(async () => {
    return await primarySelect.locator('option').evaluateAll(options => options.map(o => o.value));
  }, {message: `expected ${version} release option`, timeout: RELEASE_OPTIONS_TIMEOUT}).toContain(version);
  const alignToggle = dialog.getByTestId('align-versions-toggle');
  if (!await alignToggle.isChecked()) await alignToggle.check();
  await primarySelect.selectOption(version);
  await dialog.getByRole('button', {name: 'Upgrade'}).click();
  await expect(dialog.getByTestId('deployment-upgrade-complete')).toBeVisible({timeout: UPGRADE_TIMEOUT});
  await dialog.getByRole('button', {name: 'Close', exact: true}).click();
  await expect(dialog).toBeHidden({timeout: LONG_UI_TIMEOUT});
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
    networkingMode: NETWORKING_VIRTUAL,
  });
  await expectDeploymentRunning(page, name);
  await expectDeploymentOutput(page, name, ['database system is ready to accept connections']);
}

export async function createPostgresClientDeployment(page, {
  name = 'postgresclient',
  machine = 'worker-1',
  postgresHost = 'postgres18.space-1.internal',
} = {}) {
  await createNixDockerDeployment(page, {
    name,
    machine,
    flake: 'testexamples/postgresclient/flake.nix',
    networkingMode: NETWORKING_VIRTUAL,
    env: {
      PGHOST: postgresHost,
      PGPORT: '5432',
      PGUSER: {type: 'secret', name: 'postgres'},
      PGPASSWORD: {type: 'secret', name: 'postgrespass'},
      PGDATABASE: 'postgres',
    },
    expectedEnv: {},
  });
  // An address-typed host resolves to a ULA the test cannot predict, so only
  // its fd prefix is asserted.
  const expectedHost = postgresHost?.type === 'address' ? /^fd/ : postgresHost;
  await expectDeploymentOutputRecords(page, name, [
    ['postgresclient starting', {host: expectedHost}],
    ['postgresclient row', {id: '1', name: 'alpha'}],
    ['postgresclient row', {id: '2', name: 'bravo'}],
    ['postgresclient row', {id: '3', name: 'charlie'}],
    ['postgresclient verified rows', {count: '3'}],
  ]);
}

export async function stopDeployment(page, {name, machine = 'worker-1'} = {}) {
  await updateNixDockerDeployment(page, {name, machine, desiredRunning: false});
  await expectDeploymentStopped(page, {name, machine});
}

export async function deleteDeployment(page, {name, machine = 'worker-1'} = {}) {
  await byTestId(page, 'nav-status', page.getByText('Deployments')).click();
  const row = deploymentRow(page, {name, machine});
  await expect(row).toBeVisible({timeout: LONG_UI_TIMEOUT});
  await row.locator('td').first().click();
  const inspector = page.getByTestId('deployment-inspector');
  await expect(inspector).toBeVisible();
  await inspector.getByRole('button', {name: 'Delete', exact: true}).click();
  const overlay = page.getByTestId('deployment-delete-overlay');
  await expect(overlay).toBeVisible();
  const response = page.waitForResponse(res => {
    const request = res.request();
    return request.method() === 'POST' && new URL(request.url()).pathname === '/v1/deployments/delete';
  }, {timeout: LONG_UI_TIMEOUT});
  await overlay.getByRole('button', {name: 'Delete', exact: true}).click();
  expect((await response).ok()).toBe(true);
  await expect(row).toBeHidden({timeout: LONG_UI_TIMEOUT});
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
  await createSecretOrConfig(page, {type: 'config', name, value});
}

export async function createSecret(page, {name, value} = {}) {
  await createSecretOrConfig(page, {type: 'secret', name, value});
}

export async function rotateSecret(page, {name, value, referencingDeployments} = {}) {
  await byTestId(page, 'nav-secrets', page.getByText('Secrets / Configs')).click();
  const row = page.getByRole('row', {name: new RegExp(escapeRegExp(name))});
  await expect(row).toBeVisible({timeout: LONG_UI_TIMEOUT});
  await row.getByRole('button', {name: `Edit ${name}`, exact: true}).click();

  const dialog = page.getByTestId('resource-value-overlay').getByRole('dialog');
  await expect(dialog).toBeVisible({timeout: LONG_UI_TIMEOUT});
  await fillCodeEditor(dialog, `Value for ${name}`, value);
  if (referencingDeployments !== undefined) {
    const toggle = dialog.getByRole('switch', {name: `Update ${referencingDeployments} referencing deployments`});
    await expect(toggle).toBeVisible();
    await toggle.click();
    await expect(toggle).toHaveAttribute('aria-checked', 'true');
  }
  const response = page.waitForResponse(res => {
    const request = res.request();
    return request.method() === 'POST' && new URL(request.url()).pathname === '/v1/secrets/set';
  }, {timeout: LONG_UI_TIMEOUT});
  await dialog.getByRole('button', {name: /Save version \d+/}).click();
  expect((await response).ok()).toBe(true);
  await expect(dialog).toBeHidden({timeout: LONG_UI_TIMEOUT});
}

async function createSecretOrConfig(page, {type, name, value}) {
  await byTestId(page, 'nav-secrets', page.getByText('Secrets / Configs')).click();
  await page.getByRole('button', {name: `New ${type}`, exact: true}).click();

  const dialog = page.getByTestId(`create-${type}-overlay`).getByRole('dialog');
  await dialog.getByPlaceholder(`${type} name`).fill(name);
  await fillCodeEditor(dialog, `Value for new ${type}`, value);
  await dialog.getByRole('button', {name: `Add ${type}`}).click();
  await expect(dialog).toBeHidden({timeout: LONG_UI_TIMEOUT});
  await expect(page.getByRole('row', {name: new RegExp(escapeRegExp(name))})).toBeVisible({timeout: LONG_UI_TIMEOUT});
}

export async function expectReferenceUsage(page, {
  resourceType,
  resourceName,
  deploymentName,
  space = 'global',
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
  // Deployment env pickers label references as "<space> / <name> vN"; settings
  // pickers keep the bare "<name> vN" form.
  return new RegExp(`^(.+ / )?${escapedName}( v\\d+)?$`);
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

export async function createContainerImageDeployment(page, {
  name,
  machine,
  image,
  env = {},
  dataMountPath = '',
  networkingMode = NETWORKING_HOST,
  portForwarding = [],
  assetMount,
} = {}) {
  await step(`open container deployment dialog ${name}`, async () => {
    await byTestId(page, 'nav-status', page.getByText('Deployments')).click();
    await byTestId(page, 'add-deployment-button', page.getByRole('button', {name: 'Add deployment'})).click();
  });

  const dialog = editorPanel(page, 'create-deployment-dialog');
  await expect(dialog).toBeVisible();
  await selectEditorMode(dialog, 'ui');

  await step(`fill container deployment ${name}`, async () => {
    await byTestId(dialog, 'deployment-name-input', textField(dialog, 'Name')).fill(name);
    await selectDeploymentNode(dialog, machine);
    await setDeploymentNetworkingMode(dialog, networkingMode);
    await setDeploymentPortForwarding(dialog, portForwarding);
    await byTestId(dialog, 'deployment-source-type-select', selectField(dialog, 'Source type')).selectOption('containerImage');
    await byTestId(dialog, 'deployment-container-image-input', textField(dialog, 'Image')).fill(image);
    // A running deployment needs a validated source even when the image
    // reference pins its own tag.
    await dialog.getByTestId('source-validate-button').click();
    await expectSourceStatus(dialog, 'Source valid');
    await setDeploymentEnvVars(dialog, env);
    if (dataMountPath) await setDeploymentDataMountPath(dialog, dataMountPath);
    if (assetMount) await setDeploymentAssetMount(dialog, assetMount);
  });
  const submit = byTestId(dialog, 'create-deployment-submit', dialog.getByRole('button', {name: 'Create'}));
  await step(`submit container deployment ${name}`, async () => {
    await expect(submit).toBeEnabled({timeout: LONG_UI_TIMEOUT});
    const createResponse = page.waitForResponse(response => {
      const request = response.request();
      return request.method() === 'POST' && new URL(request.url()).pathname === '/v1/deployments/create';
    }, {timeout: LONG_UI_TIMEOUT});
    await submit.click();
    expect((await createResponse).ok()).toBe(true);
    await expect(dialog).toBeHidden({timeout: LONG_UI_TIMEOUT});
  });

  const row = deploymentRow(page, {name, machine});
  await step(`wait for container deployment row ${name}`, () => expect(row).toBeVisible({timeout: LONG_UI_TIMEOUT}));
}

export async function createAsset(page, {key, content} = {}) {
  await byTestId(page, 'nav-assets', page.getByText('Assets')).click();
  await expect(page.getByPlaceholder('Search assets')).toBeVisible();
  await page.getByRole('button', {name: 'New asset', exact: true}).click();

  await page.getByLabel('New asset name').fill(key);
  await fillCodeEditor(page, `Content for asset ${key}`, content);
  const createResponse = page.waitForResponse(response => {
    const request = response.request();
    return request.method() === 'POST' && new URL(request.url()).pathname === '/v1/assets/upload';
  }, {timeout: LONG_UI_TIMEOUT});
  await page.getByRole('button', {name: 'Create asset'}).click();
  expect((await createResponse).ok()).toBe(true);
  // The modal editor closes itself after a successful create.
  await expect(page.getByRole('button', {name: 'Create asset'})).toBeHidden({timeout: LONG_UI_TIMEOUT});
  await expect(page.getByRole('row', {name: new RegExp(escapeRegExp(key))})).toBeVisible();
}

export async function updateAsset(page, {key, content} = {}) {
  await byTestId(page, 'nav-assets', page.getByText('Assets')).click();
  const row = page.getByRole('row', {name: new RegExp(escapeRegExp(key))});
  await expect(row).toBeVisible({timeout: LONG_UI_TIMEOUT});
  await row.getByRole('button', {name: `Edit asset ${key}`}).click();
  await fillCodeEditor(page, `Content for asset ${key}`, content);
  const response = page.waitForResponse(res => {
    const request = res.request();
    return request.method() === 'POST' && new URL(request.url()).pathname === '/v1/assets/upload';
  }, {timeout: LONG_UI_TIMEOUT});
  await page.getByRole('button', {name: /Save version \d+/}).click();
  expect((await response).ok()).toBe(true);
  // The modal editor closes itself after a successful save.
  await expect(page.getByRole('button', {name: 'Close editor'})).toBeHidden({timeout: LONG_UI_TIMEOUT});
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
  const overlay = page.locator('.fixed.inset-0.z-50').filter({hasText: 'Upload asset'}).last();
  await expect(overlay).toBeVisible();
  const uploadResponse = page.waitForResponse(response => {
    const request = response.request();
    return request.method() === 'POST' && new URL(request.url()).pathname === '/v1/assets/upload';
  }, {timeout: ASSET_UPLOAD_TIMEOUT});
  await overlay.getByRole('button', {name: 'Upload'}).click();
  expect((await uploadResponse).ok()).toBe(true);
  const assetRow = page.getByRole('row', {name: new RegExp(escapeRegExp(fileName))});
  await expect(assetRow).toBeVisible({timeout: ASSET_UPLOAD_TIMEOUT});
  const closeButton = overlay.getByRole('button', {name: 'Close'});
  if (await closeButton.isVisible().catch(() => false)) {
    await closeButton.click();
    await expect(overlay).toBeHidden({timeout: LONG_UI_TIMEOUT});
  }
  await assetRow.getByRole('button', {name: `Edit asset ${fileName}`}).click();
  await expect(page.getByText(/[0-9.]+ (B|KB|MB|GB|TB) large asset/)).toBeVisible({timeout: LONG_UI_TIMEOUT});
  // The editor is a modal now; leave the page clear for the next case.
  await page.getByRole('button', {name: 'Close editor'}).click();
  await expect(page.getByRole('button', {name: 'Close editor'})).toBeHidden({timeout: LONG_UI_TIMEOUT});
}

// ---- Explorer directory helpers ----
//
// The secrets/configs and assets pages share one Finder-style explorer: rows
// select, the inspector on the right carries Move/Rename/Delete, and the
// pathbar (`explorer-pathbar`) echoes the selection's full location. These
// helpers drive that shared surface; the caller has already navigated to the
// right page.

export function explorerPathbar(page) {
  return page.getByTestId('explorer-pathbar');
}

export async function expectExplorerPath(page, pathText) {
  await expect(explorerPathbar(page)).toContainText(pathText, {timeout: LONG_UI_TIMEOUT});
}

export async function selectExplorerRow(page, name) {
  const row = page.getByRole('row', {name: new RegExp(escapeRegExp(name))}).first();
  await expect(row).toBeVisible({timeout: LONG_UI_TIMEOUT});
  await row.click();
  return row;
}

// Creates a folder under the current selection (a selected space's root, or a
// selected folder), which is how the toolbar button targets a parent.
export async function createExplorerFolder(page, name) {
  await page.getByRole('button', {name: 'New folder', exact: true}).click();
  const dialog = page.getByRole('dialog').filter({hasText: 'New folder'});
  await dialog.getByLabel('Folder name', {exact: true}).fill(name);
  await dialog.getByRole('button', {name: 'Create', exact: true}).click();
  await expect(dialog).toBeHidden({timeout: LONG_UI_TIMEOUT});
}

// Moves the selected row via the inspector's Move dialog. destination is the
// option label: '/' for the space root, otherwise the folder's full path.
export async function moveExplorerSelection(page, destination) {
  await page.getByRole('button', {name: 'Move', exact: true}).click();
  const dialog = page.getByRole('dialog').filter({hasText: /Move /});
  await expect(dialog).toBeVisible({timeout: LONG_UI_TIMEOUT});
  await dialog.getByRole('button', {name: destination, exact: true}).click();
  await expect(dialog).toBeHidden({timeout: LONG_UI_TIMEOUT});
}

// Moves the selected item into another space through the Move dialog's
// destination space picker. destination names the folder option inside that
// space ('/' for the space root — a fresh space pick always resets to it).
// Only item dialogs offer the picker; folder moves stay within their space.
export async function moveExplorerSelectionToSpace(page, {space, destination = '/'}) {
  await page.getByRole('button', {name: 'Move', exact: true}).click();
  const dialog = page.getByRole('dialog').filter({hasText: /Move /});
  await expect(dialog).toBeVisible({timeout: LONG_UI_TIMEOUT});
  await dialog.getByLabel('Destination space', {exact: true}).selectOption({label: space});
  await dialog.getByRole('button', {name: destination, exact: true}).click();
  await expect(dialog).toBeHidden({timeout: LONG_UI_TIMEOUT});
}

// Attempts a cross-space move the server must refuse. The dialog stays open on
// failure and the page error line carries the refusal; the error is dismissed
// after the dialog is cancelled because the modal overlay covers the banner.
export async function expectMoveToSpaceBlocked(page, {space, destination = '/', message}) {
  await page.getByRole('button', {name: 'Move', exact: true}).click();
  const dialog = page.getByRole('dialog').filter({hasText: /Move /});
  await expect(dialog).toBeVisible({timeout: LONG_UI_TIMEOUT});
  await dialog.getByLabel('Destination space', {exact: true}).selectOption({label: space});
  await dialog.getByRole('button', {name: destination, exact: true}).click();
  await expect(page.getByText(message)).toBeVisible({timeout: LONG_UI_TIMEOUT});
  await dialog.getByRole('button', {name: 'Cancel', exact: true}).click();
  await expect(dialog).toBeHidden({timeout: LONG_UI_TIMEOUT});
  await page.getByRole('button', {name: 'Dismiss error'}).click();
}

// Deletes the selection expecting the server to refuse (e.g. a mounted
// asset): the confirm dialog closes itself, the error line reports, and the
// caller asserts the row survived.
export async function expectDeleteSelectionBlocked(page, {message}) {
  await page.getByRole('button', {name: 'Delete', exact: true}).click();
  const dialog = page.getByRole('dialog').filter({hasText: 'Confirm delete'});
  await dialog.getByRole('button', {name: 'Confirm', exact: true}).click();
  await expect(dialog).toBeHidden({timeout: LONG_UI_TIMEOUT});
  await expect(page.getByText(message)).toBeVisible({timeout: LONG_UI_TIMEOUT});
  await page.getByRole('button', {name: 'Dismiss error'}).click();
}

// Filters the explorer to name and selects its row. An active query
// force-expands whatever matches, so this reaches rows whose space or folder
// is collapsed in the caller's persisted view.
export async function searchExplorerAndSelect(page, placeholder, name) {
  await page.getByPlaceholder(placeholder).fill(name);
  await selectExplorerRow(page, name);
}

export async function clearExplorerSearch(page, placeholder) {
  await page.getByPlaceholder(placeholder).fill('');
}

export async function renameExplorerSelection(page, newName) {
  await page.getByRole('button', {name: 'Rename', exact: true}).click();
  const nameInput = page.getByLabel('New name', {exact: true});
  await nameInput.fill(newName);
  await nameInput.press('Enter');
  await expect(nameInput).toBeHidden({timeout: LONG_UI_TIMEOUT});
}

export async function deleteExplorerSelection(page) {
  await page.getByRole('button', {name: 'Delete', exact: true}).click();
  const dialog = page.getByRole('dialog').filter({hasText: 'Confirm delete'});
  await dialog.getByRole('button', {name: 'Confirm', exact: true}).click();
  await expect(dialog).toBeHidden({timeout: LONG_UI_TIMEOUT});
}

export async function closeExplorerInspector(page) {
  await page.getByRole('button', {name: 'Close details', exact: true}).click();
}

// Creates a secret/config through the toolbar against the current selection's
// folder, asserting the create dialog names that location.
export async function createValueInSelection(page, {type, name, value, location}) {
  await page.getByRole('button', {name: `New ${type}`, exact: true}).click();
  const dialog = page.getByTestId(`create-${type}-overlay`).getByRole('dialog');
  if (location) {
    // location reads `<space>/<folders...>/`. The dialog renders the space as
    // a select whose text contains every visible space, so assert the chosen
    // option and the folder-path label separately.
    const [space, ...folders] = location.split('/').filter(Boolean);
    await expect(dialog.getByLabel('Destination space', {exact: true}).locator('option:checked'))
      .toHaveText(space, {timeout: LONG_UI_TIMEOUT});
    if (folders.length) await expect(dialog).toContainText(`/${folders.join('/')}/`);
  }
  await dialog.getByPlaceholder(`${type} name`).fill(name);
  await fillCodeEditor(dialog, `Value for new ${type}`, value);
  await dialog.getByRole('button', {name: `Add ${type}`}).click();
  await expect(dialog).toBeHidden({timeout: LONG_UI_TIMEOUT});
}

// Walks the secrets/configs explorer through the full folder lifecycle:
// nested folders, creating items inside them, moving items in and out, moving
// and renaming a folder, and both the empty-delete and non-empty-rejection
// paths.
export async function verifyValueDirectoryExplorer(page) {
  await byTestId(page, 'nav-secrets', page.getByText('Secrets / Configs')).click();
  await expect(page.getByPlaceholder('Search secrets / configs')).toBeVisible();

  await test.step('create nested folders', async () => {
    await selectExplorerRow(page, 'global');
    await createExplorerFolder(page, 'e2e-folder-a');
    await expectExplorerPath(page, 'global/e2e-folder-a');
    await createExplorerFolder(page, 'e2e-folder-b');
    await expectExplorerPath(page, 'global/e2e-folder-a/e2e-folder-b');
  });

  await test.step('create config and secret inside folders', async () => {
    await createValueInSelection(page, {
      type: 'config', name: 'e2e.dir.config', value: 'dir-config-value',
      location: 'global/e2e-folder-a/e2e-folder-b/',
    });
    await expectExplorerPath(page, 'global/e2e-folder-a/e2e-folder-b/e2e.dir.config');
    await selectExplorerRow(page, 'e2e-folder-a');
    await createValueInSelection(page, {
      type: 'secret', name: 'e2e.dir.secret', value: 'dir-secret-value',
      location: 'global/e2e-folder-a/',
    });
    await expectExplorerPath(page, 'global/e2e-folder-a/e2e.dir.secret');
  });

  await test.step('move config out to the root and back into a folder', async () => {
    await selectExplorerRow(page, 'e2e.dir.config');
    await moveExplorerSelection(page, '/');
    await expectExplorerPath(page, 'global/e2e.dir.config');
    await expect(explorerPathbar(page)).not.toContainText('e2e-folder-b');
    await moveExplorerSelection(page, 'e2e-folder-a');
    await expectExplorerPath(page, 'global/e2e-folder-a/e2e.dir.config');
  });

  await test.step('move, rename, and delete a folder', async () => {
    await selectExplorerRow(page, 'e2e-folder-b');
    await moveExplorerSelection(page, '/');
    await expectExplorerPath(page, 'global/e2e-folder-b');
    await moveExplorerSelection(page, 'e2e-folder-a');
    await expectExplorerPath(page, 'global/e2e-folder-a/e2e-folder-b');
    await renameExplorerSelection(page, 'e2e-folder-c');
    await expectExplorerPath(page, 'global/e2e-folder-a/e2e-folder-c');
    await deleteExplorerSelection(page);
    await expect(page.getByRole('row', {name: /e2e-folder-c/})).toBeHidden({timeout: LONG_UI_TIMEOUT});
  });

  await test.step('non-empty folder delete is rejected', async () => {
    await selectExplorerRow(page, 'e2e-folder-a');
    await deleteExplorerSelection(page);
    await expect(page.getByText(/Folder is not empty/)).toBeVisible({timeout: LONG_UI_TIMEOUT});
    await page.getByRole('button', {name: 'Dismiss error'}).click();
    await expect(page.getByRole('row', {name: /e2e\.dir\.config/})).toBeVisible();
    await expect(page.getByRole('row', {name: /e2e\.dir\.secret/})).toBeVisible();
    await closeExplorerInspector(page);
  });
}

// Walks the assets explorer through the same folder lifecycle, plus moving a
// non-empty folder to prove the subtree travels with it.
export async function verifyAssetDirectoryExplorer(page) {
  await byTestId(page, 'nav-assets', page.getByText('Assets')).click();
  await expect(page.getByPlaceholder('Search assets')).toBeVisible();

  await test.step('create nested folders', async () => {
    await selectExplorerRow(page, 'global');
    await createExplorerFolder(page, 'e2e-asset-folder-a');
    await expectExplorerPath(page, 'global/e2e-asset-folder-a');
    await createExplorerFolder(page, 'e2e-asset-folder-b');
    await expectExplorerPath(page, 'global/e2e-asset-folder-a/e2e-asset-folder-b');
  });

  await test.step('create an asset inside a nested folder', async () => {
    await page.getByRole('button', {name: 'New asset', exact: true}).click();
    await page.getByLabel('New asset name').fill('e2e-dir-asset.txt');
    await fillCodeEditor(page, 'Content for asset e2e-dir-asset.txt', 'dir-asset-content');
    const createResponse = page.waitForResponse(response => {
      const request = response.request();
      return request.method() === 'POST' && new URL(request.url()).pathname === '/v1/assets/upload';
    }, {timeout: LONG_UI_TIMEOUT});
    await page.getByRole('button', {name: 'Create asset'}).click();
    expect((await createResponse).ok()).toBe(true);
    // The modal editor closes itself after a successful create.
    await expect(page.getByRole('button', {name: 'Create asset'})).toBeHidden({timeout: LONG_UI_TIMEOUT});
    await expectExplorerPath(page, 'global/e2e-asset-folder-a/e2e-asset-folder-b/e2e-dir-asset.txt');
  });

  await test.step('move the asset out to the root and back in by path', async () => {
    await moveExplorerSelection(page, '/');
    await expectExplorerPath(page, 'global/e2e-dir-asset.txt');
    await expect(explorerPathbar(page)).not.toContainText('e2e-asset-folder-b');
    await moveExplorerSelection(page, 'e2e-asset-folder-a/e2e-asset-folder-b');
    await expectExplorerPath(page, 'global/e2e-asset-folder-a/e2e-asset-folder-b/e2e-dir-asset.txt');
  });

  await test.step('a moved folder takes its contents with it', async () => {
    await selectExplorerRow(page, 'e2e-asset-folder-b');
    await moveExplorerSelection(page, '/');
    await expectExplorerPath(page, 'global/e2e-asset-folder-b');
    await renameExplorerSelection(page, 'e2e-asset-folder-c');
    await expectExplorerPath(page, 'global/e2e-asset-folder-c');
    await selectExplorerRow(page, 'e2e-dir-asset.txt');
    await expectExplorerPath(page, 'global/e2e-asset-folder-c/e2e-dir-asset.txt');
  });

  await test.step('delete an empty folder; a non-empty delete is rejected', async () => {
    await selectExplorerRow(page, 'e2e-asset-folder-a');
    await deleteExplorerSelection(page);
    await expect(page.getByRole('row', {name: /e2e-asset-folder-a/})).toBeHidden({timeout: LONG_UI_TIMEOUT});
    await selectExplorerRow(page, 'e2e-asset-folder-c');
    await deleteExplorerSelection(page);
    await expect(page.getByText(/Folder is not empty/)).toBeVisible({timeout: LONG_UI_TIMEOUT});
    await page.getByRole('button', {name: 'Dismiss error'}).click();
    await expect(page.getByRole('row', {name: /e2e-dir-asset\.txt/})).toBeVisible();
    await closeExplorerInspector(page);
  });
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

export async function expectDeploymentOutputOccurrences(page, name, text, count) {
  await openDeploymentOutput(page, name);
  await expect.poll(async () => {
    const occurrences = await outputOccurrenceCount(page, text);
    if (occurrences < count) await page.getByTestId('logs-search-button').click();
    return occurrences;
  }, {message: `expected ${name} output to contain ${count} occurrences of ${text}`, timeout: PGBACKREST_TIMEOUT}).toBeGreaterThanOrEqual(count);
}

export async function expectDeploymentOutputDistinctMatches(page, name, pattern, count) {
  await openDeploymentOutput(page, name);
  await expect.poll(async () => {
    const output = await logsResultText(page);
    const distinct = new Set([...output.matchAll(pattern)].map(match => match[1] ?? match[0]));
    if (distinct.size < count) await page.getByTestId('logs-search-button').click();
    return distinct.size;
  }, {message: `expected ${name} output to contain ${count} distinct matches of ${pattern}`, timeout: LOG_OUTPUT_TIMEOUT}).toBeGreaterThanOrEqual(count);
}

// readDeploymentOutputMatch pulls a value out of a deployment's own output —
// used to learn a workload's stable inbound address from the workload itself,
// which is the only cross-space, cross-node way to address it (DNS is
// node-local and address env refs cannot cross a space boundary).
export async function readDeploymentOutputMatch(page, name, pattern) {
  await openDeploymentOutput(page, name);
  let value = null;
  await expect.poll(async () => {
    const output = await logsResultText(page);
    const match = output.match(pattern);
    value = match ? (match[1] ?? match[0]) : null;
    if (!value) await page.getByTestId('logs-search-button').click();
    return value;
  }, {message: `expected ${name} output to match ${pattern}`, timeout: LOG_OUTPUT_TIMEOUT}).not.toBeNull();
  return value;
}

export async function deploymentOutputOccurrenceCount(page, name, text) {
  await openDeploymentOutput(page, name);
  return outputOccurrenceCount(page, text);
}

async function openDeploymentOutput(page, name) {
  await byTestId(page, 'nav-status', page.getByText('Deployments')).click();
  const row = byTestId(page, `deployment-row-${name}`, page.locator('tr').filter({hasText: name}));
  await expect(row).toBeVisible({timeout: LONG_UI_TIMEOUT});
  await openDeploymentLogsSearch(page, row);
}

async function outputOccurrenceCount(page, text) {
  const output = await logsResultText(page);
  return output.split(text).length - 1;
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

// expectHTTPBlocked waits for the URL to stop answering, then requires it to
// stay unreachable for holdMs so a transient recreate gap cannot pass as an
// applied source filter.
export async function expectHTTPBlocked(url, {timeout = DEPLOYMENT_RUNNING_TIMEOUT, holdMs = 4000} = {}) {
  const probe = async () => {
    try {
      await fetch(url, {cache: 'no-store', signal: AbortSignal.timeout(2000)});
      return 'reachable';
    } catch {
      return 'blocked';
    }
  };
  await expect.poll(probe, {message: `expected ${url} to become unreachable`, timeout}).toBe('blocked');
  const deadline = Date.now() + holdMs;
  while (Date.now() < deadline) {
    expect(await probe(), `expected ${url} to stay unreachable`).toBe('blocked');
    await new Promise(resolve => setTimeout(resolve, 500));
  }
}

export async function expectTLSIngress(hostname, {backend, certificateBundle, timeout = DEPLOYMENT_RUNNING_TIMEOUT} = {}) {
  if (!backend || !certificateBundle) throw new Error('TLS ingress backend and certificate bundle are required');
  const expectedBody = `backend=${backend}\nsni=${hostname}\n`;
  const expectedFingerprint = new crypto.X509Certificate(Buffer.from(certificateBundle, 'base64')).fingerprint256;
  await expect.poll(async () => {
    try {
      return await requestTLSIngress(hostname);
    } catch {
      return null;
    }
  }, {message: `expected TLS ingress for ${hostname}`, timeout}).toEqual({
    body: expectedBody,
    fingerprint: expectedFingerprint,
  });
}

export async function expectTLSIngressUnavailable(hostname, {timeout = DEPLOYMENT_RUNNING_TIMEOUT} = {}) {
  await expectTLSProbeRejected(hostname, `expected TLS ingress for ${hostname} to fail closed`, timeout);
}

function requestTLSIngress(hostname) {
  const tunnelHost = process.env.OPD_TLS_INGRESS_HOST || 'host.docker.internal';
  const tunnelPort = Number(process.env.OPD_TLS_INGRESS_PORT || '18443');
  const ca = Buffer.from(process.env.OPD_TLS_INGRESS_CA_B64 || '', 'base64');
  if (ca.length === 0) return Promise.reject(new Error('OPD_TLS_INGRESS_CA_B64 is required'));
  return new Promise((resolve, reject) => {
    let fingerprint;
    const req = https.request({
      host: tunnelHost,
      port: tunnelPort,
      path: '/',
      method: 'GET',
      servername: hostname,
      headers: {host: hostname},
      ca,
      rejectUnauthorized: true,
      agent: false,
    }, response => {
      let body = '';
      response.setEncoding('utf8');
      response.on('data', chunk => { body += chunk; });
      response.on('end', () => {
        if (response.statusCode !== 200) {
          reject(new Error(`TLS ingress ${hostname} returned HTTP ${response.statusCode}`));
          return;
        }
        resolve({body, fingerprint});
      });
    });
    req.on('socket', socket => socket.on('secureConnect', () => {
      fingerprint = socket.getPeerCertificate().fingerprint256;
    }));
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

export async function deploymentRestartCount(page, {name, machine} = {}) {
  await byTestId(page, 'nav-status', page.getByText('Deployments')).click();
  const row = byTestId(page, `deployment-row-${name}`, deploymentRow(page, {name, machine}));
  await expect(row).toBeVisible({timeout: LONG_UI_TIMEOUT});
  const text = await row.getByTestId(`deployment-restarts-${name}`).textContent();
  return Number.parseInt(text || '0', 10) || 0;
}

export async function expectDeploymentRestartCount(page, nameOrOpts, count) {
  const {name, machine, expectedCount} = typeof nameOrOpts === 'string'
    ? {name: nameOrOpts, expectedCount: count}
    : {name: nameOrOpts.name, machine: nameOrOpts.machine, expectedCount: nameOrOpts.count};
  await expect.poll(async () => {
    return await deploymentRestartCount(page, {name, machine});
  }, {message: `expected ${name} to restart ${expectedCount} times`, timeout: RESTART_TIMEOUT}).toBe(expectedCount);
  const row = byTestId(page, `deployment-row-${name}`, deploymentRow(page, {name, machine}));
  await expect(row.getByTitle('View run output').last()).toContainText('Running', {timeout: LONG_UI_TIMEOUT});
}

async function upgradeOpenDeployAgent(page, {machine, version}) {
  return upgradeOpenDeployDeployment(page, {name: 'opendeploy', machine, version});
}

// upgradeOpenDeployDeployment upgrades one node of a system deployment group
// through the merged-row group overlay: Align versions is switched off so only
// the requested node's target changes; unchanged members are skipped by the
// overlay's rollout. The overlay itself waits for the node to report the new
// version, so "done" in its status cell is the convergence signal.
async function upgradeOpenDeployDeployment(page, {name, machine, version}) {
  await byTestId(page, 'nav-status', page.getByText('Deployments')).click();
  await showOpendeployDeployments(page);
  const row = page.getByTestId(`deployment-row-${name}`);
  await expect(row).toBeVisible({timeout: LONG_UI_TIMEOUT});
  await row.getByRole('button', {name: 'Update'}).click();

  const dialog = editorPanel(page, 'update-deployment-dialog');
  await expect(dialog).toBeVisible();
  const targetSelect = dialog.getByTestId(`deployment-target-version-${name}-${machine}`);
  await expect.poll(async () => {
    return await targetSelect.locator('option').evaluateAll(options => options.map(o => o.value));
  }, {message: `expected ${version} release option`, timeout: RELEASE_OPTIONS_TIMEOUT}).toContain(version);
  const alignToggle = dialog.getByTestId('align-versions-toggle');
  if (await alignToggle.isChecked()) await alignToggle.uncheck();
  await targetSelect.selectOption(version);
  await dialog.getByRole('button', {name: 'Upgrade'}).click();
  await expect(dialog.getByTestId(`deployment-upgrade-status-${name}-${machine}`))
    .toHaveText('done', {timeout: UPGRADE_TIMEOUT});
  // Wait for the whole rollout to finish before closing: a bare 'Close' name
  // would also match the mid-run 'Stop and close' button and abort the rollout.
  await expect(dialog.getByTestId('deployment-upgrade-complete')).toBeVisible({timeout: UPGRADE_TIMEOUT});
  await dialog.getByRole('button', {name: 'Close', exact: true}).click();
  await expect(dialog).toBeHidden({timeout: LONG_UI_TIMEOUT});
}

export async function expectOpenDeployAgentVersion(page, {machine, version}) {
  return expectOpenDeployDeploymentVersion(page, {name: 'opendeploy', machine, version});
}

export async function expectOpenDeployNetVersion(page, {machine, version}) {
  return expectOpenDeployDeploymentVersion(page, {name: 'opendeploy-net', machine, version});
}

async function expectOpenDeployDeploymentVersion(page, {name, machine, version}) {
  await byTestId(page, 'nav-status', page.getByText('Deployments')).click();
  await showOpendeployDeployments(page);
  const row = page.getByTestId(`deployment-row-${name}`);
  await expect(row).toBeVisible({timeout: LONG_UI_TIMEOUT});
  await row.locator('td').first().click();
  const inspector = page.getByTestId('deployment-inspector');
  await expect(inspector).toBeVisible();
  // Instance rows are keyed by node; the attribute-prefix match also covers a
  // transient rollover instance that carries an instance-ordinal suffix.
  await expect(inspector.locator(`[data-testid^="deployment-version-${name}-${machine}"]`).last())
    .toContainText(version, {timeout: UPGRADE_TIMEOUT});
  await expect(inspector.locator(`[data-testid^="deployment-runner-status-${name}-${machine}"]`).last())
    .toHaveText('Running', {timeout: UPGRADE_TIMEOUT});
  await inspector.getByLabel('Close inspector').click();
  await expect(inspector).toBeHidden();
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

export async function expectDeploymentRunning(page, opts = {}) {
  const {name, machine} = typeof opts === 'string' ? {name: opts} : opts;
  await byTestId(page, 'nav-status', page.getByText('Deployments')).click();
  const row = deploymentRow(page, {name, machine});
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
  await expect(runnerStatus).toHaveText('Running', {timeout: RUNNER_START_TIMEOUT});
}

export async function expectDeploymentStopped(page, opts = {}) {
  const {name, machine} = typeof opts === 'string' ? {name: opts} : opts;
  await byTestId(page, 'nav-status', page.getByText('Deployments')).click();
  const row = deploymentRow(page, {name, machine});
  await expect(row.getByTestId(`deployment-runner-status-${name}`)).toHaveText('Stopped', {timeout: RESTART_TIMEOUT});
}

async function openDeploymentLogsSearch(page, row) {
  // During a rollover the row shows one status badge per scheduled instance;
  // all of them open the same deployment's run output, so take the newest.
  await row.getByTitle('View run output').last().click();
  await expect(byTestId(page, 'nav-logs', page.getByText('Logs'))).toBeVisible();
  await expect(page.getByTestId('logs-space-filter')).toBeVisible();
  await expect(page.getByTestId('logs-deployment-select')).not.toHaveValue('');
  await expect(page.getByTestId('logs-results')).toBeVisible();
  // Opening the pane does not replace the hook, so a read taken straight after
  // switching deployments can still observe the previous deployment's records —
  // counts and matches would then belong to a workload nobody asked about.
  // Clear and re-issue: the page drops stale responses by generation, so
  // whatever repopulates the hook belongs to the deployment now selected. -1
  // means the response is still in flight; 0 is a legitimate empty result.
  await page.evaluate(() => { window.__logsResult = null; });
  await page.getByTestId('logs-search-button').click();
  await expect.poll(
    async () => page.evaluate(() => window.__logsResult?.records?.length ?? -1),
    {message: 'expected the run output search to land for the deployment just opened', timeout: LOG_OUTPUT_TIMEOUT},
  ).toBeGreaterThanOrEqual(0);
}

async function showOpendeployDeployments(page) {
  const filter = page.getByLabel('Filter spaces');
  if (await filter.count() === 0) return;
  await filter.click();
  const item = page.getByRole('menuitemcheckbox').filter({hasText: '_system'});
  await expect(item).toBeVisible();
  if (await item.getAttribute('aria-checked') !== 'true') {
    await item.click();
  }
  await page.mouse.click(2, 2);
  await expect(item).toBeHidden();
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

// Deployment editors open as tabs on the Deployments page. Hidden tabs stay
// mounted, so an editor is always looked up as the visible panel; the system
// group overlay carries the same test id and resolves the same way.
function editorPanel(page, testID) {
  return page.locator(`[data-testid="${testID}"]:visible`).last();
}

// The editor remembers the last UI/Code choice per browser and opens in Code
// by default, so form-driven helpers switch explicitly.
async function selectEditorMode(dialog, mode) {
  const tab = dialog.getByTestId(`deployment-editor-mode-${mode}`);
  await expect(tab).toBeVisible({timeout: LONG_UI_TIMEOUT});
  if (await tab.getAttribute('aria-selected') !== 'true') await tab.click();
  await expect(tab).toHaveAttribute('aria-selected', 'true', {timeout: LONG_UI_TIMEOUT});
}

// The footer's source status pill reads Source not validated, Validating...,
// Source valid, Source unchanged, or Source invalid.
async function expectSourceStatus(dialog, text) {
  await expect(dialog.getByTestId('source-status-button')).toContainText(text, {timeout: VALIDATE_REQUEST_TIMEOUT});
}

// Picks the index-th row of the footer version dropdown (newest first).
async function selectDeploymentVersion(dialog, index = 0) {
  await dialog.getByTestId('version-select-button').click();
  const options = dialog.getByTestId('version-option');
  await expect(options.first()).toBeVisible({timeout: VALIDATE_REQUEST_TIMEOUT});
  await options.nth(index).click();
  await expect(dialog.getByTestId('version-select-panel')).toBeHidden({timeout: LONG_UI_TIMEOUT});
}

function byTestId(root, testID, fallback) {
  return root.getByTestId(testID).or(fallback).first();
}

async function fillCodeEditor(root, ariaLabel, value) {
  const host = root.getByLabel(ariaLabel, {exact: true});
  await expect(host).toBeVisible({timeout: LONG_UI_TIMEOUT});
  const editor = host.locator('.cm-content');
  await expect(editor).toBeVisible({timeout: LONG_UI_TIMEOUT});
  await editor.fill(String(value ?? ''));
}

async function setDeploymentEnvVars(dialog, env) {
  const entries = Object.entries(env || {}).filter(([key]) => key);
  if (entries.length === 0) return;

  await byTestId(dialog, 'deployment-env-vars-toggle', dialog.getByRole('button', {name: 'View / edit'})).click();
  const pane = dialog.getByRole('heading', {name: 'Environment variables'})
    .locator('xpath=ancestor::div[contains(@class, "border-l")][1]');

  for (const [key, value] of entries) {
    let row = await existingEnvRow(pane, key);
    if (value === null) {
      if (row) await row.getByRole('button', {name: 'Remove'}).click();
      continue;
    }
    if (!row) {
      await pane.getByRole('button', {name: '+ Add environment variable'}).click();
      row = pane.locator('tbody tr').last();
      await row.locator('input').first().fill(key);
    }

    const ref = envVarRef(value);
    if (ref.type !== 'value') {
      await row.locator('select').selectOption(ref.type);
      await selectEnvReference(row, ref);
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
    const hostPortInput = row.locator('input').nth(0);
    await hostPortInput.pressSequentially(String(rule.hostPort));
    await expect(hostPortInput).toBeFocused();
    await expect(hostPortInput).toHaveValue(String(rule.hostPort));
    const containerPortInput = row.locator('input').nth(1);
    await containerPortInput.pressSequentially(String(rule.containerPort));
    await expect(containerPortInput).toBeFocused();
    await expect(containerPortInput).toHaveValue(String(rule.containerPort));
    if (rule.allow !== undefined) {
      const allowInput = row.locator('input').nth(2);
      await allowInput.fill(rule.allow);
      await expect(allowInput).toHaveValue(rule.allow);
    }
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
    const hostnameInput = byTestId(row, 'deployment-ingress-hostname-input', row.locator('input').nth(0));
    await hostnameInput.pressSequentially(route.hostname);
    await expect(hostnameInput).toBeFocused();
    await expect(hostnameInput).toHaveValue(route.hostname);
    if (route.hostPort) {
      const hostPortInput = byTestId(row, 'deployment-ingress-host-port-input', row.locator('input').nth(1));
      await hostPortInput.pressSequentially(String(route.hostPort));
      await expect(hostPortInput).toBeFocused();
      await expect(hostPortInput).toHaveValue(String(route.hostPort));
    }
    const containerPortInput = byTestId(row, 'deployment-ingress-container-port-input', row.locator('input').nth(2));
    await containerPortInput.pressSequentially(String(route.containerPort));
    await expect(containerPortInput).toBeFocused();
    await expect(containerPortInput).toHaveValue(String(route.containerPort));
  }
  await pane.getByTitle('Close').click();
  await expect(pane).toBeHidden({timeout: LONG_UI_TIMEOUT});
}

async function setDeploymentNetworkingMode(dialog, networkingMode) {
  const pane = await openDeploymentNetworkingPane(dialog);
  await byTestId(pane, 'deployment-networking-mode-select', selectField(pane, 'Mode')).selectOption(String(networkingMode));
  if (String(networkingMode) === NETWORKING_VIRTUAL) {
    await expect(pane.getByText('Port forwarding', {exact: true})).toBeVisible({timeout: LONG_UI_TIMEOUT});
  }
  await pane.getByTitle('Close').click();
  await expect(pane).toBeHidden({timeout: LONG_UI_TIMEOUT});
}

async function setDeploymentDesiredRunning(dialog, running) {
  const toggle = dialog.getByTestId('deployment-desired-state-toggle');
  await expect(toggle).toBeVisible({timeout: LONG_UI_TIMEOUT});
  const checked = await toggle.getAttribute('aria-checked') === 'true';
  if (checked !== running) await toggle.click();
  await expect(toggle).toHaveAttribute('aria-checked', String(running));
}

async function openDeploymentNetworkingPane(dialog) {
  const pane = dialog.getByRole('heading', {name: 'Networking'}).locator('xpath=ancestor::div[contains(@class, "border-l")][1]');
  if (await pane.isVisible().catch(() => false)) return pane;
  await dialog.getByText(/^Networking:/).locator('xpath=ancestor::div[contains(@class, "justify-between")][1]').getByRole('button').click();
  await expect(pane).toBeVisible({timeout: LONG_UI_TIMEOUT});
  return pane;
}

function envVarRef(value) {
  if (value && typeof value === 'object' && (value.type === 'secret' || value.type === 'config' || value.type === 'address')) {
    return {type: value.type, name: value.name || ''};
  }
  return {type: 'value', value: String(value ?? '')};
}

async function selectEnvReference(row, {type, name}) {
  const picker = row.locator('input').nth(1);
  await picker.fill(name);
  await expect(row.locator('li').filter({hasText: name}).first()).toBeVisible({timeout: LONG_UI_TIMEOUT});
  await picker.press('Enter');
  // Address pickers label as "<space> / <name> (#id)".
  const selectedValue = type === 'address'
    ? new RegExp(`^.+ / ${escapeRegExp(name)} \\(#\\d+\\)$`)
    : versionedReferenceValue(name);
  await expect(picker).toHaveValue(selectedValue, {timeout: LONG_UI_TIMEOUT});
}

async function setDeploymentDataMountPath(dialog, mountPath) {
  await dialog.getByRole('button', {name: 'Click to manage'}).click();
  await expect(dialog.getByRole('heading', {name: 'Mounted volumes'})).toBeVisible();
  const pane = dialog.getByRole('heading', {name: 'Mounted volumes'}).locator('xpath=ancestor::div[contains(@class, "border-l")][1]');
  await field(pane, 'Container mount path').getByRole('textbox').fill(mountPath);
}

async function setDeploymentAssetMount(dialog, {asset, path: mountPath}) {
  const summary = dialog.getByText(/^(No mounted assets|\d+ mounted assets?)$/)
    .locator('xpath=ancestor::div[contains(@class, "justify-between")][1]');
  await summary.getByRole('button').click();
  await expect(dialog.getByRole('heading', {name: 'Mounted assets'})).toBeVisible();
  const pane = dialog.getByRole('heading', {name: 'Mounted assets'}).locator('xpath=ancestor::div[contains(@class, "border-l")][1]');
  // Opening the pane from the summary auto-adds an empty row; only add one
  // explicitly when none exists (e.g. reopening a pane that had its row removed).
  // exact: the preview button's aria-label "Preview mounted asset" would
  // otherwise substring-match "Mounted asset" too.
  if (await pane.getByLabel('Mounted asset', {exact: true}).count() === 0) {
    await pane.getByRole('button', {name: 'Add mount'}).click();
  }
  const assetSelect = pane.getByLabel('Mounted asset', {exact: true}).last();
  const assetOption = assetSelect.locator('option').filter({hasText: asset}).last();
  await expect(assetOption).toBeAttached({timeout: LONG_UI_TIMEOUT});
  const assetValue = await assetOption.getAttribute('value');
  await assetSelect.selectOption(assetValue);
  await expect(assetSelect).toHaveValue(assetValue);
  const pathInput = pane.getByLabel('Container path', {exact: true}).last();
  await pathInput.fill(mountPath);
  await expect(pathInput).toHaveValue(mountPath);
  await pathInput.blur();
  await expect(dialog.getByText('1 mounted asset')).toBeVisible();
  await pane.getByTitle('Close').click();
  await expect(pane).toBeHidden({timeout: LONG_UI_TIMEOUT});
}

// JSON log lines are shredded server side into a message and a field map, so
// the raw line is never on the wire — assertions on structured output address
// msg and fields rather than matching the JSON text.
async function logsResultRecords(page) {
  return await page.evaluate(() =>
    (window.__logsResult?.records || []).map(r => ({msg: r.msg || '', fields: r.fields || {}})));
}

// matchesRecord takes an exact string, or a RegExp where a looser match is
// wanted. Every field value arrives as a string, numbers included: the app's
// {"count":3} reads back as fields.count === '3'.
function matchesRecord(record, msg, fields) {
  if (record.msg !== msg) return false;
  return Object.entries(fields).every(([key, want]) => {
    const got = record.fields[key];
    if (got === undefined) return false;
    return want instanceof RegExp ? want.test(got) : got === want;
  });
}

function describeRecord(msg, fields) {
  const parts = Object.entries(fields).map(([k, v]) => `${k}=${v instanceof RegExp ? v.source : v}`);
  return parts.length ? `${msg} (${parts.join(' ')})` : msg;
}

async function expectOutputRecord(page, msg, fields) {
  const deadline = Date.now() + LOG_OUTPUT_TIMEOUT;
  while (Date.now() < deadline) {
    const records = await logsResultRecords(page);
    if (records.some(record => matchesRecord(record, msg, fields))) return;
    await page.getByTestId('logs-search-button').click();
    await page.waitForTimeout(LOG_OUTPUT_POLL_TIMEOUT);
  }
  const records = await logsResultRecords(page);
  expect(
    records.some(record => matchesRecord(record, msg, fields)),
    `expected log output to contain record ${describeRecord(msg, fields)}, got ${JSON.stringify(records)}`,
  ).toBe(true);
}

// expectDeploymentOutputRecords is the structured-log counterpart of
// expectDeploymentOutput: each entry is [msg, fields].
export async function expectDeploymentOutputRecords(page, name, expectedRecords) {
  await byTestId(page, 'nav-status', page.getByText('Deployments')).click();
  const row = byTestId(page, `deployment-row-${name}`, page.locator('tr').filter({hasText: name}));
  await expect(row).toBeVisible({timeout: LONG_UI_TIMEOUT});
  await openDeploymentLogsSearch(page, row);
  for (const [msg, fields = {}] of expectedRecords) {
    await expectOutputRecord(page, msg, fields);
  }
}

async function expectOutputText(page, text) {
  const deadline = Date.now() + LOG_OUTPUT_TIMEOUT;
  while (Date.now() < deadline) {
    if ((await logsResultText(page)).includes(text)) return;
    await page.getByTestId('logs-search-button').click();
    await page.waitForTimeout(LOG_OUTPUT_POLL_TIMEOUT);
  }
  expect(await logsResultText(page), `expected log output to contain ${text}`).toContain(text);
}

function trackRepoValidateRequests(page) {
  const requests = [];
  const responses = [];
  const isValidateRequest = request => {
    const url = new URL(request.url());
    return request.method() === 'POST' && url.pathname === '/v1/repos/validate';
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

export async function createSpaceViaUI(page, name) {
  await page.getByTestId('nav-spaces').click();
  await page.getByRole('button', {name: 'Add space'}).click();
  await page.getByPlaceholder('New space name').fill(name);
  await page.getByRole('button', {name: 'Save', exact: true}).click();
  const row = page.locator('[data-testid^="space-row-"]')
    .filter({has: page.getByLabel(`Space name ${name}`, {exact: true})});
  await expect(row).toBeVisible({timeout: LONG_UI_TIMEOUT});
  return Number((await row.getAttribute('data-testid')).replace('space-row-', ''));
}

export async function spaceIdByName(page, name) {
  await page.getByTestId('nav-spaces').click();
  // Built-in spaces render their name as text rather than an editable input,
  // so match on row text rather than the rename field's label.
  const row = page.locator('[data-testid^="space-row-"]').filter({hasText: name}).first();
  await expect(row).toBeVisible({timeout: LONG_UI_TIMEOUT});
  return Number((await row.getAttribute('data-testid')).replace('space-row-', ''));
}

// deploymentIdByName reads a deployment's id from the network policy form's
// deployment picker, whose option values are deployment ids. The kernel checks
// that run after the Playwright flow address workloads by id (netns
// `opendeploy-<id>-v<version>`, veth `od<id>s<slot>`), and this is the only
// place the UI exposes one.
export async function deploymentIdByName(page, name) {
  await page.getByTestId('nav-network').click();
  await page.getByRole('button', {name: 'Add policy'}).click();
  const form = page.getByTestId('network-policy-form');
  await expect(form).toBeVisible({timeout: LONG_UI_TIMEOUT});
  await form.getByLabel('Source kind', {exact: true}).selectOption({label: 'deployment'});
  const option = form.getByLabel('Source deployment', {exact: true}).locator('option')
    .filter({hasText: new RegExp(`^${escapeRegExp(name)} \\(space \\d+\\)$`)});
  await expect(option).toHaveCount(1, {timeout: LONG_UI_TIMEOUT});
  const id = Number(await option.getAttribute('value'));
  await form.getByRole('button', {name: 'Discard', exact: true}).click();
  await expect(form).toBeHidden({timeout: LONG_UI_TIMEOUT});
  return id;
}

// writeNetworkPolicyState hands the ids of the policy workloads to the
// orchestrator's post-flow kernel checks, which cannot authenticate to the API.
// Same channel as the backup-restore extension.
export function writeNetworkPolicyState(values) {
  const path = process.env.OPD_NETPOLICY_STATE || '/e2e/test-results/netpolicy.env';
  const lines = [
    '# Generated by the network policy E2E cases for the kernel checks.',
    ...Object.entries(values).map(([key, value]) => `${key}=${shellQuote(String(value))}`),
    '',
  ];
  fs.writeFileSync(path, lines.join('\n'));
  console.log(`[opendeploy-e2e] network policy kernel check state: ${JSON.stringify(values)}`);
}

async function networkPolicyRowIds(page) {
  const rows = page.locator('[data-testid^="network-policy-row-"]');
  const ids = [];
  for (let i = 0; i < await rows.count(); i += 1) {
    ids.push(Number((await rows.nth(i).getAttribute('data-testid')).replace('network-policy-row-', '')));
  }
  return ids;
}

async function fillNetworkPolicyForm(form, {sourceKind, source, destinationKind, destination, ports}) {
  await form.getByLabel('Source kind', {exact: true}).selectOption({label: sourceKind});
  await form.getByLabel(`Source ${sourceKind}`, {exact: true}).selectOption({label: source});
  await form.getByLabel('Destination kind', {exact: true}).selectOption({label: destinationKind});
  await form.getByLabel(`Destination ${destinationKind}`, {exact: true}).selectOption({label: destination});
  await form.getByLabel('Ports', {exact: true}).fill(ports || '');
}

// createNetworkPolicy returns the new policy's id, identified by diffing the
// row ids rather than by matching peer labels: several cases keep policies
// with the same source or destination alive at once, and a label filter would
// silently return the wrong row. With expectError it asserts the rejection
// instead and leaves no policy behind.
export async function createNetworkPolicy(page, {
  sourceKind = 'space',
  source,
  destinationKind = 'space',
  destination,
  ports = '',
  expectError,
} = {}) {
  await page.getByTestId('nav-network').click();
  const before = await networkPolicyRowIds(page);
  await page.getByRole('button', {name: 'Add policy'}).click();
  const form = page.getByTestId('network-policy-form');
  await expect(form).toBeVisible({timeout: LONG_UI_TIMEOUT});
  await fillNetworkPolicyForm(form, {sourceKind, source, destinationKind, destination, ports});
  await page.getByRole('button', {name: 'Create', exact: true}).click();

  if (expectError) {
    await expect(page.getByText(expectError)).toBeVisible({timeout: LONG_UI_TIMEOUT});
    await expect(form).toBeVisible();
    await form.getByRole('button', {name: 'Discard', exact: true}).click();
    await expect(form).toBeHidden({timeout: LONG_UI_TIMEOUT});
    expect(await networkPolicyRowIds(page), 'rejected policy must not be stored').toEqual(before);
    return null;
  }

  await expect(form).toBeHidden({timeout: LONG_UI_TIMEOUT});
  let created = null;
  await expect.poll(async () => {
    created = (await networkPolicyRowIds(page)).find(id => !before.includes(id)) ?? null;
    return created;
  }, {message: `expected a new network policy row for ${source} → ${destination}`, timeout: LONG_UI_TIMEOUT})
    .not.toBeNull();
  return created;
}

export async function updateNetworkPolicy(page, {
  id,
  sourceKind = 'space',
  source,
  destinationKind = 'space',
  destination,
  ports = '',
  expectError,
} = {}) {
  await page.getByTestId('nav-network').click();
  await page.getByLabel(`Edit policy ${id}`, {exact: true}).click();
  const form = page.getByTestId('network-policy-form');
  await expect(form).toBeVisible({timeout: LONG_UI_TIMEOUT});
  await fillNetworkPolicyForm(form, {sourceKind, source, destinationKind, destination, ports});
  await page.getByRole('button', {name: 'Save', exact: true}).click();

  if (expectError) {
    await expect(page.getByText(expectError)).toBeVisible({timeout: LONG_UI_TIMEOUT});
    await form.getByRole('button', {name: 'Discard', exact: true}).click();
    await expect(form).toBeHidden({timeout: LONG_UI_TIMEOUT});
    return;
  }

  await expect(form).toBeHidden({timeout: LONG_UI_TIMEOUT});
  await expect(page.getByTestId(`network-policy-row-${id}`)).toContainText(ports || 'all ports', {timeout: LONG_UI_TIMEOUT});
}

export async function deleteNetworkPolicy(page, policyId) {
  await page.getByTestId('nav-network').click();
  await page.getByLabel(`Delete policy ${policyId}`, {exact: true}).click();
  await page.getByRole('button', {name: 'Confirm', exact: true}).click();
  await expect(page.getByTestId(`network-policy-row-${policyId}`)).toHaveCount(0, {timeout: LONG_UI_TIMEOUT});
}

export async function expectNetworkPolicyDangling(page, policyId) {
  await page.getByTestId('nav-network').click();
  const row = page.getByTestId(`network-policy-row-${policyId}`);
  await expect(row).toBeVisible({timeout: LONG_UI_TIMEOUT});
  await expect(row.getByText('dangling', {exact: true})).toBeVisible({timeout: LONG_UI_TIMEOUT});
}

export async function expectNetworkPolicyCount(page, count) {
  await page.getByTestId('nav-network').click();
  await expect.poll(async () => (await networkPolicyRowIds(page)).length,
    {message: `expected ${count} visible network policies`, timeout: LONG_UI_TIMEOUT}).toBe(count);
}

// expectDeploymentNetworkPolicies asserts the deployment inspector's derived
// read-only view. `present` entries are `<source label> → <destination label>`
// strings as the summary renders them.
export async function expectDeploymentNetworkPolicies(page, {name, machine, present = [], absent = []} = {}) {
  await byTestId(page, 'nav-status', page.getByText('Deployments')).click();
  const row = deploymentRow(page, {name, machine});
  await expect(row).toBeVisible({timeout: LONG_UI_TIMEOUT});
  await row.locator('td').first().click();
  const inspector = page.getByTestId('deployment-inspector');
  await expect(inspector).toBeVisible({timeout: LONG_UI_TIMEOUT});
  const summary = inspector.getByTestId('deployment-network-policies');
  await expect(summary).toBeVisible({timeout: LONG_UI_TIMEOUT});
  for (const entry of present) {
    await expect(summary.getByText(entry, {exact: false})).toBeVisible({timeout: LONG_UI_TIMEOUT});
  }
  for (const entry of absent) {
    await expect(summary.getByText(entry, {exact: false})).toHaveCount(0, {timeout: LONG_UI_TIMEOUT});
  }
  await inspector.getByLabel('Close inspector').click();
  await expect(inspector).toBeHidden();
}

// moveDeploymentToSpace changes a deployment's space through the update
// dialog's Space picker, which carries the new space in the single
// /v2/deployments/update call. The move is a connection-breaking
// security-domain migration: the deployment's addresses and DNS name change
// with its space.
export async function moveDeploymentToSpace(page, {name, machine, space} = {}) {
  await step(`open update dialog ${name}`, async () => {
    await byTestId(page, 'nav-status', page.getByText('Deployments')).click();
    const row = deploymentRow(page, {name, machine});
    await expect(row).toBeVisible({timeout: LONG_UI_TIMEOUT});
    await row.getByRole('button', {name: 'Update'}).click();
  });

  const dialog = editorPanel(page, 'update-deployment-dialog');
  await expect(dialog).toBeVisible();
  await selectEditorMode(dialog, 'ui');

  await step(`select space ${space} for ${name}`, async () => {
    const spaceSelect = byTestId(dialog, 'deployment-space-select', selectField(dialog, 'Space'));
    const spaceOption = spaceSelect.locator('option').filter({hasText: space});
    await expect(spaceOption).toHaveCount(1, {timeout: LONG_UI_TIMEOUT});
    await spaceSelect.selectOption(await spaceOption.getAttribute('value'));
  });

  await step(`submit space move ${name}`, async () => {
    const submit = dialog.getByRole('button', {name: 'Update deployment'});
    await expect(submit).toBeEnabled({timeout: LONG_UI_TIMEOUT});
    const moveResponse = page.waitForResponse(response => {
      const request = response.request();
      return request.method() === 'POST' && new URL(request.url()).pathname === '/v2/deployments/update';
    }, {timeout: LONG_UI_TIMEOUT});
    await submit.click();
    expect((await moveResponse).ok()).toBe(true);
    await expect(dialog).toBeHidden({timeout: LONG_UI_TIMEOUT});
  });
}
