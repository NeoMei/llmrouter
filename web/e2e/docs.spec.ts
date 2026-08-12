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

const expectedSections = [
  'start',
  'get-token',
  'connection-address',
  'choose-model',
  'configure-agent',
  'verify',
  'troubleshooting',
]

test.beforeEach(async ({ page }) => {
  await page.addInitScript(() => {
    window.localStorage.setItem('setup_status_checked', 'true')
  })
})

test('renders the public concise LLMRouter connection guide', async ({
  page,
}) => {
  await page.goto('/docs')

  await expect(
    page.getByRole('heading', { name: 'Connect an LLMRouter Agent' })
  ).toBeVisible()
  await expect(page.getByText('SDK', { exact: false })).toHaveCount(0)
  await expect(page.getByText('API tutorial', { exact: false })).toHaveCount(0)

  for (const sectionId of expectedSections) {
    await expect(page.locator(`#${sectionId}`)).toBeVisible()
  }

  await expect(
    page.getByRole('link', { name: 'Manage Tokens' })
  ).toHaveAttribute('href', '/keys')
  await expect(page.getByRole('link', { name: 'View models' })).toHaveAttribute(
    'href',
    '/dashboard/models'
  )
  await expect(
    page.getByRole('link', { name: 'Manage routes' })
  ).toHaveAttribute('href', '/client-model-routes')
  await expect(
    page.getByRole('link', { name: 'Install scripts' })
  ).toHaveAttribute('href', '/install/')
})

test('persists an explicitly selected documentation language', async ({
  page,
}) => {
  await page.goto('/docs')

  await page.getByRole('button', { name: '中文' }).click()
  await expect(
    page.getByRole('heading', { name: '连接 LLMRouter Agent' })
  ).toBeVisible()

  await page.reload()
  await expect(
    page.getByRole('heading', { name: '连接 LLMRouter Agent' })
  ).toBeVisible()

  await page.getByRole('button', { name: 'English' }).click()
  await expect(
    page.getByRole('heading', { name: 'Connect an LLMRouter Agent' })
  ).toBeVisible()
})

test('copies the Base URL and confirms the action', async ({ page }) => {
  await page.addInitScript(() => {
    Object.defineProperty(navigator, 'clipboard', {
      configurable: true,
      value: undefined,
    })
    document.execCommand = () => true
  })
  await page.goto('/docs')

  await page.getByRole('button', { name: 'Copy Base URL' }).click()

  await expect(page.getByText('Copied', { exact: true })).toBeVisible()
  await expect(
    page.getByText('https://llmrouter.quukk.com/v1', { exact: true })
  ).toBeVisible()
})

test('lists supported Agents and the install script link', async ({ page }) => {
  await page.goto('/docs#configure-agent')

  const configureAgent = page.locator('#configure-agent')
  await expect(configureAgent).toBeVisible()
  await expect(configureAgent.getByText('Codex CLI/Desktop')).toBeVisible()
  await expect(configureAgent.getByText('Claude Code/Desktop')).toBeVisible()
  await expect(configureAgent.getByText('MCP tool access')).toBeVisible()
  await expect(configureAgent.getByText('OpenCode')).toBeVisible()
  await expect(configureAgent.getByText('Continue')).toBeVisible()
  await expect(configureAgent.getByText('Aider')).toBeVisible()
  await expect(
    configureAgent.getByRole('link', { name: 'Install scripts' })
  ).toHaveAttribute('href', '/install/')
})

test('keeps the configure-agent anchor after a refresh', async ({ page }) => {
  await page.goto('/docs#configure-agent')
  await expect(page).toHaveURL(/\/docs#configure-agent$/)
  await expect(page.locator('#configure-agent')).toBeVisible()

  await page.reload()
  await expect(page).toHaveURL(/\/docs#configure-agent$/)
  await expect(page.locator('#configure-agent')).toBeVisible()
})

test('shows documentation navigation on mobile', async ({ page }, testInfo) => {
  test.skip(
    testInfo.project.name !== 'mobile-chromium',
    'The desktop project has persistent sidebar navigation instead.'
  )

  await page.goto('/docs')

  const navigation = page.getByRole('navigation', {
    name: 'Documentation navigation',
  })
  await expect(navigation).toBeVisible()
  await expect(
    navigation.getByRole('link', { name: 'Configure an Agent' })
  ).toHaveAttribute('href', '#configure-agent')
})
