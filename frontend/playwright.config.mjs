import { defineConfig } from '@playwright/test';

export default defineConfig({
  testDir: './e2e',
  outputDir: '../tmp/e2e-results',
  // Each test owns port 5173; never reuse a developer's running server.
  workers: 1,
  fullyParallel: false,
  timeout: 60_000,
  expect: { timeout: 10_000 },
  retries: 0,
  reporter: [['list'], ['json', { outputFile: '../tmp/e2e-results/results.json' }]],
  use: {
    channel: process.env.CONNECT_E2E_CHANNEL || undefined,
    viewport: { width: 880, height: 640 },
    locale: 'zh-CN',
    serviceWorkers: 'block',
    screenshot: 'only-on-failure',
    trace: 'retain-on-failure',
  },
});
