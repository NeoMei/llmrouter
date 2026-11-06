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

const now = new Date('2026-11-06T06:00:00Z')
const timestamp = now.getTime() / 1000

async function openKeys(page: Page) {
  const key = {
    id: 2,
    name: 'menu regression fixture',
    key: 'synthetic-masked-key',
    status: 1,
    remain_quota: 100,
    used_quota: 0,
    unlimited_quota: false,
    expired_time: timestamp + 60,
    created_time: timestamp - 31,
    accessed_time: timestamp - 31,
    group: 'default',
    cross_group_retry: false,
    model_limits_enabled: false,
    model_limits: '',
    allow_ips: '',
  }
  const unexpected: string[] = []
  let keys = [key]
  let releaseKey = () => {}
  const keyResponse = new Promise<void>((resolve) => {
    releaseKey = resolve
  })
  await page.clock.install({ time: now })
  await page.addInitScript(() => {
    localStorage.setItem('setup_status_checked', 'true')
    localStorage.setItem('i18nextLng', 'en')
  })
  await page.route('**/*', async (route) => {
    const url = new URL(route.request().url())
    const path = url.pathname
    if (url.origin !== 'http://127.0.0.1:58026') {
      unexpected.push(`${route.request().method()} ${url.origin}${path}`)
      return route.abort('blockedbyclient')
    }
    if (!path.startsWith('/api/')) return route.continue()
    if (path === '/api/user/auth/refresh') {
      return route.fulfill({
        json: {
          success: true,
          data: {
            access_token: 'synthetic-menu-browser',
            token_type: 'Bearer',
            access_expires_at: timestamp + 3600,
            user: { id: 1, username: 'fixture', role: 100, status: 1 },
            session: {
              sid: 'synthetic-menu-session',
              current: true,
              login_method: 'password',
              ip: '127.0.0.1',
              user_agent: 'test',
              created_at: timestamp,
              last_active_at: timestamp,
              expires_at: timestamp + 3600,
            },
          },
        },
      })
    }
    if (path === '/api/token/') {
      return route.fulfill({
        json: { success: true, data: { items: keys, total: keys.length } },
      })
    }
    if (path === '/api/token/2/key') {
      await keyResponse
      return route.fulfill({
        json: { success: true, data: { key: 'synthetic-full-key' } },
      })
    }
    if (path === '/api/user/self/groups') {
      return route.fulfill({
        json: {
          success: true,
          data: { default: { desc: 'Default', ratio: 1 } },
        },
      })
    }
    if (path === '/api/user/self') {
      return route.fulfill({
        json: {
          success: true,
          data: { id: 1, username: 'fixture', role: 100, status: 1 },
        },
      })
    }
    if (path === '/api/status') {
      return route.fulfill({
        json: {
          success: true,
          data: {
            system_name: 'Key menu fixture',
            quota_display_type: 'TOKENS',
            quota_per_unit: 500000,
          },
        },
      })
    }
    if (path === '/api/notice') {
      return route.fulfill({ json: { success: true, data: '' } })
    }
    unexpected.push(`${route.request().method()} ${path}`)
    return route.abort('blockedbyclient')
  })
  await page.goto('/keys')
  await expect(page.getByText(key.name, { exact: true })).toBeVisible()
  return {
    releaseKey,
    unexpected,
    replaceKey: () => {
      keys = [
        {
          ...key,
          id: 3,
          name: 'replacement key fixture',
          key: 'replacement-masked',
        },
      ]
    },
  }
}

test('row menu stays open while a key loads and remains usable after it resolves', async ({
  page,
}) => {
  const fixture = await openKeys(page)
  const trigger = page.getByRole('button', { name: 'Open menu', exact: true })
  try {
    const keyLoaded = page.waitForResponse('**/api/token/2/key')
    await trigger.click()
    await expect(trigger).toHaveAttribute('aria-expanded', 'true')
    fixture.releaseKey()
    await keyLoaded
    await page.clock.fastForward(30_000)
    await expect(trigger).toHaveAttribute('aria-expanded', 'true')
    await expect(
      page.getByRole('menuitem', { name: 'Delete', exact: true })
    ).toBeVisible()
    await page.getByRole('menuitem', { name: 'Delete', exact: true }).click()
    await expect(page.getByRole('alertdialog')).toContainText(
      'menu regression fixture'
    )
    await page.getByRole('button', { name: 'Cancel', exact: true }).click()
    expect(fixture.unexpected).toEqual([])
  } finally {
    fixture.releaseKey()
  }
})

test('time refresh updates timestamps without closing an open key popover', async ({
  page,
}) => {
  const fixture = await openKeys(page)
  fixture.releaseKey()
  const trigger = page.getByRole('button', {
    name: 'sk-synthetic-masked-key',
    exact: true,
  })
  const keyLoaded = page.waitForResponse('**/api/token/2/key')
  await trigger.click()
  // Warming the key cache is independent of the timer regression under test.
  await keyLoaded
  if ((await trigger.getAttribute('aria-expanded')) !== 'true') {
    await trigger.click()
  }
  await expect(page.getByText('Full API Key', { exact: true })).toBeVisible()
  await expect(page.locator('input[readonly]')).toHaveValue(
    'sk-synthetic-full-key'
  )
  await expect(page.getByText('Just now', { exact: true })).toHaveCount(2)
  await page.clock.fastForward(30_000)
  await expect(page.getByText('Just now', { exact: true })).toHaveCount(0)
  await expect(trigger).toHaveAttribute('aria-expanded', 'true')
  await expect(page.getByText('Full API Key', { exact: true })).toBeVisible()
  await expect(page.locator('input[readonly]')).toHaveValue(
    'sk-synthetic-full-key'
  )
  expect(fixture.unexpected).toEqual([])
})

test('a refreshed list does not transfer selection or an open menu to a different key', async ({
  page,
}) => {
  const fixture = await openKeys(page)
  fixture.releaseKey()
  await page.getByRole('checkbox', { name: 'Select row', exact: true }).check()
  const keyLoaded = page.waitForResponse('**/api/token/2/key')
  await page.getByRole('button', { name: 'Open menu', exact: true }).click()
  await keyLoaded
  await page.clock.fastForward(30_000)
  await expect(
    page.getByRole('menuitem', { name: 'Delete', exact: true })
  ).toBeVisible()
  fixture.replaceKey()
  // Reconnection is a normal background refresh; it does not click outside
  // the open menu or change the current user.
  const refreshed = page.waitForResponse('**/api/token/?*')
  await page.evaluate(() => {
    window.dispatchEvent(new Event('offline'))
    window.dispatchEvent(new Event('online'))
  })
  await refreshed
  await expect(
    page.getByText('replacement key fixture', { exact: true })
  ).toBeVisible()
  await expect(
    page.getByRole('checkbox', { name: 'Select row', exact: true })
  ).not.toBeChecked()
  await expect(
    page.getByRole('button', { name: 'Open menu', exact: true })
  ).toHaveAttribute('aria-expanded', 'false')
  await expect(
    page.getByRole('menuitem', { name: 'Delete', exact: true })
  ).toHaveCount(0)
  expect(fixture.unexpected).toEqual([])
})
