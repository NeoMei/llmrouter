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
            rankings: { enabled: false },
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
    page.locator('a[href="/keys"],a[href="/wallet"],a[href="/profile"]')
  ).toHaveCount(0)
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
