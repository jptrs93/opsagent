import {defineConfig, devices} from '@playwright/test';

const baseURL = process.env.OPD_BASE_URL || 'http://localhost:8080';

export default defineConfig({
  testDir: './flows',
  timeout: 90 * 1000,
  expect: {
    timeout: 10 * 1000,
  },
  retries: 0,
  reporter: [['list']],
  use: {
    baseURL,
    actionTimeout: 10 * 1000,
    navigationTimeout: 20 * 1000,
    trace: 'retain-on-failure',
    screenshot: 'only-on-failure',
    video: 'retain-on-failure',
  },
  projects: [
    {
      name: 'chromium',
      use: {
        ...devices['Desktop Chrome'],
        launchOptions: {
          args: [`--unsafely-treat-insecure-origin-as-secure=${baseURL}`],
        },
      },
    },
  ],
});
