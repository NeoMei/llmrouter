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

import { ManagedAccess, limitAmount } from '../managed-access'
import { UniHubClient } from '../sdk'

test('managed key requests bind the selected payer, product and instance and preserve exact limits', async () => {
  const requests: { url: string; method?: string; body: unknown }[] = []
  const client = new UniHubClient({
    baseUrl: 'https://hub.test',
    clientId: 'router',
    redirectUri: 'https://router.test/callback',
    fetch: async (url, init) => {
      requests.push({
        url: String(url),
        method: init?.method,
        body: init?.body ? JSON.parse(String(init.body)) : undefined,
      })
      return new Response(
        init?.method === 'DELETE'
          ? null
          : JSON.stringify(
              init?.method === 'GET' ? [] : { id: 'key-1', token: 'shown-once' }
            ),
        { status: init?.method === 'DELETE' ? 204 : 200 }
      )
    },
  })
  const access = new ManagedAccess(client, 'payer-A', 'llmrouter', 'production')
  await access.createKey({
    name: 'work',
    models: ['model-1'],
    funding_mode: 'prepaid_cash',
    max_total: '9007199254740993123',
    max_per_call: '0',
  })
  await access.keys()
  await access.revokeKey('key-1')
  assert.deepEqual(requests, [
    {
      url: 'https://hub.test/v1/accounts/payer-A/api-keys',
      method: 'POST',
      body: {
        name: 'work',
        models: ['model-1'],
        funding_mode: 'prepaid_cash',
        max_total: '9007199254740993123',
        max_per_call: '0',
        origin_product_id: 'llmrouter',
        instance_id: 'production',
      },
    },
    {
      url: 'https://hub.test/v1/accounts/payer-A/api-keys?product_id=llmrouter&instance_id=production',
      method: 'GET',
      body: undefined,
    },
    {
      url: 'https://hub.test/v1/accounts/payer-A/api-keys/key-1?product_id=llmrouter&instance_id=production',
      method: 'DELETE',
      body: undefined,
    },
  ])
})

test('budget writes retain asset selection and compare-and-set version without floating point amounts', async () => {
  let captured: unknown
  const client = new UniHubClient({
    baseUrl: 'https://hub.test',
    clientId: 'router',
    redirectUri: 'https://router.test/callback',
    fetch: async (url, init) => {
      captured = { url: String(url), body: JSON.parse(String(init?.body)) }
      return new Response('{}')
    },
  })
  await new ManagedAccess(client, 'A', 'llmrouter', 'default').setBudget(
    'credits',
    { version: 7, max_total: '123000000', max_per_call: '' }
  )
  assert.deepEqual(captured, {
    url: 'https://hub.test/v1/accounts/A/usage-budget?product_id=llmrouter&instance_id=default&funding_mode=credits',
    body: { version: 7, max_total: '123000000', max_per_call: '' },
  })
  assert.equal(limitAmount('9007199254740.993123'), '9007199254740993123')
  assert.equal(limitAmount('0'), '0')
  assert.equal(limitAmount(''), '')
  for (const invalid of ['-1', '01', '1e3', '1.1234567']) {
    assert.throws(() => limitAmount(invalid))
  }
})

test('key list uses the real central DTO without operation grants', async () => {
  const client = new UniHubClient({
    baseUrl: 'https://hub.test',
    clientId: 'sw',
    redirectUri: 'https://sw.test/callback',
    fetch: async () =>
      new Response(
        JSON.stringify([
          {
            id: 'key',
            credential_kind: 'api_key',
            name: 'name',
            actor_id: 'actor',
            account_id: 'payer',
            origin_product_id: 'superwagie',
            instance_id: 'default',
            models: ['chat'],
            funding_mode: 'prepaid_cash',
            asset: 'CNY/6',
            billing_profile_version: 1,
            expires_at: null,
            created_at: '2026-01-01T00:00:00Z',
            revoked_at: null,
            max_total: '',
            max_per_call: '',
            held: '0',
            used: '0',
            remaining: null,
          },
        ])
      ),
  })
  const keys = await new ManagedAccess(
    client,
    'payer',
    'superwagie',
    'default'
  ).keys()
  assert.deepEqual(keys[0].models, ['chat'])
  assert.equal('operations' in keys[0], false)
})
