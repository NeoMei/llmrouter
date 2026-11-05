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
import { test, expect } from '@playwright/test'

import {
  fixture,
  user,
  funding,
  bundle,
  screenshot,
  walletResponse,
} from './settings-fixture'

for (const failure of ['business', 'network'] as const) {
  test(`initial wallet ${failure} failure stays unavailable and safe GET retry accepts zero`, async ({
    page,
  }) => {
    const { writes } = await fixture(page)
    let failed = true
    await walletResponse(page, (route) => {
      if (!failed) return route.fulfill({ json: { success: true, data: user } })
      if (failure === 'network') return route.abort()
      return route.fulfill({
        json: { success: false, message: 'fixture unavailable' },
      })
    })
    await page.goto('/wallet')
    await expect(
      page.getByRole('alert').filter({ hasText: 'Wallet unavailable' })
    ).toBeVisible()
    await expect(
      page.getByText('Current Balance', { exact: true })
    ).toHaveCount(0)
    await expect(
      page.getByRole('button', {
        name: 'Create WeChat Pay QR code',
        exact: true,
      })
    ).toBeDisabled()
    await screenshot(page, `wallet-${failure}-failure`)
    failed = false
    await page
      .getByRole('button', { name: 'Retry wallet', exact: true })
      .click()
    await expect(
      page.getByText('Current Balance', { exact: true })
    ).toBeVisible()
    await expect(
      page.getByRole('alert').filter({ hasText: 'Wallet unavailable' })
    ).toHaveCount(0)
    expect(writes).toEqual([])
    await screenshot(page, 'wallet-successful-zero')
  })
}

test('failed shared-wallet refresh keeps labeled stale cash and blocks money actions', async ({
  page,
}) => {
  const { writes, requests } = await fixture(page, true)
  await page.goto('/wallet')
  await expect(page.getByText('1.230001 CNY', { exact: true })).toBeVisible()
  await screenshot(page, 'wallet-shared-cash')
  await walletResponse(page, (route) =>
    route.fulfill({ json: { success: false } })
  )
  await page
    .getByRole('button', { name: 'Refresh wallet', exact: true })
    .click()
  await expect(
    page.getByText('Showing previous wallet data. Refresh failed.', {
      exact: true,
    })
  ).toBeVisible()
  await expect(page.getByText('1.230001 CNY', { exact: true })).toBeVisible()
  await expect(
    page.getByRole('button', { name: 'Create WeChat Pay QR code', exact: true })
  ).toBeDisabled()
  expect(requests).not.toContain('/api/user/aff')
  expect(requests).not.toContain('/api/user/amount')
  expect(writes).toEqual([])
})

test('late prior-account wallet response cannot replace the new account zero', async ({
  page,
}) => {
  await fixture(page, true)
  let release!: () => void
  const gate = new Promise<void>((resolve) => {
    release = resolve
  })
  let started!: () => void
  const pending = new Promise<void>((resolve) => {
    started = resolve
  })
  await walletResponse(page, async (route) => {
    if (
      route.request().headers()['authorization'] === 'Bearer fixture-only-42'
    ) {
      started()
      await gate
      await route.fulfill({
        json: { success: true, data: { ...user, funding } },
      })
      return
    }
    await route.fulfill({ json: { success: true, data: { ...user, id: 99 } } })
  })
  await page.goto('/wallet')
  await pending
  await page.evaluate(
    (value) => window.settingsFixture.signIn(value),
    bundle(99)
  )
  await expect(page.getByText('Current Balance', { exact: true })).toBeVisible()
  release()
  await expect(page.getByText('1.230001 CNY', { exact: true })).toHaveCount(0)
  await screenshot(page, 'wallet-native-after-account-switch')
})

test('billing failure differs from successful empty history and retry stays read-only', async ({
  page,
}) => {
  const { writes } = await fixture(page)
  let failed = true
  await page.route('**/api/user/topup/self**', (route) =>
    route.fulfill({
      json: failed
        ? { success: false }
        : { success: true, data: { items: [], total: 0 } },
    })
  )
  await page.goto('/wallet')
  await page.getByRole('button', { name: 'Order History', exact: true }).click()
  const dialog = page.getByRole('dialog', {
    name: 'Billing History',
    exact: true,
  })
  await expect(dialog.getByRole('alert')).toContainText(
    'Failed to load billing history'
  )
  await expect(
    dialog.getByText('No billing records found', { exact: true })
  ).toHaveCount(0)
  failed = false
  await dialog
    .getByRole('button', { name: 'Retry history', exact: true })
    .click()
  await expect(
    dialog.getByText('No billing records found', { exact: true })
  ).toBeVisible()
  expect(writes).toEqual([])
})

test('cached cash is cleared immediately when a different account has a failing wallet', async ({
  page,
}) => {
  await fixture(page, true)
  await page.goto('/wallet')
  await expect(page.getByText('1.230001 CNY', { exact: true })).toBeVisible()
  await walletResponse(page, (route) =>
    route.fulfill({ json: { success: false } })
  )
  await page.evaluate(
    (value) => window.settingsFixture.signIn(value),
    bundle(99)
  )
  await expect(page.getByText('1.230001 CNY', { exact: true })).toHaveCount(0)
  await expect(
    page.getByRole('alert').filter({ hasText: 'Wallet unavailable' })
  ).toBeVisible()
})

test('history refresh failure retains labeled records and account change hides them', async ({
  page,
}) => {
  await fixture(page)
  let failed = false
  await page.route('**/api/user/topup/self**', (route) =>
    route.fulfill({
      json: failed
        ? { success: false }
        : {
            success: true,
            data: {
              items: [
                {
                  id: 1,
                  trade_no: 'fixture-order-A',
                  amount: 1,
                  money: 1,
                  create_time: 1,
                  payment_method: 'wechat_native',
                  status: 'pending',
                },
              ],
              total: 1,
            },
          },
    })
  )
  await page.goto('/wallet')
  await page.getByRole('button', { name: 'Order History', exact: true }).click()
  let dialog = page.getByRole('dialog', {
    name: 'Billing History',
    exact: true,
  })
  await expect(
    dialog.getByText('fixture-order-A', { exact: true })
  ).toBeVisible()
  failed = true
  await dialog
    .getByRole('button', { name: 'Refresh history', exact: true })
    .click()
  await expect(
    dialog.getByText('Showing previous history. Refresh failed.', {
      exact: true,
    })
  ).toBeVisible()
  await expect(
    dialog.getByText('fixture-order-A', { exact: true })
  ).toBeVisible()
  await page.evaluate(
    (value) => window.settingsFixture.signIn(value),
    bundle(99)
  )
  await expect(page.getByText('fixture-order-A', { exact: true })).toHaveCount(
    0
  )
  await page.getByRole('button', { name: 'Order History', exact: true }).click()
  dialog = page.getByRole('dialog', { name: 'Billing History', exact: true })
  await expect(dialog.getByRole('alert')).toContainText(
    'Failed to load billing history'
  )
  await expect(
    dialog.getByText('No billing records found', { exact: true })
  ).toHaveCount(0)
})

for (const invalid of ['missing metrics', 'invalid cash quantity'] as const) {
  test(`successful response with ${invalid} stays unavailable rather than inventing financial values`, async ({
    page,
  }) => {
    await fixture(page)
    const data =
      invalid === 'missing metrics'
        ? { id: 42, username: 'fixture', role: 1 }
        : { ...user, funding: { ...funding, available: 'unknown' } }
    await walletResponse(page, (route) =>
      route.fulfill({ json: { success: true, data } })
    )
    await page.goto('/wallet')
    await expect(
      page.getByRole('alert').filter({ hasText: 'Wallet unavailable' })
    ).toBeVisible()
    await expect(
      page.getByText('Current Balance', { exact: true })
    ).toHaveCount(0)
  })
}

test('Chinese wallet failure keeps translated unavailable and retry actions', async ({
  page,
}) => {
  await fixture(page, false, 'zh')
  await walletResponse(page, (route) => route.abort())
  await page.goto('/wallet')
  await expect(
    page.getByRole('alert').filter({ hasText: '暂时无法获取钱包' })
  ).toBeVisible()
  await expect(
    page.getByRole('button', { name: '重试获取钱包', exact: true })
  ).toBeVisible()
  await expect(page.getByText('当前余额', { exact: true })).toHaveCount(0)
  await screenshot(page, 'wallet-failure-zh')
})

test('Chinese wallet successful zero retains the balance label and independent history action', async ({
  page,
}) => {
  await fixture(page, false, 'zh')
  await page.goto('/wallet')
  await expect(page.getByText('当前余额', { exact: true })).toBeVisible()
  const usageLabel = page.getByText('LLMRouter 产品用量', { exact: true })
  expect(
    await usageLabel.evaluate(
      (element) => element.scrollWidth <= element.clientWidth
    )
  ).toBe(true)
  await expect(
    page.getByRole('button', { name: '订单历史', exact: true })
  ).toBeVisible()
  await screenshot(page, 'wallet-zero-zh')
})

test('Chinese shared cash keeps precise amount and translated measurement boundary', async ({
  page,
}) => {
  await fixture(page, true, 'zh')
  await page.goto('/wallet')
  await expect(page.getByText('1.230001 CNY', { exact: true })).toBeVisible()
  await expect(
    page.getByText(
      '共享现金用于 LLMRouter 原生 API Key。LR_QUOTA 表示产品用量，不代表已确认的 UniHub 现金扣费。',
      { exact: true }
    )
  ).toBeVisible()
  await screenshot(page, 'wallet-shared-cash-zh')
})

test('unresolved admin funding keeps order history personal and cannot complete an order', async ({
  page,
}) => {
  const { requests, writes } = await fixture(page, false, 'en', 100)
  await walletResponse(page, (route) => route.abort())
  await page.goto('/wallet')
  await expect(
    page.getByRole('alert').filter({ hasText: 'Wallet unavailable' })
  ).toBeVisible()
  await page.getByRole('button', { name: 'Order History', exact: true }).click()
  await expect(
    page.getByText('No billing records found', { exact: true })
  ).toBeVisible()
  expect(requests).toContain('/api/user/topup/self')
  expect(requests).not.toContain('/api/user/topup')
  await expect(
    page.getByRole('button', { name: 'Complete Order', exact: true })
  ).toHaveCount(0)
  expect(writes).toEqual([])
})
