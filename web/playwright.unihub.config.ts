import { defineConfig } from '@playwright/test'

export default defineConfig({
  testDir: './e2e',
  testMatch: /unihub.spec.ts/,
  workers: 1,
  reporter: 'line',
  use: {
    baseURL: 'http://127.0.0.1:58004',
    channel: 'chrome',
    screenshot: 'only-on-failure',
  },
  webServer: {
    command: 'bun run dev --host 127.0.0.1 --port 58004',
    url: 'http://127.0.0.1:58004',
    reuseExistingServer: false,
    timeout: 120000,
  },
})
