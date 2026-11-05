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
import { test, expect, type Page } from '@playwright/test'

import {
  fixture,
  funding,
  user,
  walletResponse,
  bundle,
} from './settings-fixture'

async function cashFixture(
  page: Page,
  account = 'payer',
  role = 'owner',
  language = 'en'
) {
  const tracked = await fixture(page, true, language)
  await walletResponse(page, (route) =>
    route.fulfill({
      json: {
        success: true,
        data: {
          ...user,
          identity_source: 'unihub',
          unihub_user_id: 'fixture-central',
          funding: {
            ...funding,
            available: '0',
            ...(account ? { account_id: account } : {}),
          },
        },
      },
    })
  )
  await page.route('**/api/unihub/bff/v1/accounts', (route) =>
    route.fulfill({
      json: [
        { id: 'other', kind: 'personal', name: 'Other payer', role: 'owner' },
        { id: 'payer', kind: 'personal', name: 'Fixture payer', role },
      ],
    })
  )
  await page.route('**/api/unihub/bff/v1/accounts/*/wallets', (route) => {
    tracked.requests.push(new URL(route.request().url()).pathname)
    return route.fulfill({
      json: [
        { product_id: '', asset: 'CNY/6', amount: '0' },
        { product_id: 'llmrouter', asset: 'CNY/6', amount: '300000000' },
      ],
    })
  })
  await page.route('**/api/user/topup/info', (route) =>
    route.fulfill({
      json: {
        success: true,
        data: {
          funding_mode: 'unified_cash',
          enable_wechat_pay: false,
          enable_online_topup: false,
          enable_redemption: false,
          pay_methods: [],
          amount_options: [],
          discount: {},
          min_topup: 1,
        },
      },
    })
  )
  return tracked
}

test('zero unified cash and product 300 remain separate with an account-bound central recharge link', async ({
  page,
}) => {
  const tracked = await cashFixture(page)
  await page.goto('/wallet')
  await expect(
    page.getByText('Unified cash balance', { exact: true })
  ).toBeVisible()
  await expect(page.getByText('0.000000 CNY', { exact: true })).toBeVisible()
  await expect(page.getByText('300.000000 CNY', { exact: true })).toBeVisible()
  const recharge = page.getByRole('link', {
    name: 'Recharge unified cash in UniHub',
    exact: true,
  })
  await expect(recharge).toHaveAttribute(
    'href',
    'https://hub.invalid/?view=wallets&account=payer&wallet=unified'
  )
  await expect(
    page.getByRole('link', {
      name: 'Manage model wallet in UniHub',
      exact: true,
    })
  ).toHaveAttribute(
    'href',
    'https://hub.invalid/?view=wallets&account=payer&product=llmrouter'
  )
  await expect(
    page.getByText(
      'For SuperWagie managed model calls. This balance does not fund native LLMRouter keys or subscriptions.',
      { exact: true }
    )
  ).toBeVisible()
  expect(tracked.requests).toContain(
    '/api/unihub/bff/v1/accounts/payer/wallets'
  )
  expect(tracked.requests).not.toContain(
    '/api/unihub/bff/v1/accounts/other/wallets'
  )
  await expect(
    page.getByText('International payment', { exact: true })
  ).toHaveCount(0)
  expect(tracked.writes).toEqual([])
  await page.screenshot({
    path: `../.superpowers/sdd/2026-11-06-wallet-source/screenshots/wallet-${page.viewportSize()?.width}.png`,
    fullPage: true,
    animations: 'disabled',
  })
})

for (const [label, account, role] of [
  ['missing payer', '', 'owner'],
  ['different payer', 'unknown', 'owner'],
  ['member role', 'payer', 'member'],
]) {
  test(`${label} cannot guess a central recharge destination`, async ({
    page,
  }) => {
    const tracked = await cashFixture(page, account, role)
    await page.goto('/wallet')
    await expect(page.getByText('0.000000 CNY', { exact: true })).toBeVisible()
    await expect(
      page.getByRole('link', {
        name: 'Recharge unified cash in UniHub',
        exact: true,
      })
    ).toHaveCount(0)
    await expect(page.getByText('300.000000 CNY', { exact: true })).toHaveCount(
      0
    )
    await expect(
      page.getByText(
        'Central wallet details are unavailable. Refresh or sign in with UniHub to verify the payer.',
        { exact: true }
      )
    ).toBeVisible()
    expect(tracked.writes).toEqual([])
  })
}

test('a forbidden central balance never becomes zero and removes the recharge destination until retry succeeds', async ({
  page,
}) => {
  const tracked = await cashFixture(page)
  let denied = true
  await page.route('**/api/unihub/bff/v1/accounts/payer/wallets', (route) =>
    route.fulfill(
      denied
        ? { status: 403, json: { error: 'forbidden' } }
        : {
            json: [
              { product_id: 'llmrouter', asset: 'CNY/6', amount: '300000000' },
            ],
          }
    )
  )
  await page.goto('/wallet')
  await expect(
    page.getByText(
      'Central wallet details are unavailable. Refresh or sign in with UniHub to verify the payer.',
      { exact: true }
    )
  ).toBeVisible()
  await expect(
    page.getByRole('link', {
      name: 'Recharge unified cash in UniHub',
      exact: true,
    })
  ).toHaveCount(0)
  denied = false
  await page
    .getByRole('button', { name: 'Retry central wallet', exact: true })
    .click()
  await expect(page.getByText('300.000000 CNY', { exact: true })).toBeVisible()
  expect(tracked.writes).toEqual([])
})

test('a late product balance cannot appear after the authenticated identity changes', async ({
  page,
}) => {
  const tracked = await cashFixture(page)
  let start!: () => void
  const started = new Promise<void>((resolve) => {
    start = resolve
  })
  let release!: () => void
  const gate = new Promise<void>((resolve) => {
    release = resolve
  })
  await page.route(
    '**/api/unihub/bff/v1/accounts/payer/wallets',
    async (route) => {
      start()
      await gate
      await route.fulfill({
        json: [
          { product_id: 'llmrouter', asset: 'CNY/6', amount: '300000000' },
        ],
      })
    }
  )
  await page.goto('/wallet')
  await started
  await walletResponse(page, (route) =>
    route.fulfill({ json: { success: true, data: { ...user, id: 99 } } })
  )
  const cancelledRead = page.waitForEvent('requestfailed', {
    predicate: (request) =>
      new URL(request.url()).pathname ===
      '/api/unihub/bff/v1/accounts/payer/wallets',
  })
  await page.evaluate(
    (value) => window.settingsFixture.signIn(value),
    bundle(99)
  )
  await expect(page.getByText('Current Balance', { exact: true })).toBeVisible()
  release()
  await cancelledRead
  await page.evaluate(
    () => new Promise<void>((resolve) => requestAnimationFrame(() => resolve()))
  )
  await expect(page.getByText('300.000000 CNY', { exact: true })).toHaveCount(0)
  await expect(
    page.getByRole('link', {
      name: 'Recharge unified cash in UniHub',
      exact: true,
    })
  ).toHaveCount(0)
  expect(tracked.writes).toEqual([])
})

test('insufficient native subscription balance uses the same unified recharge destination', async ({
  page,
}) => {
  const tracked = await cashFixture(page)
  await page.route('**/api/subscription/plans', (route) =>
    route.fulfill({
      json: {
        success: true,
        data: [
          {
            plan: {
              id: 1,
              title: 'Native subscription',
              price_amount: 5,
              currency: 'USD',
              duration_unit: 'month',
              duration_value: 1,
              quota_reset_period: 'never',
              enabled: true,
              sort_order: 0,
              allow_balance_pay: true,
              allow_wallet_overflow: true,
              max_purchase_per_user: 0,
              total_amount: 1000000,
            },
          },
        ],
      },
    })
  )
  await page.route('**/api/subscription/self', (route) =>
    route.fulfill({
      json: {
        success: true,
        data: {
          billing_preference: 'wallet_only',
          subscriptions: [],
          all_subscriptions: [],
        },
      },
    })
  )
  await page.goto('/wallet')
  await expect(page.getByText('300.000000 CNY', { exact: true })).toBeVisible()
  await page.getByRole('button', { name: 'Subscribe Now', exact: true }).click()
  const dialog = page.getByRole('dialog')
  await expect(dialog.getByRole('alert')).toContainText('Insufficient balance')
  await expect(
    dialog.getByRole('button', { name: 'Pay with Balance', exact: true })
  ).toBeDisabled()
  await expect(
    dialog.getByRole('link', {
      name: 'Recharge unified cash in UniHub',
      exact: true,
    })
  ).toHaveAttribute(
    'href',
    'https://hub.invalid/?view=wallets&account=payer&wallet=unified'
  )
  expect(tracked.writes).toEqual([])
})

test('expired central authority clears its destination instead of presenting a usable zero', async ({
  page,
}) => {
  const tracked = await cashFixture(page)
  await page.route('**/api/unihub/bff/v1/accounts/payer/wallets', (route) =>
    route.fulfill({ status: 401, json: { error: 'unauthorized' } })
  )
  await page.goto('/wallet')
  await expect(
    page.getByText('Wallet funding source is not confirmed yet.', {
      exact: true,
    })
  ).toBeVisible()
  await expect(page.getByText('300.000000 CNY', { exact: true })).toHaveCount(0)
  await expect(page.getByText('0.000000 CNY', { exact: true })).toHaveCount(0)
  await expect(
    page.getByRole('link', {
      name: 'Recharge unified cash in UniHub',
      exact: true,
    })
  ).toHaveCount(0)
  expect(tracked.writes).toEqual([])
})

test('an unsafe public origin cannot become a central recharge link', async ({
  page,
}) => {
  const tracked = await cashFixture(page)
  await page.route('**/api/unihub/config', (route) =>
    route.fulfill({
      json: {
        enabled: true,
        public_origin: 'https://hub.invalid/?token=secret',
        product_id: 'llmrouter',
        client_id: 'fixture-client',
        redirect_uri: 'http://127.0.0.1:58026/unihub',
        models: [],
      },
    })
  )
  await page.goto('/wallet')
  await expect(
    page.getByText(
      'Central wallet details are unavailable. Refresh or sign in with UniHub to verify the payer.',
      { exact: true }
    )
  ).toBeVisible()
  await expect(
    page.getByRole('link', {
      name: 'Recharge unified cash in UniHub',
      exact: true,
    })
  ).toHaveCount(0)
  expect(tracked.writes).toEqual([])
})

test('Chinese cash wallet keeps distinct balances and central actions readable without horizontal overflow', async ({
  page,
}) => {
  const tracked = await cashFixture(page, 'payer', 'owner', 'zh')
  await page.goto('/wallet')
  await expect(page.getByText('统一钱包余额', { exact: true })).toBeVisible()
  await expect(page.getByText('300.000000 CNY', { exact: true })).toBeVisible()
  await expect(
    page.getByRole('link', { name: '前往 UniHub 充值统一钱包', exact: true })
  ).toBeVisible()
  expect(
    await page.evaluate(
      () => document.documentElement.scrollWidth <= window.innerWidth
    )
  ).toBe(true)
  await page.screenshot({
    path: `../.superpowers/sdd/2026-11-06-wallet-source/screenshots/wallet-zh-${page.viewportSize()?.width}.png`,
    fullPage: true,
  })
  expect(tracked.writes).toEqual([])
})

test('near-expiry OIDC refresh preserves the same payer and restores central wallet actions', async ({
  page,
}) => {
  const tracked = await cashFixture(page)
  await page.goto('/wallet')
  await expect(page.getByText('300.000000 CNY', { exact: true })).toBeVisible()
  let refreshes = 0
  await page.route('**/api/user/auth/refresh', (route) => {
    refreshes += 1
    return route.fulfill({ json: { success: true, data: bundle(42, true) } })
  })
  const nearExpiry = bundle(42, true)
  nearExpiry.access_expires_at = Math.floor(Date.now() / 1000) + 30
  await page.evaluate(
    (value) => window.settingsFixture.signIn(value, false, true),
    nearExpiry
  )
  await page
    .getByRole('button', { name: 'Refresh wallet', exact: true })
    .click()
  await expect.poll(() => refreshes).toBe(1)
  await expect(page.getByText('300.000000 CNY', { exact: true })).toBeVisible()
  await expect(
    page.getByRole('link', {
      name: 'Recharge unified cash in UniHub',
      exact: true,
    })
  ).toHaveAttribute(
    'href',
    'https://hub.invalid/?view=wallets&account=payer&wallet=unified'
  )
  expect(refreshes).toBe(1)
  expect(tracked.writes).toEqual([])
})

test('switching identity during OIDC refresh cannot authorize the previous payer', async ({
  page,
}) => {
  const tracked = await cashFixture(page)
  await page.goto('/wallet')
  await expect(page.getByText('300.000000 CNY', { exact: true })).toBeVisible()
  const previousReads = tracked.requests.filter((path) =>
    path.endsWith('/wallets')
  ).length
  let start!: () => void
  const started = new Promise<void>((resolve) => {
    start = resolve
  })
  let release!: () => void
  const gate = new Promise<void>((resolve) => {
    release = resolve
  })
  await page.route('**/api/user/auth/refresh', async (route) => {
    start()
    await gate
    await route.fulfill({ json: { success: true, data: bundle(42, true) } })
  })
  const nearExpiry = bundle(42, true)
  nearExpiry.access_expires_at = Math.floor(Date.now() / 1000) + 30
  await page.evaluate(
    (value) => window.settingsFixture.signIn(value, false, true),
    nearExpiry
  )
  await page
    .getByRole('button', { name: 'Refresh wallet', exact: true })
    .click()
  await started
  await walletResponse(page, (route) =>
    route.fulfill({ json: { success: true, data: { ...user, id: 99 } } })
  )
  await page.evaluate(
    (value) => window.settingsFixture.signIn(value),
    bundle(99)
  )
  await expect(page.getByText('Current Balance', { exact: true })).toBeVisible()
  const lateRefresh = page.waitForResponse(
    (response) => new URL(response.url()).pathname === '/api/user/auth/refresh'
  )
  release()
  await (await lateRefresh).finished()
  await page.evaluate(
    () => new Promise<void>((resolve) => requestAnimationFrame(() => resolve()))
  )
  await expect(page.getByText('300.000000 CNY', { exact: true })).toHaveCount(0)
  await expect(
    page.getByRole('link', {
      name: 'Recharge unified cash in UniHub',
      exact: true,
    })
  ).toHaveCount(0)
  expect(
    tracked.requests.filter((path) => path.endsWith('/wallets')).length
  ).toBe(previousReads)
  expect(tracked.writes).toEqual([])
})

test('product broker login shows the verified payer unified recharge link', async ({
  page,
}) => {
  const tracked = await cashFixture(page)
  const broker = bundle(42, true)
  broker.session.login_method = 'product_broker'
  await page.route('**/api/user/auth/refresh', (route) =>
    route.fulfill({ json: { success: true, data: broker } })
  )
  await page.goto('/wallet')
  await expect(
    page.getByRole('link', {
      name: 'Recharge unified cash in UniHub',
      exact: true,
    })
  ).toHaveAttribute(
    'href',
    'https://hub.invalid/?view=wallets&account=payer&wallet=unified'
  )
  await expect(page.getByText('300.000000 CNY', { exact: true })).toBeVisible()
  expect(tracked.requests).toContain(
    '/api/unihub/bff/v1/accounts/payer/wallets'
  )
  expect(tracked.writes).toEqual([])
})

for (const failure of ['member', 'forbidden', 'unavailable']) {
  test(`product broker ${failure} wallet authority cannot expose a recharge link`, async ({
    page,
  }) => {
    const tracked = await cashFixture(
      page,
      'payer',
      failure === 'member' ? 'member' : 'owner'
    )
    const broker = bundle(42, true)
    broker.session.login_method = 'product_broker'
    await page.route('**/api/user/auth/refresh', (route) =>
      route.fulfill({ json: { success: true, data: broker } })
    )
    if (failure !== 'member') {
      await page.route('**/api/unihub/bff/v1/accounts/payer/wallets', (route) =>
        route.fulfill({
          status: failure === 'forbidden' ? 403 : 503,
          json: { error: 'unavailable' },
        })
      )
    }
    await page.goto('/wallet')
    await expect(page.getByRole('alert')).toContainText(
      'Central wallet details are unavailable'
    )
    await expect(
      page.getByRole('link', { name: 'Recharge unified cash in UniHub' })
    ).toHaveCount(0)
    await expect(page.getByText('300.000000 CNY', { exact: true })).toHaveCount(
      0
    )
    await expect(
      page.getByLabel('Managed model wallet unavailable', { exact: true })
    ).toHaveText('—')
    expect(tracked.writes).toEqual([])
  })
}

test('near-expiry product broker refresh retains the verified payer link', async ({
  page,
}) => {
  const tracked = await cashFixture(page)
  const broker = bundle(42, true)
  broker.session.login_method = 'product_broker'
  await page.route('**/api/user/auth/refresh', (route) =>
    route.fulfill({ json: { success: true, data: broker } })
  )
  await page.goto('/wallet')
  const recharge = page.getByRole('link', {
    name: 'Recharge unified cash in UniHub',
    exact: true,
  })
  await expect(recharge).toBeVisible()
  let refreshes = 0
  await page.route('**/api/user/auth/refresh', (route) => {
    refreshes += 1
    return route.fulfill({ json: { success: true, data: broker } })
  })
  const nearExpiry = {
    ...broker,
    access_expires_at: Math.floor(Date.now() / 1000) + 30,
  }
  await page.evaluate(
    (value) => window.settingsFixture.signIn(value, false, true),
    nearExpiry
  )
  await page
    .getByRole('button', { name: 'Refresh wallet', exact: true })
    .click()
  await expect.poll(() => refreshes).toBe(1)
  await expect(recharge).toHaveAttribute(
    'href',
    'https://hub.invalid/?view=wallets&account=payer&wallet=unified'
  )
  expect(tracked.writes).toEqual([])
})

for (const from of ['unihub', 'product_broker']) {
  test(`switching login method from ${from} discards the previous wallet response`, async ({
    page,
  }) => {
    const tracked = await cashFixture(page)
    const current = bundle(42, true)
    current.session.login_method = from
    await page.route('**/api/user/auth/refresh', (route) =>
      route.fulfill({ json: { success: true, data: current } })
    )
    let start!: () => void
    const started = new Promise<void>((resolve) => {
      start = resolve
    })
    let release!: () => void
    const gate = new Promise<void>((resolve) => {
      release = resolve
    })
    await page.route(
      '**/api/unihub/bff/v1/accounts/payer/wallets',
      async (route) => {
        start()
        await gate
        await route.fulfill({
          json: [
            { product_id: 'llmrouter', asset: 'CNY/6', amount: '300000000' },
          ],
        })
      }
    )
    await page.goto('/wallet')
    await started
    await page.route('**/api/unihub/bff/v1/accounts', (route) =>
      route.fulfill({ status: 403, json: { error: 'forbidden' } })
    )
    const cancelledRead = page.waitForEvent('requestfailed', {
      predicate: (request) =>
        new URL(request.url()).pathname ===
        '/api/unihub/bff/v1/accounts/payer/wallets',
    })
    const changed = bundle(42, true)
    changed.session.login_method =
      from === 'unihub' ? 'product_broker' : 'unihub'
    await page.evaluate(
      (value) => window.settingsFixture.signIn(value),
      changed
    )
    await expect(page.getByRole('alert')).toContainText(
      'Central wallet details are unavailable'
    )
    release()
    await cancelledRead
    await page.evaluate(
      () =>
        new Promise<void>((resolve) => requestAnimationFrame(() => resolve()))
    )
    await expect(page.getByText('300.000000 CNY', { exact: true })).toHaveCount(
      0
    )
    await expect(
      page.getByRole('link', { name: 'Recharge unified cash in UniHub' })
    ).toHaveCount(0)
    expect(tracked.writes).toEqual([])
  })
}
