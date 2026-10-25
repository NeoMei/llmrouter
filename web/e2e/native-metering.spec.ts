import { test, expect, type Page } from '@playwright/test'

async function fixture(page: Page) {
  page.on('pageerror', (e) => console.log('PAGEERROR', e.message))
  page.on('console', (m) => {
    if (m.type() === 'error') console.log('CONSOLE', m.text())
  })
  const writes: { key: string; value: string }[] = []
  const now = Math.floor(Date.now() / 1000)
  await page.addInitScript(() => {
    localStorage.setItem('setup_status_checked', 'true')
    localStorage.setItem('i18nextLng', 'en')
    localStorage.setItem(
      'status',
      JSON.stringify({ system_name: 'Local native acceptance' })
    )
  })
  await page.route('**/api/**', async (route) => {
    const path = new URL(route.request().url()).pathname
    if (path === '/api/user/auth/refresh') {
      return route.fulfill({
        json: {
          success: true,
          data: {
            access_token: 'fixture',
            token_type: 'Bearer',
            access_expires_at: now + 3600,
            user: { id: 1, username: 'fixture', role: 100, status: 1 },
            session: {
              sid: 'local-native',
              current: true,
              login_method: 'password',
              ip: '127.0.0.1',
              user_agent: 'fixture',
              created_at: now,
              last_active_at: now,
              expires_at: now + 3600,
            },
          },
        },
      })
    }
    if (path === '/api/option/') {
      if (route.request().method() === 'PUT') {
        writes.push(route.request().postDataJSON())
        return route.fulfill({ json: { success: true } })
      }
      return route.fulfill({
        json: {
          success: true,
          data: [
            { key: 'ModelNativePricing', value: '{}' },
            { key: 'ModelNativeReservationLimits', value: '{}' },
          ],
        },
      })
    }
    if (path === '/api/native-billing/self') {
      return route.fulfill({
        json: {
          success: true,
          data: [
            {
              id: 'pending-fixture',
              state: 'pending_measurement',
              reserved: '100',
              platform: false,
            },
            {
              id: 'zero-fixture',
              state: 'settled',
              reserved: '100',
              final: '0',
              platform: false,
            },
          ],
        },
      })
    }
    if (path === '/api/notice') {
      return route.fulfill({ json: { success: true, data: '' } })
    }
    if (path === '/api/status') {
      return route.fulfill({
        json: {
          success: true,
          data: { system_name: 'Local native acceptance' },
        },
      })
    }
    return route.fulfill({ json: { success: true, data: [], total: 0 } })
  })
  return writes
}
test('native editor saves image tier contract and reservation option independently', async ({
  page,
}) => {
  const writes = await fixture(page)
  await page.goto('/system-settings/models/native-pricing')
  await page.getByLabel('Model', { exact: true }).fill('synthetic-image')
  await page
    .getByRole('combobox', { name: 'Operation', exact: true })
    .selectOption('image_generation')
  await page.getByLabel('Dimension or image tier').fill('1024x1024')
  await page.getByLabel('Price numerator').fill('3')
  await page.getByLabel('Price denominator').fill('2')
  await page
    .getByRole('button', { name: 'Save native price', exact: true })
    .click()
  await expect.poll(() => writes.length).toBe(1)
  expect(writes[0].key).toBe('ModelNativePricing')
  expect(
    JSON.parse(writes[0].value)['synthetic-image'].image_generation
  ).toMatchObject({
    operation: 'image_generation',
    policy: 'successful_outputs_v1',
    terms: [
      {
        key: '1024x1024',
        unit: 'image',
        price: { numerator: '3', denominator: '2' },
      },
    ],
  })
  await page.getByLabel('Reservation model').fill('synthetic-tts')
  await page.getByLabel('Maximum quantity').fill('2000')
  await page
    .getByRole('button', { name: 'Save reservation limit', exact: true })
    .click()
  await expect.poll(() => writes.length).toBe(2)
  expect(writes[1].key).toBe('ModelNativeReservationLimits')
  expect(JSON.parse(writes[1].value)).toEqual({
    'synthetic-tts': { speech: { value: '2000', scale: 0 } },
  })
})
test('user native receipts distinguish pending from confirmed zero', async ({
  page,
}) => {
  await fixture(page)
  await page.goto('/usage-logs/common')
  const pending = page
    .getByRole('listitem')
    .filter({ hasText: 'pending-fixture' })
  await expect(pending).toContainText('Final charge: Pending')
  const zero = page.getByRole('listitem').filter({ hasText: 'zero-fixture' })
  await expect(zero).toContainText('Final charge: 0')
})
