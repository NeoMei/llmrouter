import { defineConfig } from '@playwright/test'

export default defineConfig({
  testDir: './src/features/usage-logs/__tests__',
  testMatch: 'technical-usage.spec.ts',
  workers: 1,
  reporter: 'line',
  use: {
    baseURL: 'http://127.0.0.1:58036',
    channel: 'chrome',
    launchOptions: { args: ['--disable-features=MacAppCodeSignClone'] },
  },
  webServer: {
    command: 'bun run dev --host 127.0.0.1 --port 58036',
    url: 'http://127.0.0.1:58036',
    reuseExistingServer: false,
  },
})
