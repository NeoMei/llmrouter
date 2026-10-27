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
import assert from 'node:assert/strict'
import { test } from 'node:test'

import { useAuthStore } from '../../../stores/auth-store'
import { createProductSession, restoreProductSession } from '../session'
import { safeProductReturn, productLoginURL } from '../sso'

test('SSO keeps a safe management deep link and rejects foreign returns', () => {
  assert.equal(
    productLoginURL('/channels?filter=abc'),
    '/api/unihub/login?return_to=%2Fchannels%3Ffilter%3Dabc'
  )
  for (const raw of [
    '//evil.test',
    'https://evil.test',
    '/\\evil',
    '/%2f%2fevil',
  ]) {
    assert.equal(safeProductReturn(raw), '/dashboard')
  }
})

test('server session restores personal payer and sends only native proof through BFF', async () => {
  const previous = globalThis.fetch
  const calls: string[] = []
  useAuthStore.getState().auth.setBundle({
    access_token: 'native-dashboard-proof',
    token_type: 'Bearer',
    access_expires_at: Date.now() / 1000 + 3600,
    session: {
      sid: 'sid',
      current: true,
      login_method: 'unihub',
      ip: '',
      user_agent: '',
      created_at: 1,
      last_active_at: 1,
      expires_at: Date.now() / 1000 + 3600,
    },
    user: {
      id: 1,
      username: 'projection',
      role: 1,
      identity_source: 'unihub',
      unihub_user_id: 'central',
    },
  })
  globalThis.fetch = async (input, init) => {
    const path = String(input)
    calls.push(path)
    assert.equal(
      new Headers(init?.headers).get('Authorization'),
      'Bearer native-dashboard-proof'
    )
    assert.equal(path.startsWith('/api/unihub/'), true)
    if (path === '/api/unihub/access') {
      return Response.json({
        access: {
          subject: 'central',
          user: { id: 'central', name: 'A', email: 'a@test' },
          roles: ['viewer'],
          permissions: ['channel:read'],
          expires_at: Date.now() / 1000 + 3600,
        },
        permissions: { admin_permissions: { channel: { read: true } } },
        csrf_token: 'csrf',
      })
    }
    if (path.endsWith('/v1/accounts') && init?.method === 'GET') {
      return Response.json([
        { id: 'team', kind: 'team', name: 'Team', role: 'owner' },
        { id: 'personal', kind: 'personal', name: 'Personal', role: 'owner' },
      ])
    }
    assert.equal(new Headers(init?.headers).get('X-UniHub-CSRF'), 'csrf')
    return Response.json({ id: 'created' })
  }
  try {
    const session = createProductSession({
      enabled: true,
      public_origin: 'https://hub.test',
      client_id: 'client',
      redirect_uri: 'https://app.test/api/unihub/callback',
      product_id: 'llmrouter',
      models: [],
    })
    await restoreProductSession(session)
    assert.equal(session.account, 'personal')
    assert.equal(session.client.hasAccessToken(), false)
    session.select('team')
    session.intents.set('pending', { account: 'team' } as never)
    await restoreProductSession(session)
    assert.equal(session.account, 'team')
    await session.client.createTeam('New team')
    assert.ok(calls.includes('/api/unihub/bff/v1/accounts'))
    session.clear()
  } finally {
    globalThis.fetch = previous
    useAuthStore.getState().auth.reset()
  }
})

test('SSO retains only known product interface destinations', () => {
  for (const value of [
    '/dashboard/models?filter=x',
    '/usage-logs/common',
    '/models/metadata',
    '/client-model-routes',
    '/system-settings/models/global',
    '/unihub',
  ]) {
    assert.equal(safeProductReturn(value), value)
  }
  for (const value of [
    '/api/user/self',
    '/platform/v1/chat/completions',
    '/not-a-product-page',
    '/system-settings/auth/login',
    '/models/deployments',
    '/dashboard/%2e%2e/%2e%2e/api/user/self',
  ]) {
    assert.equal(safeProductReturn(value), '/dashboard')
  }
})
