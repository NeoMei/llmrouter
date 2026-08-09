import assert from 'node:assert/strict';
import { mkdtemp, readFile, stat } from 'node:fs/promises';
import { tmpdir } from 'node:os';
import path from 'node:path';
import test from 'node:test';

import { writeClaudeConfig } from './claude.js';
import { writeCodexConfig } from './codex.js';

test('writes deterministic Codex config and secrets with mode 0600', async () => {
  const homeDir = await mkdtemp(path.join(tmpdir(), 'codex-gateway-cli-'));
  const token = `secret-'with-quote`;

  await writeCodexConfig('https://gateway.example', token, { homeDir, shell: '/bin/zsh' });
  await writeCodexConfig('https://gateway.example', token, { homeDir, shell: '/bin/zsh' });

  const configPath = path.join(homeDir, '.codex', 'config.toml');
  const profilePath = path.join(homeDir, '.zshrc');
  const config = await readFile(configPath, 'utf8');
  const profile = await readFile(profilePath, 'utf8');

  assert.equal((await stat(configPath)).mode & 0o777, 0o600);
  assert.equal((await stat(profilePath)).mode & 0o777, 0o600);
  assert.equal(config.includes(token), false);
  assert.equal((profile.match(/CODEX_GATEWAY_KEY/g) ?? []).length, 1);
  assert.equal(profile.includes('\nexport PWNED='), false);
});

test('Claude configuration never prints its API secret', async () => {
  const homeDir = await mkdtemp(path.join(tmpdir(), 'codex-gateway-claude-'));
  const token = 'anthropic-super-secret';
  const messages: string[] = [];

  await writeClaudeConfig('https://gateway.example', token, {
    homeDir,
    shell: '/bin/bash',
    log: (message) => messages.push(message),
  });

  assert.equal(messages.join('\n').includes(token), false);
  assert.equal((await stat(path.join(homeDir, '.bashrc'))).mode & 0o777, 0o600);
});
