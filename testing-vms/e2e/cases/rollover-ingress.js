import {expect} from '@playwright/test';
import {Buffer} from 'node:buffer';
import {
  createNixDockerDeployment,
  createSecret,
  NETWORKING_VIRTUAL,
  setDeploymentHttpsRoutes,
  updateNixDockerDeployment,
  UPGRADE_ROLLOVER,
} from '../helpers/ui.js';
import {ingressCertificateBundle, requestHTTPSIngress} from '../helpers/httpsClient.js';

const ROLLOVER_FLAKE = 'testexamples/rollover/flake.nix';
const HOST = 'rollover.ingress.opendeploy.test';
const CERT_SECRET = 'e2e.tls.ingress.rollover';
const NAME = 'rollover-ingress';

const generationBody = generation => `rollover generation=${generation}`;

async function expectGeneration(generation, {timeout = 180_000} = {}) {
  await expect.poll(async () => {
    try {
      const response = await requestHTTPSIngress(HOST, {path: '/'});
      return {status: response.status, body: response.body.trim()};
    } catch (error) {
      return {error: String(error?.message || error)};
    }
  }, {message: `expected ${HOST} to serve ${generationBody(generation)}`, timeout}).toEqual({
    status: 200,
    body: generationBody(generation),
  });
}

export const rolloverIngressCases = [
  {
    id: 'rollover-ingress-created',
    title: 'create HTTPS ingress rollover deployment',
    description: 'Creates a virtual-network ROLLOVER deployment on worker-2 serving plain HTTP behind a terminating HTTPS ingress route.',
    requires: ['worker-2-enrolled', 'https-routes-restored'],
    async run(ctx) {
      await createSecret(ctx.page, {
        name: CERT_SECRET,
        value: Buffer.from(ingressCertificateBundle('rollover'), 'base64').toString('utf8'),
      });
      await createNixDockerDeployment(ctx.page, {
        name: NAME,
        machine: 'worker-2',
        flake: ROLLOVER_FLAKE,
        networkingMode: NETWORKING_VIRTUAL,
        upgradeStrategy: UPGRADE_ROLLOVER,
        env: {
          OPD_ROLLOVER_GENERATION: 'ingress-v1',
          OPD_ROLLOVER_ADDR: ':8080',
        },
        expectedEnv: {},
        verifyLogs: false,
      });
      await setDeploymentHttpsRoutes(ctx.page, {
        name: NAME,
        routes: [`https("${HOST}", 8080, { cert = secret("${CERT_SECRET}", { version = 1 }) })`],
      });
      await expectGeneration('ingress-v1');
    },
  },
  {
    id: 'rollover-ingress-continuity',
    title: 'verify ingress continuity across rollover',
    description: 'Hammers the HTTPS ingress route while the deployment rolls over with an 8s readiness delay: every response must come from the old or new generation, with no errors and no non-200s during warm-up or promotion.',
    requires: ['rollover-ingress-created'],
    async run(ctx) {
      const v1 = generationBody('ingress-v1');
      const v2 = generationBody('ingress-v2');
      const samples = [];
      let stop = false;
      const poller = (async () => {
        while (!stop) {
          try {
            const response = await requestHTTPSIngress(HOST, {path: '/'});
            samples.push({status: response.status, body: response.body.trim()});
          } catch (error) {
            samples.push({error: String(error?.message || error)});
          }
          await new Promise(resolve => setTimeout(resolve, 50));
        }
      })();

      try {
        await updateNixDockerDeployment(ctx.page, {
          name: NAME,
          machine: 'worker-2',
          env: {
            OPD_ROLLOVER_GENERATION: 'ingress-v2',
            OPD_ROLLOVER_ADDR: ':8080',
            OPD_ROLLOVER_READY_DELAY_MS: '8000',
          },
          upgradeStrategy: UPGRADE_ROLLOVER,
          readinessTimeoutSeconds: 60,
        });
        await expect.poll(
          () => samples.length > 0 && samples[samples.length - 1].body === v2,
          {message: 'expected the poller to observe ingress-v2 after promotion', timeout: 180_000},
        ).toBe(true);
      } finally {
        stop = true;
        await poller;
      }

      const failures = samples.filter(sample => sample.error || sample.status !== 200 || (sample.body !== v1 && sample.body !== v2));
      expect(failures, 'every sampled response must be a 200 from a live generation').toEqual([]);
      expect(samples.filter(sample => sample.body === v1).length, 'the poller must observe the old generation during warm-up').toBeGreaterThan(0);
      const lastV1 = samples.findLastIndex(sample => sample.body === v1);
      const firstV2 = samples.findIndex(sample => sample.body === v2);
      expect(lastV1, 'the generation switch must be monotonic').toBeLessThan(firstV2);
    },
  },
];
