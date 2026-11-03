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
import { createHash } from 'node:crypto'
import { test } from 'node:test'

import { UniHubClient, UniHubError } from '../sdk'

test('password login continues through TOTP with the original PKCE verifier', async () => {
  const requests: { path: string; body: Record<string, string> }[] = []
  const client = new UniHubClient({
    baseUrl: 'https://hub.test',
    clientId: 'router',
    redirectUri: 'https://router.test/api/unihub/callback',
    fetch: async (input, init) => {
      const path = new URL(String(input)).pathname
      const body = JSON.parse(String(init?.body)) as Record<string, string>
      requests.push({ path, body })
      if (path.endsWith('/password')) {
        return Response.json({
          error: 'mfa_pending',
          mfa_token: 'm'.repeat(43),
          expires_in: 300,
        })
      }
      if (path.endsWith('/mfa')) {
        return Response.json({
          authorization_id: 'a'.repeat(43),
          code: 'b'.repeat(64),
          expires_in: 300,
        })
      }
      return Response.json({
        access_token: 'access',
        id_token: 'id',
        token_type: 'Bearer',
        scope: 'openid profile email',
        expires_in: 600,
      })
    },
  })
  let mfa: { token: string; verifier: string } | undefined
  try {
    await client.passwordLogin({ username: 'neomei', password: 'password' })
    assert.fail('expected MFA challenge')
  } catch (error) {
    assert.ok(error instanceof Error && 'mfa' in error)
    mfa = error.mfa as { token: string; verifier: string }
  }
  assert.equal(client.hasAccessToken(), false)
  assert.equal(requests.length, 1)
  const tokens = await client.mfaLogin(mfa, '123456')
  assert.equal(tokens.access_token, 'access')
  assert.equal(client.hasAccessToken(), true)
  assert.deepEqual(requests[1], {
    path: '/v1/identity/authorize/mfa',
    body: {
      client_id: 'router',
      mfa_token: 'm'.repeat(43),
      passcode: '123456',
    },
  })
  assert.equal(requests[2].path, '/v1/identity/token')
  assert.equal(requests[2].body.code_verifier, mfa.verifier)
  assert.equal(
    createHash('sha256').update(mfa.verifier).digest('base64url'),
    requests[0].body.code_challenge
  )
})

test('interaction-required policy is surfaced without exchanging a token', async () => {
  let calls = 0
  const client = new UniHubClient({
    baseUrl: 'https://hub.test',
    clientId: 'router',
    redirectUri: 'https://router.test/api/unihub/callback',
    fetch: async () => {
      calls++
      return Response.json(
        {
          error: 'interaction_required',
          interaction: { kind: 'password_change' },
        },
        { status: 409 }
      )
    },
  })
  await assert.rejects(
    client.passwordLogin({ username: 'neomei', password: 'password' }),
    (error: unknown) =>
      error instanceof UniHubError && error.code === 'interaction_required'
  )
  assert.equal(calls, 1)
  assert.equal(client.hasAccessToken(), false)
})
