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

import i18n from 'i18next'

await i18n.init({ lng: 'en', resources: {} })

import { applyAuthBundle, clearAuthentication } from '@/lib/auth-session'
import { useAuthStore, type AuthBundle } from '@/stores/auth-store'

import {
  authorizeProductSession,
  revokeRejectedSession,
} from '../native-session'
import { UniHubClient } from '../sdk'
import { ProductSession } from '../session'

const bundle: AuthBundle = {
  access_token: 'rejected-access',
  token_type: 'Bearer',
  access_expires_at: 9999999999,
  user: {
    id: 42,
    username: 'business',
    role: 1,
    identity_source: 'unihub',
    unihub_user_id: 'subject',
  },
  session: {
    sid: 'rejected-sid',
    current: true,
    login_method: 'unihub',
    ip: '',
    user_agent: '',
    created_at: 1,
    last_active_at: 1,
    expires_at: 9999999999,
  },
}

for (const mode of ['clear', 'newer', 'bound']) {
  const newer = mode === 'newer'
  test(`delayed session response after ${mode} revokes exact stale session without changing current auth`, async (context) => {
    clearAuthentication(false)
    let release!: () => void
    let requested!: () => void
    const waiting = new Promise<void>((resolve) => {
      requested = resolve
    })
    const gate = new Promise<void>((resolve) => {
      release = resolve
    })
    const revoked: string[] = []
    let cookieSID = ''
    const fetchMock: typeof fetch = async (input, init) => {
      const url = String(input)
      if (url.endsWith('/authorize/password')) {
        const body = JSON.parse(String(init?.body)) as { state: string }
        return Response.json({
          authorization_id: 'a'.repeat(43),
          code: 'a'.repeat(64),
          state: body.state,
        })
      }
      if (url.endsWith('/identity/token')) {
        return Response.json({
          access_token: 'product-token',
          id_token: 'id',
          token_type: 'Bearer',
          scope: 'openid profile email',
          expires_in: 300,
        })
      }
      if (url === '/api/unihub/identity') {
        return Response.json({
          user: { id: 'subject', name: 'Business', email: '' },
          accounts: [],
        })
      }
      if (url === '/api/unihub/session') {
        requested()
        await gate
        cookieSID = bundle.session.sid
        return Response.json({ success: true, data: bundle })
      }
      assert.equal(url, '/api/user/auth/logout')
      assert.equal(init?.credentials, 'omit')
      assert.equal(init?.redirect, 'error')
      const headers = new Headers(init?.headers)
      assert.equal(headers.get('Authorization'), 'Bearer rejected-access')
      assert.equal(headers.get('X-Auth-Session'), 'rejected-sid')
      revoked.push(headers.get('X-Auth-Session') ?? '')
      return Response.json({ success: true })
    }
    context.mock.method(globalThis, 'fetch', fetchMock)
    const session = new ProductSession(
      new UniHubClient({
        baseUrl: 'https://hub.test',
        clientId: 'c',
        redirectUri: 'https://app.test/unihub',
        fetch: fetchMock,
      })
    )
    const pending = authorizeProductSession(session, {
      username: 'business',
      password: 'fixture-only',
    })
    await waiting
    const current = {
      ...bundle,
      access_token: 'new-access',
      session: { ...bundle.session, sid: 'new-sid' },
      user: { id: 7, username: 'operator', role: 10 },
    }
    if (newer) applyAuthBundle(current, false)
    else clearAuthentication(false)
    release()
    await assert.rejects(pending, /Product identity proof rejected/)
    assert.deepEqual(revoked, ['rejected-sid'])
    assert.equal(
      revoked.includes(cookieSID),
      true,
      'cookie-restored session has been revoked'
    )
    assert.equal(
      useAuthStore.getState().auth.session?.sid,
      newer ? 'new-sid' : undefined
    )
    session.clear()
    clearAuthentication(false)
  })
}

test('rejected immutable subject revokes valid session bundle and cleanup failure is visible', async (context) => {
  clearAuthentication(false)
  context.mock.method(
    globalThis,
    'fetch',
    async (input: RequestInfo | URL, init?: RequestInit) => {
      const url = String(input)
      if (url.endsWith('/authorize/password')) {
        const body = JSON.parse(String(init?.body)) as { state: string }
        return Response.json({
          authorization_id: 'a'.repeat(43),
          code: 'a'.repeat(64),
          state: body.state,
        })
      }
      if (url.endsWith('/identity/token')) {
        return Response.json({
          access_token: 'product',
          id_token: 'id',
          token_type: 'Bearer',
          scope: 'openid profile email',
          expires_in: 300,
        })
      }
      if (url.endsWith('/identity')) {
        return Response.json({
          user: { id: 'other-subject', name: 'Other', email: '' },
          accounts: [],
        })
      }
      if (url.endsWith('/session')) {
        return Response.json({ success: true, data: bundle })
      }
      assert.equal(url, '/api/user/auth/logout')
      return Response.json({ success: false }, { status: 503 })
    }
  )
  const session = new ProductSession(
    new UniHubClient({
      baseUrl: 'https://hub.test',
      clientId: 'c',
      redirectUri: 'https://app.test/unihub',
    })
  )
  await assert.rejects(
    authorizeProductSession(session, {
      username: 'business',
      password: 'fixture-only',
    }),
    /Session cleanup failed/
  )
  assert.equal(useAuthStore.getState().auth.user, null)
  session.clear()
})

test('exact session cleanup supplies bounded cancellation and never changes auth state', async (context) => {
  clearAuthentication(false)
  applyAuthBundle(
    { ...bundle, session: { ...bundle.session, sid: 'current-sid' } },
    false
  )
  context.mock.method(
    globalThis,
    'fetch',
    async (_input: RequestInfo | URL, init?: RequestInit) => {
      assert.ok(init?.signal)
      assert.equal(init.signal.aborted, false)
      assert.equal(init.credentials, 'omit')
      return Response.json({ success: true })
    }
  )
  await revokeRejectedSession(bundle)
  assert.equal(useAuthStore.getState().auth.session?.sid, 'current-sid')
  clearAuthentication(false)
})
