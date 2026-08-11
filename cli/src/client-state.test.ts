import assert from 'node:assert/strict';
import test from 'node:test';
import { configureClaude, configureCodex } from './client-state.js';
test('compatibility configuration requires a token before invoking trusted onboarding', async () => {
  await assert.rejects(() => configureCodex({ home: '/missing', serverUrl: 'https://gateway.test', apiToken: '', model: 'model' }), /LLMAPI_TOKEN/);
  await assert.rejects(() => configureClaude({ home: '/missing', serverUrl: 'https://gateway.test', apiToken: '' }), /LLMAPI_TOKEN/);
});
