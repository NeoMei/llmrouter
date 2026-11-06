import { defineConfig } from '@playwright/test'

export default defineConfig({
  testDir: '.',
  testMatch: /(?:settings-state|key-quota-edit|key-menu)\.spec\.ts/,
  workers: 1,
  reporter: 'line',
  timeout: 20000,
  use: {
    baseURL: 'http://127.0.0.1:58026',
    viewport: process.env.SETTINGS_MOBILE
      ? { width: 390, height: 844 }
      : { width: 1280, height: 720 },
    channel: 'chrome',
    screenshot: 'only-on-failure',
  },
  webServer: {
    command:
      'bun x rsbuild dev --config rsbuild.settings.config.ts --host 127.0.0.1 --port 58026',
    url: 'http://127.0.0.1:58026',
    reuseExistingServer: false,
    timeout: 120000,
  },
})
