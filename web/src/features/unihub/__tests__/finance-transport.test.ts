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

import { useAuthStore } from '@/stores/auth-store'

import { createProductSession } from '../session'

test('current broker finance permits only approved payer-scoped writes and rejects stale or read-only authority', async () => {
  const now = Math.floor(Date.now() / 1000)
  const user = {
    id: 42,
    username: 'member',
    role: 1,
    identity_source: 'unihub',
    unihub_user_id: 'subject',
  }
  useAuthStore.getState().auth.setBundle({
    access_token: 'fixture-token',
    token_type: 'Bearer',
    access_expires_at: now + 600,
    user,
    session: {
      sid: 'sid',
      current: true,
      login_method: 'product_broker',
      ip: '',
      user_agent: '',
      created_at: now,
      last_active_at: now,
      expires_at: now + 600,
    },
  })
  const session = createProductSession({
    enabled: true,
    public_origin: 'https://hub.test',
    client_id: 'client',
    redirect_uri: 'https://app.test/unihub',
    product_id: 'llmrouter',
    models: [],
  })
  const proof = {
    access: {
      subject: 'subject',
      user: { id: 'subject', name: 'Member', email: '' },
      roles: [],
      permissions: [],
      expires_at: now + 120,
    },
    permissions: { admin_permissions: {} },
    csrf_token: 'csrf',
    finance_access: {
      available: true,
      transport: 'runtime_actor',
      central_user_id: 'subject',
      expires_at: now + 120,
      write_scopes: [
        'model-credentials:write',
        'settings:write',
        'recharges:write',
      ],
    },
  }
  const original = globalThis.fetch
  const sent: string[] = []
  globalThis.fetch = async (url, init) => {
    assert.equal(
      new Headers(init?.headers).get('Authorization'),
      'Bearer fixture-token'
    )
    assert.equal(new Headers(init?.headers).get('X-UniHub-CSRF'), 'csrf')
    sent.push(`${init?.method} ${url}`)
    return new Response('{}', { status: 200 })
  }
  try {
    session.acceptFinanceAccess(proof)
    session.accounts = [
      { id: 'payer', role: 'owner', kind: 'personal', name: 'Payer' },
      { id: 'member', role: 'member', kind: 'team', name: 'Team' },
    ]
    session.select('payer')
    session.csrfToken = 'csrf'
    assert.equal(session.canWrite, false)
    for (const [method, path] of [
      ['POST', 'api-keys'],
      ['DELETE', 'api-keys/key'],
      ['PUT', 'usage-budget'],
      ['POST', 'recharges'],
      ['POST', 'recharges/order/sandbox-settle'],
    ]) {
      await session.client.request(
        method,
        `/v1/accounts/payer/${path}`,
        method === 'DELETE' ? {} : { body: {} }
      )
    }
    assert.equal(sent.length, 5)
    for (const [method, path] of [
      ['POST', '/v1/accounts/other/api-keys'],
      ['POST', '/v1/accounts/member/recharges'],
      ['POST', '/v1/accounts/payer/invitations'],
      ['PUT', '/v1/accounts/payer/settings'],
      ['POST', '/v1/accounts'],
      ['PATCH', '/v1/me/profile'],
      ['POST', '/v1/accounts/payer/api-keys/extra'],
    ]) {
      await assert.rejects(
        session.client.request(method, path, { body: {} }),
        /read only/
      )
    }
    session.acceptFinanceAccess({
      ...proof,
      finance_access: {
        ...proof.finance_access,
        write_scopes: ['model-credentials:write'],
      },
    })
    await assert.rejects(
      session.client.request('PUT', '/v1/accounts/payer/usage-budget', {
        body: {},
      }),
      /read only/
    )
    await assert.rejects(
      session.client.request('POST', '/v1/accounts/payer/recharges', {
        body: {},
      }),
      /read only/
    )
    session.acceptFinanceAccess(proof)
    session.select('member')
    await assert.rejects(
      session.client.request('PUT', '/v1/accounts/member/usage-budget', {
        body: {},
      }),
      /read only/
    )
    session.select('payer')
    session.acceptFinanceAccess({
      ...proof,
      finance_access: { ...proof.finance_access, write_scopes: [] },
    })
    await assert.rejects(
      session.client.request('POST', '/v1/accounts/payer/api-keys', {
        body: {},
      }),
      /read only/
    )
    session.acceptFinanceAccess(proof)
    session.financeExpiresAt = now - 1
    await assert.rejects(
      session.client.request('POST', '/v1/accounts/payer/api-keys', {
        body: {},
      }),
      /read only/
    )
    assert.equal(sent.length, 5)
  } finally {
    session.clear()
    globalThis.fetch = original
    useAuthStore.getState().auth.reset()
  }
})
