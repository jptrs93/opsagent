import {test} from '@playwright/test';
import {orderedCases} from '../cases/bootstrap-enroll-nixdocker.js';
import {runCases} from '../cases/runner.js';

// Run the complete baseline suite, bringing the changed value/asset UI paths
// forward so regressions are caught before the slower networking scenarios.
const earlyIDs = new Set([
  'config-created', 'secret-created', 'small-asset-created',
  'value-directories-explorer-verified', 'asset-directories-explorer-verified',
  'asset-backed-nix-docker-deployment', 'reference-usage-overlays-verified',
  'asset-backed-output-verified',
]);
const cases = orderedCases.flatMap(c => c.id === 'nix-docker-baseline'
  ? [c, ...orderedCases.filter(item => earlyIDs.has(item.id))]
  : earlyIDs.has(c.id) ? [] : [c]);
if (cases.length !== orderedCases.length || new Set(cases.map(c => c.id)).size !== cases.length) throw new Error('scenario set changed');

test('complete global state stream implementation validation', async ({page}) => {
  await runCases({page}, cases);
});
