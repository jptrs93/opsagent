import {enableLogMetricsArchive, expectNodeMetricsArchived} from '../helpers/ui.js';

export const archiveEnableCases = [
  {
    id: 'archive-enabled',
    title: 'turn on the log and metrics archive',
    description: 'Turns on the archive under Settings with its own bucket path and checks the Replication tab reads it as on.',
    requires: ['large-asset-storage-configured'],
    async run(ctx) {
      await enableLogMetricsArchive(ctx.page);
    },
  },
];

export const archiveVerifyCases = [
  {
    id: 'archive-status-verified',
    title: 'verify archived node metrics',
    description: 'Checks the Replication tab counts the primary\'s sealed node metrics file as held in the bucket once the upgraded agent has sent it.',
    requires: ['archive-enabled', 'opendeploy-agents-upgraded'],
    async run(ctx) {
      await expectNodeMetricsArchived(ctx.page, {nodes: ['primary']});
    },
  },
];
