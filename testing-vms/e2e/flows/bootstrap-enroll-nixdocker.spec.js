import {test} from '@playwright/test';
import {orderedCases} from '../cases/bootstrap-enroll-nixdocker.js';
import {runCases} from '../cases/runner.js';

test('bootstrap primary, enroll worker, and create Nix Docker deployment', async ({page}) => {
  const ctx = {page};
  await runCases(ctx, orderedCases);
});
