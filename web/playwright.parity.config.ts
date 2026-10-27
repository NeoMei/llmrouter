import { defineConfig } from '@playwright/test'

export default defineConfig({
  testDir: './e2e',
  testMatch: /product-user-parity\.spec\.ts/,
  workers: 1,
  reporter: 'line',
  use: {
    baseURL: 'http://127.0.0.1:58006',
    channel: 'chrome',
    screenshot: 'only-on-failure',
  },
  webServer: {
    command: 'bun run dev --host 127.0.0.1 --port 58006',
    url: 'http://127.0.0.1:58006',
    reuseExistingServer: false,
    timeout: 120000,
  },
})
