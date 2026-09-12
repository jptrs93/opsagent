import { defineConfig } from '@playwright/test';

export default defineConfig({
  testDir: './tests',
  fullyParallel: true,
  workers: 4,
  retries: 0,
  reporter: 'list',
  outputDir: './test-results',
  use: {
    browserName: 'chromium',
    baseURL: 'http://127.0.0.1:4174',
    contextOptions: { reducedMotion: 'reduce' },
    trace: 'retain-on-failure',
  },
  projects: [1440, 375].flatMap((width) =>
    (['light', 'dark'] as const).map((colorScheme) => ({
      name: `chromium-${width}-${colorScheme}`,
      use: { viewport: { width, height: 900 }, colorScheme },
    })),
  ),
  webServer: {
    command: 'python3 -m http.server 4174 --bind 127.0.0.1 --directory build',
    url: 'http://127.0.0.1:4174/logging/',
    reuseExistingServer: false,
    stderr: 'ignore',
  },
});
