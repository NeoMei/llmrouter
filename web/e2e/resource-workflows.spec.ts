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
import { expect, test, type Page } from '@playwright/test'

const now = Math.floor(Date.now() / 1000)

async function installFixture(
  page: Page,
  chats: Array<Record<string, string>> = []
) {
  await page.addInitScript(() => {
    window.localStorage.setItem('setup_status_checked', 'true')
    window.localStorage.setItem('i18nextLng', 'en')
    window.localStorage.setItem(
      'status',
      JSON.stringify({ system_name: 'Codex Gateway E2E' })
    )
  })

  await page.route('**/api/**', async (route) => {
    const path = new URL(route.request().url()).pathname
    const user = {
      id: 1,
      username: 'e2e-user',
      display_name: 'E2E User',
      role: 1,
      status: 1,
    }
    if (path === '/api/user/self') {
      await route.fulfill({ json: { success: true, data: user } })
      return
    }
    if (path === '/api/user/auth/refresh') {
      await route.fulfill({
        json: {
          success: true,
          data: {
            access_token: 'e2e-access-token',
            token_type: 'Bearer',
            access_expires_at: now + 3600,
            user,
            session: {
              sid: 'e2e-session',
              current: true,
              login_method: 'password',
              ip: '127.0.0.1',
              user_agent: 'Playwright',
              created_at: now,
              last_active_at: now,
              expires_at: now + 3600,
            },
          },
        },
      })
      return
    }
    if (path === '/api/status') {
      await route.fulfill({
        json: {
          success: true,
          data: { system_name: 'Codex Gateway E2E', chats },
        },
      })
      return
    }
    if (path === '/api/notice') {
      await route.fulfill({ json: { success: true, data: '' } })
      return
    }
    await route.fulfill({ json: { success: true, data: {} } })
  })
}

for (const path of ['/files', '/fine-tuning']) {
  test(`retired console route ${path} shows a 404 without making provider requests`, async ({
    page,
  }) => {
    const providerRequests: string[] = []
    await installFixture(page)
    await page.route('**/v1/**', (route) => {
      providerRequests.push(route.request().url())
      return route.abort()
    })
    await page.goto(path)
    await expect(
      page.getByRole('heading', { name: '404', exact: true })
    ).toBeVisible()
    expect(providerRequests).toEqual([])
  })
}

test('production iframe policy rejects insecure chat presets', async ({
  page,
}) => {
  await installFixture(page, [{ Insecure: 'http://example.com/preview' }])
  await page.goto('/chat/0')
  await expect(page.getByRole('alert')).toContainText('Unable to open chat')
  await expect(page.locator('iframe')).toHaveCount(0)
})

test('system settings require super-admin state', async ({ page }) => {
  await installFixture(page)
  await page.goto('/system-settings/site/system-info')
  await expect(page).toHaveURL(/\/403$/)
})
