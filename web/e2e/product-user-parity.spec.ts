import { expect, test, type Page } from '@playwright/test'

async function fixture(
  page: Page,
  source: 'native' | 'unihub',
  role = 1,
  anonymous = false,
  funding?: { available: string; held: string; consumed: string }
) {
  const requests: string[] = []
  const writes: Record<string, unknown>[] = []
  const now = Math.floor(Date.now() / 1000)
  const user = {
    id: 42,
    username: 'product-user',
    display_name: 'Product User',
    role,
    status: 1,
    identity_source: source === 'unihub' ? 'unihub' : undefined,
    unihub_user_id: source === 'unihub' ? 'central-user' : undefined,
    quota: 5000000,
    used_quota: 500000,
    request_count: 20,
    group: 'default',
    permissions: {
      admin_permissions:
        role >= 10
          ? {
              channel: { read: true, write: true },
              catalog: { read: true, write: true },
              user: { read: true, write: true },
              routing: { read: true, write: true },
              settings: { read: true, write: true },
              log: { read: true },
            }
          : { product: { use: true } },
    },
  }
  await page.addInitScript(() => {
    localStorage.setItem('setup_status_checked', 'true')
    localStorage.setItem('i18nextLng', 'en')
  })
  await page.route('**/api/**', async (route) => {
    const path = new URL(route.request().url()).pathname
    requests.push(path)
    let body: unknown = { success: true, data: [] }
    if (path === '/api/user/auth/refresh') {
      if (anonymous) {
        return route.fulfill({ status: 401, json: { success: false } })
      }
      body = {
        success: true,
        data: {
          user,
          access_token: 'test-access',
          token_type: 'Bearer',
          access_expires_at: now + 3600,
          session: {
            sid: 'product-session',
            login_method: source,
            current: true,
            created_at: now,
            last_active_at: now,
            expires_at: now + 3600,
            ip: '',
            user_agent: 'fixture',
          },
        },
      }
    } else if (path === '/api/unihub/config') {
      body = {
        enabled: true,
        native_access: false,
        public_origin: 'https://hub.invalid',
        client_id: 'client',
        redirect_uri: 'http://127.0.0.1:58006/unihub',
        product_id: 'llmrouter',
        models: ['test-model'],
      }
    } else if (path === '/api/unihub/access') {
      body = {
        access: {
          subject: 'central-user',
          user: { id: 'central-user', name: 'Product User', email: '' },
          roles: [role >= 10 ? 'admin' : 'member'],
          permissions: ['product:use'],
          expires_at: now + 3600,
        },
        user,
        permissions: user.permissions,
        csrf_token: 'csrf',
      }
    } else if (path === '/api/unihub/bff/v1/accounts') {
      body = []
    } else if (path === '/api/status') {
      body = {
        success: true,
        data: {
          system_name: 'LLMRouter',
          register_enabled: true,
          password_login_enabled: true,
          default_use_auto_group: true,
          display_in_currency: Boolean(funding),
          quota_display_type: 'USD',
          usd_exchange_rate: 7,
          quota_per_unit: 500000,
          SidebarModulesAdmin: '',
          announcements_enabled: false,
          HeaderNavModules: JSON.stringify({
            home: true,
            console: true,
            pricing: { enabled: true },
            docs: true,
            about: true,
          }),
        },
      }
    } else if (path === '/api/notice') body = { success: true, data: '' }
    else if (path === '/api/user/self') {
      body = {
        success: true,
        data: {
          ...user,
          ...(funding && {
            funding: {
              mode: 'unified_cash',
              asset: 'CNY/6',
              unit_version: 'lr-cny-v1',
              ...funding,
            },
          }),
        },
      }
    } else if (path === '/api/token/') {
      if (route.request().method() === 'POST') {
        writes.push(route.request().postDataJSON() as Record<string, unknown>)
        body = { success: true }
      } else {
        body = {
          success: true,
          data: { items: [], total: 0, page: 1, page_size: 10 },
        }
      }
    } else if (path === '/api/user/self/groups') {
      body = {
        success: true,
        data: {
          default: { ratio: 1, desc: 'Default' },
          auto: { ratio: 1, desc: 'Automatic' },
        },
      }
    } else if (path === '/api/user/models' || path === '/api/models') {
      body = { success: true, data: ['test-model'] }
    } else if (path === '/api/user/2fa/status') {
      body = { success: true, data: { enabled: true } }
    } else if (path === '/api/user/passkey') {
      body = { success: true, data: { enabled: true, credentials: [] } }
    } else if (path === '/api/user/topup/info') {
      body = {
        success: true,
        data: {
          min_topup: 1,
          amount_options: [1, 10],
          discount: {},
          pay_methods: [],
        },
      }
    } else if (/\/api\/(user|log|mj|task|channel)\/?$/.test(path)) {
      body = {
        success: true,
        data: { items: [], total: 0, page: 1, page_size: 10 },
      }
    } else if (path.endsWith('/stat')) {
      body = {
        success: true,
        data: { quota: 500000, token: 40, rpm: 1, tpm: 40 },
      }
    }
    await route.fulfill({ json: body })
  })
  return { requests, writes }
}

for (const balance of [
  { available: '9007199254740993123', display: '9007199254740.993123 CNY' },
  { available: '0', display: '0.000000 CNY' },
]) {
  test(`shared cash ${balance.available} is exact on the original overview and wallet`, async ({
    page,
  }) => {
    await page.setViewportSize({ width: 390, height: 844 })
    const { requests } = await fixture(page, 'unihub', 1, false, {
      available: balance.available,
      held: '1200001',
      consumed: '2500002',
    })
    await page.goto('/dashboard/overview')
    await expect(page.getByText(balance.display, { exact: true })).toBeVisible()
    const cashBox = await page
      .getByText(balance.display, { exact: true })
      .boundingBox()
    if (!cashBox) throw new Error('Cash balance must have a visible layout box')
    expect(cashBox.x + cashBox.width).toBeLessThanOrEqual(390)
    await expect(
      page.getByText('Shared cash balance', { exact: true })
    ).toBeVisible()
    await expect(page.getByText('1.200001 CNY', { exact: true })).toBeVisible()
    await expect(
      page.getByText('500,000 LR_QUOTA', { exact: true })
    ).toBeVisible()
    await expect(page.getByText('Runway', { exact: true })).toHaveCount(0)
    await expect(page.getByText('$10', { exact: true })).toHaveCount(0)
    await page.goto('/wallet')
    await expect(page.getByText(balance.display, { exact: true })).toBeVisible()
    await expect(
      page.getByText('Shared cash balance', { exact: true })
    ).toBeVisible()
    await expect(
      page.getByText('Referral Program', { exact: true })
    ).toHaveCount(0)
    expect(requests).not.toContain('/api/user/aff')
    expect(requests).not.toContain('/api/user/amount')
    await expect(
      page.getByText('Product quota usage', { exact: true })
    ).toBeVisible()
    await expect(
      page.getByText('500,000 LR_QUOTA', { exact: true })
    ).toBeVisible()
    await expect(page.getByText('$10', { exact: true })).toHaveCount(0)
  })
}

test('cash administrator uses original personal order history with exact CNY amounts', async ({
  page,
}) => {
  const { requests } = await fixture(page, 'unihub', 10, false, {
    available: '1000000',
    held: '0',
    consumed: '0',
  })
  await page.route('**/api/user/topup/self**', async (route) => {
    requests.push(new URL(route.request().url()).pathname)
    await route.fulfill({
      json: {
        success: true,
        data: {
          items: [
            {
              id: 1,
              user_id: 42,
              amount: 0,
              money: 1.23,
              trade_no: 'cash-order',
              payment_method: 'wechat_native',
              payment_provider: 'wechat_pay',
              create_time: 1,
              status: 'success',
              currency: 'CNY',
              paid_amount_minor: 123,
              funding_asset: 'CNY/6',
              credit_amount: '1230000',
              credit_status: 'applied',
            },
          ],
          total: 1,
          page: 1,
          page_size: 10,
        },
      },
    })
  })
  await page.goto('/wallet')
  await expect(
    page.getByText('Shared cash balance', { exact: true })
  ).toBeVisible()
  expect(requests).not.toContain('/api/user/topup/self')
  await page.getByRole('button', { name: 'Order History', exact: true }).click()
  await expect(
    page.getByRole('dialog', { name: 'Billing History', exact: true })
  ).toBeVisible()
  await expect(page.getByText('cash-order', { exact: true })).toBeVisible()
  await expect(page.getByText('1.230000 CNY', { exact: true })).toHaveCount(2)
  expect(requests).toContain('/api/user/topup/self')
  expect(requests).not.toContain('/api/user/topup')
  await expect(
    page.getByRole('button', { name: 'Complete Order', exact: true })
  ).toHaveCount(0)
})

test('cash WeChat payment waits for ledger delivery and refreshes balance once without creating another order', async ({
  page,
}) => {
  const funding = { available: '1000000', held: '0', consumed: '0' }
  const { requests } = await fixture(page, 'unihub', 1, false, funding)
  await page.route('**/api/user/topup/info', (route) =>
    route.fulfill({
      json: {
        success: true,
        data: {
          funding_mode: 'unified_cash',
          funding_asset: 'CNY/6',
          funding_unit_version: 'lr-cny-v1',
          enable_wechat_pay: true,
          wechat_pay_min_amount: '1.00',
          wechat_pay_max_amount: '5000.00',
          enable_redemption: false,
          enable_affiliate_transfer: false,
          min_topup: 1,
          amount_options: [],
          discount: {},
          pay_methods: [],
        },
      },
    })
  )
  const order = {
    trade_no: 'cash-payment',
    code_url: 'weixin://wxpay/bizpayurl?pr=cash-payment',
    amount: '1.00',
    currency: 'CNY',
    credited_quota: 0,
    funding_asset: 'CNY/6',
    funding_unit_version: 'lr-cny-v1',
    credit_amount: '1000000',
    expires_at: Math.floor(Date.now() / 1000) + 3600,
  }
  const creates: Record<string, unknown>[] = []
  let orderReads = 0
  let releaseCredit = () => {}
  const ledgerReady = new Promise<void>((resolve) => {
    releaseCredit = resolve
  })
  await page.route('**/api/user/wechat-pay/native', async (route) => {
    expect(route.request().method()).toBe('POST')
    creates.push(route.request().postDataJSON() as Record<string, unknown>)
    await route.fulfill({
      json: {
        success: true,
        data: { ...order, status: 'pending', credit_status: 'pending' },
      },
    })
  })
  await page.route(
    '**/api/user/wechat-pay/orders/cash-payment',
    async (route) => {
      expect(route.request().method()).toBe('GET')
      orderReads++
      if (orderReads > 1) {
        await ledgerReady
        funding.available = '2000000'
      }
      await route.fulfill({
        json: {
          success: true,
          data: {
            ...order,
            status: 'success',
            credit_status: orderReads > 1 ? 'applied' : 'pending',
          },
        },
      })
    }
  )
  try {
    await page.goto('/wallet')
    await expect(page.getByText('1.000000 CNY', { exact: true })).toBeVisible()
    await page.getByLabel('CNY amount', { exact: true }).fill('1.00')
    const selfReadsBeforePayment = requests.filter(
      (path) => path === '/api/user/self'
    ).length
    await page
      .getByRole('button', { name: 'Create WeChat Pay QR code', exact: true })
      .click()
    const dialog = page.getByRole('dialog', { name: 'Scan with WeChat Pay' })
    await expect(
      dialog.getByText(
        'Payment confirmed. Balance credit is pending. Do not pay again.',
        { exact: true }
      )
    ).toBeVisible()
    const successToasts = page
      .locator('[data-sonner-toast][data-type="success"]')
      .filter({ hasText: 'Payment successful' })
    await expect(successToasts).toHaveCount(1)
    await expect.poll(() => orderReads).toBe(2)
    expect(requests.filter((path) => path === '/api/user/self')).toHaveLength(
      selfReadsBeforePayment
    )
    await expect(
      dialog.getByRole('button', { name: 'Retry QR code' })
    ).toHaveCount(0)
    releaseCredit()
    await expect(page.getByText('2.000000 CNY', { exact: true })).toBeVisible()
    await expect(
      dialog.getByText('Payment successful', { exact: true })
    ).toBeVisible()
    await expect(successToasts).toHaveCount(1)
    expect(requests.filter((path) => path === '/api/user/self')).toHaveLength(
      selfReadsBeforePayment + 1
    )
    expect(creates).toHaveLength(1)
    expect(creates[0]).toMatchObject({ amount: '1.00' })
    expect(creates[0]?.idempotency_key).toEqual(expect.any(String))
    expect(creates[0]?.idempotency_key).not.toBe('')
    await dialog
      .getByRole('button', { name: 'Close', exact: true })
      .last()
      .click()
    expect(creates).toHaveLength(1)
  } finally {
    releaseCredit()
  }
})

for (const source of ['native', 'unihub'] as const) {
  test(`${source} member creates keys with the complete original policy form`, async ({
    page,
  }) => {
    const { requests, writes } = await fixture(page, source)
    await page.goto('/keys')
    await expect(
      page.getByRole('heading', { name: 'API Keys', exact: true })
    ).toBeVisible()
    await expect(
      page.getByRole('button', { name: /^Local ·|^UniHub account$/ })
    ).toHaveCount(0)
    await page
      .getByRole('button', { name: 'Create API Key', exact: true })
      .click()
    await expect(
      page.getByText('Cross-group retry', { exact: true })
    ).toBeVisible()
    await expect(
      page.getByText('Expiration Time', { exact: true })
    ).toBeVisible()
    await page.getByText('Advanced Settings', { exact: true }).click()
    await expect(
      page.getByText('IP Whitelist (supports CIDR)', { exact: true })
    ).toBeVisible()
    await expect(page.getByText('Model Limits', { exact: true })).toBeVisible()
    await page.getByLabel('Name', { exact: true }).fill('Product key')
    await page
      .getByLabel('IP Whitelist (supports CIDR)', { exact: true })
      .fill('192.0.2.0/24')
    await page
      .getByRole('switch', { name: 'Cross-group retry', exact: true })
      .check()
    await page
      .getByRole('button', { name: 'Save changes', exact: true })
      .focus()
    await page.keyboard.press('Enter')
    await expect.poll(() => writes.length).toBe(1)
    expect(writes[0]).toMatchObject({
      name: 'Product key',
      group: 'auto',
      cross_group_retry: true,
      allow_ips: '192.0.2.0/24',
      unlimited_quota: true,
      expired_time: -1,
    })
    expect(requests).toContain('/api/token/')
    expect(
      requests.some((path) => path.includes('/bff/v1/model-api-keys'))
    ).toBe(false)
  })

  test(`${source} member retains profile wallet and balance overview`, async ({
    page,
  }) => {
    await fixture(page, source)
    await page.goto('/dashboard/overview')
    await expect(
      page.getByText('Monitor balance, usage, and request volume', {
        exact: true,
      })
    ).toBeVisible()
    await expect(
      page.getByText('Credit remaining', { exact: true })
    ).toBeVisible()
    await expect(page.getByText('$10', { exact: true })).toBeVisible()
    await expect(
      page.getByRole('button', { name: /^Local ·|^UniHub account$/ })
    ).toHaveCount(0)
    await expect(
      page.locator('[data-slot="sidebar"] a[href="/wallet"]')
    ).toBeVisible()
    await expect(
      page.locator('[data-slot="sidebar"] a[href="/profile"]')
    ).toBeVisible()
    await page.goto('/wallet')
    await expect(
      page.getByRole('heading', { name: 'Wallet', exact: true })
    ).toBeVisible()
    await expect(
      page.getByRole('button', { name: 'Order History', exact: true })
    ).toBeVisible()
    await expect(
      page.getByText('Current Balance', { exact: true })
    ).toBeVisible()
    await expect(page.getByText('$10', { exact: true })).toBeVisible()
    await expect(
      page.getByRole('button', { name: /^Local ·|^UniHub account$/ })
    ).toHaveCount(0)
    await expect(page).toHaveURL(/\/wallet$/)
  })
}

test('central backend retains the original anonymous sign-in and registration forms', async ({
  page,
}) => {
  await fixture(page, 'unihub', 1, true)
  await page.goto('/sign-in?unihub_silent=done')
  await expect(
    page.getByRole('heading', { name: 'Sign in', exact: true })
  ).toBeVisible()
  await expect(page.getByLabel('Password', { exact: true })).toBeVisible()
  await page.goto('/sign-up')
  await expect(
    page.getByRole('heading', { name: 'Create an account', exact: true })
  ).toBeVisible()
  await expect(
    page.getByLabel('Confirm password', { exact: true })
  ).toBeVisible()
})

test('silent SSO failure returns to the original form and does not repeat after reload', async ({
  page,
}) => {
  await fixture(page, 'unihub', 1, true)
  const attempts: string[] = []
  await page.route('**/api/unihub/login?*', async (route) => {
    const url = new URL(route.request().url())
    attempts.push(url.searchParams.get('prompt') ?? '')
    await route.fulfill({
      status: 303,
      headers: { Location: '/sign-in?unihub_silent=done&redirect=%2Fkeys' },
    })
  })
  await page.goto('/sign-in?redirect=%2Fkeys')
  await expect(page).toHaveURL(/unihub_silent=done/)
  await expect(page.getByLabel('Password', { exact: true })).toBeVisible()
  await page.reload()
  await expect(page.getByLabel('Password', { exact: true })).toBeVisible()
  expect(attempts).toEqual(['none'])
})

for (const source of ['native', 'unihub'] as const) {
  test(`${source} usage keeps original charge and native receipts visible`, async ({
    page,
  }) => {
    const { requests } = await fixture(page, source)
    await page.route('**/api/log/self?*', (route) =>
      route.fulfill({
        json: {
          success: true,
          data: {
            total: 1,
            page: 1,
            page_size: 10,
            items: [
              {
                id: 1,
                user_id: 42,
                created_at: Math.floor(Date.now() / 1000),
                type: 2,
                content: '',
                username: 'product-user',
                token_name: 'Product key',
                model_name: 'test-model',
                quota: 123456,
                prompt_tokens: 10,
                completion_tokens: 5,
                use_time: 1,
                request_id: 'call-1',
                other: JSON.stringify({
                  funding_owner: 'unihub',
                  central_status: 'SETTLED',
                }),
              },
            ],
          },
        },
      })
    )
    await page.goto('/usage-logs/common')
    await expect(
      page.getByRole('heading', { name: 'Common Logs', exact: true })
    ).toBeVisible()
    await expect(page.getByRole('table')).toBeVisible()
    await expect(
      page.getByRole('button', { name: /^Local ·|^UniHub account$/ })
    ).toHaveCount(0)
    await expect(
      page.getByText('Native usage and receipts', { exact: true })
    ).toBeVisible()
    await expect(page.getByText('Product key', { exact: true })).toBeVisible()
    await expect(page.getByText('0.246912', { exact: true })).toBeVisible()
    expect(requests).toContain('/api/native-billing/self')
    await expect(
      page.getByRole('link', { name: 'UniHub billing', exact: true })
    ).toHaveCount(0)
  })

  test(`${source} root retains complete routing reliability settings`, async ({
    page,
  }) => {
    await fixture(page, source, 100)
    await page.goto('/system-settings/models/routing-reliability')
    await expect(
      page.getByText('Scheduled channel tests', { exact: true })
    ).toBeVisible()
    await expect(
      page.getByText('Channel test mode', { exact: true })
    ).toBeVisible()
    await expect(
      page.getByRole('button', { name: 'Save Changes', exact: true })
    ).toBeEnabled()
    await page.goto('/models/deployments')
    await expect(
      page.getByRole('tab', { name: 'Deployments', exact: true })
    ).toBeVisible()
  })
}

test('central product administrator retains the original user management page', async ({
  page,
}) => {
  await fixture(page, 'unihub', 10)
  await page.goto('/users')
  await expect(
    page.getByRole('heading', { name: 'Users', exact: true })
  ).toBeVisible()
  await expect(
    page.getByRole('button', { name: /Add User|Create User/ })
  ).toBeVisible()
})

for (const source of ['native', 'unihub'] as const) {
  test(`${source} balance purchase retries preserve the original operation across reload`, async ({
    page,
  }) => {
    await fixture(page, source)
    await page.route('**/api/subscription/plans', (route) =>
      route.fulfill({
        json: {
          success: true,
          data: [
            {
              plan: {
                id: 901,
                title: 'Original subscription',
                price_amount: 2,
                currency: 'USD',
                duration_unit: 'month',
                duration_value: 1,
                enabled: true,
                total_amount: 1000000,
                max_purchase_per_user: 0,
                allow_balance_pay: true,
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
            subscriptions: [],
            all_subscriptions: [],
            billing_preference: 'wallet_only',
          },
        },
      })
    )
    const keys: string[] = []
    await page.route('**/api/subscription/balance/pay', async (route) => {
      expect(route.request().postDataJSON()).toEqual({ plan_id: 901 })
      keys.push(route.request().headers()['idempotency-key'] || '')
      await route.fulfill({
        json:
          keys.length === 1
            ? {
                success: false,
                code: 'PRODUCT_PAYMENT_PENDING',
                message: 'Payment is pending reconciliation',
              }
            : { success: true },
      })
    })
    await page.goto('/wallet')
    await page
      .getByRole('button', { name: 'Subscribe Now', exact: true })
      .click()
    await page
      .getByRole('button', { name: 'Pay with Balance', exact: true })
      .click()
    await expect.poll(() => keys.length).toBe(1)
    await expect(
      page.getByRole('button', { name: 'Pay with Balance', exact: true })
    ).toBeEnabled()
    expect(keys[0]).not.toBe('')
    await page.reload()
    await page
      .getByRole('button', { name: 'Subscribe Now', exact: true })
      .click()
    await page
      .getByRole('button', { name: 'Pay with Balance', exact: true })
      .click()
    await expect.poll(() => keys.length).toBe(2)
    expect(keys[1]).toBe(keys[0])
    await expect(
      page.getByRole('button', { name: 'Pay with Balance', exact: true })
    ).toHaveCount(0)
    await page
      .getByRole('button', { name: 'Subscribe Now', exact: true })
      .click()
    await page
      .getByRole('button', { name: 'Pay with Balance', exact: true })
      .click()
    await expect.poll(() => keys.length).toBe(3)
    expect(keys[2]).not.toBe(keys[1])
  })
}
