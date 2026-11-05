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
import type { Page, Route } from '@playwright/test'

export const funding = {
  mode: 'unified_cash',
  asset: 'CNY/6',
  unit_version: 'lr-cny-v1',
  available: '1230001',
  held: '200000',
  consumed: '300000',
} as const
export const user = {
  id: 42,
  username: 'fixture',
  role: 1,
  status: 1,
  quota: 0,
  used_quota: 0,
  request_count: 0,
  group: 'default',
  aff_quota: 0,
  aff_count: 0,
  aff_history_quota: 0,
}
export function bundle(id = 42, cash = false, role = 1) {
  const now = Math.floor(Date.now() / 1000)
  return {
    user: {
      ...user,
      id,
      role,
      ...(cash
        ? {
            identity_source: 'unihub',
            unihub_user_id: 'fixture-central',
            funding,
          }
        : {}),
    },
    access_token: `fixture-only-${id}`,
    token_type: 'Bearer',
    access_expires_at: now + 3600,
    session: {
      sid: `fixture-${id}`,
      current: true,
      login_method: cash ? 'unihub' : 'password',
      ip: '',
      user_agent: 'fixture',
      created_at: now,
      last_active_at: now,
      expires_at: now + 3600,
    },
  }
}
export async function fixture(
  page: Page,
  cash = false,
  language = 'en',
  role = 1
) {
  const requests: string[] = []
  const writes: { path: string; body: unknown }[] = []
  await page.addInitScript((locale) => {
    localStorage.setItem('setup_status_checked', 'true')
    localStorage.setItem('i18nextLng', locale)
  }, language)
  await page.route('**/api/**', async (route) => {
    const path = new URL(route.request().url()).pathname
    requests.push(path)
    if (
      !['GET', 'HEAD'].includes(route.request().method()) &&
      path !== '/api/user/auth/refresh'
    ) {
      writes.push({ path, body: route.request().postDataJSON() })
    }
    let body: unknown = { success: true, data: [] }
    if (path === '/api/user/auth/refresh') {
      body = { success: true, data: bundle(42, cash, role) }
    } else if (path === '/api/unihub/config') {
      body = {
        enabled: true,
        native_access: false,
        public_origin: 'https://hub.invalid',
        client_id: 'fixture-client',
        redirect_uri: 'http://127.0.0.1:58026/unihub',
        product_id: 'llmrouter',
        models: ['fixture-model'],
      }
    } else if (path === '/api/unihub/access') {
      body = {
        access: {
          subject: 'fixture-central',
          user: { id: 'fixture-central', name: 'Fixture', email: '' },
          roles: ['member'],
          permissions: ['product:use'],
          expires_at: Math.floor(Date.now() / 1000) + 3600,
        },
        user: bundle(42, cash, role).user,
        permissions: { product: { use: true } },
        csrf_token: 'fixture-csrf',
      }
    } else if (path === '/api/user/self') {
      body = {
        success: true,
        data: bundle(
          route.request().headers()['authorization'] ===
            'Bearer fixture-only-99'
            ? 99
            : 42,
          cash &&
            route.request().headers()['authorization'] !==
              'Bearer fixture-only-99',
          role
        ).user,
      }
    } else if (path === '/api/status') {
      body = {
        success: true,
        data: {
          system_name: 'Settings fixture',
          quota_display_type: 'TOKENS',
          quota_per_unit: 500000,
          default_use_auto_group: true,
          password_login_enabled: true,
        },
      }
    } else if (path === '/api/notice') body = { success: true, data: '' }
    else if (path === '/api/user/topup/info') {
      body = {
        success: true,
        data: {
          enable_wechat_pay: true,
          enable_redemption: !cash,
          enable_affiliate_transfer: !cash,
          funding_mode: cash ? 'unified_cash' : 'local',
          min_topup: 1,
          amount_options: [1, 10],
          discount: {},
          pay_methods: [],
          payment_compliance_confirmed: true,
        },
      }
    } else if (path === '/api/user/self/groups') {
      body = {
        success: true,
        data: {
          default: { ratio: 1, desc: 'Default' },
          auto: { ratio: 1, desc: 'Automatic' },
        },
      }
    } else if (path === '/api/user/models') {
      body = { success: true, data: ['fixture-model'] }
    } else if (path === '/api/token/' && route.request().method() === 'POST') {
      body = { success: true }
    } else if (
      path.includes('/topup') ||
      path === '/api/token/' ||
      path === '/api/log/self'
    ) {
      body = {
        success: true,
        data: { items: [], total: 0, page: 1, page_size: 10 },
      }
    } else if (path.endsWith('/stat')) {
      body = { success: true, data: { quota: 0, token: 0, rpm: 0, tpm: 0 } }
    }
    await route.fulfill({ json: body })
  })
  return { requests, writes }
}
export async function screenshot(page: Page, name: string) {
  await page.screenshot({
    path: `../.superpowers/sdd/2026-11-05-llmrouter-settings-simplification/screenshots/${name}${page.viewportSize()?.width === 390 ? '-mobile' : ''}.png`,
    fullPage: true,
    animations: 'disabled',
  })
}

/** The layout's plain-fetch identity read succeeds; Axios wallet GETs exercise financial failure. */
export async function walletResponse(
  page: Page,
  handler: (route: Route) => unknown | Promise<unknown>
) {
  await page.route('**/api/user/self', async (route) => {
    if (
      route.request().headers()['content-type']?.includes('application/json')
    ) {
      return route.fallback()
    }
    await handler(route)
  })
}
