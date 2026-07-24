import {defineConfig, devices} from '@playwright/test';

const baseURL = process.env.OPD_BASE_URL || 'http://localhost:8080';

export default defineConfig({
  testDir: './flows',
  timeout: 30 * 60 * 1000,
  expect: {
    timeout: 4 * 1000,
  },
  retries: 0,
  reporter: [['./opd-reporter.js']],
  use: {
    baseURL,
    actionTimeout: 5 * 1000,
    navigationTimeout: 10 * 1000,
    trace: 'retain-on-failure',
    screenshot: 'only-on-failure',
    video: 'retain-on-failure',
    ignoreHTTPSErrors: process.env.OPD_IGNORE_HTTPS_ERRORS === 'true',
  },
  projects: [
    {
      name: 'chromium',
      use: {
        ...devices['Desktop Chrome'],
        launchOptions: {
          args: baseURL.startsWith('http://') ? [`--unsafely-treat-insecure-origin-as-secure=${baseURL}`] : [],
        },
      },
    },
  ],
});
