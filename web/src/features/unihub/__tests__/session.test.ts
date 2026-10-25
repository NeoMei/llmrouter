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
