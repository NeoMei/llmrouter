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
