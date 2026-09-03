import {createNixDockerDeployment, NETWORKING_VIRTUAL} from '../helpers/ui.js';
import {expectMetricsCharts, expectMetricsControls, expectMetricsOverviewRow} from '../helpers/metrics.js';

const MIB = 1024 * 1024;
const LOADGEN_FLAKE = 'testexamples/loadgen/flake.nix';

export const metricsDeployCases = [
  {
    id: 'metrics-loadgen-worker-deployed',
    title: 'create worker load generator',
    description: 'Deploys the loadgen workload on worker-1 with virtual networking so it produces CPU, memory, and network samples.',
    requires: ['worker-enrolled'],
    async run(ctx) {
      await createNixDockerDeployment(ctx.page, {
        name: 'loadgen-worker',
        machine: 'worker-1',
        flake: LOADGEN_FLAKE,
        env: {LOADGEN_CPU_PERCENT: '40', LOADGEN_MEM_MIB: '64'},
        expectedEnv: {},
        networkingMode: NETWORKING_VIRTUAL,
      });
    },
  },
  {
    id: 'metrics-loadgen-primary-deployed',
    title: 'create primary load generator',
    description: 'Deploys a second loadgen on the primary that polls the worker instance through its virtual address.',
    requires: ['metrics-loadgen-worker-deployed'],
    async run(ctx) {
      await createNixDockerDeployment(ctx.page, {
        name: 'loadgen-primary',
        machine: 'primary',
        flake: LOADGEN_FLAKE,
        env: {
          LOADGEN_CPU_PERCENT: '20',
          LOADGEN_MEM_MIB: '32',
          LOADGEN_TARGET_ADDR: {type: 'address', name: 'loadgen-worker'},
        },
        expectedEnv: {},
        networkingMode: NETWORKING_VIRTUAL,
      });
    },
  },
];

export const metricsVerifyCases = [
  {
    id: 'metrics-overview-verified',
    title: 'verify metrics overview',
    description: 'Checks the live metrics overview lists both load generators with the expected CPU and memory, across the proxied and local paths.',
    requires: ['metrics-loadgen-primary-deployed'],
    async run(ctx) {
      await expectMetricsOverviewRow(ctx.page, {name: 'loadgen-worker', minCpuCores: 0.25, minMemBytes: 60 * MIB});
      await expectMetricsOverviewRow(ctx.page, {name: 'loadgen-primary', minCpuCores: 0.1, minMemBytes: 30 * MIB});
    },
  },
  {
    id: 'metrics-charts-verified',
    title: 'verify metrics charts',
    description: 'Checks the per-deployment charts carry CPU, memory, process, and network series, and that the scope controls work.',
    requires: ['metrics-overview-verified'],
    async run(ctx) {
      await expectMetricsCharts(ctx.page, {name: 'loadgen-worker', minCpuCores: 0.25, minMemBytes: 60 * MIB, expectNetwork: true});
      await expectMetricsCharts(ctx.page, {name: 'loadgen-primary', minCpuCores: 0.1, minMemBytes: 30 * MIB, expectNetwork: true});
      await expectMetricsControls(ctx.page, {name: 'loadgen-worker'});
    },
  },
];

export const metricsAfterUpgradeCases = [
  {
    id: 'metrics-after-upgrade-verified',
    title: 'verify metrics survive agent upgrade',
    description: 'Checks adopted containers are sampled again after the agents restart: both load generators report fresh samples.',
    requires: ['opendeploy-agents-upgraded', 'metrics-charts-verified'],
    async run(ctx) {
      await expectMetricsOverviewRow(ctx.page, {name: 'loadgen-worker', minCpuCores: 0.25, minMemBytes: 60 * MIB});
      await expectMetricsOverviewRow(ctx.page, {name: 'loadgen-primary', minCpuCores: 0.1, minMemBytes: 30 * MIB});
    },
  },
];
