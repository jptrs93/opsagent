import {expect, test} from '@playwright/test';
import {installVirtualAuthenticator} from '../helpers/webauthn.js';
import {
  bootstrapFirstUser,
  createAsset,
  createExplorerFolder,
  createNixDockerDeployment,
  createValueInSelection,
  deleteDeployment,
  deleteExplorerSelection,
  expectDeploymentOutput,
  expectDeploymentRunning,
  expectExplorerPath,
  moveExplorerSelection,
  renameExplorerSelection,
  rotateSecret,
  selectExplorerRow,
  stopDeployment,
  updateNixDockerDeployment,
} from '../helpers/ui.js';

const LONG_UI_TIMEOUT = 15_000;
const ADMIN_USER = 'E2E Operator';
const RESTRICTED_USER = 'E2E Restricted';
const RESTRICTED_SPACE = 'e2e-restricted';
const RESTRICTED_DEPLOYMENT = 'nixdocker-restricted';
const RESTRICTED_SECRET = 'e2e.restricted.secret';
const RESTRICTED_CONFIG = 'e2e.restricted.config';
const RESTRICTED_ASSET = 'e2e-restricted-asset.txt';
const RESTRICTED_FOLDER = 'e2e-restricted-values';

function requiredEnv(name) {
  const value = process.env[name]?.trim();
  if (!value) throw new Error(`${name} is required`);
  return value;
}

function restrictedPage(ctx) {
  if (!ctx.restricted?.page) throw new Error('restricted user session is not open');
  return ctx.restricted.page;
}

function userRow(page, userName) {
  return page.locator('[data-testid^="user-row-"]').filter({hasText: userName});
}

function spaceRow(page, spaceName) {
  return page.locator('[data-testid^="space-row-"]').filter({hasText: spaceName});
}

function templateRow(page, templateName) {
  return page.locator('[data-testid^="template-row-"]').filter({hasText: templateName});
}

function overlayCard(page, titleText) {
  return page.locator('.fixed.inset-0.z-50').filter({hasText: titleText});
}

async function grantSpaceAdmin(page, userName, spaceName) {
  await page.getByTestId('nav-users').click();
  const row = userRow(page, userName);
  await expect(row).toBeVisible({timeout: LONG_UI_TIMEOUT});
  await row.getByRole('button', {name: `Grant access to ${userName}`, exact: true}).click();

  const overlay = overlayCard(page, `Grant access to ${userName}`);
  await expect(overlay).toBeVisible();
  await overlay.locator('select').selectOption({label: 'space_admin'});
  await overlay.getByRole('button', {name: spaceName, exact: true}).click();
  await overlay.getByRole('button', {name: 'Grant', exact: true}).click();
  await expect(overlay).toBeHidden({timeout: LONG_UI_TIMEOUT});
  await expect(row.getByText('space_admin')).toBeVisible({timeout: LONG_UI_TIMEOUT});
}

async function expectHiddenAdminContent(page) {
  await test.step('default-space deployments are hidden', async () => {
    await page.getByTestId('nav-status').click();
    await expect(page.getByRole('button', {name: 'Add deployment'})).toBeVisible({timeout: LONG_UI_TIMEOUT});
    await expect(page.locator('tr').filter({hasText: 'nixdockerbuild1'})).toHaveCount(0, {timeout: LONG_UI_TIMEOUT});
  });

  await test.step('default-space secrets and configs are hidden', async () => {
    await page.getByTestId('nav-secrets').click();
    await expect(page.getByPlaceholder('Search secrets / configs')).toBeVisible({timeout: LONG_UI_TIMEOUT});
    await expect(page.getByRole('row', {name: /e2e\.secret\.message/})).toHaveCount(0);
    await expect(page.getByRole('row', {name: /e2e\.config\.message/})).toHaveCount(0);
    await expect(page.getByRole('row', {name: /default/})).toHaveCount(0);
  });

  await test.step('default-space assets are hidden', async () => {
    await page.getByTestId('nav-assets').click();
    await expect(page.getByPlaceholder('Search assets')).toBeVisible({timeout: LONG_UI_TIMEOUT});
    await expect(page.getByRole('row', {name: /e2e-workload-asset\.txt/})).toHaveCount(0);
  });

  await test.step('cluster settings are withheld', async () => {
    await page.getByTestId('nav-settings').click();
    await expect(page.getByText('Backup enabled', {exact: true})).toHaveCount(0);
  });
}

export const accessEnforcementCases = [
  {
    id: 'access-restricted-user-created',
    title: 'create restricted user in a second session',
    description: 'Creates a second user through the setup-password bootstrap flow in an isolated browser context with its own passkey.',
    requires: ['bootstrap', 'worker-enrolled', 'nix-docker-baseline', 'config-created', 'secret-created', 'small-asset-created'],
    async run(ctx) {
      const browser = ctx.page.context().browser();
      const context = await browser.newContext({
        baseURL: process.env.OPD_BASE_URL || 'http://localhost:8080',
        ignoreHTTPSErrors: process.env.OPD_IGNORE_HTTPS_ERRORS === 'true',
      });
      const page = await context.newPage();
      await installVirtualAuthenticator(page);
      await bootstrapFirstUser(page, {username: RESTRICTED_USER});
      ctx.restricted = {context, page};
      await expect(page.locator('tr').filter({hasText: 'nixdockerbuild1'}).first()).toBeVisible({timeout: LONG_UI_TIMEOUT});
    },
  },
  {
    id: 'access-restricted-user-reduced',
    title: 'revoke the restricted user cluster_admin grant',
    description: 'The admin revokes the automatic cluster_admin grant from the new user on the Users page.',
    requires: ['access-restricted-user-created'],
    async run(ctx) {
      await ctx.page.getByTestId('nav-users').click();
      const row = userRow(ctx.page, RESTRICTED_USER);
      await expect(row).toBeVisible({timeout: LONG_UI_TIMEOUT});
      await row.getByRole('button', {name: `Revoke cluster_admin from ${RESTRICTED_USER}`, exact: true}).click();
      await expect(row.getByText('cluster_admin')).toBeHidden({timeout: LONG_UI_TIMEOUT});
    },
  },
  {
    id: 'access-restricted-blank-slate',
    title: 'verify a user without grants sees nothing',
    description: 'With every grant revoked, the live state stream empties the restricted session: no deployments, spaces, secrets, assets, nodes, other users, or cluster settings.',
    requires: ['access-restricted-user-reduced'],
    async run(ctx) {
      const page = restrictedPage(ctx);

      await expectHiddenAdminContent(page);

      await test.step('no spaces are visible', async () => {
        await page.getByTestId('nav-spaces').click();
        await expect(page.getByText('No spaces yet. Click Add space.')).toBeVisible({timeout: LONG_UI_TIMEOUT});
      });

      await test.step('no nodes are visible', async () => {
        await page.getByTestId('nav-cluster').click();
        await expect(page.getByText('No nodes found.')).toBeVisible({timeout: LONG_UI_TIMEOUT});
        await expect(page.locator('[data-testid^="machine-row-"]')).toHaveCount(0);
      });

      await test.step('users page shows only self, templates, and no global rules', async () => {
        await page.getByTestId('nav-users').click();
        await expect(userRow(page, RESTRICTED_USER)).toBeVisible({timeout: LONG_UI_TIMEOUT});
        await expect(userRow(page, ADMIN_USER)).toHaveCount(0);
        await expect(templateRow(page, 'cluster_admin')).toBeVisible();
        await expect(templateRow(page, 'space_admin')).toBeVisible();
        await expect(page.getByText(/No global rules/)).toBeVisible();
      });
    },
  },
  {
    id: 'access-restricted-space-granted',
    title: 'create a space and grant space_admin on it',
    description: 'The admin creates a dedicated space and grants the restricted user the space_admin template bound to it.',
    requires: ['access-restricted-user-reduced'],
    async run(ctx) {
      await ctx.page.getByTestId('nav-spaces').click();
      await ctx.page.getByRole('button', {name: 'Add space'}).click();
      await ctx.page.getByPlaceholder('New space name').fill(RESTRICTED_SPACE);
      await ctx.page.getByRole('button', {name: 'Save', exact: true}).click();
      await expect(spaceRow(ctx.page, RESTRICTED_SPACE)).toBeVisible({timeout: LONG_UI_TIMEOUT});

      await grantSpaceAdmin(ctx.page, RESTRICTED_USER, RESTRICTED_SPACE);
    },
  },
  {
    id: 'access-restricted-space-visible',
    title: 'verify the space admin sees exactly its scope',
    description: 'The restricted session now sees the granted space, the node directory, and all users, while default-space content stays hidden.',
    requires: ['access-restricted-space-granted'],
    async run(ctx) {
      const page = restrictedPage(ctx);

      await test.step('granted space appears, default space stays hidden', async () => {
        await page.getByTestId('nav-spaces').click();
        await expect(spaceRow(page, RESTRICTED_SPACE)).toBeVisible({timeout: LONG_UI_TIMEOUT});
        await expect(spaceRow(page, 'opendeploy')).toBeVisible();
        await expect(spaceRow(page, 'default')).toHaveCount(0);
      });

      await test.step('nodes become visible through the builtin directory rule', async () => {
        await page.getByTestId('nav-cluster').click();
        const workerRow = page.getByTestId(`machine-row-${requiredEnv('OPD_WORKER_1_MACHINE_ID')}`);
        await expect(workerRow).toBeVisible({timeout: LONG_UI_TIMEOUT});
        await expect(workerRow).toContainText('connected', {timeout: LONG_UI_TIMEOUT});
      });

      await test.step('user directory and own grant become visible', async () => {
        await page.getByTestId('nav-users').click();
        await expect(userRow(page, ADMIN_USER)).toBeVisible({timeout: LONG_UI_TIMEOUT});
        await expect(userRow(page, RESTRICTED_USER).getByText('space_admin')).toBeVisible();
      });

      await expectHiddenAdminContent(page);
    },
  },
  {
    id: 'access-restricted-values-created',
    title: 'create secret, config, folder, and asset in the granted space',
    description: 'The space admin creates and reveals values in its own space through the explorer pages.',
    requires: ['access-restricted-space-visible'],
    async run(ctx) {
      const page = restrictedPage(ctx);

      await test.step('create a folder and values in the granted space', async () => {
        await page.getByTestId('nav-secrets').click();
        await expect(page.getByPlaceholder('Search secrets / configs')).toBeVisible({timeout: LONG_UI_TIMEOUT});
        await selectExplorerRow(page, RESTRICTED_SPACE);
        await createExplorerFolder(page, RESTRICTED_FOLDER);
        await expectExplorerPath(page, `${RESTRICTED_SPACE}/${RESTRICTED_FOLDER}`);
        await createValueInSelection(page, {
          type: 'secret', name: RESTRICTED_SECRET, value: 'restricted-secret-value',
          location: `${RESTRICTED_SPACE}/${RESTRICTED_FOLDER}/`,
        });
        await expectExplorerPath(page, `${RESTRICTED_SPACE}/${RESTRICTED_FOLDER}/${RESTRICTED_SECRET}`);
        await selectExplorerRow(page, RESTRICTED_SPACE);
        await createValueInSelection(page, {
          type: 'config', name: RESTRICTED_CONFIG, value: 'restricted-config-value',
          location: `${RESTRICTED_SPACE}/`,
        });
        await expectExplorerPath(page, `${RESTRICTED_SPACE}/${RESTRICTED_CONFIG}`);
      });

      await test.step('reveal the secret in the granted space', async () => {
        await selectExplorerRow(page, RESTRICTED_SECRET);
        await page.getByRole('button', {name: `Reveal ${RESTRICTED_SECRET}`, exact: true}).click();
        await expect(page.getByText('restricted-secret-value')).toBeVisible({timeout: LONG_UI_TIMEOUT});
      });

      await test.step('create an asset in the granted space', async () => {
        await createAsset(page, {key: RESTRICTED_ASSET, content: 'restricted-asset-content'});
        await selectExplorerRow(page, RESTRICTED_ASSET);
        await expectExplorerPath(page, `${RESTRICTED_SPACE}/${RESTRICTED_ASSET}`);
      });
    },
  },
  {
    id: 'access-restricted-deployment-created',
    title: 'create a deployment in the granted space',
    description: 'The space admin deploys to a worker in its own space, wiring in its secret, config, and asset, and reads the deployment logs.',
    requires: ['access-restricted-values-created'],
    async run(ctx) {
      const page = restrictedPage(ctx);

      await test.step('deployment dialog offers only the granted space', async () => {
        await page.getByTestId('nav-status').click();
        await page.getByRole('button', {name: 'Add deployment'}).click();
        const dialog = page.locator('.fixed.inset-0.z-50').filter({hasText: 'Deployment identity'}).last();
        await expect(dialog).toBeVisible();
        const spaceOptions = dialog.getByTestId('deployment-space-select').locator('option');
        await expect(spaceOptions.filter({hasText: RESTRICTED_SPACE})).toHaveCount(1, {timeout: LONG_UI_TIMEOUT});
        await expect(spaceOptions.filter({hasText: 'default'})).toHaveCount(0);
        await dialog.getByRole('button', {name: 'Cancel', exact: true}).click();
        await expect(dialog).toBeHidden({timeout: LONG_UI_TIMEOUT});
      });

      await createNixDockerDeployment(page, {
        name: RESTRICTED_DEPLOYMENT,
        machine: 'worker-1',
        space: RESTRICTED_SPACE,
        env: {
          OPENDEPLOY_E2E_MESSAGE: {type: 'config', name: RESTRICTED_CONFIG},
          OPENDEPLOY_E2E_COLOR: {type: 'secret', name: RESTRICTED_SECRET},
        },
        expectedEnv: {
          OPENDEPLOY_E2E_MESSAGE: 'restricted-config-value',
          OPENDEPLOY_E2E_COLOR: 'restricted-secret-value',
        },
        assetMount: {
          asset: RESTRICTED_ASSET,
          path: '/tmp/opendeploy-restricted-asset.txt',
        },
      });

      await expectDeploymentOutput(page, RESTRICTED_DEPLOYMENT, [
        'nixdockerbuild1 asset content opendeploy-restricted-asset.txt=restricted-asset-content',
      ]);
    },
  },
  {
    id: 'access-admin-cross-check',
    title: 'verify the admin sees the restricted space work',
    description: 'The cluster admin session sees the restricted user deployment, space, and values without any filtering.',
    requires: ['access-restricted-deployment-created'],
    async run(ctx) {
      await ctx.page.getByTestId('nav-status').click();
      await expect(ctx.page.locator('tr').filter({hasText: RESTRICTED_DEPLOYMENT}).first()).toBeVisible({timeout: LONG_UI_TIMEOUT});
      await ctx.page.getByTestId('nav-secrets').click();
      await expect(ctx.page.getByRole('row', {name: new RegExp(RESTRICTED_SPACE)}).first()).toBeVisible({timeout: LONG_UI_TIMEOUT});
      await ctx.page.getByTestId('nav-spaces').click();
      await expect(spaceRow(ctx.page, RESTRICTED_SPACE)).toBeVisible({timeout: LONG_UI_TIMEOUT});
      await expect(spaceRow(ctx.page, 'default')).toBeVisible();
    },
  },
  {
    id: 'access-restricted-deployment-managed',
    title: 'update, stop, and delete the restricted deployment',
    description: 'The space admin manages its deployment through the full lifecycle: env update with redeploy, stop, and delete.',
    requires: ['access-restricted-deployment-created'],
    async run(ctx) {
      const page = restrictedPage(ctx);

      await updateNixDockerDeployment(page, {
        name: RESTRICTED_DEPLOYMENT,
        machine: 'worker-1',
        env: {OPENDEPLOY_E2E_CONFIG: 'restricted-updated-value'},
      });
      await expectDeploymentRunning(page, {name: RESTRICTED_DEPLOYMENT, machine: 'worker-1'});
      await expectDeploymentOutput(page, RESTRICTED_DEPLOYMENT, [
        'nixdockerbuild1 env OPENDEPLOY_E2E_CONFIG=restricted-updated-value',
      ]);

      await stopDeployment(page, {name: RESTRICTED_DEPLOYMENT, machine: 'worker-1'});
      await deleteDeployment(page, {name: RESTRICTED_DEPLOYMENT, machine: 'worker-1'});
    },
  },
  {
    id: 'access-restricted-values-managed',
    title: 'rotate, rename, move, and delete values in the granted space',
    description: 'The space admin exercises edit and delete on its secrets, configs, folders, and assets once the deployment no longer references them.',
    requires: ['access-restricted-deployment-managed'],
    async run(ctx) {
      const page = restrictedPage(ctx);

      await rotateSecret(page, {name: RESTRICTED_SECRET, value: 'restricted-secret-value-2'});

      await page.getByTestId('nav-secrets').click();
      await selectExplorerRow(page, RESTRICTED_CONFIG);
      await renameExplorerSelection(page, `${RESTRICTED_CONFIG}.renamed`);
      await expectExplorerPath(page, `${RESTRICTED_SPACE}/${RESTRICTED_CONFIG}.renamed`);
      await deleteExplorerSelection(page);
      await expect(page.getByRole('row', {name: new RegExp(`${RESTRICTED_CONFIG.replaceAll('.', '\\.')}\\.renamed`)})).toBeHidden({timeout: LONG_UI_TIMEOUT});

      await selectExplorerRow(page, RESTRICTED_SECRET);
      await moveExplorerSelection(page, '/');
      await expectExplorerPath(page, `${RESTRICTED_SPACE}/${RESTRICTED_SECRET}`);
      await deleteExplorerSelection(page);
      await expect(page.getByRole('row', {name: new RegExp(RESTRICTED_SECRET.replaceAll('.', '\\.'))})).toBeHidden({timeout: LONG_UI_TIMEOUT});

      await selectExplorerRow(page, RESTRICTED_FOLDER);
      await deleteExplorerSelection(page);
      await expect(page.getByRole('row', {name: new RegExp(RESTRICTED_FOLDER)})).toBeHidden({timeout: LONG_UI_TIMEOUT});

      await page.getByTestId('nav-assets').click();
      await selectExplorerRow(page, RESTRICTED_ASSET);
      await renameExplorerSelection(page, 'e2e-restricted-asset-renamed.txt');
      await expectExplorerPath(page, `${RESTRICTED_SPACE}/e2e-restricted-asset-renamed.txt`);
      await deleteExplorerSelection(page);
      await expect(page.getByRole('row', {name: /e2e-restricted-asset-renamed\.txt/})).toBeHidden({timeout: LONG_UI_TIMEOUT});
    },
  },
  {
    id: 'access-restricted-denied-actions',
    title: 'verify cluster-level actions are denied for the space admin',
    description: 'Space creation, grant management, and node renames are rejected with an access denied error for the space admin.',
    requires: ['access-restricted-space-visible'],
    async run(ctx) {
      const page = restrictedPage(ctx);

      await test.step('space creation is denied', async () => {
        await page.getByTestId('nav-spaces').click();
        await page.getByRole('button', {name: 'Add space'}).click();
        await page.getByPlaceholder('New space name').fill('e2e-denied-space');
        await page.getByRole('button', {name: 'Save', exact: true}).click();
        await expect(page.getByText('Error: Access denied')).toBeVisible({timeout: LONG_UI_TIMEOUT});
        await page.getByRole('button', {name: 'Discard', exact: true}).click();
      });

      await test.step('granting access is denied', async () => {
        await page.getByTestId('nav-users').click();
        const row = userRow(page, RESTRICTED_USER);
        await expect(row).toBeVisible({timeout: LONG_UI_TIMEOUT});
        await row.getByRole('button', {name: `Grant access to ${RESTRICTED_USER}`, exact: true}).click();
        const overlay = overlayCard(page, `Grant access to ${RESTRICTED_USER}`);
        await expect(overlay).toBeVisible();
        await overlay.getByRole('button', {name: 'Grant', exact: true}).click();
        await expect(overlay.getByText('Access denied')).toBeVisible({timeout: LONG_UI_TIMEOUT});
        await overlay.getByRole('button', {name: 'Cancel', exact: true}).click();
        await expect(overlay).toBeHidden({timeout: LONG_UI_TIMEOUT});
      });

      await test.step('node rename is denied', async () => {
        const machineID = requiredEnv('OPD_WORKER_1_MACHINE_ID');
        await page.getByTestId('nav-cluster').click();
        const row = page.getByTestId(`machine-row-${machineID}`);
        await expect(row).toBeVisible({timeout: LONG_UI_TIMEOUT});
        await row.getByLabel(`Node name for ${machineID}`, {exact: true}).fill('worker-1-denied');
        await row.getByRole('button', {name: `Save node name ${machineID}`, exact: true}).click();
        await expect(row.getByText('Access denied')).toBeVisible({timeout: LONG_UI_TIMEOUT});
        await row.getByRole('button', {name: `Discard node name change for ${machineID}`, exact: true}).click();
      });
    },
  },
  {
    id: 'access-restricted-session-closed',
    title: 'close the restricted session and verify the admin view',
    description: 'Closes the restricted browser context and confirms the admin session still sees the full default-space state.',
    requires: ['access-restricted-denied-actions', 'access-restricted-values-managed'],
    async run(ctx) {
      await ctx.restricted.context.close();
      ctx.restricted = null;

      await ctx.page.getByTestId('nav-status').click();
      await expect(ctx.page.locator('tr').filter({hasText: 'nixdockerbuild1'}).first()).toBeVisible({timeout: LONG_UI_TIMEOUT});
      await ctx.page.getByTestId('nav-secrets').click();
      await expect(ctx.page.getByRole('row', {name: /e2e\.secret\.message/})).toBeVisible({timeout: LONG_UI_TIMEOUT});
    },
  },
];
