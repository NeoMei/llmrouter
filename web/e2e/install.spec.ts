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
import { expect, test } from '@playwright/test'

test.beforeEach(async ({ page }) => {
  await page.route('**/v1/models', (route) =>
    route.fulfill({
      json: { data: [{ id: 'model-a' }, { id: 'model-b' }] },
    })
  )
  await page.route('**/install/manifest.json', (route) =>
    route.fulfill({
      json: {
        schemaVersion: 1,
        installerMinVersion: '0.1.0',
        fallbackModels: ['model-a'],
        clients: [
          {
            id: 'codex-cli',
            displayName: 'Codex CLI',
            platforms: ['linux', 'darwin', 'windows'],
            integration: 'direct-responses',
            delivery: 'automatic',
            configFamily: 'codex',
            probe: 'responses',
          },
          {
            id: 'claude-desktop',
            displayName: 'Claude Desktop',
            platforms: ['darwin', 'windows'],
            integration: 'mcp-tool',
            delivery: 'automatic',
            configFamily: 'claude-desktop',
            probe: 'mcp',
          },
        ],
        artifacts: {
          basePath: '/install/bin',
          checksumPath: '/install/checksums.txt',
          binaries: [
            {
              id: 'llmrouter-onboard',
              namePattern: 'llmrouter-onboard-{os}-{arch}[.exe]',
            },
            {
              id: 'llmrouter-mcp-bridge',
              namePattern: 'llmrouter-mcp-bridge-{os}-{arch}[.exe]',
            },
          ],
        },
      },
    })
  )
})

test('install page generates same-origin commands without secrets', async ({
  page,
}) => {
  await page.goto('/install/')

  await expect(
    page.getByRole('heading', { name: /Connect an LLMROUTER client/i })
  ).toBeVisible()

  // Claude Desktop is labelled as MCP tool access, not model replacement
  await expect(page.getByText('MCP tool access', { exact: true })).toBeVisible()
  await expect(page.getByText('does not replace Claude Desktop')).toBeVisible()

  // The generated command must reference the page origin and never a token
  const command = await page.locator('#install-command').textContent()
  expect(command).toBeTruthy()
  expect(command).toContain(page.url().split('/install/')[0])
  expect(command).not.toMatch(/sk-[A-Za-z0-9]/i)

  // No token input field exists on the page
  await expect(page.locator('input[type=password]')).toHaveCount(0)
  await expect(page.locator('input[type=text]')).toHaveCount(0)
})

test('install page shows fallback models and supports platform switch', async ({
  page,
}) => {
  await page.goto('/install/')

  // Model selector populates from the API
  await expect(page.locator('#model option')).toHaveCount(2)
  await expect(page.locator('#model')).toContainText('model-a')

  // Platform switch updates the command
  await page.locator('#platform').selectOption('windows')
  await page.waitForTimeout(300)
  const command = await page.locator('#install-command').textContent()
  expect(command).toContain('Invoke-WebRequest')
  expect(command).toContain('LLMROUTER_SERVER_URL')
})

test('install page copy button provides feedback', async ({ page }) => {
  await page.addInitScript(() => {
    Object.defineProperty(navigator, 'clipboard', {
      configurable: true,
      value: { writeText: () => Promise.resolve() },
    })
  })
  await page.goto('/install/')

  await page.locator('#copy-command').click()
  await expect(page.locator('#copy-status')).not.toBeEmpty({ timeout: 3000 })
})
