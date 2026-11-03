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
import { once } from 'node:events'
import { createServer } from 'node:http'
import { test } from 'node:test'

import { UniHubClient, UniHubError } from '../sdk'
import { ProductSession } from '../session'

test('MFA invalid code remains readable from real HTTP and can be retried', async () => {
  let attempts = 0
  const server = createServer((request, response) => {
    response.setHeader('Content-Type', 'application/json')
    if (request.url === '/v1/identity/authorize/password') {
      response
        .writeHead(401)
        .end(JSON.stringify({ error: 'invalid_credentials' }))
    } else if (request.url === '/v1/identity/authorize/mfa') {
      attempts++
      if (attempts === 1) {
        response.writeHead(401)
        response.end(JSON.stringify({ error: 'invalid_mfa_code' }))
      } else {
        response.end(
          JSON.stringify({
            authorization_id: 'a'.repeat(43),
            code: 'b'.repeat(64),
            expires_in: 300,
          })
        )
      }
    } else if (request.url === '/v1/identity/token') {
      response.end(
        JSON.stringify({
          access_token: 'access',
          id_token: 'id',
          token_type: 'Bearer',
          scope: 'openid profile email',
          expires_in: 300,
        })
      )
    } else {
      response.writeHead(404).end('{}')
    }
  })
  server.listen(0, '127.0.0.1')
  await once(server, 'listening')
  const address = server.address()
  assert.ok(address && typeof address === 'object')
  const session: ProductSession = new ProductSession(
    new UniHubClient({
      baseUrl: `http://127.0.0.1:${address.port}`,
      clientId: 'client',
      redirectUri: 'http://127.0.0.1/callback',
      fetch: (input, init) => fetch(input, { ...init, signal: session.signal }),
    })
  )
  const mfa = { token: 't'.repeat(43), verifier: 'v'.repeat(43) }
  try {
    await assert.rejects(
      session.bound(() =>
        session.client.passwordLogin({ username: 'user', password: 'wrong' })
      ),
      (error: unknown) =>
        error instanceof UniHubError && error.code === 'invalid_credentials'
    )
    await assert.rejects(
      session.bound(() => session.client.mfaLogin(mfa, '000000')),
      (error: unknown) =>
        error instanceof UniHubError && error.code === 'invalid_mfa_code'
    )
    assert.equal(session.client.hasAccessToken(), false)
    const tokens = await session.bound(() =>
      session.client.mfaLogin(mfa, '123456')
    )
    assert.equal(tokens.access_token, 'access')
    assert.equal(attempts, 2)
  } finally {
    session.clear()
    server.close()
    await once(server, 'close')
  }
})
