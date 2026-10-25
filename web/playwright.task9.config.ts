/*
Copyright (C) 2023-2026 QuantumNous

This program is free software: you can redistribute it and/or modify
it under the terms of the GNU Affero General Public License as
published by the Free Software Foundation, either version 3 of the
License, or (at your option) any later version.

This program is distributed in the hope that it will be useful,
but WITHOUT ANY WARRANTY; without even the implied warranty of
MERCHANTABILITY or FITNESS FOR A PARTICULAR PURPOSE. See the
GNU Affero General Public License for more details.

You should have received a copy of the GNU Affero General Public License
along with this program. If not, see <https://www.gnu.org/licenses/>.

For commercial licensing, please contact support@quantumnous.com
*/
import { defineConfig, devices } from '@playwright/test'

const port = Number(process.env.TASK9_PORT)
const output = process.env.TASK9_ARTIFACT_DIR
// Keep config loading side-effect-free for tooling; require isolation before launching.
const configured =
  Number.isInteger(port) && port > 0 && port <= 65535 && !!output
const artifactRoot = output || 'test-results/account-pools-unconfigured'
export default defineConfig({
  testDir: './e2e',
  testMatch: /task9-admin.spec.ts/,
  fullyParallel: false,
  workers: 1,
  reporter: [
    ['line'],
    ['json', { outputFile: `${artifactRoot}/results.json` }],
  ],
  outputDir: `${artifactRoot}/artifacts`,
  use: {
    baseURL: `http://127.0.0.1:${port}`,
    screenshot: 'only-on-failure',
    trace: 'on',
    channel: 'chrome',
  },
  projects: [{ name: 'chromium', use: { ...devices['Desktop Chrome'] } }],
  webServer: {
    command: configured
      ? `bun run dev --host 127.0.0.1 --port ${port}`
      : `node -e "throw new Error('Set TASK9_PORT and TASK9_ARTIFACT_DIR to isolated fixture values')"`,
    url: `http://127.0.0.1:${port}`,
    reuseExistingServer: false,
    timeout: 120000,
  },
})
