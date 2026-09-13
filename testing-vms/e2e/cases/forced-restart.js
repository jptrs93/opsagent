import {
  deploymentOutputOccurrenceCount,
  expectDeploymentHistoryText,
  expectDeploymentOutputOccurrences,
  expectDeploymentRunning,
  expectHTTPText,
  restartDeployment,
  restartDeploymentFromEditor,
} from '../helpers/ui.js';

const SECONDARY_HOST = process.env.OPD_SECONDARY_HOST || 'opendeploy-secondary';
const BASELINE_START_LINE = 'nixdockerbuild1 count=1 ';
const ROLLOVER_READY_LINE = 'rollover readiness sent generation=host-v2';

// A restart writes a deployment event whose definition is unchanged, so only
// the top-level version moves and the scheduler replaces the placement under
// the deployment's upgrade strategy. Each case counts a once-per-start log
// line before the restart and expects one more afterwards.
export const forcedRestartCases = [
  {
    id: 'forced-restart-recreate',
    title: 'restart a recreate deployment',
    description: 'Restarts the baseline deployment from the inspector: the workload starts again at the same spec version and history records the restart.',
    requires: ['nix-docker-baseline'],
    async run(ctx) {
      const name = 'nixdockerbuild1';
      const before = await deploymentOutputOccurrenceCount(ctx.page, name, BASELINE_START_LINE);
      await restartDeployment(ctx.page, {name});
      await expectDeploymentRunning(ctx.page, {name});
      await expectDeploymentOutputOccurrences(ctx.page, name, BASELINE_START_LINE, before + 1);
      await expectDeploymentHistoryText(ctx.page, {name, text: 'restarted'});
    },
  },
  {
    id: 'forced-restart-editor',
    title: 'restart from the deployment editor',
    description: 'Restarts the baseline deployment from the update editor footer: the button is disabled while the editor holds changes and the workload starts again once confirmed.',
    requires: ['forced-restart-recreate'],
    async run(ctx) {
      const name = 'nixdockerbuild1';
      const before = await deploymentOutputOccurrenceCount(ctx.page, name, BASELINE_START_LINE);
      await restartDeploymentFromEditor(ctx.page, {name, edit: {RESTART_EDITOR_PROBE: '1'}});
      await expectDeploymentRunning(ctx.page, {name});
      await expectDeploymentOutputOccurrences(ctx.page, name, BASELINE_START_LINE, before + 1);
    },
  },
  {
    id: 'forced-restart-rollover',
    title: 'restart a rollover deployment',
    description: 'Restarts a ROLLOVER deployment: the replacement signals readiness before the old placement is retired and the host port keeps answering.',
    requires: ['host-network-rollover'],
    async run(ctx) {
      const name = 'rollover-host';
      const before = await deploymentOutputOccurrenceCount(ctx.page, name, ROLLOVER_READY_LINE);
      await restartDeployment(ctx.page, {name});
      await expectDeploymentOutputOccurrences(ctx.page, name, ROLLOVER_READY_LINE, before + 1);
      await expectDeploymentRunning(ctx.page, {name});
      await expectHTTPText(`http://${SECONDARY_HOST}:18180/`, 'rollover generation=host-v2');
    },
  },
];
