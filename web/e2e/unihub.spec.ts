import { test, expect, type Page, type Route } from '@playwright/test'

const accounts = [
  { id: 'personal-a', name: 'Personal A', kind: 'personal', role: 'owner' },
  { id: 'team-b', name: 'Team B', kind: 'team', role: 'member' },
]
async function fixture(
  page: Page,
  options: {
    expiry?: number
    registerUnknown?: boolean
    models?: string[]
    mismatch?: boolean
    permissions?: string[]
  } = {},
) {
  let authorized = false
  const permissions = {
    channel: {
      read: (options.permissions ?? []).includes('channel:read'),
      operate: (options.permissions ?? []).includes('channel:operate'),
      write: (options.permissions ?? []).includes('channel:write'),
    },
  }
  const expiry = Math.floor(Date.now() / 1000) + (options.expiry ?? 300)
  const nativeBundle = {
    access_token: 'native-session-token',
    token_type: 'Bearer',
    access_expires_at: expiry,
    user: {
      id: 42,
      username: 'product-user',
      display_name: 'Product user',
      role: 1,
      identity_source: 'unihub',
      unihub_user_id: 'immutable-user',
      permissions: { admin_permissions: permissions },
    },
    session: {
      sid: 'sid-unihub',
      current: true,
      login_method: 'unihub',
      ip: '',
      user_agent: 'fixture',
      created_at: 1,
      last_active_at: 1,
      expires_at: expiry,
    },
  }
  await page.route('**/api/**', async (route) => {
    const path = new URL(route.request().url()).pathname
    if (path === '/api/unihub/login') {
      authorized = true
      return route.fulfill({
        status: 303,
        headers: {
          Location:
            new URL(route.request().url()).searchParams.get('return_to') ??
            '/dashboard',
        },
      })
    }
    if (path === '/api/unihub/access') {
      if (!authorized || options.mismatch || Date.now() / 1000 >= expiry) {
        return route.fulfill({ status: 401, json: { success: false } })
      }
      return route.fulfill({
        json: {
          access: {
            subject: 'immutable-user',
            user: { id: 'immutable-user', name: 'Product user', email: '' },
            roles: ['member'],
            permissions: options.permissions ?? ['product:use'],
            expires_at: expiry,
          },
          permissions: { admin_permissions: permissions },
          csrf_token: 'csrf',
        },
      })
    }
    if (path.startsWith('/api/unihub/bff/')) return productRouteHandler(route)
    if (
      [
        '/api/channel/models',
        '/api/channel/models_enabled',
        '/api/models/',
        '/api/prefill_group/',
      ].includes(path)
    ) {
      return route.fulfill({ json: { success: true, data: [] } })
    }
    if (['/api/group/', '/api/group'].includes(path)) {
      return route.fulfill({ json: { success: true, data: [] } })
    }
    if (path === '/api/channel' || path === '/api/channel/') {
      return route.fulfill({
        json: {
          success: true,
          data: { items: [], total: 0, page: 1, page_size: 10 },
        },
      })
    }
    if (path === '/api/unihub/config') {
      return route.fulfill({
        json: {
          enabled: true,
          native_access: false,
          public_origin: 'http://127.0.0.1:58006',
          client_id: 'client',
          redirect_uri: 'http://127.0.0.1:58004/unihub',
          product_id: 'llmrouter',
          models: options.models ?? ['synthetic'],
        },
      })
    }
    if (path === '/api/user/auth/refresh') {
      return route.fulfill(
        authorized && Date.now() / 1000 < expiry
          ? { json: { success: true, data: nativeBundle } }
          : { status: 401, json: { success: false } },
      )
    }
    if (path === '/api/user/auth/logout') {
      authorized = false
      return route.fulfill({ json: { success: true } })
    }
    if (path === '/api/unihub/identity') {
      return route.fulfill({
        json: {
          user: { id: 'immutable-user', name: 'Product user' },
          accounts,
        },
      })
    }
    return route.fulfill({ json: { success: true, data: { status: true } } })
  })
  let state = ''
  const mutations: { account: string; key: string; body: string }[] = []
  const productRouteHandler = async (route: Route) => {
    const request = route.request()
    const path = new URL(request.url()).pathname.replace('/api/unihub/bff', '')
    const headers = {
      'Access-Control-Allow-Origin': 'http://127.0.0.1:58004',
      'Access-Control-Allow-Headers': '*',
      'Access-Control-Allow-Methods': '*',
    }
    if (request.method() === 'OPTIONS') {
      return route.fulfill({ headers, status: 204 })
    }
    let body: unknown = []
    if (path.endsWith('/authorize/password')) {
      const input = request.postDataJSON()
      state = input.state
      expect(input.code_challenge_method).toBe('S256')
      expect(input.code_challenge).toHaveLength(43)
      body = { authorization_id: 'a'.repeat(43), code: 'a'.repeat(64), state }
    } else if (path.endsWith('/identity/token')) {
      expect(request.postDataJSON().code_verifier).toHaveLength(43)
      body = {
        access_token: 'memory-only-product',
        id_token: 'id',
        token_type: 'Bearer',
        scope: 'openid profile email',
        expires_in: options.expiry ?? 300,
      }
    } else if (path.endsWith('/registrations')) {
      mutations.push({
        account: 'registration',
        key: request.headers()['idempotency-key'],
        body: request.postData() ?? '',
      })
      body = {
        registration_id: 'registration',
        status: options.registerUnknown ? 'UNKNOWN' : 'CREATED',
      }
    } else if (path.endsWith('/billing-profile')) {
      body = {
        usage: { enabled: true, funding_modes: ['prepaid_cash'] },
        addons: { enabled: true },
        subscription: { enabled: false },
      }
    } else if (
      path.endsWith('/model-credentials') &&
      request.method() === 'POST'
    ) {
      const input = request.postDataJSON()
      body = {
        id: 'grant',
        account_id: path.split('/')[3],
        task_id: input.task_id,
        models: input.models,
        funding_mode: input.funding_mode,
        expires_at: new Date(Date.now() + 60000).toISOString(),
        token: 'uh_model_task',
      }
    } else if (
      path.includes('/model-credentials/') &&
      request.method() === 'DELETE'
    ) {
      return route.fulfill({ headers, status: 204 })
    } else if (path.endsWith('/accounts')) body = accounts
    else if (path.endsWith('/resources')) {
      body = {
        account_id: path.split('/')[3],
        product_id: 'llmrouter',
        subscriptions: [],
        wallets: [
          {
            product_id: 'llmrouter',
            asset: 'CNY/6',
            amount: '9007199254740993123',
          },
        ],
        payments: { real_payments_configured: false },
        billing_profile: {
          addons: { enabled: true },
          subscription: { enabled: false },
        },
      }
    } else if (path.endsWith('/catalog')) {
      body = [
        {
          id: 'addon',
          name: 'Immutable addon',
          account_kind: 'both',
          product_id: 'llmrouter',
          status: 'published',
          kind: 'addon',
          price: '1000000',
          monthly_credit: '1000000',
        },
      ]
    } else if (path.includes('/model-pool/')) {
      return route.fulfill({
        headers,
        status: 403,
        json: { error: 'scope_denied' },
      })
    } else if (path.endsWith('/summary')) {
      body = {
        calls_total: '0',
        pending_calls: '0',
        financial_details: false,
        accuracy: 'ledger',
        as_of: 'now',
      }
    } else if (path.endsWith('/calls')) body = { items: [], has_more: false }
    else if (path.endsWith('/recharges') && request.method() === 'POST') {
      mutations.push({
        account: path.split('/')[3],
        key: request.headers()['idempotency-key'],
        body: request.postData() ?? '',
      })
      return route.fulfill({
        headers,
        status: 503,
        json: { error: 'response_unknown' },
      })
    }
    return route.fulfill({ headers, json: body })
  }
  await page.route('http://127.0.0.1:58006/**', productRouteHandler)
  return mutations
}
async function login(page: Page) {
  await page.goto('/unihub')
  await expect(page.getByText('Product user · immutable-user')).toBeVisible()
  await expect(page.getByRole('combobox').first()).toHaveValue('personal-a')
}
test('SSO restores backend session after reload and payer controls role visibility', async ({
  page,
}) => {
  await fixture(page)
  await login(page)
  await expect(
    page.getByText('9007199254740.993123 CNY', { exact: false }),
  ).toBeVisible()
  await expect(
    page.getByText('Own-origin usage only.', { exact: false }),
  ).toBeVisible()
  await expect(
    page.getByRole('button', { name: 'Create recharge', exact: true }),
  ).toBeVisible()
  await page.getByRole('combobox').first().selectOption('team-b')
  await expect(
    page.getByText('Finance actions require owner or billing_admin role.'),
  ).toBeVisible()
  await expect(
    page.getByRole('button', { name: 'Create recharge', exact: true }),
  ).toHaveCount(0)
  expect(
    await page.evaluate(() =>
      JSON.stringify({
        local: { ...localStorage },
        session: { ...sessionStorage },
      }),
    ),
  ).not.toContain('memory-only-product')
  await page.reload()
  await expect(page.getByText('Product user · immutable-user')).toBeVisible()
})
test('uncertain recharge retries retain original key and payer after switching', async ({
  page,
}) => {
  const writes = await fixture(page)
  await login(page)
  await page.getByLabel('Recharge amount (CNY)').fill('10.50')
  await page
    .getByRole('button', { name: 'Create recharge', exact: true })
    .click()
  await expect(
    page.getByRole('button', { name: 'Retry original operation' }),
  ).toBeVisible()
  await page.getByRole('combobox').first().selectOption('team-b')
  await page.getByRole('combobox').first().selectOption('personal-a')
  await page.getByRole('button', { name: 'Retry original operation' }).click()
  await expect.poll(() => writes.length).toBe(2)
  expect(writes[0]).toEqual(writes[1])
  expect(writes[0].account).toBe('personal-a')
  expect(JSON.parse(writes[0].body).amount).toBe('10500000')
})
test('revoked proof removes account resources and stops automatic sign-in', async ({
  page,
}) => {
  await fixture(page)
  await login(page)
  await page.route('**/api/unihub/access', (route) =>
    route.fulfill({ status: 401, json: { success: false } }),
  )
  await page.reload()
  await expect(
    page.getByRole('button', { name: 'Sign in with UniHub', exact: true }),
  ).toBeVisible()
  await expect(page.getByText('Product user · immutable-user')).toHaveCount(0)
})
test('unknown registration retries original payload and idempotency key', async ({
  page,
}) => {
  const writes = await fixture(page, { registerUnknown: true })
  await page.goto('/unihub?signed_out=1')
  await page.getByRole('button', { name: 'Register UniHub account' }).click()
  await page.getByLabel('Username', { exact: true }).fill('new-user')
  await page.getByLabel('Password', { exact: true }).fill('private')
  await page.getByRole('button', { name: 'Register', exact: true }).click()
  await expect(
    page.getByRole('button', { name: 'Retry original registration' }),
  ).toBeVisible()
  await page
    .getByRole('button', { name: 'Retry original registration' })
    .click()
  await expect.poll(() => writes.length).toBe(2)
  expect(writes[0]).toEqual(writes[1])
})

test('ordinary and streamed sends show text, bind stable task/call IDs and allow receipt/revoke', async ({
  page,
}) => {
  await fixture(page)
  const sends: { id: string; task: string; stream: boolean }[] = []
  await page.route('**/platform/v1/**', (route) => {
    const request = route.request()
    expect(request.headers().authorization).toBe('Bearer uh_model_task')
    if (request.method() === 'GET') {
      return route.fulfill({
        json: { status: 'SETTLED', charged: '10', asset: 'CNY/6' },
      })
    }
    const body = request.postDataJSON()
    expect(body).not.toHaveProperty('account_id')
    expect(body.max_tokens).toBeGreaterThan(0)
    sends.push({
      id: request.headers()['idempotency-key'],
      task: request.headers()['x-unihub-task-id'],
      stream: body.stream,
    })
    return route.fulfill(
      body.stream
        ? {
            contentType: 'text/event-stream',
            body: 'data: {"choices":[{"delta":{"content":"streamed text"}}]}\n\ndata: [DONE]\n\n',
          }
        : { json: { choices: [{ message: { content: 'ordinary text' } }] } },
    )
  })
  await login(page)
  await page
    .getByLabel('Funding mode', { exact: true })
    .selectOption('prepaid_cash')
  await page.getByLabel('Message', { exact: true }).fill('Hello')
  await page.getByRole('button', { name: 'Send new task' }).click()
  await expect(page.getByLabel('Model output')).toHaveText('ordinary text')
  await page.getByLabel('Stream response').check()
  await page.getByRole('button', { name: 'Send new task' }).click()
  await expect(page.getByLabel('Model output')).toHaveText('streamed text')
  expect(sends).toHaveLength(2)
  expect(sends[0].id).not.toBe(sends[1].id)
  expect(sends[0].task).not.toBe(sends[1].task)
  await page.getByRole('button', { name: 'Revoke task credential' }).click()
  await expect(
    page.getByRole('button', { name: 'Revoke task credential' }),
  ).toHaveCount(0)
})

test('late grant response after payer switch cannot send a model request', async ({
  page,
}) => {
  await fixture(page)
  let release!: () => void
  const gate = new Promise<void>((resolve) => {
    release = resolve
  })
  let entered!: () => void
  const started = new Promise<void>((resolve) => {
    entered = resolve
  })
  let sends = 0
  await page.route('**/model-credentials', async (route) => {
    if (route.request().method() === 'OPTIONS') return route.fallback()
    entered()
    await gate
    const input = route.request().postDataJSON()
    await route
      .fulfill({
        headers: { 'Access-Control-Allow-Origin': 'http://127.0.0.1:58004' },
        json: {
          id: 'stale',
          token: 'uh_model_stale',
          account_id: 'personal-a',
          task_id: input.task_id,
          expires_at: new Date(Date.now() + 60000).toISOString(),
        },
      })
      .catch(() => {})
  })
  await page.route('**/platform/v1/**', (route) => {
    sends++
    return route.fulfill({ json: {} })
  })
  await login(page)
  await page
    .getByLabel('Funding mode', { exact: true })
    .selectOption('prepaid_cash')
  await page.getByLabel('Message', { exact: true }).fill('Hello')
  await page.getByRole('button', { name: 'Send new task' }).click()
  await started
  await page.getByRole('combobox').first().selectOption('team-b')
  release()
  await expect(page.getByText('Payer: Team B', { exact: false })).toBeVisible()
  await expect(
    page.getByRole('button', { name: 'Send new task' }),
  ).toBeEnabled()
  expect(sends).toBe(0)
  await expect(page.getByLabel('Model output')).toHaveCount(0)
})

test('explicit account refresh shows a member who accepted remotely', async ({
  page,
}) => {
  await fixture(page)
  let accepted = false
  await page.route('**/personal-a/members', (route) => {
    if (route.request().method() === 'OPTIONS') return route.fallback()
    return route.fulfill({
      headers: { 'Access-Control-Allow-Origin': 'http://127.0.0.1:58004' },
      json: accepted
        ? [
            {
              user_id: 'new-recipient',
              name: 'Remote member',
              email: '',
              role: 'member',
            },
          ]
        : [],
    })
  })
  await login(page)
  await expect(page.getByText('Remote member', { exact: false })).toHaveCount(0)
  accepted = true
  await page.getByRole('button', { name: 'Refresh accounts and roles' }).click()
  await expect(
    page.getByText('Remote member · new-recipient · member'),
  ).toBeVisible()
  await expect(
    page.getByRole('combobox', { name: 'Member role new-recipient' }),
  ).toHaveValue('member')
})

test('created recharge shows exact amount unconfirmed status and queries original object only', async ({
  page,
}) => {
  await fixture(page)
  let creates = 0
  let reads = 0
  await page.route('**/personal-a/recharges', (route) => {
    if (route.request().method() === 'OPTIONS') return route.fallback()
    creates++
    return route.fulfill({
      headers: { 'Access-Control-Allow-Origin': 'http://127.0.0.1:58004' },
      json: {
        id: 'original-recharge',
        account_id: 'personal-a',
        product_id: 'llmrouter',
        amount: '10500000',
        currency: 'CNY',
        merchant: 'sandbox',
      },
    })
  })
  await page.route('**/personal-a/recharges/original-recharge', (route) => {
    if (route.request().method() === 'OPTIONS') return route.fallback()
    reads++
    return route.fulfill({
      headers: { 'Access-Control-Allow-Origin': 'http://127.0.0.1:58004' },
      json: {
        id: 'original-recharge',
        account_id: 'personal-a',
        product_id: 'llmrouter',
        amount: '10500000',
        currency: 'CNY',
        merchant: 'sandbox',
      },
    })
  })
  await login(page)
  await page.getByLabel('Recharge amount (CNY)').fill('10.50')
  await page
    .getByRole('button', { name: 'Create recharge', exact: true })
    .click()
  await expect(
    page.getByText('original-recharge', { exact: true }),
  ).toBeVisible()
  await expect(page.getByText('10.500000 CNY', { exact: true })).toBeVisible()
  await expect(
    page.getByText('Created; payment and posting are not confirmed.'),
  ).toBeVisible()
  await page.getByRole('button', { name: 'Query this original object' }).click()
  await expect.poll(() => reads).toBe(1)
  expect(creates).toBe(1)
})

test('mobile payer control and long team name do not overflow the viewport', async ({
  page,
}) => {
  await page.setViewportSize({ width: 390, height: 844 })
  await fixture(page)
  await page.route('**/api/unihub/bff/v1/accounts', (route) =>
    route.fulfill({
      json: [
        accounts[0],
        { ...accounts[1], name: 'Long team name '.repeat(12) },
      ],
    }),
  )
  await login(page)
  await page.getByRole('combobox').first().selectOption('team-b')
  await expect(
    page.getByText('Finance actions require owner or billing_admin role.'),
  ).toBeVisible()
  expect(
    await page.evaluate(() => document.documentElement.scrollWidth),
  ).toBeLessThanOrEqual(390)
})

test('definitive finance scope denial shows failure without retaining uncertain retry', async ({
  page,
}) => {
  await fixture(page)
  await page.route('**/personal-a/recharges', (route) => {
    if (route.request().method() === 'OPTIONS') return route.fallback()
    return route.fulfill({
      headers: { 'Access-Control-Allow-Origin': 'http://127.0.0.1:58004' },
      status: 403,
      json: { error: 'scope_denied' },
    })
  })
  await login(page)
  await page.getByLabel('Recharge amount (CNY)').fill('10.50')
  await page
    .getByRole('button', { name: 'Create recharge', exact: true })
    .click()
  await expect(page.getByRole('alert')).toContainText('scope_denied')
  await expect(
    page.getByRole('button', { name: 'Retry original operation' }),
  ).toHaveCount(0)
  await expect(
    page.getByRole('button', { name: 'Create recharge', exact: true }),
  ).toBeEnabled()
  await expect(page.getByText('Original financial object')).toHaveCount(0)
})

for (const rejection of [
  { status: 400, code: 'invalid_password' },
  { status: 403, code: 'origin_not_allowed' },
  { status: 409, code: 'registration_conflict' },
]) {
  test(`definitive registration ${rejection.code} unlocks correction and sign-in`, async ({
    page,
  }) => {
    await fixture(page)
    const writes: { key: string; username: string }[] = []
    await page.route('**/identity/registrations', (route) => {
      if (route.request().method() === 'OPTIONS') return route.fallback()
      writes.push({
        key: route.request().headers()['idempotency-key'],
        username: route.request().postDataJSON().username,
      })
      const response =
        writes.length === 1
          ? { status: rejection.status, json: { error: rejection.code } }
          : { status: 201, json: { registration_id: 'new', status: 'CREATED' } }
      return route.fulfill({
        headers: { 'Access-Control-Allow-Origin': 'http://127.0.0.1:58004' },
        ...response,
      })
    })
    await page.goto('/unihub?signed_out=1')
    await page.getByRole('button', { name: 'Register UniHub account' }).click()
    await page.getByLabel('Username', { exact: true }).fill('rejected-user')
    await page.getByLabel('Password', { exact: true }).fill('private')
    await page.getByRole('button', { name: 'Register', exact: true }).click()
    await expect(page.getByRole('status')).toContainText(rejection.code)
    await expect(page.getByLabel('Username', { exact: true })).toBeEnabled()
    await expect(
      page.getByRole('button', { name: 'Back to sign in' }),
    ).toBeEnabled()
    await page.getByLabel('Username', { exact: true }).fill('corrected-user')
    await page.getByRole('button', { name: 'Register', exact: true }).click()
    await expect(
      page.getByRole('button', { name: 'Sign in with UniHub', exact: true }),
    ).toBeVisible()
    expect(writes).toHaveLength(2)
    expect(writes[1].username).toBe('corrected-user')
    expect(writes[0].key).not.toBe(writes[1].key)
  })
}

for (const failure of ['network', 'pending', 'idempotency_conflict'] as const) {
  test(`uncertain registration ${failure} retains original locked payload and key`, async ({
    page,
  }) => {
    await fixture(page)
    const writes: { key: string; body: string }[] = []
    await page.route('**/identity/registrations', (route) => {
      if (route.request().method() === 'OPTIONS') return route.fallback()
      writes.push({
        key: route.request().headers()['idempotency-key'],
        body: route.request().postData() ?? '',
      })
      if (failure === 'network') return route.abort('failed')
      return route.fulfill({
        headers: { 'Access-Control-Allow-Origin': 'http://127.0.0.1:58004' },
        status: failure === 'pending' ? 202 : 409,
        json:
          failure === 'pending'
            ? { status: 'UNKNOWN', registration_id: 'original' }
            : { error: 'idempotency_conflict' },
      })
    })
    await page.goto('/unihub?signed_out=1')
    await page.getByRole('button', { name: 'Register UniHub account' }).click()
    await page.getByLabel('Username', { exact: true }).fill('uncertain-user')
    await page.getByLabel('Password', { exact: true }).fill('private')
    await page.getByRole('button', { name: 'Register', exact: true }).click()
    await expect(
      page.getByRole('button', { name: 'Retry original registration' }),
    ).toBeEnabled()
    await expect(page.getByLabel('Username', { exact: true })).toBeDisabled()
    await expect(
      page.getByRole('button', { name: 'Back to sign in' }),
    ).toBeDisabled()
    await page
      .getByRole('button', { name: 'Retry original registration' })
      .click()
    await expect.poll(() => writes.length).toBe(2)
    expect(writes[0]).toEqual(writes[1])
  })
}

for (const manual of [false, true]) {
  test(`${manual ? 'manual' : 'automatic'} settled task receipt refreshes same-payer credits and usage`, async ({
    page,
  }) => {
    await fixture(page)
    let settled = false
    let receipts = 0
    const headers = { 'Access-Control-Allow-Origin': 'http://127.0.0.1:58004' }
    await page.route('**/personal-a/resources', (route) =>
      route.request().method() === 'OPTIONS'
        ? route.fallback()
        : route.fulfill({
            headers,
            json: {
              account_id: 'personal-a',
              product_id: 'llmrouter',
              subscriptions: [],
              credits: {
                asset: 'CREDIT/6',
                spendable: settled ? '2999980' : '3000000',
                reserved: '0',
                frozen: '0',
              },
              payments: { real_payments_configured: false },
              billing_profile: {
                addons: { enabled: true },
                subscription: { enabled: false },
              },
            },
          }),
    )
    await page.route('**/personal-a/usage/summary', (route) =>
      route.request().method() === 'OPTIONS'
        ? route.fallback()
        : route.fulfill({
            headers,
            json: {
              calls_total: settled ? '1' : '0',
              pending_calls: '0',
              financial_details: true,
              charges_by_asset: { 'CREDIT/6': settled ? '20' : '0' },
              as_of: 'now',
            },
          }),
    )
    await page.route('**/platform/v1/**', (route) => {
      if (route.request().method() === 'POST') {
        return route.fulfill({
          json: { choices: [{ message: { content: 'charged text' } }] },
        })
      }
      receipts++
      settled = !manual || receipts > 1
      return route.fulfill({
        json: {
          status: settled ? 'SETTLED' : 'RESERVED',
          charged: settled ? '20' : '0',
          asset: 'CREDIT/6',
        },
      })
    })
    await login(page)
    await expect(
      page.getByText('Spendable credits: 3.000000 CREDIT', { exact: false }),
    ).toBeVisible()
    await page
      .getByLabel('Funding mode', { exact: true })
      .selectOption('prepaid_cash')
    await page.getByLabel('Message', { exact: true }).fill('Hello')
    await page.getByRole('button', { name: 'Send new task' }).click()
    if (manual) {
      await expect(
        page.getByText('Task status: RESERVED', { exact: false }),
      ).toBeVisible()
      await page.getByRole('button', { name: 'Query task receipt' }).click()
    }
    await expect(
      page.getByText('Task status: SETTLED', { exact: false }),
    ).toBeVisible()
    await expect(
      page.getByText('Spendable credits: 2.999980 CREDIT', { exact: false }),
    ).toBeVisible()
    await expect(page.getByText('Calls: 1', { exact: false })).toBeVisible()
    await expect(
      page.getByText('Charged: 0.000020 CREDIT', { exact: true }),
    ).toBeVisible()
  })
}

test('upgrade controls show only higher-rank same-period offers and accepted quote disappears', async ({
  page,
}) => {
  await fixture(page)
  const headers = { 'Access-Control-Allow-Origin': 'http://127.0.0.1:58004' }
  const offer = (
    id: string,
    rank: number,
    period = 'month',
    kind = 'membership',
  ) => ({
    id,
    name: id,
    rank,
    period,
    kind,
    account_kind: 'both',
    product_id: 'llmrouter',
    status: 'published',
    price: '1000000',
    monthly_credit: '1000000',
  })
  const offers = [
    offer('lower', 1),
    offer('current', 2),
    offer('higher', 3),
    offer('annual', 3, 'year'),
    offer('addon', 4, '', 'addon'),
  ]
  await page.route('**/catalog', (route) =>
    route.request().method() === 'OPTIONS'
      ? route.fallback()
      : route.fulfill({ headers, json: offers }),
  )
  await page.route('**/personal-a/resources', (route) =>
    route.request().method() === 'OPTIONS'
      ? route.fallback()
      : route.fulfill({
          headers,
          json: {
            account_id: 'personal-a',
            subscriptions: [
              {
                id: 'active-sub',
                product_id: 'llmrouter',
                status: 'active',
                offer: offers[1],
              },
            ],
            payments: { real_payments_configured: false },
            billing_profile: {
              addons: { enabled: true },
              subscription: { enabled: true },
            },
          },
        }),
  )
  await page.route('**/personal-a/quotes', (route) =>
    route.request().method() === 'OPTIONS'
      ? route.fallback()
      : route.fulfill({
          headers,
          json: {
            id: 'upgrade-quote',
            account_id: 'personal-a',
            subscription_id: 'active-sub',
            offer: offers[2],
            amount: '1000000',
            expires_at: '2099-01-01T00:00:00Z',
          },
        }),
  )
  await page.route('**/quotes/upgrade-quote/accept', (route) =>
    route.request().method() === 'OPTIONS'
      ? route.fallback()
      : route.fulfill({
          headers,
          json: {
            id: 'upgrade-order',
            account_id: 'personal-a',
            offer: offers[2],
            amount: '1000000',
            payment_status: 'PENDING',
            fulfillment_status: 'PENDING',
          },
        }),
  )
  await login(page)
  await expect(page.getByRole('button', { name: 'Quote upgrade' })).toHaveCount(
    1,
  )
  await page
    .getByRole('article')
    .filter({ has: page.getByRole('heading', { name: 'higher', exact: true }) })
    .getByRole('button', { name: 'Quote upgrade' })
    .click()
  await expect(
    page.getByText('Upgrade quote: upgrade-quote', { exact: false }),
  ).toBeVisible()
  await page
    .getByRole('button', { name: 'Accept quote and create order' })
    .click()
  await expect(page.getByText('upgrade-order', { exact: true })).toBeVisible()
  await expect(
    page.getByRole('button', { name: 'Accept quote and create order' }),
  ).toHaveCount(0)
})

test('late settled receipt after payer switch cannot populate or refetch the new payer', async ({
  page,
}) => {
  await fixture(page)
  let release!: () => void
  const gate = new Promise<void>((resolve) => {
    release = resolve
  })
  let entered!: () => void
  const requested = new Promise<void>((resolve) => {
    entered = resolve
  })
  let complete!: () => void
  const completed = new Promise<void>((resolve) => {
    complete = resolve
  })
  let newPayerReads = 0
  await page.route('**/team-b/resources', (route) => {
    if (route.request().method() === 'OPTIONS') return route.fallback()
    newPayerReads++
    return route.fulfill({
      headers: { 'Access-Control-Allow-Origin': 'http://127.0.0.1:58004' },
      json: {
        account_id: 'team-b',
        subscriptions: [],
        payments: { real_payments_configured: false },
        billing_profile: {
          addons: { enabled: true },
          subscription: { enabled: false },
        },
      },
    })
  })
  await page.route('**/platform/v1/**', async (route) => {
    if (route.request().method() === 'POST') {
      return route.fulfill({
        json: { choices: [{ message: { content: 'old payer output' } }] },
      })
    }
    entered()
    await gate
    try {
      await route.fulfill({
        json: { status: 'SETTLED', charged: '20', asset: 'CREDIT/6' },
      })
    } catch {
      /* switching payer aborts this in-flight response */
    } finally {
      complete()
    }
  })
  await login(page)
  await page
    .getByLabel('Funding mode', { exact: true })
    .selectOption('prepaid_cash')
  await page.getByLabel('Message', { exact: true }).fill('Hello')
  await page.getByRole('button', { name: 'Send new task' }).click()
  await requested
  await page.getByRole('combobox').first().selectOption('team-b')
  await expect(
    page.getByText('Finance actions require owner or billing_admin role.'),
  ).toBeVisible()
  release()
  await completed
  await expect(
    page.getByText('Task status: SETTLED', { exact: false }),
  ).toHaveCount(0)
  expect(newPayerReads).toBe(1)
})

test('public sign-in keeps the safe requested wallet and excludes native actions', async ({
  page,
}) => {
  await fixture(page)
  await page.goto('/sign-in?redirect=%2Fwallet')
  await expect(page).toHaveURL(/\/wallet$/)
  await expect(page.getByText('Product user · immutable-user')).toBeVisible()
  await expect(
    page.getByRole('link', {
      name: /Local operator sign in|API keys|Change password/,
    }),
  ).toHaveCount(0)
  await page.getByRole('button', { name: 'Sign out', exact: true }).click()
  await expect(page).toHaveURL(/signed_out=1/)
  await page.reload()
  await page.goto('/dashboard')
  await expect(
    page.getByRole('button', { name: 'Sign in with UniHub', exact: true }),
  ).toBeVisible()
  await expect(page.getByText('Product user · immutable-user')).toHaveCount(0)
})
test('external redirect is rejected and public SSO retains account session', async ({
  page,
}) => {
  await fixture(page)
  await page.goto('/unihub?redirect=https%3A%2F%2Fevil.test%2Fsteal')
  await expect(page).toHaveURL(/\/dashboard$/)
  await expect(page.getByText('Product user · immutable-user')).toBeVisible()
  await expect(page.getByLabel('Password', { exact: true })).toHaveCount(0)
})
test('immutable subject mismatch clears unusable local proof and offers explicit retry', async ({
  page,
}) => {
  await fixture(page, { mismatch: true })
  await page.goto('/unihub')
  await expect(
    page.getByRole('button', { name: 'Sign in with UniHub', exact: true }),
  ).toBeVisible()
  await expect(page.getByText('Product user · immutable-user')).toHaveCount(0)
  await expect(page).toHaveURL(/sso_error=1/)
})
test('closed model gate shows unavailable state and disables provider send', async ({
  page,
}) => {
  await fixture(page, { models: [] })
  await login(page)
  await expect(
    page.getByText('No available models', { exact: true }),
  ).toBeVisible()
  await expect(
    page.getByRole('button', { name: 'Send new task' }),
  ).toBeDisabled()
})

for (const scenario of [
  'unchanged',
  'edited',
  'UNKNOWN',
  'network',
  'unproven',
] as const) {
  test(`registration throttle safely recovers ${scenario} with manual retry`, async ({
    page,
  }) => {
    await fixture(page)
    await page.clock.install()
    const prior = scenario === 'UNKNOWN' || scenario === 'network'
    const safe = !prior && scenario !== 'unproven'
    const writes: { key: string; body: string }[] = []
    await page.route('**/identity/registrations', (route) => {
      if (route.request().method() === 'OPTIONS') return route.fallback()
      writes.push({
        key: route.request().headers()['idempotency-key'],
        body: route.request().postData() ?? '',
      })
      if (scenario === 'network' && writes.length === 1) {
        return route.abort('failed')
      }
      const unknown = scenario === 'UNKNOWN' && writes.length === 1
      return route.fulfill({
        headers: { 'Access-Control-Allow-Origin': 'http://127.0.0.1:58004' },
        status: unknown ? 202 : 429,
        json: unknown
          ? { status: 'UNKNOWN', registration_id: 'original' }
          : {
              error: 'rate_limited',
              retry_after: 10,
              registration_not_started: scenario !== 'unproven',
            },
      })
    })
    await page.goto('/unihub?signed_out=1')
    await page.getByRole('button', { name: 'Register UniHub account' }).click()
    await page.getByLabel('Username', { exact: true }).fill('throttled-user')
    await page.getByLabel('Password', { exact: true }).fill('private')
    await page.getByRole('button', { name: 'Register', exact: true }).click()
    if (prior) {
      await page
        .getByRole('button', { name: 'Retry original registration' })
        .click()
    }
    await expect(page.getByRole('status')).toContainText(
      'Too many registration attempts',
    )
    await expect(page.getByLabel('Username', { exact: true })).toBeEnabled({
      enabled: safe,
    })
    await expect(
      page.getByRole('button', { name: 'Back to sign in' }),
    ).toBeEnabled({ enabled: safe })
    if (scenario === 'edited') {
      await page.getByLabel('Username', { exact: true }).fill('corrected-user')
    }
    const submit = page.locator('button[type="submit"]')
    await expect(submit).toBeDisabled()
    const count = writes.length
    await page
      .locator('form')
      .evaluate((form: HTMLFormElement) =>
        form.dispatchEvent(
          new Event('submit', { bubbles: true, cancelable: true }),
        ),
      )
    expect(writes).toHaveLength(count)
    await page.clock.fastForward(11000)
    await expect(submit).toBeEnabled()
    expect(writes).toHaveLength(count)
    await submit.click()
    await expect.poll(() => writes.length).toBe(count + 1)
    if (scenario === 'edited') {
      expect(writes[count].key).not.toBe(writes[0].key)
      expect(JSON.parse(writes[count].body).username).toBe('corrected-user')
    } else expect(writes[count]).toEqual(writes[0])
    if (safe) {
      await page.getByRole('button', { name: 'Back to sign in' }).click()
      await expect(
        page.getByRole('button', { name: 'Sign in with UniHub', exact: true }),
      ).toBeEnabled()
    }
  })
}

test('viewer opens channels with common stored role and no native finance links', async ({
  page,
}) => {
  await fixture(page, { permissions: ['channel:read'] })
  await page.goto('/channels')
  await expect(
    page.getByRole('heading', { name: 'Channels', exact: true }),
  ).toBeVisible()
  await expect(
    page
      .getByRole('navigation', { name: 'UniHub account' })
      .getByRole('link', { name: 'Channels', exact: true }),
  ).toBeVisible()
  await expect(
    page.getByRole('button', { name: 'Add Channel', exact: true }),
  ).toHaveCount(0)
  await expect(
    page.getByRole('link', { name: 'Users', exact: true }),
  ).toHaveCount(0)
})

test('ordinary user cannot open channels and logout waits for explicit re-entry', async ({
  page,
}) => {
  await fixture(page)
  await login(page)
  await page.getByRole('button', { name: 'Sign out', exact: true }).click()
  await expect(
    page.getByRole('button', { name: 'Sign in with UniHub', exact: true }),
  ).toBeVisible()
  await expect(page).toHaveURL(/signed_out=1/)
  await page
    .getByRole('button', { name: 'Sign in with UniHub', exact: true })
    .click()
  await expect(page.getByText('Product user · immutable-user')).toBeVisible()
  await page.goto('/channels')
  await expect(page).toHaveURL(/\/403$/)
})

test('first product entry at root completes SSO at the dashboard', async ({
  page,
}) => {
  await fixture(page)
  await page.goto('/')
  await expect(page).toHaveURL(/\/dashboard$/)
  await expect(page.getByText('Product user · immutable-user')).toBeVisible()
})

test('restored product root reload opens dashboard instead of denied route', async ({
  page,
}) => {
  await fixture(page)
  await login(page)
  await page.goto('/')
  await expect(page).toHaveURL(/\/dashboard$/)
  await page.reload()
  await expect(page).toHaveURL(/\/dashboard$/)
  await expect(page.getByText('Product user · immutable-user')).toBeVisible()
})

test('authenticated sign-in honors safe wallet and channel destinations', async ({
  page,
}) => {
  await fixture(page, { permissions: ['channel:read'] })
  await login(page)
  await page.goto('/sign-in?redirect=%2Fwallet')
  await expect(page).toHaveURL(/\/wallet$/)
  await expect(page.getByText('Product user · immutable-user')).toBeVisible()
  await page.goto('/sign-in?redirect=%2Fchannels%3Ffilter%3Dsentinel')
  await expect(page).toHaveURL(/\/channels\?filter=sentinel$/)
  await expect(
    page.getByRole('heading', { name: 'Channels', exact: true }),
  ).toBeVisible()
})
