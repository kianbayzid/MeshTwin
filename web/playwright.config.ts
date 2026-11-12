import { defineConfig } from '@playwright/test';

// Smoke tests against a running API that serves the e2e build (npm run build:e2e) and the API's test fixtures:
//   api -static-dir web/dist-e2e -summaries-dir api/testdata -listen 127.0.0.1:8080
// CI runs them in the pinned Playwright container (bundled Chromium); locally PW_CHANNEL=chrome reuses an installed Chrome.
export default defineConfig({
  testDir: 'e2e',
  timeout: 30_000,
  retries: process.env.CI ? 1 : 0,
  reporter: process.env.CI ? [['list'], ['html', { open: 'never' }]] : 'list',
  use: {
    baseURL: process.env.E2E_BASE_URL ?? 'http://127.0.0.1:8080',
    channel: process.env.PW_CHANNEL,
    viewport: { width: 1440, height: 900 },
    trace: 'retain-on-failure',
  },
});
