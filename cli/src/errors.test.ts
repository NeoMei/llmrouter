import assert from 'node:assert/strict';
import test from 'node:test';

import { connectionFailureMessage, redactSecrets } from './errors.js';

test('redacts secrets from diagnostic text', () => {
  const secret = 'sk-production-secret';
  assert.equal(redactSecrets(`request failed for ${secret}`, [secret]), 'request failed for [REDACTED]');
  assert.equal(connectionFailureMessage('https://gateway.example', secret).includes(secret), false);
});
