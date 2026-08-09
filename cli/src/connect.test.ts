import assert from 'node:assert/strict';
import { createServer } from 'node:http';
import test from 'node:test';

import { normalizeServerUrl, verifyConnection } from './api.js';

test('normalizes safe gateway URLs and rejects credentials', () => {
  assert.equal(normalizeServerUrl('https://gateway.example/'), 'https://gateway.example');
  assert.equal(normalizeServerUrl('http://localhost:3000/v1'), 'http://localhost:3000');
  assert.throws(() => normalizeServerUrl('https://token@gateway.example'));
  assert.throws(() => normalizeServerUrl('http://gateway.example'));
});

test('verifies connectivity without exposing the bearer token', async () => {
  const token = 'connection-secret';
  const server = createServer((request, response) => {
    assert.equal(request.url, '/v1/models');
    assert.equal(request.headers.authorization, `Bearer ${token}`);
    response.writeHead(200).end('{}');
  });
  await new Promise<void>((resolve) => server.listen(0, '127.0.0.1', resolve));
  const address = server.address();
  assert(address && typeof address !== 'string');

  try {
    assert.equal(
      await verifyConnection(`http://127.0.0.1:${address.port}`, token, { timeoutMs: 1_000 }),
      true
    );
  } finally {
    await new Promise<void>((resolve, reject) =>
      server.close((error) => (error ? reject(error) : resolve()))
    );
  }
});

test('connection failures have a deterministic false result', async () => {
  assert.equal(
    await verifyConnection('http://127.0.0.1:1', 'never-log-me', { timeoutMs: 50 }),
    false
  );
});
