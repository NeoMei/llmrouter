import assert from 'node:assert/strict';
import test from 'node:test';
import { normalizeServerUrl } from './api.js';
test('compatibility CLI keeps strict LLMAPI URL validation', () => {
  assert.equal(normalizeServerUrl('https://gateway.example/v1'), 'https://gateway.example');
  assert.throws(() => normalizeServerUrl('https://gateway.example/other'));
});
