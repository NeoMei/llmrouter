import { mkdir, writeFile } from 'node:fs/promises'
import { join } from 'node:path'

import { test, expect, type Page } from '@playwright/test'

async function openKeyDrawer(page: Page) {
  const now = Math.floor(Date.now() / 1000)
  const key = {
    id: 2,
    name: 'native key',
    key: 'synthetic-masked-key',
    status: 1,
    remain_quota: 100,
    used_quota: 0,
    unlimited_quota: false,
    expired_time: -1,
    created_time: now,
    accessed_time: now,
    group: 'default',
    cross_group_retry: false,
    model_limits_enabled: false,
    model_limits: '',
    allow_ips: '',
  }
  const writes: { url: string; body: Record<string, unknown> }[] = []
  await page.addInitScript(() => {
    localStorage.setItem('setup_status_checked', 'true')
    localStorage.setItem('i18nextLng', 'en')
  })
  await page.route('**/api/**', async (route) => {
    const url = new URL(route.request().url())
    const path = url.pathname
    if (path === '/api/user/auth/refresh') {
      return route.fulfill({
        json: {
          success: true,
          data: {
            access_token: 'synthetic-native-browser',
            token_type: 'Bearer',
            access_expires_at: now + 3600,
            user: { id: 1, username: 'native', role: 100, status: 1 },
            session: {
              sid: 'synthetic-native-session',
              current: true,
              login_method: 'password',
              ip: '127.0.0.1',
              user_agent: 'test',
              created_at: now,
              last_active_at: now,
              expires_at: now + 3600,
            },
          },
        },
      })
    }
    if (path === '/api/token/' && route.request().method() === 'PUT') {
      writes.push({
        url: path + url.search,
        body: route.request().postDataJSON(),
      })
      return route.fulfill({ json: { success: true, data: key } })
    }
    if (path === '/api/token/') {
      return route.fulfill({
        json: {
          success: true,
          data: { items: [key], total: 1, page: 1, page_size: 10 },
        },
      })
    }
    if (path === '/api/token/2') {
      return route.fulfill({ json: { success: true, data: key } })
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
          data: { id: 1, username: 'native', role: 100, status: 1 },
        },
      })
    }
    if (path === '/api/status') {
      return route.fulfill({
        json: {
          success: true,
          data: {
            system_name: 'Native key fixture',
            quota_display_type: 'TOKENS',
            quota_per_unit: 500000,
          },
        },
      })
    }
    if (path === '/api/notice') {
      return route.fulfill({ json: { success: true, data: '' } })
    }
    return route.fulfill({ json: { success: true, data: [] } })
  })
  await page.goto('/keys')
  await page.getByRole('button', { name: 'Edit', exact: true }).first().click()
  await expect(
    page.getByRole('textbox', { name: 'Name', exact: true })
  ).toHaveValue('native key')
  return writes
}

for (const edit of ['name', 'quota', 'unlimited'] as const) {
  test(`key drawer ${edit} edit sends the intended quota mutation scope`, async ({
    page,
  }) => {
    const writes = await openKeyDrawer(page)
    if (edit === 'name') {
      await page
        .getByRole('textbox', { name: 'Name', exact: true })
        .fill('renamed only')
    }
    if (edit === 'quota') await page.getByRole('spinbutton').fill('50')
    if (edit === 'unlimited') {
      await page
        .getByRole('switch', { name: 'Unlimited Quota', exact: true })
        .click()
    }
    await page
      .getByRole('button', { name: 'Save changes', exact: true })
      .press('Enter')
    await expect.poll(() => writes.length).toBe(1)
    expect(writes[0].url).toBe(
      edit === 'name' ? '/api/token/?preserve_quota=true' : '/api/token/'
    )
    expect(writes[0].body.id).toBe(2)
    if (edit === 'name') expect(writes[0].body.remain_quota).toBe(100)
    if (edit === 'quota') expect(writes[0].body.remain_quota).toBe(50)
    if (edit === 'unlimited') expect(writes[0].body.unlimited_quota).toBe(true)
    if (process.env.KEY_QUOTA_WIRE_DIR) {
      await mkdir(process.env.KEY_QUOTA_WIRE_DIR, { recursive: true })
      await writeFile(
        join(process.env.KEY_QUOTA_WIRE_DIR, `${edit}.json`),
        JSON.stringify(writes[0])
      )
    }
  })
}
