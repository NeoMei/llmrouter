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

import { registrationDefinitivelyRejected } from '../registration'
import { UniHubError } from '../sdk'

test('definitive pre-create validation or reconciled conflict unlocks registration correction', () => {
  for (const [status, code] of [
    [400, 'invalid_request'],
    [400, 'invalid_password'],
    [403, 'client_not_enabled'],
    [403, 'origin_not_allowed'],
    [409, 'registration_conflict'],
    [409, 'interaction_required'],
  ] as const) {
    assert.equal(
      registrationDefinitivelyRejected(new UniHubError(status, code, {})),
      true,
      code
    )
  }
})
test('network pending idempotency or unrecognized service failure keeps original registration locked', () => {
  for (const error of [
    new TypeError('Failed to fetch'),
    new UniHubError(503, 'service_unavailable', {}),
    new UniHubError(409, 'idempotency_conflict', {}),
    new UniHubError(409, 'registration_pending', {}),
    new UniHubError(400, 'invalid_json_response', {}),
    new UniHubError(403, 'unknown_code', {}),
  ]) {
    assert.equal(registrationDefinitivelyRejected(error), false, error.message)
  }
})

test('registration throttle reads server proof and prefers Retry-After seconds', async () => {
  const { registrationThrottle } = await import('../registration')
  assert.deepEqual(
    registrationThrottle(
      new UniHubError(
        429,
        'rate_limited',
        { registration_not_started: true, retry_after: 60 },
        new Headers({ 'Retry-After': '10' })
      ),
      0
    ),
    { notStarted: true, retryAfterMs: 10000 }
  )
})
test('registration throttle supports HTTP dates and conservative body or missing-header fallback', async () => {
  const { registrationThrottle } = await import('../registration')
  const now = Date.UTC(2026, 9, 27)
  const error = (header: string, body: unknown = {}) =>
    new UniHubError(
      429,
      'rate_limited',
      body,
      new Headers({ 'Retry-After': header })
    )
  assert.equal(
    registrationThrottle(error(new Date(now + 10000).toUTCString()), now)
      ?.retryAfterMs,
    10000
  )
  assert.equal(
    registrationThrottle(error('-1', { retry_after: 7 }), now)?.retryAfterMs,
    7000
  )
  assert.equal(
    registrationThrottle(error('invalid', { retry_after: -1 }), now)
      ?.retryAfterMs,
    60000
  )
  assert.equal(registrationThrottle(error('0'), now)?.retryAfterMs, 1000)
  assert.equal(
    registrationThrottle(error('10', { registration_not_started: 'true' }), now)
      ?.notStarted,
    false
  )
  assert.equal(
    registrationThrottle(new UniHubError(503, 'rate_limited', {}), now),
    null
  )
  assert.equal(
    registrationThrottle(new UniHubError(429, 'unknown_code', {}), now),
    null
  )
})
