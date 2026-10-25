import { defineConfig } from '@playwright/test'
export default defineConfig({
  testDir: './e2e',
  testMatch: /native-metering.spec.ts/,
  workers: 1,
  reporter: 'line',
  use: { baseURL: 'http://127.0.0.1:58014', channel: 'chrome' },
  webServer: {
    command: 'bun run dev --host 127.0.0.1 --port 58014',
    url: 'http://127.0.0.1:58014',
    reuseExistingServer: false,
    timeout: 120000,
  },
})
