import { test, expect, type Page } from '@playwright/test'

const accounts = [
  { id: 'personal-a', name: 'Personal A', kind: 'personal', role: 'owner' },
  { id: 'team-b', name: 'Team B', kind: 'team', role: 'member' },
]
async function fixture(
  page: Page,
  options: { expiry?: number; registerUnknown?: boolean } = {}
) {
  await page.route('**/api/**', async (route) => {
    const path = new URL(route.request().url()).pathname
    if (path === '/api/unihub/config') {
      return route.fulfill({
        json: {
          enabled: true,
          public_origin: 'http://127.0.0.1:58006',
          client_id: 'client',
          redirect_uri: 'http://127.0.0.1:58004/unihub',
          product_id: 'llmrouter',
          models: ['synthetic'],
        },
      })
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
  await page.route('http://127.0.0.1:58006/**', async (route) => {
    const request = route.request()
    const path = new URL(request.url()).pathname
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
  })
  return mutations
}
async function login(page: Page) {
  await page.goto('/unihub')
  await page.getByLabel('Username', { exact: true }).fill('product')
  await page.getByLabel('Password', { exact: true }).fill('private')
  await page.getByRole('button', { name: 'Sign in', exact: true }).click()
  await expect(page.getByText('Product user · immutable-user')).toBeVisible()
  await page.getByRole('combobox').first().selectOption('personal-a')
}
test('PKCE login keeps tokens memory-only and explicit payer controls role visibility', async ({
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
  await expect(
    page.getByRole('button', { name: 'Sign in', exact: true })
  ).toBeVisible()
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
test('expiry removes account resources and requires re-login', async ({
  page,
}) => {
  await fixture(page, { expiry: 2 })
  await login(page)
  await expect(
    page.getByRole('button', { name: 'Sign in', exact: true })
  ).toBeVisible({ timeout: 6000 })
  await expect(page.getByText('Product user · immutable-user')).toHaveCount(0)
})
test('unknown registration retries original payload and idempotency key', async ({
  page,
}) => {
  const writes = await fixture(page, { registerUnknown: true })
  await page.goto('/unihub')
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
  await page.route('**/api/unihub/identity', (route) =>
    route.fulfill({
      json: {
        user: { id: 'immutable-user', name: 'Product user' },
        accounts: [
          accounts[0],
          { ...accounts[1], name: 'Long team name '.repeat(12) },
        ],
      },
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
  ).toBeEnabled()
  await expect(page.getByText('Original financial object')).toHaveCount(0)
})
