import {expect, test} from '@playwright/test';
import {
  clearExplorerSearch,
  closeExplorerInspector,
  createAsset,
  createNixDockerDeployment,
  createValueInSelection,
  deleteDeployment,
  deleteExplorerSelection,
  expectDeleteSelectionBlocked,
  expectDeploymentRunning,
  expectExplorerPath,
  expectMoveToSpaceBlocked,
  moveExplorerSelectionToSpace,
  searchExplorerAndSelect,
  selectExplorerRow,
  stopDeployment,
} from '../helpers/ui.js';

const LONG_UI_TIMEOUT = 15_000;

// Names shared with the access-enforcement cases these run between.
const RESTRICTED_SPACE = 'e2e-restricted';
const RESTRICTED_SECRET = 'e2e.restricted.secret';
const RESTRICTED_FOLDER = 'e2e-restricted-values';

const MOVE_SECRET = 'e2e.spacemove.secret';
const MOVE_CONFIG = 'e2e.spacemove.config';
const MOVE_ASSET = 'e2e-spacemove-asset.txt';
const REFHOST_DEPLOYMENT = 'spacemove-refhost';

const VALUES_SEARCH = 'Search secrets / configs';
const ASSETS_SEARCH = 'Search assets';
const MOVE_BLOCKED_MESSAGE = 'Value is referenced from outside the destination space';
const DELETE_BLOCKED_MESSAGE = 'Referenced value is still in use';

function restrictedPage(ctx) {
  if (!ctx.restricted?.page) throw new Error('restricted user session is not open');
  return ctx.restricted.page;
}

function rowNamed(page, name) {
  return page.getByRole('row', {name: new RegExp(name.replaceAll('.', '\\.'))});
}

// Cross-space move coverage. These cases run inside the access-enforcement
// window on purpose: at that point a global deployment pins the global
// baseline values, a restricted deployment pins the restricted values, and
// the restricted space_admin session is a live second observer of the state
// stream — which is what the tombstone-then-update mechanics exist for.
export const spaceMoveCases = [
  {
    id: 'space-move-value-cross-space',
    title: 'move a secret between spaces and verify both live sessions',
    description: 'The admin moves an unreferenced secret from global into the restricted space and back; the restricted session watches it appear live, reveals the value, and watches the tombstone remove it again.',
    requires: ['access-restricted-values-created'],
    async run(ctx) {
      const admin = ctx.page;
      const restricted = restrictedPage(ctx);

      await test.step('restricted session parks on secrets filtered to the move name', async () => {
        await restricted.getByTestId('nav-secrets').click();
        await restricted.getByPlaceholder(VALUES_SEARCH).fill(MOVE_SECRET);
        await expect(rowNamed(restricted, MOVE_SECRET)).toHaveCount(0);
      });

      await test.step('admin creates the secret in global and moves it across', async () => {
        await admin.getByTestId('nav-secrets').click();
        await selectExplorerRow(admin, 'global');
        await createValueInSelection(admin, {
          type: 'secret', name: MOVE_SECRET, value: 'space-move-secret-value',
          location: 'global/',
        });
        await expectExplorerPath(admin, `global/${MOVE_SECRET}`);
        await moveExplorerSelectionToSpace(admin, {space: RESTRICTED_SPACE});
        await expectExplorerPath(admin, `${RESTRICTED_SPACE}/${MOVE_SECRET}`);
      });

      await test.step('restricted session sees the row appear live and reveals the value', async () => {
        const row = rowNamed(restricted, MOVE_SECRET).first();
        await expect(row).toBeVisible({timeout: LONG_UI_TIMEOUT});
        await row.click();
        await restricted.getByRole('button', {name: 'Reveal value', exact: true}).click();
        await expect(restricted.getByText('space-move-secret-value')).toBeVisible({timeout: LONG_UI_TIMEOUT});
        await closeExplorerInspector(restricted);
      });

      await test.step('admin moves it back; the restricted session drops it live', async () => {
        await moveExplorerSelectionToSpace(admin, {space: 'global'});
        await expectExplorerPath(admin, `global/${MOVE_SECRET}`);
        // The delete-tombstone is the only removal signal a session that
        // cannot see the destination space receives.
        await expect(rowNamed(restricted, MOVE_SECRET)).toHaveCount(0, {timeout: LONG_UI_TIMEOUT});
        await clearExplorerSearch(restricted, VALUES_SEARCH);
      });

      await test.step('admin deletes the moved secret', async () => {
        await deleteExplorerSelection(admin);
        await expect(rowNamed(admin, MOVE_SECRET)).toBeHidden({timeout: LONG_UI_TIMEOUT});
      });
    },
  },
  {
    id: 'space-move-blocked-by-references',
    title: 'cross-space moves refuse while references live elsewhere',
    description: 'Moving the globally referenced secret into the restricted space, and the restricted-referenced secret into global, both refuse with the locality error and leave the rows in place.',
    requires: ['asset-backed-nix-docker-deployment', 'access-restricted-deployment-created'],
    async run(ctx) {
      const admin = ctx.page;
      await admin.getByTestId('nav-secrets').click();

      await test.step('a secret pinned by a global deployment cannot leave global', async () => {
        await searchExplorerAndSelect(admin, VALUES_SEARCH, 'e2e.secret.message');
        await expectExplorerPath(admin, 'global/e2e.secret.message');
        await expectMoveToSpaceBlocked(admin, {space: RESTRICTED_SPACE, message: MOVE_BLOCKED_MESSAGE});
        await expectExplorerPath(admin, 'global/e2e.secret.message');
      });

      await test.step('a secret pinned by the restricted deployment cannot leave its space', async () => {
        await searchExplorerAndSelect(admin, VALUES_SEARCH, RESTRICTED_SECRET);
        await expectExplorerPath(admin, `${RESTRICTED_SPACE}/${RESTRICTED_FOLDER}/${RESTRICTED_SECRET}`);
        await expectMoveToSpaceBlocked(admin, {space: 'global', message: MOVE_BLOCKED_MESSAGE});
        await expectExplorerPath(admin, `${RESTRICTED_SPACE}/${RESTRICTED_FOLDER}/${RESTRICTED_SECRET}`);
        await clearExplorerSearch(admin, VALUES_SEARCH);
        await closeExplorerInspector(admin);
      });
    },
  },
  {
    id: 'space-move-toward-references',
    title: 'a value moves toward its referencing space, never away',
    description: 'A global config referenced only by a restricted-space deployment moves into that space; moving it back refuses until the deployment is deleted.',
    requires: ['access-restricted-space-granted', 'worker-enrolled'],
    async run(ctx) {
      const admin = ctx.page;

      await test.step('create a global config and a restricted-space deployment referencing it', async () => {
        await admin.getByTestId('nav-secrets').click();
        await selectExplorerRow(admin, 'global');
        await createValueInSelection(admin, {
          type: 'config', name: MOVE_CONFIG, value: 'space-move-config-value',
          location: 'global/',
        });
        await createNixDockerDeployment(admin, {
          name: REFHOST_DEPLOYMENT,
          machine: 'worker-1',
          space: RESTRICTED_SPACE,
          env: {OPENDEPLOY_E2E_MESSAGE: {type: 'config', name: MOVE_CONFIG}},
          expectedEnv: {OPENDEPLOY_E2E_MESSAGE: 'space-move-config-value'},
        });
      });

      await test.step('the config moves into the referencing space', async () => {
        await admin.getByTestId('nav-secrets').click();
        await searchExplorerAndSelect(admin, VALUES_SEARCH, MOVE_CONFIG);
        await moveExplorerSelectionToSpace(admin, {space: RESTRICTED_SPACE});
        await expectExplorerPath(admin, `${RESTRICTED_SPACE}/${MOVE_CONFIG}`);
        // References pin immutable version ids; the move must not disturb the
        // running deployment.
        await expectDeploymentRunning(admin, {name: REFHOST_DEPLOYMENT, machine: 'worker-1'});
      });

      await test.step('moving it away from its referencer refuses', async () => {
        await admin.getByTestId('nav-secrets').click();
        await searchExplorerAndSelect(admin, VALUES_SEARCH, MOVE_CONFIG);
        await expectMoveToSpaceBlocked(admin, {space: 'global', message: MOVE_BLOCKED_MESSAGE});
      });

      await test.step('deleting the deployment releases the pin', async () => {
        // Delete is only offered once a deployment is stopped.
        await stopDeployment(admin, {name: REFHOST_DEPLOYMENT, machine: 'worker-1'});
        await deleteDeployment(admin, {name: REFHOST_DEPLOYMENT, machine: 'worker-1'});
        await admin.getByTestId('nav-secrets').click();
        await searchExplorerAndSelect(admin, VALUES_SEARCH, MOVE_CONFIG);
        await moveExplorerSelectionToSpace(admin, {space: 'global'});
        await expectExplorerPath(admin, `global/${MOVE_CONFIG}`);
        await deleteExplorerSelection(admin);
        await expect(rowNamed(admin, MOVE_CONFIG)).toBeHidden({timeout: LONG_UI_TIMEOUT});
        await clearExplorerSearch(admin, VALUES_SEARCH);
      });
    },
  },
  {
    id: 'space-move-asset',
    title: 'assets move between spaces; referenced assets refuse move and delete',
    description: 'The mounted workload asset refuses both a cross-space move and delete; a fresh asset moves into the restricted space, appears live in the restricted session, and moves back.',
    requires: ['asset-backed-nix-docker-deployment', 'access-restricted-space-granted'],
    async run(ctx) {
      const admin = ctx.page;
      const restricted = restrictedPage(ctx);

      await test.step('the mounted asset refuses cross-space move and delete', async () => {
        await admin.getByTestId('nav-assets').click();
        await searchExplorerAndSelect(admin, ASSETS_SEARCH, 'e2e-workload-asset.txt');
        await expectExplorerPath(admin, 'global/e2e-workload-asset.txt');
        await expectMoveToSpaceBlocked(admin, {space: RESTRICTED_SPACE, message: MOVE_BLOCKED_MESSAGE});
        await expectDeleteSelectionBlocked(admin, {message: DELETE_BLOCKED_MESSAGE});
        await expect(rowNamed(admin, 'e2e-workload-asset.txt').first()).toBeVisible();
        await clearExplorerSearch(admin, ASSETS_SEARCH);
      });

      await test.step('a fresh asset moves into the restricted space and back', async () => {
        await restricted.getByTestId('nav-assets').click();
        await restricted.getByPlaceholder(ASSETS_SEARCH).fill(MOVE_ASSET);
        await expect(rowNamed(restricted, MOVE_ASSET)).toHaveCount(0);

        await selectExplorerRow(admin, 'global');
        await createAsset(admin, {key: MOVE_ASSET, content: 'space-move-asset-content'});
        await searchExplorerAndSelect(admin, ASSETS_SEARCH, MOVE_ASSET);
        await expectExplorerPath(admin, `global/${MOVE_ASSET}`);
        await moveExplorerSelectionToSpace(admin, {space: RESTRICTED_SPACE});
        await expectExplorerPath(admin, `${RESTRICTED_SPACE}/${MOVE_ASSET}`);
        await expect(rowNamed(restricted, MOVE_ASSET).first()).toBeVisible({timeout: LONG_UI_TIMEOUT});

        await moveExplorerSelectionToSpace(admin, {space: 'global'});
        await expectExplorerPath(admin, `global/${MOVE_ASSET}`);
        await expect(rowNamed(restricted, MOVE_ASSET)).toHaveCount(0, {timeout: LONG_UI_TIMEOUT});
        await clearExplorerSearch(restricted, ASSETS_SEARCH);

        await deleteExplorerSelection(admin);
        await expect(rowNamed(admin, MOVE_ASSET)).toBeHidden({timeout: LONG_UI_TIMEOUT});
        await clearExplorerSearch(admin, ASSETS_SEARCH);
      });
    },
  },
  {
    id: 'space-move-restricted-scope',
    title: 'the restricted move dialog offers only visible spaces',
    description: 'For the space admin, the item Move dialog lists only its granted space — global is not offered — and folder Move dialogs carry no space picker at all.',
    requires: ['access-restricted-values-created'],
    async run(ctx) {
      const restricted = restrictedPage(ctx);
      await restricted.getByTestId('nav-secrets').click();

      await test.step('item move dialog lists only the granted space', async () => {
        await searchExplorerAndSelect(restricted, VALUES_SEARCH, RESTRICTED_SECRET);
        await restricted.getByRole('button', {name: 'Move', exact: true}).click();
        const dialog = restricted.getByRole('dialog').filter({hasText: /Move /});
        await expect(dialog).toBeVisible({timeout: LONG_UI_TIMEOUT});
        const options = dialog.getByLabel('Destination space', {exact: true}).locator('option');
        await expect(options).toHaveCount(1);
        await expect(options.first()).toHaveText(RESTRICTED_SPACE);
        await dialog.getByRole('button', {name: 'Cancel', exact: true}).click();
        await expect(dialog).toBeHidden({timeout: LONG_UI_TIMEOUT});
      });

      await test.step('folder move dialog has no space picker', async () => {
        await searchExplorerAndSelect(restricted, VALUES_SEARCH, RESTRICTED_FOLDER);
        await restricted.getByRole('button', {name: 'Move', exact: true}).click();
        const dialog = restricted.getByRole('dialog').filter({hasText: /Move /});
        await expect(dialog).toBeVisible({timeout: LONG_UI_TIMEOUT});
        await expect(dialog.getByLabel('Destination space', {exact: true})).toHaveCount(0);
        await dialog.getByRole('button', {name: 'Cancel', exact: true}).click();
        await expect(dialog).toBeHidden({timeout: LONG_UI_TIMEOUT});
        await clearExplorerSearch(restricted, VALUES_SEARCH);
        await closeExplorerInspector(restricted);
      });
    },
  },
];
