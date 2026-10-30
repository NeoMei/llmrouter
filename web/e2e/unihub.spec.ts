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
  } = {}
) {
  let authorized = false
  const permissions = {
    product: { use: true },
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
      role: options.permissions?.includes('channel:read') ? 10 : 1,
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
      if (
        new URL(route.request().url()).searchParams.get('prompt') === 'none' &&
        options.mismatch
      ) {
        return route.fulfill({
          status: 303,
          headers: { Location: '/sign-in?unihub_silent=done' },
        })
      }
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
    if (path === '/api/unihub/chat-models') {
      return route.fulfill({
        json: {
          product_id: 'llmrouter',
          instance_id: 'default',
          admission_mode: 'metered_v2',
          models: (options.models ?? ['synthetic']).map((id) => ({
            id,
            policy: 'sum',
            rate_ids: { 'CNY/6': id },
          })),
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
          : { status: 401, json: { success: false } }
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
    if (path === '/api/user/self') {
      return route.fulfill({
        json: { success: true, data: nativeBundle.user },
      })
    }
    if (path === '/api/token/') {
      return route.fulfill({
        json: {
          success: true,
          data: { items: [], total: 0, page: 1, page_size: 10 },
        },
      })
    }
    if (path === '/api/user/topup/info') {
      return route.fulfill({
        json: {
          success: true,
          data: {
            min_topup: 1,
            amount_options: [1, 10],
            discount: {},
            pay_methods: [],
          },
        },
      })
    }
    if (path === '/api/subscription/self') {
      return route.fulfill({ json: { success: true, data: [] } })
    }
    if (path === '/api/home_page_content') {
      return route.fulfill({ json: { success: true, data: '' } })
    }
    if (path === '/api/subscription/plans') {
      return route.fulfill({ json: { success: true, data: [] } })
    }
    if (path === '/api/notice') {
      return route.fulfill({ json: { success: true, data: '' } })
    }
    if (
      path.startsWith('/api/data') ||
      path === '/api/user/models' ||
      path === '/api/uptime/status'
    ) {
      return route.fulfill({ json: { success: true, data: [] } })
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
    } else if (path.endsWith('/api-keys')) {
      body = []
    } else if (path.endsWith('/model-consistency')) {
      const params = new URL(request.url()).searchParams
      expect(params.get('account_id')).toMatch(/^(personal-a|team-b)$/)
      expect(params.get('funding_mode')).toMatch(/^(prepaid_cash|credits)$/)
      const asset =
        params.get('funding_mode') === 'credits' ? 'CREDIT/6' : 'CNY/6'
      body = {
        product_id: 'llmrouter',
        asset,
        allowlist: options.models ?? ['synthetic'],
        published_rates: (options.models ?? ['synthetic']).map((model) => ({
          id: model,
          model,
          status: 'published',
          asset,
          semantics: 'metered_v2',
          metered: {
            version: 'metered_v2',
            operation: 'chat',
            policy: 'sum',
            terms: [
              {
                key: 'requests',
                unit: 'request',
                price: { numerator: '1', denominator: '1' },
              },
            ],
          },
        })),
        missing_models: [],
        extra_models: [],
        duplicate_models: [],
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
        scope: {
          account_id: path.split('/')[3],
          origin_product_id: 'llmrouter',
          visibility: path.includes('/team-b/') ? 'actor' : 'account',
          ...(path.includes('/team-b/') ? { actor_id: 'immutable-user' } : {}),
        },
        calls_total: '0',
        pending_calls: '0',
        financial_details: false,
        accuracy: 'ledger',
        as_of: 'now',
      }
    } else if (path.endsWith('/calls')) {
      body = {
        scope: {
          account_id: path.split('/')[3],
          origin_product_id: 'llmrouter',
          visibility: path.includes('/team-b/') ? 'actor' : 'account',
          ...(path.includes('/team-b/') ? { actor_id: 'immutable-user' } : {}),
        },
        items: [],
        has_more: false,
      }
    } else if (path.endsWith('/recharges') && request.method() === 'POST') {
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
  await page.goto('/api/unihub/login?return_to=%2Funihub')
  await expect(page.getByText('Product user · immutable-user')).toBeVisible()
  await expect(page.getByRole('combobox').first()).toHaveValue('personal-a')
}
test('SSO restores backend session after reload and payer controls role visibility', async ({
  page,
}) => {
  await fixture(page)
  await login(page)
  await expect(
    page.getByText('9007199254740.993123 CNY', { exact: false })
  ).toBeVisible()
  await expect(
    page.getByText('Own-origin usage only.', { exact: false })
  ).toBeVisible()
  await expect(
    page.getByRole('button', { name: 'Create recharge', exact: true })
  ).toBeVisible()
  await page.getByRole('combobox').first().selectOption('team-b')
  await expect(
    page.getByText('Finance actions require owner or billing_admin role.')
  ).toBeVisible()
  await expect(
    page.getByRole('button', { name: 'Create recharge', exact: true })
  ).toHaveCount(0)
  expect(
    await page.evaluate(() =>
      JSON.stringify({
        local: { ...localStorage },
        session: { ...sessionStorage },
      })
    )
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
    page.getByRole('button', { name: 'Retry original operation' })
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
    route.fulfill({ status: 401, json: { success: false } })
  )
  await page.reload()
  await expect(
    page.getByRole('heading', { name: 'Sign in', exact: true })
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
    page.getByRole('button', { name: 'Retry original registration' })
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
        : { json: { choices: [{ message: { content: 'ordinary text' } }] } }
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
    page.getByRole('button', { name: 'Revoke task credential' })
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
    page.getByRole('button', { name: 'Send new task' })
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
    page.getByText('Remote member · new-recipient · member')
  ).toBeVisible()
  await expect(
    page.getByRole('combobox', { name: 'Member role new-recipient' })
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
    page.getByText('original-recharge', { exact: true })
  ).toBeVisible()
  await expect(page.getByText('10.500000 CNY', { exact: true })).toBeVisible()
  await expect(
    page.getByText('Created; payment and posting are not confirmed.')
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
    })
  )
  await login(page)
  await page.getByRole('combobox').first().selectOption('team-b')
  await expect(
    page.getByText('Finance actions require owner or billing_admin role.')
  ).toBeVisible()
  expect(
    await page.evaluate(() => document.documentElement.scrollWidth)
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
    page.getByRole('button', { name: 'Retry original operation' })
  ).toHaveCount(0)
  await expect(
    page.getByRole('button', { name: 'Create recharge', exact: true })
  ).toHaveCount(0)
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
          : {
              status: 201,
              json: { registration_id: 'new', status: 'CREATED' },
            }
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
      page.getByRole('button', { name: 'Back to sign in' })
    ).toBeEnabled()
    await page.getByLabel('Username', { exact: true }).fill('corrected-user')
    await page.getByRole('button', { name: 'Register', exact: true }).click()
    await expect(
      page.getByRole('button', { name: 'Sign in with UniHub', exact: true })
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
      page.getByRole('button', { name: 'Retry original registration' })
    ).toBeEnabled()
    await expect(page.getByLabel('Username', { exact: true })).toBeDisabled()
    await expect(
      page.getByRole('button', { name: 'Back to sign in' })
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
          })
    )
    await page.route('**/personal-a/usage/summary', (route) =>
      route.request().method() === 'OPTIONS'
        ? route.fallback()
        : route.fulfill({
            headers,
            json: {
              scope: {
                account_id: 'personal-a',
                origin_product_id: 'llmrouter',
                visibility: 'account',
              },
              calls_total: settled ? '1' : '0',
              pending_calls: '0',
              financial_details: true,
              charges_by_asset: { 'CREDIT/6': settled ? '20' : '0' },
              as_of: 'now',
            },
          })
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
      page.getByText('Spendable credits: 3.000000 CREDIT', { exact: false })
    ).toBeVisible()
    await page
      .getByLabel('Funding mode', { exact: true })
      .selectOption('prepaid_cash')
    await page.getByLabel('Message', { exact: true }).fill('Hello')
    await page.getByRole('button', { name: 'Send new task' }).click()
    if (manual) {
      await expect(
        page.getByText('Task status: RESERVED', { exact: false })
      ).toBeVisible()
      await page.getByRole('button', { name: 'Query task receipt' }).click()
    }
    await expect(
      page.getByText('Task status: SETTLED', { exact: false })
    ).toBeVisible()
    await expect(
      page.getByText('Spendable credits: 2.999980 CREDIT', { exact: false })
    ).toBeVisible()
    await expect(page.getByText('Calls: 1', { exact: false })).toBeVisible()
    await expect(
      page.getByText('Charged: 0.000020 CREDIT', { exact: true })
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
    kind = 'membership'
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
      : route.fulfill({ headers, json: offers })
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
        })
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
        })
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
        })
  )
  await login(page)
  await expect(page.getByRole('button', { name: 'Quote upgrade' })).toHaveCount(
    1
  )
  await page
    .getByRole('article')
    .filter({ has: page.getByRole('heading', { name: 'higher', exact: true }) })
    .getByRole('button', { name: 'Quote upgrade' })
    .click()
  await expect(
    page.getByText('Upgrade quote: upgrade-quote', { exact: false })
  ).toBeVisible()
  await page
    .getByRole('button', { name: 'Accept quote and create order' })
    .click()
  await expect(page.getByText('upgrade-order', { exact: true })).toBeVisible()
  await expect(
    page.getByRole('button', { name: 'Accept quote and create order' })
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
    page.getByText('Finance actions require owner or billing_admin role.')
  ).toBeVisible()
  release()
  await completed
  await expect(
    page.getByText('Task status: SETTLED', { exact: false })
  ).toHaveCount(0)
  expect(newPayerReads).toBe(1)
})

test('public sign-in keeps the requested original wallet page', async ({
  page,
}) => {
  await fixture(page)
  await page.goto('/sign-in?redirect=%2Fwallet')
  await expect(page).toHaveURL(/\/wallet$/)
  await expect(
    page.getByRole('heading', { name: 'Wallet', exact: true })
  ).toBeVisible()
})
test('external redirect is rejected and public SSO retains account session', async ({
  page,
}) => {
  await fixture(page)
  await page.goto('/unihub?redirect=https%3A%2F%2Fevil.test%2Fsteal')
  await expect(page).toHaveURL(/\/dashboard\/overview$/)
  await expect(
    page.getByRole('heading', { name: 'Overview', exact: true })
  ).toBeVisible()
  await expect(page.getByLabel('Password', { exact: true })).toHaveCount(0)
})
test('immutable subject mismatch clears unusable proof and returns to the original login form', async ({
  page,
}) => {
  await fixture(page, { mismatch: true })
  await page.goto('/unihub')
  await expect(
    page.getByRole('heading', { name: 'Sign in', exact: true })
  ).toBeVisible()
  await expect(page.getByText('Product user · immutable-user')).toHaveCount(0)
  await expect(page).toHaveURL(/unihub_silent=done/)
})
test('closed model gate shows unavailable state and disables provider send', async ({
  page,
}) => {
  await fixture(page, { models: [] })
  await login(page)
  await expect(
    page.getByText('No available models', { exact: true })
  ).toBeVisible()
  await expect(
    page.getByRole('button', { name: 'Send new task' })
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
      'Too many registration attempts'
    )
    await expect(page.getByLabel('Username', { exact: true })).toBeEnabled({
      enabled: safe,
    })
    await expect(
      page.getByRole('button', { name: 'Back to sign in' })
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
          new Event('submit', { bubbles: true, cancelable: true })
        )
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
        page.getByRole('button', { name: 'Sign in with UniHub', exact: true })
      ).toBeEnabled()
    }
  })
}

test('product role and capabilities preserve channel reader controls', async ({
  page,
}) => {
  await fixture(page, { permissions: ['channel:read'] })
  await page.goto('/channels')
  await expect(
    page.getByRole('heading', { name: 'Channels', exact: true })
  ).toBeVisible()
  await expect(
    page.getByRole('button', { name: 'Add Channel', exact: true })
  ).toHaveCount(0)
  await expect(
    page.locator('[data-slot="sidebar"] a[href="/wallet"]')
  ).toBeVisible()
})

test('ordinary product user cannot open channels', async ({ page }) => {
  await fixture(page)
  await login(page)
  await page.goto('/channels')
  await expect(page).toHaveURL(/\/403$/)
})

test('product integration preserves the original public homepage', async ({
  page,
}) => {
  await fixture(page)
  await page.goto('/')
  await expect(page).toHaveURL(/:58004\/$/)
  await expect(page.locator('a[href="/sign-in"]').first()).toBeVisible()
})

test('restored product session keeps the original homepage after reload', async ({
  page,
}) => {
  await fixture(page)
  await login(page)
  await page.goto('/')
  await expect(page).toHaveURL(/:58004\/$/)
  await page.reload()
  await expect(page).toHaveURL(/:58004\/$/)
})

test('authenticated sign-in honors the original wallet and channel destinations', async ({
  page,
}) => {
  await fixture(page, { permissions: ['channel:read'] })
  await login(page)
  await page.goto('/sign-in?redirect=%2Fwallet')
  await expect(page).toHaveURL(/\/wallet$/)
  await expect(
    page.getByRole('heading', { name: 'Wallet', exact: true })
  ).toBeVisible()
  await page.goto('/sign-in?redirect=%2Fchannels%3Ffilter%3Dsentinel')
  await expect(page).toHaveURL(/\/channels\?filter=sentinel$/)
  await expect(
    page.getByRole('heading', { name: 'Channels', exact: true })
  ).toBeVisible()
})

test('interactive sign-in failure returns to UniHub without an automatic redirect loop', async ({
  page,
}) => {
  await fixture(page)
  let attempts = 0
  await page.route('**/api/unihub/login?*', async (route) => {
    attempts++
    return route.fulfill({
      status: 303,
      headers: { Location: '/unihub?sso_error=1&redirect=%2Fdashboard' },
    })
  })
  await page.goto('/unihub?signed_out=1')
  await page
    .getByRole('button', { name: 'Sign in with UniHub', exact: true })
    .click()
  await expect(page).toHaveURL(/\/unihub\?.*sso_error=1/)
  await expect(
    page.getByText('UniHub sign in failed. Try again to continue.')
  ).toBeVisible()
  await expect(
    page.getByRole('button', { name: 'Register UniHub account', exact: true })
  ).toBeVisible()
  expect(attempts).toBe(1)
})

test('central console uses the selected payer for keys, wallet and overview', async ({
  page,
}) => {
  await fixture(page)
  const seen: string[] = []
  await page.route('**/api/unihub/bff/v1/accounts/*/api-keys*', (route) => {
    const url = new URL(route.request().url())
    seen.push(url.pathname)
    const team = url.pathname.includes('/team-b/')
    return route.fulfill({
      json: [
        {
          id: team ? 'team-key' : 'personal-key',
          name: team ? 'Team key' : 'Personal key',
          credential_kind: 'api_key',
          actor_id: 'immutable-user',
          account_id: team ? 'team-b' : 'personal-a',
          origin_product_id: 'llmrouter',
          instance_id: 'default',
          admission_mode: 'metered_v2',
          models: ['qwen3-rerank'],
          funding_mode: 'prepaid_cash',
          asset: 'CNY/6',
          created_at: '2026-01-01T00:00:00Z',
          max_total: '',
          max_per_call: '',
          held: '0',
          used: '0',
        },
      ],
    })
  })
  await page.route('**/api/unihub/bff/v1/accounts/*/resources', (route) => {
    const team = route.request().url().includes('/team-b/')
    return route.fulfill({
      json: {
        account_id: team ? 'team-b' : 'personal-a',
        product_id: 'llmrouter',
        subscriptions: [],
        wallets: [
          {
            product_id: 'llmrouter',
            asset: 'CNY/6',
            amount: team ? '2000000' : '10000000',
          },
        ],
        payments: { real_payments_configured: false },
        billing_profile: {
          addons: { enabled: true },
          subscription: { enabled: false },
        },
      },
    })
  })
  await login(page)
  await page.goto('/keys')
  await expect(page.getByText('Personal key', { exact: true })).toBeVisible()
  await page.getByRole('combobox').first().selectOption('team-b')
  await expect(page.getByText('Team key', { exact: true })).toBeVisible()
  await expect(page.getByText('Personal key', { exact: true })).toHaveCount(0)
  await page.goto('/wallet')
  await page.getByRole('combobox').first().selectOption('team-b')
  await expect(page.getByText('2.000000 CNY', { exact: false })).toBeVisible()
  await expect(page.getByText('10.000000 CNY', { exact: false })).toHaveCount(0)
  await page.getByRole('combobox').first().selectOption('personal-a')
  await expect(page.getByText('10.000000 CNY', { exact: false })).toBeVisible()
  await expect(page.getByText('2.000000 CNY', { exact: false })).toHaveCount(0)
  await page.goto('/dashboard')
  await expect(page.getByText('10.000000 CNY', { exact: false })).toBeVisible()
  await expect(page.getByText('Personal key', { exact: true })).toBeVisible()
  await expect(page.getByText(/qwen3-rerank/)).toHaveCount(0)
  expect(seen).toContain('/api/unihub/bff/v1/accounts/personal-a/api-keys')
  expect(seen).toContain('/api/unihub/bff/v1/accounts/team-b/api-keys')
})

test('overview suggests only an active key with an explicitly authorized chat model', async ({
  page,
}) => {
  await fixture(page, { models: ['rerank', 'expired-chat', 'active-chat'] })
  await page.route('**/api/unihub/chat-models', (route) =>
    route.fulfill({
      json: {
        product_id: 'llmrouter',
        instance_id: 'default',
        admission_mode: 'metered_v2',
        models: ['expired-chat', 'active-chat'].map((id) => ({
          id,
          policy: 'sum',
          rate_ids: { 'CNY/6': id },
        })),
      },
    })
  )
  await page.route('**/api/unihub/bff/v1/accounts/*/api-keys*', (route) => {
    const base = {
      credential_kind: 'api_key',
      actor_id: 'immutable-user',
      account_id: 'personal-a',
      origin_product_id: 'llmrouter',
      instance_id: 'default',
      funding_mode: 'prepaid_cash',
      asset: 'CNY/6',
      created_at: '2026-01-01T00:00:00Z',
      max_total: '',
      max_per_call: '',
      held: '0',
      used: '0',
    }
    return route.fulfill({
      json: [
        {
          ...base,
          id: 'rerank',
          name: 'Rerank',
          models: ['rerank'],
        },
        {
          ...base,
          id: 'expired',
          name: 'Expired',
          models: ['expired-chat'],
          expires_at: '2000-01-01T00:00:00Z',
        },
        {
          ...base,
          id: 'active',
          name: 'Active',
          models: ['active-chat'],
        },
      ],
    })
  })
  await login(page)
  await page.goto('/dashboard')
  const example = page.locator('pre code')
  await expect(example).toContainText('"model":"active-chat"')
  await expect(example).not.toContainText('expired-chat')
  await expect(example).not.toContainText('rerank')
  await expect(example).toContainText('YOUR_API_KEY')
})

test('task chat requires an explicit active chat capability and excludes rerank', async ({
  page,
}) => {
  await fixture(page, { models: ['rerank', 'chat'] })
  let hasChat = false
  let issued = 0
  await page.route('**/api/unihub/chat-models', (route) =>
    route.fulfill({
      json: {
        product_id: 'llmrouter',
        instance_id: 'default',
        admission_mode: 'metered_v2',
        models: hasChat
          ? [{ id: 'chat', policy: 'sum', rate_ids: { 'CNY/6': 'chat' } }]
          : [],
      },
    })
  )
  await page.route(
    '**/api/unihub/bff/v1/accounts/*/model-credentials',
    (route) => {
      issued++
      return route.fulfill({ status: 500 })
    }
  )
  await login(page)
  await expect(
    page.getByText('No available models', { exact: true })
  ).toBeVisible()
  await expect(
    page.getByRole('button', { name: 'Send new task' })
  ).toBeDisabled()
  expect(issued).toBe(0)
  hasChat = true
  await page.reload()
  await expect(
    page.getByLabel('Model', { exact: true }).locator('option')
  ).toHaveText(['chat'])
  await expect(
    page.getByRole('button', { name: 'Send new task' })
  ).toBeEnabled()
  expect(issued).toBe(0)
})

test('chat suggestions reject mismatched rate, asset, payer keys and missing metadata', async ({
  page,
}) => {
  await fixture(page, { models: ['chat'] })
  let payer = 'personal-a'
  await page.route('**/api/unihub/chat-models', (route) =>
    route.fulfill({
      json: {
        product_id: 'llmrouter',
        instance_id: 'default',
        admission_mode: 'metered_v2',
        models: [
          { id: 'chat', policy: 'sum', rate_ids: { 'CNY/6': 'wrong-pin' } },
        ],
      },
    })
  )
  await page.route('**/api/unihub/bff/v1/accounts/*/api-keys*', (route) => {
    payer = route.request().url().includes('/team-b/') ? 'team-b' : 'personal-a'
    return route.fulfill({
      json: [
        {
          id: 'own',
          credential_kind: 'api_key',
          name: 'Own key',
          actor_id: 'immutable-user',
          account_id: payer,
          origin_product_id: 'llmrouter',
          instance_id: 'default',
          admission_mode: 'metered_v2',
          models: ['chat'],
          funding_mode: 'prepaid_cash',
          asset: 'CNY/6',
          billing_profile_version: 1,
          created_at: '2026-01-01T00:00:00Z',
          expires_at: null,
          revoked_at: null,
          max_total: '',
          max_per_call: '',
          held: '0',
          used: '0',
          remaining: null,
        },
        {
          id: 'other',
          credential_kind: 'api_key',
          name: 'Other user key',
          actor_id: 'other-user',
          account_id: payer,
          origin_product_id: 'llmrouter',
          instance_id: 'default',
          admission_mode: 'metered_v2',
          models: ['chat'],
          funding_mode: 'prepaid_cash',
          asset: 'CNY/6',
          billing_profile_version: 1,
          created_at: '2026-01-01T00:00:00Z',
          expires_at: null,
          revoked_at: null,
          max_total: '',
          max_per_call: '',
          held: '0',
          used: '0',
          remaining: null,
        },
      ],
    })
  })
  await login(page)
  await expect(
    page.getByRole('button', { name: 'Send new task' })
  ).toBeDisabled()
  await page.goto('/keys')
  await expect(page.getByText('Own key', { exact: true })).toBeVisible()
  await expect(page.getByText('Other user key', { exact: true })).toBeVisible()
  await page.goto('/dashboard')
  await expect(page.locator('pre code')).toHaveCount(0)
  await page.getByRole('combobox').first().selectOption('team-b')
  await expect(page.getByText('Own key', { exact: true })).toBeVisible()
  await expect(page.getByText('Other user key', { exact: true })).toHaveCount(0)
  await expect(page.locator('pre code')).toHaveCount(0)
})

test('late capability response from the previous payer cannot leak after switch', async ({
  page,
}) => {
  await fixture(page, { models: ['personal-chat', 'team-chat'] })
  let requests = 0
  let releaseFirst!: () => void
  const first = new Promise<void>((resolve) => {
    releaseFirst = resolve
  })
  await page.route('**/api/unihub/chat-models', async (route) => {
    requests++
    const id = requests === 1 ? 'personal-chat' : 'team-chat'
    if (requests === 1) await first
    await route
      .fulfill({
        json: {
          product_id: 'llmrouter',
          instance_id: 'default',
          admission_mode: 'metered_v2',
          models: [{ id, policy: 'sum', rate_ids: { 'CNY/6': id } }],
        },
      })
      .catch(() => {})
  })
  await login(page)
  await expect.poll(() => requests).toBe(1)
  await page.getByRole('combobox').first().selectOption('team-b')
  await expect(
    page.getByLabel('Model', { exact: true }).locator('option')
  ).toHaveText(['team-chat'])
  releaseFirst()
  await expect(
    page.getByLabel('Model', { exact: true }).locator('option')
  ).toHaveText(['team-chat'])
})

test('key management keeps finance roles authorized team keys and respects payer scope', async ({
  page,
}) => {
  await fixture(page, { models: ['chat'] })
  let role = 'owner'
  await page.route('**/api/unihub/bff/v1/accounts', (route) =>
    route.fulfill({
      json: [
        { id: 'personal-a', name: 'Personal A', kind: 'personal', role },
        { id: 'team-b', name: 'Team B', kind: 'team', role: 'member' },
      ],
    })
  )
  const key = {
    credential_kind: 'api_key',
    account_id: 'personal-a',
    origin_product_id: 'llmrouter',
    instance_id: 'default',
    models: ['chat'],
    funding_mode: 'prepaid_cash',
    asset: 'CNY/6',
    billing_profile_version: 1,
    created_at: '2026-01-01T00:00:00Z',
    expires_at: null,
    revoked_at: null,
    max_total: '',
    max_per_call: '',
    held: '0',
    used: '0',
    remaining: null,
  }
  await page.route('**/api/unihub/bff/v1/accounts/*/api-keys*', (route) =>
    route.fulfill({
      json: [
        {
          ...key,
          id: 'own',
          actor_id: 'immutable-user',
          name: 'Own key',
          operations: { chat: ['chat.completions.create'] },
        },
        { ...key, id: 'other', actor_id: 'other', name: 'Other key' },
        {
          ...key,
          id: 'wrong-account',
          actor_id: 'other',
          account_id: 'team-b',
          name: 'Wrong payer key',
        },
        {
          ...key,
          id: 'wrong-product',
          actor_id: 'other',
          origin_product_id: 'elsewhere',
          name: 'Wrong product key',
        },
        {
          ...key,
          id: 'wrong-instance',
          actor_id: 'other',
          instance_id: 'elsewhere',
          name: 'Wrong instance key',
        },
      ],
    })
  )
  await login(page)
  await page.goto('/keys')
  await expect(page.getByText('Other key', { exact: true })).toBeVisible()
  await expect(page.getByText('Wrong payer key', { exact: true })).toHaveCount(
    0
  )
  await expect(
    page.getByText('Wrong product key', { exact: true })
  ).toHaveCount(0)
  await expect(
    page.getByText('Wrong instance key', { exact: true })
  ).toHaveCount(0)
  const own = page.getByRole('row').filter({ hasText: 'Own key' })
  await own.locator('summary').click()
  await expect(own.getByText('chat: chat.completions.create')).toBeVisible()
  const other = page.getByRole('row').filter({ hasText: 'Other key' })
  await other.locator('summary').click()
  await expect(
    other.getByText(
      'Capabilities are checked against current published rates when the key is used.'
    )
  ).toBeVisible()
  role = 'billing_admin'
  await page.getByRole('button', { name: 'Refresh accounts and roles' }).click()
  await expect(page.getByText('Other key', { exact: true })).toBeVisible()
  role = 'member'
  await page.getByRole('button', { name: 'Refresh accounts and roles' }).click()
  await expect(page.getByText('Own key', { exact: true })).toBeVisible()
  await expect(page.getByText('Other key', { exact: true })).toHaveCount(0)
})

test('overview honors an explicit key wire restriction while accepting the real DTO without operations', async ({
  page,
}) => {
  await fixture(page, { models: ['chat'] })
  let restricted = true
  await page.route('**/api/unihub/bff/v1/accounts/*/api-keys*', (route) =>
    route.fulfill({
      json: [
        {
          id: 'own',
          credential_kind: 'api_key',
          name: 'Own key',
          actor_id: 'immutable-user',
          account_id: 'personal-a',
          origin_product_id: 'llmrouter',
          instance_id: 'default',
          admission_mode: 'metered_v2',
          models: ['chat'],
          funding_mode: 'prepaid_cash',
          asset: 'CNY/6',
          billing_profile_version: 1,
          created_at: '2026-01-01T00:00:00Z',
          expires_at: null,
          revoked_at: null,
          max_total: '',
          max_per_call: '',
          held: '0',
          used: '0',
          remaining: null,
          ...(restricted
            ? { operations: { chat: ['embeddings.create'] } }
            : {}),
        },
      ],
    })
  )
  await login(page)
  await page.goto('/dashboard')
  await expect(
    page.getByText('No available models', { exact: true })
  ).toBeVisible()
  await expect(page.locator('pre code')).toHaveCount(0)
  restricted = false
  await page.reload()
  await expect(page.locator('pre code')).toContainText('"model":"chat"')
})

test('legacy admits exclusive rate and metered mode rejects the same rate', async ({
  page,
}) => {
  await fixture(page, { models: ['chat'] })
  let mode = 'legacy'
  await page.route('**/api/unihub/chat-models', (route) =>
    route.fulfill({
      json: {
        product_id: 'llmrouter',
        instance_id: 'default',
        admission_mode: mode,
        models: [{ id: 'chat', rate_ids: {} }],
      },
    })
  )
  await page.route(
    '**/api/unihub/bff/v1/products/*/model-consistency?*',
    (route) =>
      route.fulfill({
        json: {
          product_id: 'llmrouter',
          asset: 'CNY/6',
          allowlist: ['chat'],
          duplicate_models: [],
          published_rates: [
            {
              id: 'old',
              model: 'chat',
              status: 'published',
              asset: 'CNY/6',
              semantics: 'exclusive_v1',
              input: { numerator: '1', denominator: '1' },
              output: { numerator: '1', denominator: '1' },
              cache_read: { numerator: '1', denominator: '1' },
              cache_write: { numerator: '1', denominator: '1' },
              reasoning: { numerator: '1', denominator: '1' },
            },
          ],
        },
      })
  )
  await login(page)
  await expect(
    page.getByRole('button', { name: 'Send new task' })
  ).toBeEnabled()
  mode = 'metered_v2'
  await page.getByRole('button', { name: 'Refresh accounts and roles' }).click()
  await expect(
    page.getByRole('button', { name: 'Send new task' })
  ).toBeDisabled()
})

test('capability refresh failure removes cached suggestions and disables task sending', async ({
  page,
}) => {
  await fixture(page, { models: ['chat'] })
  let failed = false
  await page.route('**/api/unihub/chat-models', (route) =>
    failed
      ? route.fulfill({ status: 503 })
      : route.fulfill({
          json: {
            product_id: 'llmrouter',
            instance_id: 'default',
            admission_mode: 'metered_v2',
            models: [
              { id: 'chat', policy: 'sum', rate_ids: { 'CNY/6': 'chat' } },
            ],
          },
        })
  )
  await login(page)
  await expect(
    page.getByRole('button', { name: 'Send new task' })
  ).toBeEnabled()
  failed = true
  await page.getByRole('button', { name: 'Refresh accounts and roles' }).click()
  await expect(
    page.getByRole('button', { name: 'Send new task' })
  ).toBeDisabled()
  await expect(
    page.getByLabel('Model', { exact: true }).locator('option')
  ).toHaveCount(0)
})

test('funding mode changes query the selected payer and matching asset', async ({
  page,
}) => {
  await fixture(page, { models: ['chat'] })
  await page.route('**/api/unihub/chat-models', (route) =>
    route.fulfill({
      json: {
        product_id: 'llmrouter',
        instance_id: 'default',
        admission_mode: 'metered_v2',
        models: [
          {
            id: 'chat',
            policy: 'sum',
            rate_ids: { 'CNY/6': 'chat', 'CREDIT/6': 'chat' },
          },
        ],
      },
    })
  )
  await page.route('**/api/unihub/bff/v1/products/*/billing-profile', (route) =>
    route.fulfill({
      json: {
        usage: { enabled: true, funding_modes: ['prepaid_cash', 'credits'] },
        addons: { enabled: true },
        subscription: { enabled: false },
      },
    })
  )
  const checks: string[] = []
  await page.route(
    '**/api/unihub/bff/v1/products/*/model-consistency?*',
    (route) => {
      const p = new URL(route.request().url()).searchParams
      checks.push(`${p.get('account_id')}:${p.get('funding_mode')}`)
      const asset = p.get('funding_mode') === 'credits' ? 'CREDIT/6' : 'CNY/6'
      return route.fulfill({
        json: {
          product_id: 'llmrouter',
          asset,
          allowlist: ['chat'],
          duplicate_models: [],
          published_rates: [
            {
              id: 'chat',
              model: 'chat',
              status: 'published',
              asset,
              semantics: 'metered_v2',
              metered: {
                version: 'metered_v2',
                operation: 'chat',
                policy: 'sum',
                terms: [],
              },
            },
          ],
        },
      })
    }
  )
  await login(page)
  await page.getByLabel('Funding mode', { exact: true }).selectOption('credits')
  await expect(
    page.getByRole('button', { name: 'Send new task' })
  ).toBeEnabled()
  expect(checks).toContain('personal-a:credits')
  await page.getByRole('combobox').first().selectOption('team-b')
  await page.getByLabel('Funding mode', { exact: true }).selectOption('credits')
  await expect(
    page.getByRole('button', { name: 'Send new task' })
  ).toBeEnabled()
  expect(checks).toContain('team-b:credits')
})

test('legacy chat accepts only the supported per-request metered tariff and honors explicit empty allowlist', async ({
  page,
}) => {
  await fixture(page, { models: ['chat'] })
  let allow = true
  let token = false
  const requestPrice = {
    version: 'metered_v2',
    operation: 'chat',
    policy: 'sum',
    terms: [
      {
        key: 'requests',
        unit: 'request',
        price: { numerator: '73', denominator: '1' },
      },
    ],
  }
  await page.route('**/api/unihub/chat-models', (route) =>
    route.fulfill({
      json: {
        product_id: 'llmrouter',
        instance_id: 'default',
        admission_mode: 'legacy',
        models: [{ id: 'chat', rate_ids: {} }],
      },
    })
  )
  await page.route(
    '**/api/unihub/bff/v1/products/*/model-consistency?*',
    (route) =>
      route.fulfill({
        json: {
          product_id: 'llmrouter',
          asset: 'CNY/6',
          allowlist: allow ? ['chat'] : [],
          duplicate_models: [],
          published_rates: [
            {
              id: 'chat-rate',
              model: 'chat',
              status: 'published',
              asset: 'CNY/6',
              semantics: 'metered_v2',
              metered: token
                ? {
                    ...requestPrice,
                    terms: [
                      {
                        key: 'input',
                        unit: 'token',
                        price: { numerator: '1', denominator: '1' },
                      },
                    ],
                  }
                : requestPrice,
            },
          ],
        },
      })
  )
  await login(page)
  await expect(
    page.getByRole('button', { name: 'Send new task' })
  ).toBeEnabled()
  token = true
  await page.getByRole('button', { name: 'Refresh accounts and roles' }).click()
  await expect(
    page.getByRole('button', { name: 'Send new task' })
  ).toBeDisabled()
  token = false
  allow = false
  await page.getByRole('button', { name: 'Refresh accounts and roles' }).click()
  await expect(
    page.getByRole('button', { name: 'Send new task' })
  ).toBeDisabled()
})

test('metered chat requires the gateway usage policy to match the published central tariff', async ({
  page,
}) => {
  await fixture(page, { models: ['chat'] })
  let policy = 'provider_reported_v1'
  await page.route('**/api/unihub/chat-models', (route) =>
    route.fulfill({
      json: {
        product_id: 'llmrouter',
        instance_id: 'default',
        admission_mode: 'metered_v2',
        models: [{ id: 'chat', rate_ids: { 'CNY/6': 'chat' }, policy }],
      },
    })
  )
  await login(page)
  await expect(
    page.getByRole('button', { name: 'Send new task' })
  ).toBeDisabled()
  policy = 'sum'
  await page.getByRole('button', { name: 'Refresh accounts and roles' }).click()
  await expect(
    page.getByRole('button', { name: 'Send new task' })
  ).toBeEnabled()
})

test('overview hides cached key and example when key refresh fails', async ({
  page,
}) => {
  await fixture(page, { models: ['chat'] })
  let failed = false
  await page.route('**/api/unihub/bff/v1/accounts/*/api-keys*', (route) =>
    failed
      ? route.fulfill({ status: 503 })
      : route.fulfill({
          json: [
            {
              id: 'own',
              credential_kind: 'api_key',
              name: 'Own key',
              actor_id: 'immutable-user',
              account_id: 'personal-a',
              origin_product_id: 'llmrouter',
              instance_id: 'default',
              models: ['chat'],
              funding_mode: 'prepaid_cash',
              asset: 'CNY/6',
              billing_profile_version: 1,
              created_at: '2026-01-01T00:00:00Z',
              expires_at: null,
              revoked_at: null,
              max_total: '',
              max_per_call: '',
              held: '0',
              used: '0',
              remaining: null,
            },
          ],
        })
  )
  await login(page)
  await page.goto('/dashboard')
  await expect(page.locator('pre code')).toContainText('"model":"chat"')
  await expect(page.getByText('Own key', { exact: true })).toBeVisible()
  failed = true
  await page.getByRole('button', { name: 'Refresh accounts and roles' }).click()
  await expect(page.getByText('Own key', { exact: true })).toHaveCount(0)
  await expect(page.locator('pre code')).toHaveCount(0)
  await expect(page.getByRole('alert')).toBeVisible()
})

test('central usage pages use selected payer receipts and do not carry another payer data across switches', async ({
  page,
}) => {
  await fixture(page)
  await page.route('**/api/log/self?*', (route) =>
    route.fulfill({
      json: {
        success: true,
        data: { items: [], total: 0, page: 1, page_size: 10 },
      },
    })
  )
  await page.route('**/api/native-billing/self', (route) =>
    route.fulfill({ json: { success: true, data: [] } })
  )
  await page.route('**/api/log/self/stat?*', (route) =>
    route.fulfill({
      json: { success: true, data: { quota: 0, rpm: 0, tpm: 0 } },
    })
  )
  await page.route('**/api/unihub/bff/v1/accounts/*/model-pool/*', (route) =>
    route.fulfill({ status: 403 })
  )
  await page.route('**/api/unihub/bff/v1/accounts/*/usage/summary', (route) => {
    const account = route.request().url().includes('/team-b/')
      ? 'team-b'
      : 'personal-a'
    return route.fulfill({
      json: {
        scope: {
          account_id: account,
          origin_product_id: 'llmrouter',
          visibility: account === 'team-b' ? 'actor' : 'account',
          ...(account === 'team-b' ? { actor_id: 'immutable-user' } : {}),
        },
        calls_total: '1',
        pending_calls: '0',
        financial_details: false,
        accuracy: 'ledger',
        consistency: 'snapshot',
        as_of: 'now',
      },
    })
  })
  await page.route('**/api/unihub/bff/v1/accounts/*/calls', (route) => {
    const account = route.request().url().includes('/team-b/')
      ? 'team-b'
      : 'personal-a'
    return route.fulfill({
      json: {
        scope: {
          account_id: account,
          origin_product_id: 'llmrouter',
          visibility: account === 'team-b' ? 'actor' : 'account',
          ...(account === 'team-b' ? { actor_id: 'immutable-user' } : {}),
        },
        items: [
          {
            call_id: `${account}-call`,
            account_id: account,
            actor_id: 'immutable-user',
            origin_product_id: 'llmrouter',
            model: 'verified-chat',
            status: 'SETTLED',
            asset: 'CNY/6',
            financial_details: false,
          },
        ],
        has_more: false,
      },
    })
  })
  await login(page)
  await page.goto('/usage-logs/common')
  await expect(page.getByText(/personal-a-call/)).toBeVisible()
  await page.getByRole('combobox').first().selectOption('team-b')
  await expect(page.getByText(/team-b-call/)).toBeVisible()
  await expect(page.getByText(/personal-a-call/)).toHaveCount(0)
  await page.getByRole('button', { name: 'Local · Common Logs' }).click()
  await expect(
    page.getByText(
      'For UniHub accounts, local log date ranges must not exceed 30 days.'
    )
  ).toBeVisible()
  await expect(
    page.getByRole('button', { name: 'UniHub account' })
  ).toBeVisible()
  await page.getByRole('button', { name: 'UniHub account' }).click()
  await expect(page.getByText(/team-b-call/)).toBeVisible()
  await page.goto('/dashboard')
  await expect(page.getByText(/personal-a-call/)).toBeVisible()
  await expect(page.getByText(/team-b-call/)).toHaveCount(0)
})

test('usage scopes require matching payer and actor; a failed refresh hides cached receipts', async ({
  page,
}) => {
  await fixture(page)
  let denied = false
  let badMemberScope = false
  const scope = (account: string) => ({
    account_id: account,
    origin_product_id: 'llmrouter',
    visibility: account === 'team-b' && !badMemberScope ? 'actor' : 'account',
    ...(account === 'team-b' && !badMemberScope
      ? { actor_id: 'immutable-user' }
      : {}),
  })
  await page.route('**/api/unihub/bff/v1/accounts/*/model-pool/*', (route) =>
    route.fulfill({ status: 403 })
  )
  await page.route('**/api/unihub/bff/v1/accounts/*/usage/summary', (route) => {
    if (denied) return route.fulfill({ status: 403 })
    const account = route.request().url().includes('/team-b/')
      ? 'team-b'
      : 'personal-a'
    return route.fulfill({
      json: {
        scope: scope(account),
        calls_total: '1',
        pending_calls: '0',
        financial_details: false,
        as_of: 'now',
      },
    })
  })
  await page.route('**/api/unihub/bff/v1/accounts/*/calls', (route) => {
    if (denied) return route.fulfill({ status: 403 })
    const account = route.request().url().includes('/team-b/')
      ? 'team-b'
      : 'personal-a'
    return route.fulfill({
      json: {
        scope: scope(account),
        items: [
          {
            call_id: `${account}-call`,
            account_id: account,
            actor_id: 'immutable-user',
            origin_product_id: 'llmrouter',
            model: 'verified-chat',
            status: 'SETTLED',
            created_at: '2026-01-01T00:00:00Z',
            asset: 'CNY/6',
            financial_details: false,
          },
        ],
        has_more: false,
      },
    })
  })
  await login(page)
  await page.goto('/usage-logs/common')
  await expect(
    page.getByText(/personal-a-call.*immutable-user.*verified-chat.*SETTLED/)
  ).toBeVisible()
  denied = true
  await page.getByRole('button', { name: 'Refresh accounts and roles' }).click()
  await expect(page.getByText(/personal-a-call/)).toHaveCount(0)
  await expect(page.getByRole('alert')).toBeVisible()
  denied = false
  badMemberScope = true
  await page.getByRole('button', { name: 'Refresh accounts and roles' }).click()
  await page.getByRole('combobox').first().selectOption('team-b')
  await expect(page.getByRole('alert')).toBeVisible()
  await expect(page.getByText(/team-b-call/)).toHaveCount(0)
})

test('overview clears old balances when resources refresh fails', async ({
  page,
}) => {
  await fixture(page)
  let failed = false
  await page.route('**/api/unihub/bff/v1/accounts/*/resources', (route) =>
    failed
      ? route.fulfill({ status: 503 })
      : route.fulfill({
          json: {
            account_id: 'personal-a',
            product_id: 'llmrouter',
            subscriptions: [],
            wallets: [
              { product_id: 'llmrouter', asset: 'CNY/6', amount: '1234567' },
            ],
            credits: {
              asset: 'CREDIT/6',
              pool: 'llmrouter',
              spendable: '7654321',
              reserved: '0',
              frozen: '0',
            },
            payments: { real_payments_configured: false },
          },
        })
  )
  await login(page)
  await page.goto('/dashboard')
  await expect(page.getByText(/1\.234567 CNY/)).toBeVisible()
  await expect(page.getByText(/7\.654321 CREDIT/)).toBeVisible()
  failed = true
  await page.getByRole('button', { name: 'Refresh accounts and roles' }).click()
  await expect(page.getByText(/1\.234567 CNY/)).toHaveCount(0)
  await expect(page.getByText(/7\.654321 CREDIT/)).toHaveCount(0)
  await expect(page.getByRole('alert')).toBeVisible()
})

test('runtime browser SSO reports account access unavailable without dropping the session', async ({
  page,
}) => {
  await fixture(page)
  await page.route('**/api/unihub/config', (route) =>
    route.fulfill({
      json: {
        enabled: false,
        browser_sso_enabled: true,
        native_access: false,
        public_origin: 'http://127.0.0.1:58006',
        client_id: 'client',
        redirect_uri: 'http://127.0.0.1:58004/api/unihub/callback',
        product_id: 'llmrouter',
        instance_id: 'default',
        models: ['synthetic'],
      },
    })
  )
  await page.goto('/api/unihub/login?return_to=%2Fkeys')
  await expect(
    page.getByText(
      'UniHub account access is unavailable for this browser session. Contact the administrator.'
    )
  ).toBeVisible()
  await expect(
    page.getByText('UniHub product integration is disabled by this deployment.')
  ).toHaveCount(0)
  await expect(page).toHaveURL(/\/keys$/)
})

test('runtime broker session is not cleared when legacy integration is also enabled', async ({
  page,
}) => {
  await fixture(page)
  const now = Math.floor(Date.now() / 1000)
  await page.route('**/api/user/auth/refresh', (route) =>
    route.fulfill({
      json: {
        success: true,
        data: {
          access_token: 'native-session-token',
          token_type: 'Bearer',
          access_expires_at: now + 300,
          user: {
            id: 42,
            username: 'product-user',
            display_name: 'Product user',
            role: 1,
            identity_source: 'unihub',
            unihub_user_id: 'immutable-user',
            permissions: { admin_permissions: { product: { use: true } } },
          },
          session: {
            sid: 'sid-broker',
            current: true,
            login_method: 'product_broker',
            ip: '',
            user_agent: 'fixture',
            created_at: now,
            last_active_at: now,
            expires_at: now + 300,
          },
        },
      },
    })
  )
  let bffCalls = 0
  await page.route('**/api/unihub/bff/**', (route) => {
    bffCalls++
    return route.fulfill({ status: 401 })
  })
  await page.goto('/api/unihub/login?return_to=%2Fkeys')
  await expect(
    page.getByText(
      'UniHub account access is unavailable for this browser session. Contact the administrator.'
    )
  ).toBeVisible()
  expect(bffCalls).toBe(0)
  await expect(page).toHaveURL(/\/keys$/)
})

test('sandbox settlement requires explicit action and server reported sandbox mode', async ({
  page,
}) => {
  await fixture(page)
  let settlements = 0
  await page.route('**/personal-a/resources', (route) =>
    route.fulfill({
      json: {
        account_id: 'personal-a',
        product_id: 'llmrouter',
        subscriptions: [],
        wallets: [],
        payments: { mode: 'sandbox', real_payments_configured: false },
      },
    })
  )
  await page.route('**/personal-a/recharges', (route) =>
    route.fulfill({
      json: {
        id: 'sandbox-recharge',
        account_id: 'personal-a',
        product_id: 'llmrouter',
        amount: '1000000',
        currency: 'CNY',
        merchant: 'sandbox',
      },
    })
  )
  await page.route(
    '**/personal-a/recharges/sandbox-recharge/sandbox-settle',
    (route) => {
      settlements++
      return route.fulfill({
        status: 202,
        json: {
          id: 'sandbox-operation',
          account_id: 'personal-a',
          kind: 'recharge',
          status: 'APPLIED',
        },
      })
    }
  )
  await login(page)
  await page.getByLabel('Recharge amount (CNY)').fill('1')
  await page
    .getByRole('button', { name: 'Create recharge', exact: true })
    .click()
  await expect(
    page.getByText('sandbox-recharge', { exact: true })
  ).toBeVisible()
  const settle = page.getByRole('button', {
    name: 'Settle sandbox recharge',
    exact: true,
  })
  await expect(settle).toBeVisible()
  expect(settlements).toBe(0)
  await settle.click()
  await expect(
    page.getByText('sandbox-operation', { exact: true })
  ).toBeVisible()
  expect(settlements).toBe(1)
})

test('sandbox settlement stays hidden when server payment mode is not sandbox', async ({
  page,
}) => {
  await fixture(page)
  await page.route('**/personal-a/resources', (route) =>
    route.fulfill({
      json: {
        account_id: 'personal-a',
        product_id: 'llmrouter',
        subscriptions: [],
        wallets: [],
        payments: { mode: 'unconfigured', real_payments_configured: false },
      },
    })
  )
  await page.route('**/personal-a/recharges', (route) =>
    route.fulfill({
      json: {
        id: 'unconfigured-recharge',
        account_id: 'personal-a',
        product_id: 'llmrouter',
        amount: '1000000',
        currency: 'CNY',
        merchant: 'sandbox',
      },
    })
  )
  await login(page)
  await page.getByLabel('Recharge amount (CNY)').fill('1')
  await page
    .getByRole('button', { name: 'Create recharge', exact: true })
    .click()
  await expect(
    page.getByText('unconfigured-recharge', { exact: true })
  ).toBeVisible()
  await expect(
    page.getByRole('button', { name: 'Settle sandbox recharge' })
  ).toHaveCount(0)
})

for (const failedEndpoint of [
  'resources',
  'catalog',
  'orders',
  'operations',
] as const) {
  test(`wallet hides ${failedEndpoint} data after a background refresh fails`, async ({
    page,
  }) => {
    await fixture(page)
    let failed = false
    await page.route('**/api/unihub/bff/v1/**', async (route) => {
      const path = new URL(route.request().url()).pathname
      if (failed && path.endsWith(`/${failedEndpoint}`)) {
        return route.fulfill({ status: 503 })
      }
      if (path.endsWith('/resources')) {
        return route.fulfill({
          json: {
            account_id: 'personal-a',
            product_id: 'llmrouter',
            subscriptions: [],
            wallets: [
              { product_id: 'llmrouter', asset: 'CNY/6', amount: '1234567' },
            ],
            payments: { mode: 'sandbox', real_payments_configured: false },
            billing_profile: {
              addons: { enabled: true },
              subscription: { enabled: false },
            },
          },
        })
      }
      if (path.endsWith('/catalog')) {
        return route.fulfill({
          json: [
            {
              id: 'addon',
              name: 'Published plan',
              account_kind: 'both',
              product_id: 'llmrouter',
              status: 'published',
              kind: 'addon',
              price: '1000000',
              monthly_credit: '1000000',
            },
          ],
        })
      }
      if (path.endsWith('/orders')) {
        return route.fulfill({
          json: [
            {
              id: 'order-visible',
              amount: '1000000',
              payment_status: 'PENDING',
              fulfillment_status: 'PENDING',
            },
          ],
        })
      }
      if (path.endsWith('/operations')) {
        return route.fulfill({
          json: [
            { id: 'operation-visible', kind: 'recharge', status: 'PENDING' },
          ],
        })
      }
      return route.fallback()
    })
    await login(page)
    await page.goto('/wallet')
    await expect(page.getByText('1.234567 CNY', { exact: false })).toBeVisible()
    await expect(
      page.getByText('Published plan', { exact: true })
    ).toBeVisible()
    await expect(page.getByText(/order-visible/)).toBeVisible()
    await expect(page.getByText(/operation-visible/)).toBeVisible()
    failed = true
    await page
      .getByRole('button', { name: 'Refresh accounts and roles' })
      .click()
    await expect(page.getByRole('alert').first()).toBeVisible()
    if (failedEndpoint === 'resources') {
      await expect(
        page.getByText('1.234567 CNY', { exact: false })
      ).toHaveCount(0)
      await expect(
        page.getByRole('button', { name: 'Purchase addon' })
      ).toHaveCount(0)
    }
    if (failedEndpoint === 'catalog') {
      await expect(
        page.getByText('Published plan', { exact: true })
      ).toHaveCount(0)
      await expect(
        page.getByRole('button', { name: 'Purchase addon' })
      ).toHaveCount(0)
    }
    if (failedEndpoint === 'orders') {
      await expect(page.getByText(/order-visible/)).toHaveCount(0)
    }
    if (failedEndpoint === 'operations') {
      await expect(page.getByText(/operation-visible/)).toHaveCount(0)
    }
  })
}

for (const failedEndpoint of [
  'resources',
  'billing-profile',
  'usage-budget',
  'api-keys',
] as const) {
  test(`key settings hide ${failedEndpoint} data and prevent unsafe changes after refresh fails`, async ({
    page,
  }) => {
    await fixture(page, { models: ['verified-chat'] })
    let failed = false
    await page.route('**/api/unihub/bff/v1/**', async (route) => {
      const path = new URL(route.request().url()).pathname
      if (failed && path.endsWith(`/${failedEndpoint}`)) {
        return route.fulfill({ status: 503 })
      }
      if (path.endsWith('/resources')) {
        return route.fulfill({
          json: {
            account_id: 'personal-a',
            product_id: 'llmrouter',
            subscriptions: [],
            wallets: [
              { product_id: 'llmrouter', asset: 'CNY/6', amount: '7654321' },
            ],
            payments: { mode: 'sandbox', real_payments_configured: false },
          },
        })
      }
      if (path.endsWith('/billing-profile')) {
        return route.fulfill({
          json: {
            usage: {
              enabled: true,
              default_funding_mode: 'prepaid_cash',
              funding_modes: ['prepaid_cash'],
            },
          },
        })
      }
      if (path.endsWith('/usage-budget')) {
        return route.fulfill({
          json: {
            account_id: 'personal-a',
            origin_product_id: 'llmrouter',
            instance_id: 'default',
            funding_mode: 'prepaid_cash',
            asset: 'CNY/6',
            version: 1,
            max_total: '5000000',
            max_per_call: '',
            held: '0',
            used: '1234567',
          },
        })
      }
      if (path.endsWith('/api-keys')) {
        return route.fulfill({
          json: [
            {
              id: 'key-visible',
              name: 'Visible managed key',
              actor_id: 'immutable-user',
              account_id: 'personal-a',
              origin_product_id: 'llmrouter',
              instance_id: 'default',
              models: ['verified-chat'],
              funding_mode: 'prepaid_cash',
              asset: 'CNY/6',
              created_at: '2026-01-01T00:00:00Z',
              max_total: '',
              max_per_call: '',
              held: '0',
              used: '0',
            },
          ],
        })
      }
      return route.fallback()
    })
    await login(page)
    await page.goto('/keys')
    await page.getByLabel('verified-chat').check()
    await expect(
      page.getByRole('button', { name: 'Create API key' })
    ).toBeEnabled()
    await expect(
      page.getByText('Visible managed key', { exact: true })
    ).toBeVisible()
    await expect(page.getByText(/7\.654321 CNY/)).toBeVisible()
    await expect(
      page.getByRole('button', { name: 'Save account budget' })
    ).toBeVisible()
    failed = true
    await page
      .getByRole('button', { name: 'Refresh accounts and roles' })
      .click()
    await expect(page.getByRole('alert').first()).toBeVisible()
    await expect(
      page.getByRole('button', { name: 'Create API key' })
    ).toBeDisabled()
    if (failedEndpoint === 'resources') {
      await expect(page.getByText(/7\.654321 CNY/)).toHaveCount(0)
    }
    if (failedEndpoint === 'api-keys') {
      await expect(
        page.getByText('Visible managed key', { exact: true })
      ).toHaveCount(0)
    }
    if (failedEndpoint === 'usage-budget') {
      await expect(
        page.getByRole('button', { name: 'Save account budget' })
      ).toHaveCount(0)
    }
  })
}

test('a failed offer refresh discards a previously quoted upgrade', async ({
  page,
}) => {
  await fixture(page)
  let failCatalog = false
  const offer = (id: string, rank: number) => ({
    id,
    name: id,
    rank,
    period: 'month',
    kind: 'membership',
    account_kind: 'both',
    product_id: 'llmrouter',
    status: 'published',
    price: '1000000',
    monthly_credit: '1000000',
  })
  const current = offer('current-plan', 1)
  const next = offer('higher-plan', 2)
  await page.route('**/api/unihub/bff/v1/catalog', (route) =>
    failCatalog
      ? route.fulfill({ status: 503 })
      : route.fulfill({ json: [current, next] })
  )
  await page.route(
    '**/api/unihub/bff/v1/accounts/personal-a/resources',
    (route) =>
      route.fulfill({
        json: {
          account_id: 'personal-a',
          product_id: 'llmrouter',
          subscriptions: [
            {
              id: 'current-sub',
              product_id: 'llmrouter',
              status: 'active',
              offer: current,
            },
          ],
          payments: { real_payments_configured: false },
          billing_profile: {
            addons: { enabled: true },
            subscription: { enabled: true },
          },
        },
      })
  )
  await page.route('**/api/unihub/bff/v1/accounts/personal-a/quotes', (route) =>
    route.fulfill({
      json: {
        id: 'old-quote',
        account_id: 'personal-a',
        subscription_id: 'current-sub',
        offer: next,
        amount: '1000000',
        expires_at: '2099-01-01T00:00:00Z',
      },
    })
  )
  await login(page)
  await page.getByRole('button', { name: 'Quote upgrade' }).click()
  await expect(
    page.getByText('Upgrade quote: old-quote', { exact: false })
  ).toBeVisible()
  failCatalog = true
  await page.getByRole('button', { name: 'Refresh accounts and roles' }).click()
  await expect(page.getByRole('alert').first()).toBeVisible()
  await expect(
    page.getByText('Upgrade quote: old-quote', { exact: false })
  ).toHaveCount(0)
  failCatalog = false
  await page.getByRole('button', { name: 'Refresh accounts and roles' }).click()
  await expect(page.getByRole('heading', { name: 'higher-plan' })).toBeVisible()
  await expect(
    page.getByText('Upgrade quote: old-quote', { exact: false })
  ).toHaveCount(0)
})

test('broker finance is read-only with a current server proof; no write request is sent', async ({
  page,
}) => {
  await fixture(page, { models: ['synthetic'] })
  const now = Math.floor(Date.now() / 1000)
  await page.route('**/api/user/auth/refresh', (route) =>
    route.fulfill({
      json: {
        success: true,
        data: {
          access_token: 'broker-test-token',
          token_type: 'Bearer',
          access_expires_at: now + 300,
          user: {
            id: 42,
            username: 'product-user',
            role: 1,
            identity_source: 'unihub',
            unihub_user_id: 'immutable-user',
            permissions: { admin_permissions: { product: { use: true } } },
          },
          session: {
            sid: 'sid-broker-proof',
            current: true,
            login_method: 'product_broker',
            ip: '',
            user_agent: 'fixture',
            created_at: now,
            last_active_at: now,
            expires_at: now + 300,
          },
        },
      },
    })
  )
  await page.route('**/api/unihub/access', (route) =>
    route.fulfill({
      json: {
        access: {
          subject: 'immutable-user',
          user: { id: 'immutable-user', name: 'Product user', email: '' },
          roles: ['member'],
          permissions: ['product:use'],
          expires_at: now + 300,
        },
        permissions: { admin_permissions: { product: { use: true } } },
        csrf_token: 'csrf',
        finance_access: {
          available: true,
          transport: 'runtime_actor',
          central_user_id: 'immutable-user',
          expires_at: now + 120,
        },
      },
    })
  )
  await page.route('**/api/unihub/bff/v1/accounts/*/api-keys*', (route) =>
    route.fulfill({
      json: [
        {
          id: 'broker-key',
          name: 'Read-only broker key',
          credential_kind: 'api_key',
          actor_id: 'immutable-user',
          account_id: 'personal-a',
          origin_product_id: 'llmrouter',
          instance_id: 'default',
          models: ['synthetic'],
          funding_mode: 'prepaid_cash',
          asset: 'CNY/6',
          created_at: '2026-01-01T00:00:00Z',
          max_total: '',
          max_per_call: '',
          held: '0',
          used: '0',
        },
      ],
    })
  )
  const writes: string[] = []
  page.on('request', (request) => {
    if (
      request.url().includes('/api/unihub/bff/') &&
      request.method() !== 'GET'
    ) {
      writes.push(request.method())
    }
  })
  await page.goto('/api/unihub/login?return_to=%2Fkeys')
  await expect(
    page.getByText('Read-only broker key', { exact: true })
  ).toBeVisible()
  await expect(
    page.getByRole('button', { name: 'Create API key' })
  ).toBeDisabled()
  await expect(page.getByRole('button', { name: 'Revoke' })).toHaveCount(0)
  await expect(
    page.getByRole('button', { name: 'Save account budget' })
  ).toHaveCount(0)
  await page.goto('/wallet')
  await expect(page.getByText(/9007199254740\.993123 CNY/)).toBeVisible()
  await expect(
    page.getByRole('button', { name: 'Create recharge' })
  ).toHaveCount(0)
  await expect(
    page.getByRole('button', { name: 'Purchase addon' })
  ).toHaveCount(0)
  expect(writes).toEqual([])
})

for (const sessionKind of ['product_broker', 'password'] as const) {
  for (const failure of ['unavailable', 'expired', 'mismatch'] as const) {
    test(`${sessionKind} refuses financial reads when readiness is ${failure}`, async ({
      page,
    }) => {
      await fixture(page)
      const now = Math.floor(Date.now() / 1000)
      await page.route('**/api/user/auth/refresh', (route) =>
        route.fulfill({
          json: {
            success: true,
            data: {
              access_token: 'runtime-test-token',
              token_type: 'Bearer',
              access_expires_at: now + 300,
              user: {
                id: 42,
                username: 'product-user',
                role: 1,
                identity_source: 'unihub',
                unihub_user_id: 'immutable-user',
                permissions: { admin_permissions: { product: { use: true } } },
              },
              session: {
                sid: 'sid-runtime',
                current: true,
                login_method: sessionKind,
                ip: '',
                user_agent: 'fixture',
                created_at: now,
                last_active_at: now,
                expires_at: now + 300,
              },
            },
          },
        })
      )
      await page.route('**/api/unihub/access', (route) =>
        route.fulfill({
          json: {
            access: {
              subject: 'immutable-user',
              user: { id: 'immutable-user', name: 'Product user', email: '' },
              roles: ['member'],
              permissions: ['product:use'],
              expires_at: now + 300,
            },
            permissions: { admin_permissions: { product: { use: true } } },
            csrf_token: 'csrf',
            finance_access: {
              available: failure !== 'unavailable',
              transport: 'runtime_actor',
              central_user_id:
                failure === 'mismatch' ? 'wrong-user' : 'immutable-user',
              expires_at: failure === 'expired' ? now - 1 : now + 120,
            },
          },
        })
      )
      let bffCalls = 0
      page.on('request', (request) => {
        if (request.url().includes('/api/unihub/bff/')) bffCalls++
      })
      await page.goto('/api/unihub/login?return_to=%2Fkeys')
      await expect(
        page.getByText(
          'UniHub account access is unavailable for this browser session. Contact the administrator.'
        )
      ).toBeVisible()
      expect(bffCalls).toBe(0)
      await expect(
        page.getByRole('button', { name: 'Create API key' })
      ).toHaveCount(0)
      await expect(page).toHaveURL(/\/keys$/)
    })
  }
}

test('sandbox settlement stays unavailable while payment configuration refresh is pending', async ({
  page,
}) => {
  await fixture(page)
  let delay = false
  let release: () => void = () => {}
  let started: () => void = () => {}
  const pending = new Promise<void>((resolve) => {
    release = resolve
  })
  const requested = new Promise<void>((resolve) => {
    started = resolve
  })
  await page.route('**/personal-a/resources', async (route) => {
    if (delay) {
      started()
      await pending
    }
    return route.fulfill({
      json: {
        account_id: 'personal-a',
        product_id: 'llmrouter',
        subscriptions: [],
        wallets: [],
        payments: { mode: 'sandbox', real_payments_configured: false },
      },
    })
  })
  await page.route('**/personal-a/recharges', (route) =>
    route.fulfill({
      json: {
        id: 'sandbox-refresh',
        account_id: 'personal-a',
        product_id: 'llmrouter',
        amount: '1000000',
        currency: 'CNY',
        merchant: 'sandbox',
      },
    })
  )
  await login(page)
  await page.getByLabel('Recharge amount (CNY)').fill('1')
  await page
    .getByRole('button', { name: 'Create recharge', exact: true })
    .click()
  const settle = page.getByRole('button', {
    name: 'Settle sandbox recharge',
    exact: true,
  })
  await expect(settle).toBeVisible()
  delay = true
  await page.getByRole('button', { name: 'Refresh accounts and roles' }).click()
  await requested
  await expect(settle).toHaveCount(0)
  release()
  await expect(settle).toBeVisible()
})

test('signed-out browser explicitly switches to a cookie-verified account without prior local history', async ({
  page,
}) => {
  await fixture(page)
  await page.addInitScript(() => {
    if (!sessionStorage.getItem('switch-test-initialized')) {
      sessionStorage.setItem('switch-test-initialized', '1')
      sessionStorage.setItem('new-api:auth:selected-sid:v1', '')
      localStorage.removeItem('new-api:auth:recent-sids:v1')
      localStorage.removeItem('new-api:auth:selected-sid:v1')
    }
  })
  const requests: string[] = []
  const silentLogins: string[] = []
  page.on('request', (request) => {
    if (new URL(request.url()).pathname === '/api/unihub/login') {
      silentLogins.push(request.url())
    }
  })
  let fail = true
  const expires = Math.floor(Date.now() / 1000) + 300
  await page.route('**/api/user/auth/session-options', (route) =>
    route.fulfill({
      json: {
        success: true,
        data: {
          sessions: [
            {
              sid: 'sid-central',
              realm: 'central',
              display_name: 'Verified central user',
            },
          ],
          unavailable: false,
        },
      },
    })
  )
  await page.route('**/api/user/auth/refresh', (route) => {
    const sid = route.request().headers()['x-auth-session'] ?? ''
    requests.push(sid)
    if (fail || sid !== 'sid-central') {
      return route.fulfill({ status: 503, json: { success: false } })
    }
    return route.fulfill({
      json: {
        success: true,
        data: {
          access_token: 'synthetic-access',
          token_type: 'Bearer',
          access_expires_at: expires,
          user: { id: 42, username: 'central-user', role: 1 },
          session: {
            sid: 'sid-central',
            current: true,
            login_method: 'unihub',
            ip: '',
            user_agent: '',
            created_at: 1,
            last_active_at: 1,
            expires_at: expires,
          },
        },
      },
    })
  })
  await page.goto('/sign-in')
  const account = page
    .locator('div')
    .filter({ hasText: 'Verified central user' })
    .filter({
      has: page.getByRole('button', { name: /Switch account|切换账号/ }),
    })
    .last()
  await expect(page.getByText('Verified central user')).toBeVisible()
  await page.screenshot({ path: '/tmp/llmrouter-account-switch-desktop.png' })
  await page.setViewportSize({ width: 390, height: 844 })
  await expect(account.getByRole('button')).toBeVisible()
  await page.screenshot({ path: '/tmp/llmrouter-account-switch-mobile.png' })
  expect(requests).toEqual([])
  expect(silentLogins).toEqual([])
  await account.getByRole('button').click()
  await expect(
    page
      .getByRole('alert')
      .filter({ hasText: /Unable to switch account|无法切换账号/ })
  ).toBeVisible()
  expect(
    await page.evaluate(() =>
      sessionStorage.getItem('new-api:auth:selected-sid:v1')
    )
  ).toBe('')
  expect(requests).toEqual(['sid-central'])
  fail = false
  await Promise.all([
    page.waitForURL('**/dashboard'),
    account.getByRole('button').click(),
  ])
  await expect.poll(() => requests.length).toBeGreaterThanOrEqual(2)
  await expect
    .poll(() =>
      page.evaluate(() =>
        sessionStorage.getItem('new-api:auth:selected-sid:v1')
      )
    )
    .toBe('sid-central')
  expect(requests.every((sid) => sid === 'sid-central')).toBe(true)
})
