import { defineConfig, devices } from '@playwright/test'

export default defineConfig({
  testDir: './e2e',
  fullyParallel: true,
  forbidOnly: Boolean(process.env.CI),
  retries: process.env.CI ? 2 : 0,
  reporter: process.env.CI ? [['line'], ['html', { open: 'never' }]] : 'line',
  use: {
    baseURL: 'http://127.0.0.1:3200',
    screenshot: 'only-on-failure',
    trace: 'retain-on-failure',
  },
  projects: [
    { name: 'chromium', use: { ...devices['Desktop Chrome'] } },
    { name: 'mobile-chromium', use: { ...devices['Pixel 7'] } },
  ],
  webServer: {
    command:
      'cd .. && PORT=3200 SQLITE_PATH=/tmp/codex-gateway-e2e.db OBJECT_STORE_TYPE=local OBJECT_STORE_LOCAL_ROOT=/tmp/codex-gateway-e2e-files go run .',
    url: 'http://127.0.0.1:3200/healthz',
    timeout: 120_000,
    reuseExistingServer: !process.env.CI,
  },
})
