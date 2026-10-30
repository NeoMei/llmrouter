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

import { UniHubClient } from '../sdk'
import { exactAmount, ProductSession, FinancialIntent } from '../session'

test('canonical quantities larger than 2^53 retain all digits', () => {
  assert.equal(
    exactAmount('9007199254740993123', 'CNY/6'),
    '9007199254740.993123 CNY'
  )
  assert.throws(() => exactAmount('01', 'CNY/6'))
})
test('account change discards delayed reads and logout clears token', async () => {
  const client = new UniHubClient({
    baseUrl: 'https://hub.test',
    clientId: 'c',
    redirectUri: 'https://app.test/unihub',
  })
  client.setAccessToken('secret')
  const session = new ProductSession(client)
  session.select('A')
  let finish!: (value: string) => void
  const result = session.bound(
    () =>
      new Promise<string>((resolve) => {
        finish = resolve
      })
  )
  session.select('B')
  finish('A data')
  await assert.rejects(result, /changed/)
  session.clear()
  assert.equal(client.hasAccessToken(), false)
  assert.equal(session.account, '')
})
test('uncertain financial intent retains account payload and key', () => {
  const intent = new FinancialIntent('A', 'recharge', { amount: '1000000' })
  const key = intent.key
  assert.equal(intent.key, key)
  assert.equal(intent.account, 'A')
  assert.throws(() => intent.assertAccount('B'), /payer/)
})

test('CNY input converts decimal yuan exactly without Number rounding', async () => {
  const { cnyMillionths } = await import('../session')
  assert.equal(cnyMillionths('10.50'), '10500000')
  assert.equal(cnyMillionths('9007199254740.993123'), '9007199254740993123')
  for (const value of ['0', '-1', '01', '1e3', '1.0000001']) {
    assert.throws(() => cnyMillionths(value))
  }
})

test('PKCE mismatched state never exchanges token or logs in', async () => {
  let calls = 0
  const client = new UniHubClient({
    baseUrl: 'https://hub.test',
    clientId: 'c',
    redirectUri: 'https://app.test/unihub',
    fetch: async () => {
      calls++
      return new Response(
        JSON.stringify({
          authorization_id: 'a'.repeat(43),
          code: 'a'.repeat(64),
          state: 'wrong',
        }),
        { status: 200 }
      )
    },
  })
  await assert.rejects(
    client.passwordLogin({ username: 'user', password: 'private' }),
    /invalid_authorization_response/
  )
  assert.equal(calls, 1)
  assert.equal(client.hasAccessToken(), false)
})

test('401 clears active session and retained financial intent', async () => {
  const { UniHubError } = await import('../sdk')
  const client = new UniHubClient({
    baseUrl: 'https://hub.test',
    clientId: 'c',
    redirectUri: 'https://app.test/unihub',
  })
  const session = new ProductSession(client)
  client.setAccessToken('secret')
  session.select('A')
  session.intents.set(
    'A',
    new FinancialIntent('A', 'purchase', { id: 'offer' })
  )
  await assert.rejects(
    session.bound(async () => {
      throw new UniHubError(401, 'expired', {})
    })
  )
  assert.equal(session.account, '')
  assert.equal(session.intents.size, 0)
  assert.equal(client.hasAccessToken(), false)
})

test('late prior-payer 401 cannot clear a newer selected account', async () => {
  const { UniHubError } = await import('../sdk')
  const client = new UniHubClient({
    baseUrl: 'https://hub.test',
    clientId: 'c',
    redirectUri: 'https://app.test/unihub',
  })
  const session = new ProductSession(client)
  client.setAccessToken('secret')
  session.select('A')
  let reject!: (value: unknown) => void
  const request = session.bound(
    () =>
      new Promise((_, failure) => {
        reject = failure
      })
  )
  session.select('B')
  reject(new UniHubError(401, 'expired', {}))
  await assert.rejects(request)
  assert.equal(session.account, 'B')
  assert.equal(client.hasAccessToken(), true)
})

test('same native id switching identity source clears product token and intents', async () => {
  const { useAuthStore } = await import('@/stores/auth-store')
  const { createProductSession } = await import('../session')
  const auth = useAuthStore.getState().auth
  auth.setUser({
    id: 42,
    username: 'projected',
    role: 1,
    identity_source: 'unihub',
    unihub_user_id: 'subject-a',
  })
  const session = createProductSession({
    enabled: true,
    public_origin: 'https://hub.test',
    client_id: 'c',
    redirect_uri: 'https://app.test/unihub',
    product_id: 'llmrouter',
    models: [],
  })
  session.user = { id: 'subject-a', name: 'Business user', email: '' }
  session.client.setAccessToken('memory-only')
  session.intents.set(
    'purchase',
    new FinancialIntent('A', 'purchase', { id: 'offer' })
  )
  auth.setUser({ id: 42, username: 'local', role: 1, identity_source: 'local' })
  assert.equal(session.client.hasAccessToken(), false)
  assert.equal(session.intents.size, 0)
  assert.equal(session.user, null)
  auth.reset()
})

test('same native id changing immutable subject clears old product authorization', async () => {
  const { useAuthStore } = await import('@/stores/auth-store')
  const { createProductSession } = await import('../session')
  const auth = useAuthStore.getState().auth
  auth.setUser({
    id: 42,
    username: 'projected',
    role: 1,
    identity_source: 'unihub',
    unihub_user_id: 'subject-a',
  })
  const session = createProductSession({
    enabled: true,
    public_origin: 'https://hub.test',
    client_id: 'c',
    redirect_uri: 'https://app.test/unihub',
    product_id: 'llmrouter',
    models: [],
  })
  session.user = { id: 'subject-a', name: 'Business user', email: '' }
  session.client.setAccessToken('memory-only')
  auth.setUser({
    id: 42,
    username: 'projected',
    role: 1,
    identity_source: 'unihub',
    unihub_user_id: 'subject-b',
  })
  assert.equal(session.client.hasAccessToken(), false)
  assert.equal(session.user, null)
  auth.reset()
})

test('global clear advances anonymous authentication epoch and fences pending login', async () => {
  const { clearAuthentication, getAuthEpoch } =
    await import('@/lib/auth-session')
  clearAuthentication(false)
  const captured = getAuthEpoch()
  clearAuthentication(false)
  assert.notEqual(getAuthEpoch(), captured)
})

test('same user SID and authorization version changes invalidate payer, secrets and in-flight work', async () => {
  const { useAuthStore } = await import('@/stores/auth-store')
  const { createProductSession } = await import('../session')
  const sessionView = {
    sid: 'sid-a',
    current: true,
    login_method: 'product_broker',
    ip: '',
    user_agent: '',
    created_at: 1,
    last_active_at: 1,
    expires_at: 4_000_000_000,
  }
  const user = {
    id: 42,
    username: 'member',
    role: 1,
    identity_source: 'unihub',
    unihub_user_id: 'subject-a',
  }
  const token = (sv: number, uv: number) =>
    `e30.${Buffer.from(JSON.stringify({ sv, uv })).toString('base64url')}.signature`
  const bundle = {
    access_token: token(1, 1),
    token_type: 'Bearer',
    access_expires_at: 4_000_000_000,
    user,
    session: sessionView,
  }
  useAuthStore.getState().auth.setBundle(bundle)
  const session = createProductSession({
    enabled: true,
    public_origin: 'https://hub.test',
    client_id: 'c',
    redirect_uri: 'https://app.test/unihub',
    product_id: 'llmrouter',
    models: [],
  })
  session.user = { id: 'subject-a', name: 'Member', email: '' }
  session.csrfToken = 'old-csrf'
  session.account = 'old-payer'
  session.intents.set(
    'old-payer',
    new FinancialIntent('old-payer', 'recharge', { amount: '1' })
  )
  let resolve!: (value: string) => void
  const pending = session.bound(
    () =>
      new Promise<string>((done) => {
        resolve = done
      })
  )
  useAuthStore
    .getState()
    .auth.setBundle({ ...bundle, session: { ...sessionView, sid: 'sid-b' } })
  resolve('old data')
  await assert.rejects(pending, /changed/)
  assert.equal(session.csrfToken, '')
  assert.equal(session.account, '')
  assert.equal(session.intents.size, 0)
  session.user = { id: 'subject-a', name: 'Member', email: '' }
  session.csrfToken = 'csrf2'
  useAuthStore.getState().auth.setBundle({
    ...bundle,
    session: { ...sessionView, sid: 'sid-b' },
    access_token: token(2, 1),
  })
  assert.equal(session.user, null)
  assert.equal(session.csrfToken, '')
  session.user = { id: 'subject-a', name: 'Member', email: '' }
  useAuthStore.getState().auth.setBundle({
    ...bundle,
    session: { ...sessionView, sid: 'sid-b' },
    access_token: token(2, 2),
  })
  assert.equal(session.user, null)
  useAuthStore.getState().auth.reset()
})

test('a late revoked response from the previous SID cannot sign out a new session', async () => {
  const { useAuthStore } = await import('@/stores/auth-store')
  const { refreshProductAccess } = await import('../session')
  const expiry = Math.floor(Date.now() / 1000) + 600
  const user = {
    id: 42,
    username: 'member',
    role: 1,
    identity_source: 'unihub',
    unihub_user_id: 'subject-a',
  }
  const view = {
    sid: 'sid-old',
    current: true,
    login_method: 'unihub',
    ip: '',
    user_agent: '',
    created_at: 1,
    last_active_at: 1,
    expires_at: expiry,
  }
  const bundle = {
    access_token: 'old-token',
    token_type: 'Bearer',
    access_expires_at: expiry,
    user,
    session: view,
  }
  useAuthStore.getState().auth.setBundle(bundle)
  const originalFetch = globalThis.fetch
  let finish!: (response: Response) => void
  globalThis.fetch = () =>
    new Promise<Response>((resolve) => {
      finish = resolve
    })
  try {
    const pending = refreshProductAccess()
    await Promise.resolve()
    useAuthStore.getState().auth.setBundle({
      ...bundle,
      access_token: 'new-token',
      session: { ...view, sid: 'sid-new' },
    })
    finish(new Response('{}', { status: 401 }))
    await assert.rejects(pending, /changed/)
    assert.equal(useAuthStore.getState().auth.session?.sid, 'sid-new')
  } finally {
    globalThis.fetch = originalFetch
    useAuthStore.getState().auth.reset()
  }
})

test('revoked payer access clears payer resources and pending intents without clearing another session', async () => {
  const { UniHubError } = await import('../sdk')
  const session = new ProductSession(
    new UniHubClient({
      baseUrl: 'https://hub.test',
      clientId: 'c',
      redirectUri: 'https://app.test/unihub',
    })
  )
  session.user = { id: 'subject-a', name: 'Member', email: '' }
  session.select('payer')
  session.csrfToken = 'csrf'
  session.intents.set(
    'payer',
    new FinancialIntent('payer', 'recharge', { amount: '1' })
  )
  await assert.rejects(
    session.bound(async () => {
      throw new UniHubError(403, 'revoked', {})
    })
  )
  assert.equal(session.account, '')
  assert.equal(session.csrfToken, '')
  assert.equal(session.intents.size, 0)
  assert.equal(session.user?.id, 'subject-a')
})

test('a late forbidden response from a previous payer cannot clear the newly selected payer', async () => {
  const { UniHubError } = await import('../sdk')
  const session = new ProductSession(
    new UniHubClient({
      baseUrl: 'https://hub.test',
      clientId: 'c',
      redirectUri: 'https://app.test/unihub',
    })
  )
  session.select('payer-a')
  let reject!: (error: Error) => void
  const pending = session.bound(
    () =>
      new Promise<never>((_resolve, failure) => {
        reject = failure
      })
  )
  session.select('payer-b')
  reject(new UniHubError(403, 'old-forbidden', {}))
  await assert.rejects(pending)
  assert.equal(session.account, 'payer-b')
})
