/*
Copyright (C) 2023-2026 QuantumNous

This program is free software: you can redistribute it and/or modify
it under the terms of the GNU Affero AGPL License version 3 as published
by the Free Software Foundation. See the GNU Affero AGPL for details.
*/
import { test, expect, type Page } from '@playwright/test'

import {
  fixture,
  funding,
  user,
  walletResponse,
  bundle,
} from './settings-fixture'

const NO_UNIHUB_LINKS = [
  'Recharge unified cash in UniHub',
  'Manage model wallet in UniHub',
  '前往 UniHub 充值统一钱包',
]
const NEW_UNAVAILABLE =
  'Central wallet details are unavailable. Refresh to verify the payer.'

async function expectNoUniHubLinks(page: Page) {
  for (const name of NO_UNIHUB_LINKS) {
    await expect(page.getByRole('link', { name })).toHaveCount(0)
  }
}

async function expectCashTopupCard(page: Page) {
  await expect(
    page.getByText('Recharge with WeChat Pay or Alipay', { exact: true })
  ).toBeVisible()
  await expect(
    page.getByRole('textbox', { name: 'Amount (CNY)', exact: true })
  ).toBeVisible()
  for (const tier of [20, 50, 100, 300, 1000]) {
    await expect(
      page.getByRole('button', { name: `¥${tier}`, exact: true })
    ).toBeVisible()
  }
  await expect(
    page.getByRole('button', { name: 'WeChat Pay', exact: true })
  ).toBeVisible()
  await expect(
    page.getByRole('button', { name: 'Alipay', exact: true })
  ).toBeVisible()
  await expect(
    page.getByRole('button', { name: 'Pay WeChat Pay', exact: true })
  ).toBeVisible()
}

async function cashFixture(
  page: Page,
  {
    account = 'payer',
    role = 'owner',
    language = 'en',
    cashTopupAvailable = true,
  }: {
    account?: string
    role?: string
    language?: string
    cashTopupAvailable?: boolean
  } = {}
) {
  const tracked = await fixture(page, true, language, 1, cashTopupAvailable)
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

test('a password LEGACY payer sees the in-site cash topup card and distinct verified balances', async ({
  page,
}) => {
  const tracked = await cashFixture(page)
  await page.goto('/wallet')
  await expect(
    page.getByText('Unified cash balance', { exact: true })
  ).toBeVisible()
  await expect(page.getByText('0.000000 CNY', { exact: true })).toBeVisible()
  await expect(page.getByText('300.000000 CNY', { exact: true })).toBeVisible()
  await expectCashTopupCard(page)
  await expectNoUniHubLinks(page)
  await expect(
    page.getByText(
      'Native API keys and subscriptions use unified cash. Use the recharge card below to add funds here.',
      { exact: true }
    )
  ).toBeVisible()
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
})

test('cash topup without finance permission hides the card but keeps the verified central balance', async ({
  page,
}) => {
  const tracked = await cashFixture(page, { cashTopupAvailable: false })
  await page.goto('/wallet')
  await expect(page.getByText('300.000000 CNY', { exact: true })).toBeVisible()
  await expect(
    page.getByText('Recharge with WeChat Pay or Alipay', { exact: true })
  ).toHaveCount(0)
  await expect(
    page.getByRole('button', { name: '¥100', exact: true })
  ).toHaveCount(0)
  await expect(
    page.getByRole('textbox', { name: 'Amount (CNY)', exact: true })
  ).toHaveCount(0)
  await expectNoUniHubLinks(page)
  expect(tracked.requests).toContain(
    '/api/unihub/bff/v1/accounts/payer/wallets'
  )
  expect(tracked.writes).toEqual([])
})

test('cash topup stays hidden when the finance grant uses another transport', async ({
  page,
}) => {
  const tracked = await cashFixture(page)
  await page.route('**/api/unihub/access', (route) =>
    route.fulfill({
      json: {
        access: {
          subject: 'fixture-central',
          user: { id: 'fixture-central', name: 'Fixture', email: '' },
          roles: ['member'],
          permissions: ['product:use'],
          expires_at: Math.floor(Date.now() / 1000) + 3600,
        },
        user: bundle(42, true).user,
        permissions: { product: { use: true } },
        csrf_token: 'fixture-csrf',
        finance_access: {
          available: true,
          transport: 'unsupported',
          cash_topup_available: true,
          central_user_id: 'fixture-central',
          expires_at: Math.floor(Date.now() / 1000) + 3600,
          write_scopes: ['recharges:write'],
        },
      },
    })
  )
  await page.goto('/wallet')
  await expect(page.getByText('300.000000 CNY', { exact: true })).toBeVisible()
  await expect(
    page.getByText('Recharge with WeChat Pay or Alipay', { exact: true })
  ).toHaveCount(0)
  await expectNoUniHubLinks(page)
  expect(tracked.writes).toEqual([])
})

for (const [label, account, role] of [
  ['missing payer', '', 'owner'],
  ['different payer', 'unknown', 'owner'],
  ['member role', 'payer', 'member'],
] as const) {
  test(`${label} cannot guess a central wallet and leaves recharge safely hidden`, async ({
    page,
  }) => {
    const tracked = await cashFixture(page, { account, role })
    await page.goto('/wallet')
    await expect(page.getByText('0.000000 CNY', { exact: true })).toBeVisible()
    await expect(page.getByText('300.000000 CNY', { exact: true })).toHaveCount(
      0
    )
    await expect(page.getByText(NEW_UNAVAILABLE, { exact: true })).toBeVisible()
    if (account === '') {
      // Without a payer account id there is no funding target for a recharge.
      await expect(
        page.getByText('Recharge with WeChat Pay or Alipay', { exact: true })
      ).toHaveCount(0)
    } else {
      // An eligible finance grant still shows the in-site card; the central
      // balance stays locked behind payer/role verification.
      await expectCashTopupCard(page)
    }
    await expectNoUniHubLinks(page)
    expect(tracked.writes).toEqual([])
  })
}

test('a forbidden central balance never becomes zero and removes the card until the payer verifies', async ({
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
  await expect(page.getByText(NEW_UNAVAILABLE, { exact: true })).toBeVisible()
  await expectNoUniHubLinks(page)
  denied = false
  await page
    .getByRole('button', { name: 'Retry central wallet', exact: true })
    .click()
  await expect(page.getByText('300.000000 CNY', { exact: true })).toBeVisible()
  await expectCashTopupCard(page)
  expect(tracked.writes).toEqual([])
})

for (const [status, code] of [
  [401, 'unauthorized'],
  [503, 'unavailable'],
] as const) {
  const unavailableText =
    status === 401
      ? 'Wallet funding source is not confirmed yet.'
      : 'Central wallet details are unavailable. Refresh to verify the payer.'
  test(`central wallet ${status} stays unavailable and cannot authorize a zero balance`, async ({
    page,
  }) => {
    const tracked = await cashFixture(page)
    await page.route('**/api/unihub/bff/v1/accounts/payer/wallets', (route) =>
      route.fulfill({ status, json: { error: code } })
    )
    await page.goto('/wallet')
    await expect(page.getByText(unavailableText, { exact: true })).toBeVisible()
    await expect(page.getByText('300.000000 CNY', { exact: true })).toHaveCount(
      0
    )
    // A 401 clears auth and removes the funding source; a transient 503 keeps
    // the last known zero balance visible but never exposes a managed balance.
    if (status === 401) {
      await expect(page.getByText('0.000000 CNY', { exact: true })).toHaveCount(
        0
      )
    }
    await expectNoUniHubLinks(page)
    expect(tracked.writes).toEqual([])
  })
}

test('an unsafe public origin cannot become a central recharge destination', async ({
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
  await expect(page.getByText(NEW_UNAVAILABLE, { exact: true })).toBeVisible()
  await expectNoUniHubLinks(page)
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
  await expectNoUniHubLinks(page)
  expect(tracked.writes).toEqual([])
})

test('insufficient native subscription balance no longer links to UniHub', async ({
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
  for (const name of NO_UNIHUB_LINKS) {
    await expect(dialog.getByRole('link', { name })).toHaveCount(0)
  }
  expect(tracked.writes).toEqual([])
})

test('Chinese cash wallet shows the in-site recharge card and keeps native actions readable', async ({
  page,
}) => {
  const tracked = await cashFixture(page, { language: 'zh' })
  await page.goto('/wallet')
  await expect(page.getByText('统一钱包余额', { exact: true })).toBeVisible()
  await expect(page.getByText('300.000000 CNY', { exact: true })).toBeVisible()
  await expect(
    page.getByText('使用微信支付或支付宝充值', { exact: true })
  ).toBeVisible()
  for (const tier of [20, 50, 100, 300, 1000]) {
    await expect(
      page.getByRole('button', { name: `¥${tier}`, exact: true })
    ).toBeVisible()
  }
  await expectNoUniHubLinks(page)
  expect(
    await page.evaluate(
      () => document.documentElement.scrollWidth <= window.innerWidth
    )
  ).toBe(true)
  expect(tracked.writes).toEqual([])
})

test('near-expiry refresh preserves the payer, verified balance, and in-site card', async ({
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
  await expectCashTopupCard(page)
  await expectNoUniHubLinks(page)
  expect(refreshes).toBe(1)
  expect(tracked.writes).toEqual([])
})

test('switching identity during a refresh cannot authorize the previous payer', async ({
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
  await expectNoUniHubLinks(page)
  expect(
    tracked.requests.filter((path) => path.endsWith('/wallets')).length
  ).toBe(previousReads)
  expect(tracked.writes).toEqual([])
})

for (const method of ['oidc', 'product_broker'] as const) {
  test(`${method} sessions with identity_source=unihub also see the in-site card`, async ({
    page,
  }) => {
    const tracked = await cashFixture(page)
    const session = bundle(42, true)
    session.session.login_method = method
    await page.route('**/api/user/auth/refresh', (route) =>
      route.fulfill({ json: { success: true, data: session } })
    )
    await page.goto('/wallet')
    await expectCashTopupCard(page)
    await expect(
      page.getByText('300.000000 CNY', { exact: true })
    ).toBeVisible()
    expect(tracked.requests).toContain(
      '/api/unihub/bff/v1/accounts/payer/wallets'
    )
    await expectNoUniHubLinks(page)
    expect(tracked.writes).toEqual([])
  })
}

for (const failure of ['member', 'forbidden', 'unavailable'] as const) {
  test(`${failure} wallet authority hides the central balance but not the in-site card`, async ({
    page,
  }) => {
    const tracked = await cashFixture(page, {
      role: failure === 'member' ? 'member' : 'owner',
    })
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
    await expect(page.getByText('300.000000 CNY', { exact: true })).toHaveCount(
      0
    )
    await expect(
      page.getByLabel('Managed model wallet unavailable', { exact: true })
    ).toHaveText('—')
    await expectCashTopupCard(page)
    await expectNoUniHubLinks(page)
    expect(tracked.writes).toEqual([])
  })
}

test('a near-expiry product broker refresh retains the verified payer and in-site card', async ({
  page,
}) => {
  const tracked = await cashFixture(page)
  const broker = bundle(42, true)
  broker.session.login_method = 'product_broker'
  await page.route('**/api/user/auth/refresh', (route) =>
    route.fulfill({ json: { success: true, data: broker } })
  )
  await page.goto('/wallet')
  await expect(page.getByText('300.000000 CNY', { exact: true })).toBeVisible()
  await expectCashTopupCard(page)
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
  await expect(page.getByText('300.000000 CNY', { exact: true })).toBeVisible()
  await expectCashTopupCard(page)
  await expectNoUniHubLinks(page)
  expect(tracked.writes).toEqual([])
})

test('switching login method discards the previous central wallet response', async ({
  page,
}) => {
  const tracked = await cashFixture(page)
  const current = bundle(42, true)
  current.session.login_method = 'oidc'
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
  changed.session.login_method = 'product_broker'
  await page.evaluate((value) => window.settingsFixture.signIn(value), changed)
  await expect(page.getByRole('alert')).toContainText(
    'Central wallet details are unavailable'
  )
  release()
  await cancelledRead
  await page.evaluate(
    () => new Promise<void>((resolve) => requestAnimationFrame(() => resolve()))
  )
  await expect(page.getByText('300.000000 CNY', { exact: true })).toHaveCount(0)
  await expectNoUniHubLinks(page)
  expect(tracked.writes).toEqual([])
})

test('WeChat confirmation replaces QR with success and refreshes the credited balance', async ({
  page,
}) => {
  await cashFixture(page)
  await page.route('**/api/user/auth/refresh', (route) => {
    const auth = bundle(42, true)
    auth.user.funding = { ...funding, account_id: 'payer' } as typeof funding
    return route.fulfill({ json: { success: true, data: auth } })
  })
  await page.route('**/api/unihub/access', (route) =>
    route.fulfill({
      json: {
        csrf_token: 'csrf-fixture',
        finance_access: {
          transport: 'runtime_actor',
          available: true,
          cash_topup_available: true,
          central_user_id: 'fixture-central',
          write_scopes: ['recharges:write'],
        },
        access: {
          subject: 'fixture-central',
          user: { id: 'fixture-central', name: 'Fixture', email: '' },
          roles: ['member'],
          permissions: ['product:use'],
          expires_at: Math.floor(Date.now() / 1000) + 3600,
        },
        user: {
          ...bundle(42, true).user,
          funding: { ...funding, account_id: 'payer' },
        },
        permissions: { product: { use: true } },
      },
    })
  )
  let paid = false
  let credited = false
  let walletReads = 0
  await walletResponse(page, (route) => {
    walletReads++
    return route.fulfill({
      json: {
        success: true,
        data: {
          ...user,
          identity_source: 'unihub',
          unihub_user_id: 'fixture-central',
          funding: {
            ...funding,
            account_id: 'payer',
            available: credited ? '20000000' : '0',
          },
        },
      },
    })
  })
  await page.route('**/cash-checkouts', (route) =>
    route.fulfill({
      status: 202,
      json: {
        id: 'ct_confirm1',
        account_id: 'payer',
        provider: 'wechat',
        amount: '20000000',
        payment_status: 'CREATED',
        expires_at: new Date(Date.now() + 900000).toISOString(),
        session: { kind: 'qr', url: 'weixin://wxpay/bizpayurl?pr=test' },
      },
    })
  )
  await page.route('**/cash-checkouts/ct_confirm1', (route) =>
    route.fulfill({
      json: {
        id: 'ct_confirm1',
        account_id: 'payer',
        amount: '20000000',
        payment_status: paid ? 'PAID' : 'CREATED',
        operation_id: paid ? 'op_confirm1' : undefined,
        wallet_target: 'unified',
        credit_status: credited ? 'APPLIED' : 'CREATED',
      },
    })
  )
  await page.goto('/wallet')
  await page.getByRole('button', { name: '¥20', exact: true }).click()
  await page
    .getByRole('button', { name: 'Pay WeChat Pay', exact: true })
    .click()
  const dialog = page.getByRole('dialog')
  await expect(
    dialog.locator('svg').filter({ has: page.locator('title') })
  ).toBeVisible()
  paid = true
  await expect(
    dialog.getByText('Payment received. Updating your balance.')
  ).toBeVisible()
  await expect(
    dialog.locator('svg').filter({ has: page.locator('title') })
  ).toHaveCount(0)
  const readsBefore = walletReads
  credited = true
  await expect(
    dialog.getByText('Recharge complete. Your balance has been updated.')
  ).toBeVisible()
  expect(walletReads).toBeGreaterThan(readsBefore)
  await expect(page.getByText('20.000000 CNY', { exact: true })).toBeVisible()
  await dialog
    .getByRole('button', { name: 'Back to wallet', exact: true })
    .click()
  await expect(dialog).toHaveCount(0)

  await page.route(
    '**/api/unihub/bff/v1/accounts/payer/cash-checkouts',
    (route) =>
      route.fulfill({
        json: [
          {
            id: 'ct_confirm1',
            account_id: 'payer',
            product_id: 'llmrouter',
            provider: 'wechat',
            amount: '20000000',
            currency: 'CNY',
            payment_status: 'PAID',
            wallet_target: 'unified',
            credit_status: 'APPLIED',
            created_at: '2026-11-18T03:00:00.123456789Z',
            expires_at: '2026-11-18T03:15:00Z',
          },
        ],
      })
  )
  await page.getByRole('button', { name: 'Order History', exact: true }).click()
  await expect(dialog.getByText('ct_confirm1', { exact: true })).toBeVisible()
  await expect(
    dialog.getByText('Unified wallet', { exact: true })
  ).toBeVisible()
  await expect(dialog.getByText('WeChat Pay', { exact: true })).toBeVisible()
})

test('Alipay return verifies the checkout with the server before showing success', async ({
  page,
}) => {
  await cashFixture(page)
  await page.route('**/api/user/auth/refresh', (route) => {
    const auth = bundle(42, true)
    auth.user.funding = { ...funding, account_id: 'payer' } as typeof funding
    return route.fulfill({ json: { success: true, data: auth } })
  })
  await page.route('**/api/unihub/access', (route) =>
    route.fulfill({
      json: {
        csrf_token: 'csrf-fixture',
        finance_access: {
          transport: 'runtime_actor',
          available: true,
          cash_topup_available: true,
          central_user_id: 'fixture-central',
          write_scopes: ['recharges:write'],
        },
        access: {
          subject: 'fixture-central',
          user: { id: 'fixture-central', name: 'Fixture', email: '' },
          roles: ['member'],
          permissions: ['product:use'],
          expires_at: Math.floor(Date.now() / 1000) + 3600,
        },
        user: {
          ...bundle(42, true).user,
          funding: { ...funding, account_id: 'payer' },
        },
        permissions: { product: { use: true } },
      },
    })
  )
  let paid = false
  await page.route('**/cash-checkouts/ct_return1', (route) =>
    route.fulfill({
      json: {
        id: 'ct_return1',
        account_id: 'payer',
        amount: '20000000',
        payment_status: paid ? 'PAID' : 'CREATED',
        operation_id: paid ? 'op_return1' : undefined,
      },
    })
  )
  await page.route('**/operations/op_return1', (route) =>
    route.fulfill({ json: { id: 'op_return1', status: 'APPLIED' } })
  )
  await page.goto('/wallet?cash_checkout=ct_return1&trade_status=TRADE_SUCCESS')
  await expect(
    page.getByText('Waiting for payment', { exact: true })
  ).toBeVisible()
  await expect(
    page.getByText('Recharge complete. Your balance has been updated.')
  ).toHaveCount(0)
  paid = true
  await expect(
    page.getByText('Recharge complete. Your balance has been updated.')
  ).toBeVisible()
  expect(new URL(page.url()).pathname).toBe('/wallet')
})

test('checking an expired order reconciles explicitly and displays the credited result', async ({
  page,
}) => {
  await cashFixture(page)
  await page.route('**/api/user/auth/refresh', (route) => {
    const auth = bundle(42, true)
    auth.user.funding = { ...funding, account_id: 'payer' } as typeof funding
    return route.fulfill({ json: { success: true, data: auth } })
  })
  const order = {
    id: 'ct_expired1',
    account_id: 'payer',
    amount: '20000000',
    payment_status: 'PENDING',
    credit_status: 'CREATED',
    expires_at: '2000-01-01T00:00:00Z',
  }
  let reads = 0
  let reconciles = 0
  await page.route('**/cash-checkouts/ct_expired1', (route) => {
    expect(route.request().method()).toBe('GET')
    reads++
    return route.fulfill({ json: order })
  })
  let releaseCheck!: () => void
  const checkResult = new Promise<void>((resolve) => (releaseCheck = resolve))
  await page.route('**/cash-checkouts/ct_expired1/reconcile', async (route) => {
    expect(route.request().method()).toBe('POST')
    expect(route.request().headers()['x-unihub-csrf']).toBe('fixture-csrf')
    reconciles++
    if (reconciles === 1) {
      await checkResult
      return route.fulfill({ json: { ...order, payment_status: 'CREATED' } })
    }
    return route.fulfill({
      json: { ...order, payment_status: 'PAID', credit_status: 'APPLIED' },
    })
  })

  await page.goto('/wallet?cash_checkout=ct_expired1')
  const dialog = page.getByRole('dialog')
  await expect(dialog.getByRole('status')).toContainText('QR code has expired')
  expect(reads).toBeGreaterThan(0)
  expect(reconciles).toBe(0)

  const check = dialog.getByRole('button', { name: 'Check payment status' })
  await check.click()
  try {
    await expect.poll(() => reconciles, { timeout: 3000 }).toBe(1)
    await expect(check).toBeDisabled()
  } finally {
    releaseCheck()
  }
  await expect(check).toBeEnabled()
  await expect(dialog.getByRole('status')).toContainText('QR code has expired')
  await check.click()
  await expect(dialog.getByRole('status')).toHaveText(
    'Recharge complete. Your balance has been updated.'
  )
})

test('a manual retry after reload reuses the checkout intent and shows a replay without a payment link', async ({
  page,
}) => {
  await cashFixture(page)
  await page.route('**/api/user/auth/refresh', (route) => {
    const auth = bundle(42, true)
    auth.user.funding = { ...funding, account_id: 'payer' } as typeof funding
    return route.fulfill({ json: { success: true, data: auth } })
  })
  const expired = {
    id: 'ct_replay1',
    account_id: 'payer',
    amount: '20000000',
    payment_status: 'CREATED',
    credit_status: 'CREATED',
    expires_at: '2000-01-01T00:00:00Z',
  }
  const attempts: { key: string; amount: string }[] = []
  await page.route('**/cash-checkouts/ct_replay1', (route) =>
    route.fulfill({ json: expired })
  )
  await page.route('**/cash-checkouts/ct_new1', (route) =>
    route.fulfill({
      json: { ...expired, id: 'ct_new1', expires_at: undefined },
    })
  )
  await page.route('**/cash-checkouts', (route) => {
    expect(route.request().method()).toBe('POST')
    attempts.push({
      key: route.request().headers()['idempotency-key'],
      amount: route.request().postDataJSON().amount,
    })
    if (attempts.length === 1) {
      return route.fulfill({ status: 502, json: { error: 'unavailable' } })
    }
    if (attempts.length === 2) {
      return route.fulfill({ status: 200, json: expired })
    }
    return route.fulfill({
      status: 202,
      json: {
        ...expired,
        id: 'ct_new1',
        expires_at: undefined,
        session: { kind: 'qr', url: 'weixin://wxpay/bizpayurl?pr=new' },
      },
    })
  })

  await page.goto('/wallet')
  const amount = page.getByRole('textbox', { name: 'Amount (CNY)' })
  const pay = page.getByRole('button', { name: 'Pay WeChat Pay', exact: true })
  await page.getByRole('button', { name: '¥20', exact: true }).click()
  await pay.click()
  await expect(
    page.getByText('Recharge request failed. Please try again.')
  ).toBeVisible()
  expect(attempts).toHaveLength(1)
  expect(attempts[0].key).toBeTruthy()

  await page.reload()
  await amount.fill('20.00')
  expect(attempts).toHaveLength(1)
  await pay.click()
  await expect.poll(() => attempts.length).toBe(2)
  expect(attempts[1]).toEqual(attempts[0])
  const dialog = page.getByRole('dialog')
  await expect(dialog.getByRole('status')).toContainText('QR code has expired')
  await expect(
    dialog.getByRole('button', { name: 'Check payment status' })
  ).toBeEnabled()
  await page.keyboard.press('Escape')
  await expect(dialog).toHaveCount(0)

  await pay.click()
  await expect(
    dialog.getByText('Scan with WeChat Pay', { exact: true })
  ).toBeVisible()
  expect(attempts).toHaveLength(3)
  expect(attempts[2].key).not.toBe(attempts[1].key)
  expect(attempts[2].amount).toBe('20000000')
})
