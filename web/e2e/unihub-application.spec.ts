import { expect, test, type Page } from '@playwright/test'

async function productFixture(
  page: Page,
  grants = ['product:use'],
  anonymous = false
) {
  let current = grants
  let authorized = !anonymous
  const loginReturns: string[] = []
  const requests: string[] = []
  const expiry = Math.floor(Date.now() / 1000) + 3600
  const user = () => ({
    id: 42,
    username: 'unified-member',
    display_name: 'Unified member',
    role: 1,
    identity_source: 'unihub',
    unihub_user_id: 'central-user',
    quota: 0,
    used_quota: 0,
    request_count: 0,
    permissions: {
      admin_permissions: Object.fromEntries(
        [...new Set(current.map((value) => value.split(':')[0]))].map(
          (resource) => [
            resource,
            Object.fromEntries(
              current
                .filter((value) => value.startsWith(`${resource}:`))
                .map((value) => [value.split(':')[1], true])
            ),
          ]
        )
      ),
    },
  })
  await page.route('**/api/**', async (route) => {
    const path = new URL(route.request().url()).pathname
    requests.push(path)
    if (path === '/api/unihub/login') {
      const destination =
        new URL(route.request().url()).searchParams.get('return_to') ??
        '/dashboard'
      loginReturns.push(destination)
      authorized = true
      return route.fulfill({ status: 303, headers: { Location: destination } })
    }
    if (
      !authorized &&
      ['/api/user/auth/refresh', '/api/unihub/access'].includes(path)
    ) {
      return route.fulfill({ status: 401, json: { success: false } })
    }
    let body: unknown = { success: true, data: [] }
    if (path === '/api/unihub/config') {
      body = {
        enabled: true,
        native_access: false,
        public_origin: 'https://hub.invalid',
        client_id: 'client',
        redirect_uri: 'http://127.0.0.1:58004/api/unihub/callback',
        product_id: 'llmrouter',
        models: ['fixture-model'],
      }
    } else if (path === '/api/user/auth/refresh') {
      body = {
        success: true,
        data: {
          user: user(),
          access_token: 'native-proof',
          token_type: 'Bearer',
          access_expires_at: expiry,
          session: {
            sid: 'unified-sid',
            login_method: 'unihub',
            current: true,
            created_at: 1,
            last_active_at: 1,
            expires_at: expiry,
            ip: '',
            user_agent: 'fixture',
          },
        },
      }
    } else if (path === '/api/unihub/access') {
      body = {
        access: {
          subject: 'central-user',
          user: { id: 'central-user', name: 'Unified member', email: '' },
          roles: ['member'],
          permissions: current,
          expires_at: expiry,
        },
        permissions: user().permissions,
        csrf_token: 'csrf',
      }
    } else if (path === '/api/status') {
      body = {
        success: true,
        data: {
          system_name: 'LLMRouter',
          display_in_currency: false,
          quota_per_unit: 500000,
          HeaderNavModules: JSON.stringify({
            home: true,
            console: true,
            pricing: { enabled: true, requireAuth: true },
            rankings: { enabled: true },
            docs: true,
            about: true,
          }),
          SidebarModulesAdmin: '',
          announcements_enabled: false,
        },
      }
    } else if (path === '/api/notice') body = { success: true, data: '' }
    else if (path === '/api/user/self') body = { success: true, data: user() }
    else if (path === '/api/user/models' || path === '/api/models') {
      body = { success: true, data: ['fixture-model'] }
    } else if (path === '/api/user/self/groups') {
      body = { success: true, data: { default: { ratio: 1, desc: 'Default' } } }
    } else if (path.includes('/api/data/flow')) {
      body = {
        success: true,
        data: {
          nodes: [],
          links: [],
          summary: { requests: 0, tokens: 0, quota: 0 },
        },
      }
    } else if (path.endsWith('/stat')) {
      body = { success: true, data: { quota: 0, token: 0, rpm: 0, tpm: 0 } }
    } else if (/\/api\/(log|mj|task|channel)(\/|$)/.test(path)) {
      body = {
        success: true,
        data: { items: [], total: 0, page: 1, page_size: 10 },
      }
    } else if (path === '/api/client_model_routes/options') {
      body = { success: true, data: { channels: [], billing_models: [] } }
    } else if (path.startsWith('/api/unihub/bff/')) {
      if (path.endsWith('/accounts')) {
        body = [
          { id: 'personal', name: 'Personal', kind: 'personal', role: 'owner' },
        ]
      } else if (path.endsWith('/billing-profile')) {
        body = {
          usage: { enabled: false, funding_modes: [] },
          addons: { enabled: false },
          subscription: { enabled: false },
        }
      } else if (path.endsWith('/resources')) {
        body = {
          account_id: 'personal',
          product_id: 'llmrouter',
          wallets: [],
          subscriptions: [],
          payments: { real_payments_configured: false },
          billing_profile: {
            addons: { enabled: false },
            subscription: { enabled: false },
          },
        }
      } else if (path.endsWith('/summary')) {
        body = {
          calls_total: '0',
          pending_calls: '0',
          financial_details: false,
          charges_by_asset: {},
        }
      } else if (path.endsWith('/calls')) body = { items: [], has_more: false }
      else body = []
    }
    await route.fulfill({ json: body })
  })
  return {
    requests,
    loginReturns,
    revoke: () => {
      current = ['product:use']
    },
  }
}

test('unified member gets real LLMRouter overview and self analytics instead of the account page', async ({
  page,
}) => {
  const fixture = await productFixture(page)
  await page.goto('/dashboard/overview')
  await expect(
    page.getByRole('heading', { name: 'Overview', exact: true })
  ).toBeVisible()
  await expect(page.locator('[data-slot="sidebar"]')).toBeVisible()
  await expect(page.locator('a[href="/rankings"]')).toHaveCount(0)
  await expect(
    page.getByRole('heading', { name: 'LLMRouter · UniHub' })
  ).toHaveCount(0)
  await page.goto('/dashboard/models')
  await expect(
    page.getByRole('heading', { name: 'Model Call Analytics', exact: true })
  ).toBeVisible()
  await expect
    .poll(() => fixture.requests.includes('/api/data/self'))
    .toBe(true)
  await page.reload()
  await expect(
    page.getByRole('heading', { name: 'Model Call Analytics', exact: true })
  ).toBeVisible()
  expect(
    fixture.requests.filter((path) =>
      /^\/api\/(token|native-billing|user\/(2fa|passkey|topup))/.test(path)
    )
  ).toEqual([])
  await expect(
    page.locator('a[href="/wallet"],a[href="/profile"]')
  ).toHaveCount(0)
  await expect(page.locator('a[href="/keys"]')).toBeVisible()
})

test('unified keys keep payer scope, one-time secrets and a separate finance budget', async ({
  page,
}) => {
  await productFixture(page)
  const writes: { path: string; body: Record<string, unknown> }[] = []
  let revoked = false
  await page.route('**/api/unihub/bff/**', async (route) => {
    const request = route.request()
    const path = new URL(request.url()).pathname
    if (path.endsWith('/accounts')) {
      return route.fulfill({
        json: [
          { id: 'personal', name: 'Personal', kind: 'personal', role: 'owner' },
          { id: 'team', name: 'Team', kind: 'team', role: 'member' },
        ],
      })
    }
    if (path.endsWith('/billing-profile')) {
      return route.fulfill({
        json: {
          usage: {
            enabled: true,
            funding_modes: ['prepaid_cash', 'credits'],
            default_funding_mode: 'prepaid_cash',
          },
        },
      })
    }
    if (path.endsWith('/resources')) {
      return route.fulfill({
        json: path.includes('/team/')
          ? {}
          : {
              wallets: [
                { product_id: 'llmrouter', asset: 'CNY/6', amount: '80000000' },
              ],
              credits: { spendable: '0' },
            },
      })
    }
    if (path.endsWith('/usage-budget')) {
      if (request.method() === 'PUT') {
        writes.push({ path, body: request.postDataJSON() })
      }
      return route.fulfill({
        json: {
          version: 1,
          asset: 'CNY/6',
          funding_mode: 'prepaid_cash',
          max_total: '10000000',
          max_per_call: '',
          held: '0',
          used: '1000000',
        },
      })
    }
    if (path.endsWith('/api-keys/key-1')) {
      revoked = true
      return route.fulfill({ status: 204 })
    }
    if (path.endsWith('/api-keys')) {
      if (request.method() === 'POST') {
        const body = request.postDataJSON()
        writes.push({ path, body })
        return route.fulfill({
          status: 201,
          json: { id: 'key-1', token: 'uh_key_fixture_secret_once' },
        })
      }
      return route.fulfill({
        json: path.includes('/team/')
          ? []
          : [
              {
                id: 'key-1',
                credential_kind: 'api_key',
                name: 'Personal key',
                models: ['fixture-model'],
                asset: 'CNY/6',
                held: '0',
                used: '1000000',
                max_total: '5000000',
                max_per_call: '',
                expires_at: null,
                revoked_at: revoked ? new Date().toISOString() : null,
              },
            ],
      })
    }
    return route.fallback()
  })
  await page.goto('/keys')
  await expect(page.getByText('Personal key', { exact: true })).toBeVisible()
  await expect(
    page.getByText('Available funds: 80.000000 CNY', { exact: true })
  ).toBeVisible()
  await page.getByLabel('Funding mode', { exact: true }).selectOption('credits')
  await expect(
    page.getByText('Available funds: 0.000000 CREDIT', { exact: true })
  ).toBeVisible()
  await page
    .getByLabel('Funding mode', { exact: true })
    .selectOption('prepaid_cash')
  await page.getByLabel('Key name', { exact: true }).fill('SDK key')
  await page.getByLabel('Key lifetime limit', { exact: true }).fill('5.25')
  await page.getByLabel('fixture-model', { exact: true }).check()
  await page
    .getByRole('button', { name: 'Create API key', exact: true })
    .click()
  await expect(
    page.getByText('uh_key_fixture_secret_once', { exact: true })
  ).toBeVisible()
  expect(writes[0]).toEqual({
    path: '/api/unihub/bff/v1/accounts/personal/api-keys',
    body: {
      name: 'SDK key',
      models: ['fixture-model'],
      funding_mode: 'prepaid_cash',
      max_total: '5250000',
      max_per_call: '',
      origin_product_id: 'llmrouter',
      instance_id: 'default',
    },
  })
  await page.getByLabel('Explicit payer account').selectOption('team')
  await expect(
    page.getByText(
      'Account balance is unavailable with your current permissions.',
      { exact: true }
    )
  ).toBeVisible()
  await expect(
    page.getByText('Available funds: 0.000000 CNY', { exact: true })
  ).toHaveCount(0)
  await expect(page.getByText('Personal key', { exact: true })).toHaveCount(0)
  await expect(
    page.getByText('uh_key_fixture_secret_once', { exact: true })
  ).toHaveCount(0)
  await expect(
    page.getByRole('button', { name: 'Save account budget', exact: true })
  ).toHaveCount(0)
  await page.getByLabel('Explicit payer account').selectOption('personal')
  await expect(page.getByText('Personal key', { exact: true })).toBeVisible()
  await expect(
    page.getByLabel('Account lifetime budget', { exact: true })
  ).toHaveValue('10.000000')
  await page.getByLabel('Account lifetime budget', { exact: true }).fill('12')
  await page
    .getByRole('button', { name: 'Save account budget', exact: true })
    .click()
  await expect.poll(() => writes.length).toBe(2)
  expect(writes[1].body).toEqual({
    version: 1,
    max_total: '12000000',
    max_per_call: '',
  })
  await page.getByRole('button', { name: 'Revoke', exact: true }).click()
  await expect(
    page.getByRole('button', { name: 'Revoke', exact: true })
  ).toHaveCount(0)
  expect(
    await page.evaluate(() =>
      JSON.stringify({ ...localStorage, ...sessionStorage })
    )
  ).not.toContain('uh_key_fixture_secret_once')
})

test('unified usage cost points to central settlement instead of showing a zero native charge', async ({
  page,
}) => {
  await productFixture(page)
  await page.route('**/api/log/self?*', (route) =>
    route.fulfill({
      json: {
        success: true,
        data: {
          page: 1,
          page_size: 10,
          total: 2,
          items: [
            {
              id: 1,
              created_at: Math.floor(Date.now() / 1000),
              type: 2,
              model_name: 'fixture-model',
              token_name: 'unihub:key-1',
              prompt_tokens: 10,
              completion_tokens: 5,
              quota: 0,
              use_time: 1,
              request_id: 'call-1',
              other: JSON.stringify({
                funding_owner: 'unihub',
                central_status: 'SETTLED',
                central_asset: 'CNY/6',
              }),
            },
            {
              id: 2,
              created_at: Math.floor(Date.now() / 1000),
              type: 2,
              model_name: 'legacy-model',
              token_name: 'legacy-key',
              prompt_tokens: 25,
              completion_tokens: 15,
              quota: 0,
              use_time: 1,
              request_id: 'native-consumption',
              other: JSON.stringify({ financial_details: false }),
            },
          ],
        },
      },
    })
  )
  await page.goto('/usage-logs/common')
  await expect(
    page.getByRole('link', { name: 'UniHub billing', exact: true })
  ).toBeVisible()
  await expect(page.getByText('SETTLED', { exact: true })).toBeVisible()
  await expect(
    page.getByText('Finance access required', { exact: true })
  ).toBeVisible()
})

test('unified member usage logs stay self scoped and omit native billing', async ({
  page,
}) => {
  const fixture = await productFixture(page)
  await page.goto('/usage-logs/common')
  await expect(
    page.getByRole('heading', { name: 'Common Logs', exact: true })
  ).toBeVisible()
  await expect.poll(() => fixture.requests.includes('/api/log/self')).toBe(true)
  expect(fixture.requests).not.toContain('/api/log/')
  expect(
    fixture.requests.filter((path) =>
      /native-billing|billing\/native/.test(path)
    )
  ).toEqual([])
})

test('account is separate and has a return to application', async ({
  page,
}) => {
  await productFixture(page)
  await page.goto('/unihub')
  await expect(
    page.getByRole('heading', { name: 'LLMRouter · UniHub' })
  ).toBeVisible()
  await page.getByRole('link', { name: 'Console', exact: true }).click()
  await expect(
    page.getByRole('heading', { name: 'Overview', exact: true })
  ).toBeVisible()
})

test('explicit capabilities restore administration pages without native admin role', async ({
  page,
}) => {
  await productFixture(page, [
    'product:use',
    'channel:read',
    'catalog:read',
    'log:read',
    'routing:read',
    'settings:read',
  ])
  await page.goto('/client-model-routes')
  await expect(
    page.getByRole('heading', { name: 'Client Model Routes', exact: true })
  ).toBeVisible()
  await expect(
    page.getByRole('button', { name: 'New route', exact: true })
  ).toHaveCount(0)
  await page.goto('/models/metadata')
  await expect(
    page.getByRole('heading', { name: 'Metadata', exact: true })
  ).toBeVisible()
  await expect(
    page.getByRole('button', { name: 'Add Model', exact: true })
  ).toHaveCount(0)
  await expect(
    page.getByRole('tab', { name: 'Deployments', exact: true })
  ).toHaveCount(0)
  await page.goto('/system-settings/models/global')
  await expect(
    page.getByRole('heading', {
      name: 'Global Model Configuration',
      exact: true,
    })
  ).toBeVisible()
  await expect(
    page.locator(
      'a[href*="system-settings/auth"],a[href*="system-settings/billing"],a[href*="native-pricing"]'
    )
  ).toHaveCount(0)
  await page.reload()
  await expect(
    page.getByRole('heading', {
      name: 'Global Model Configuration',
      exact: true,
    })
  ).toBeVisible()
})

test('capability revocation removes navigation and blocks direct reentry', async ({
  page,
}) => {
  const fixture = await productFixture(page, ['product:use', 'routing:read'])
  await page.goto('/client-model-routes')
  await expect(
    page.getByRole('heading', { name: 'Client Model Routes', exact: true })
  ).toBeVisible()
  fixture.revoke()
  await page.goto('/dashboard/overview')
  await expect(
    page.locator('[data-slot="sidebar"] a[href="/client-model-routes"]')
  ).toHaveCount(0)
  await page.goto('/client-model-routes')
  await expect(page).toHaveURL(/\/403/)
})

test('fresh product SSO preserves an actual nested analytics destination', async ({
  page,
}) => {
  const fixture = await productFixture(page, ['product:use'], true)
  await page.goto('/dashboard/models')
  await expect(
    page.getByRole('heading', { name: 'Model Call Analytics', exact: true })
  ).toBeVisible()
  expect(fixture.loginReturns).toEqual(['/dashboard/models'])
  await page.reload()
  await expect(
    page.getByRole('heading', { name: 'Model Call Analytics', exact: true })
  ).toBeVisible()
})

test('product writers get working administration controls while readers cannot mutate', async ({
  page,
}) => {
  await productFixture(page, [
    'product:use',
    'routing:read',
    'routing:write',
    'catalog:read',
    'catalog:write',
    'settings:read',
    'settings:write',
  ])
  await page.goto('/client-model-routes')
  await expect(
    page.getByRole('button', { name: 'New route', exact: true })
  ).toBeVisible()
  await expect(
    page.getByRole('button', { name: 'Save route', exact: true })
  ).toBeEnabled()
  await page.goto('/models/metadata')
  await expect(
    page.getByRole('button', { name: 'Add Model', exact: true })
  ).toBeVisible()
  await page.goto('/system-settings/models/routing-reliability')
  await expect(
    page.getByRole('heading', { name: 'Routing Reliability', exact: true })
  ).toBeVisible()
  await expect(
    page.getByRole('button', { name: 'Save Changes', exact: true })
  ).toBeEnabled()
  await expect(
    page.getByText('Scheduled channel tests', { exact: true })
  ).toHaveCount(0)
})

for (const path of ['/unihub', '/dashboard/models']) {
  for (const failure of ['429', '503', 'network']) {
    test(`temporary refresh ${failure} at ${path} waits for manual recovery without SSO`, async ({
      page,
    }) => {
      const fixture = await productFixture(page)
      let recovered = false
      let refreshes = 0
      let logouts = 0
      page.on('request', (request) => {
        if (new URL(request.url()).pathname === '/api/user/auth/logout') {
          logouts++
        }
      })
      await page.route('**/api/user/auth/refresh', async (route) => {
        refreshes++
        if (recovered) return route.fallback()
        if (failure === 'network') return route.abort('failed')
        return route.fulfill({
          status: Number(failure),
          headers: { 'Retry-After': '1' },
          json: { success: false, code: 'AUTH_TEMPORARY_FAILURE' },
        })
      })
      await page.goto(path)
      const retry = page.getByRole('button', { name: 'Retry', exact: true })
      await expect(retry).toBeVisible()
      await expect(page).toHaveURL(
        new RegExp(`${path.replaceAll('/', '\\/')}$`)
      )
      expect(fixture.loginReturns).toEqual([])
      const before = refreshes
      // A stable retry view must not rotate cookies or initiate background SSO.
      await page.waitForTimeout(300)
      expect(refreshes).toBe(before)
      expect(logouts).toBe(0)
      // Retrying while still unavailable stays on the same stable error view.
      await retry.click()
      await expect(retry).toBeEnabled()
      expect(fixture.loginReturns).toEqual([])
      recovered = true
      await retry.click()
      if (path === '/unihub') {
        await expect(
          page.getByRole('heading', { name: 'LLMRouter · UniHub', exact: true })
        ).toBeVisible()
      } else {
        await expect(
          page.getByRole('heading', {
            name: 'Model Call Analytics',
            exact: true,
          })
        ).toBeVisible()
      }
      await expect(retry).toHaveCount(0)
      expect(fixture.loginReturns).toEqual([])
      expect(logouts).toBe(0)
    })
  }
}
