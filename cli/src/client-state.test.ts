import assert from 'node:assert/strict';
import { mkdtemp, readFile, stat } from 'node:fs/promises';
import { tmpdir } from 'node:os';
import path from 'node:path';
import test from 'node:test';

import { configureClaude, configureCodex, restoreClient } from './client-state.js';

async function makeHome(): Promise<string> {
  return mkdtemp(path.join(tmpdir(), 'codex-gateway-cli-'));
}

test('configure then restore reproduces the original bytes', async () => {
  const home = await makeHome();
  const original = 'model = "native"\n';
  const configPath = path.join(home, '.codex', 'config.toml');
  await import('node:fs/promises').then((fs) => fs.mkdir(path.dirname(configPath), { recursive: true, mode: 0o700 }));
  await import('node:fs/promises').then((fs) => fs.writeFile(configPath, original, { mode: 0o600 }));

  const receipt = await configureCodex({
    home,
    shell: '/bin/zsh',
    serverUrl: 'https://gateway.test',
    apiToken: 'secret',
    model: 'deepseek/deepseek-v4-flash',
  });

  await restoreClient(receipt);
  const restored = await readFile(configPath, 'utf8');
  assert.equal(restored, original);
});

test('configure is idempotent and never duplicates managed env keys', async () => {
  const home = await makeHome();
  await configureClaude({ home, shell: '/bin/zsh', serverUrl: 'https://gateway.test', apiToken: 'secret' });
  await configureClaude({ home, shell: '/bin/zsh', serverUrl: 'https://gateway.test', apiToken: 'secret' });
  const profile = await readFile(path.join(home, '.zshrc'), 'utf8');
  assert.equal(profile.match(/CODEX-GATEWAY:BEGIN/g)?.length, 1);
});

test('refuses to modify a symlink', async () => {
  const home = await makeHome();
  const profile = path.join(home, '.zshrc');
  const real = path.join(home, '.zshrc.real');
  await import('node:fs/promises').then((fs) => fs.writeFile(real, ''));
  await import('node:fs/promises').then((fs) => fs.symlink(real, profile));

  await assert.rejects(
    () => configureClaude({ home, shell: '/bin/zsh', serverUrl: 'https://gateway.test', apiToken: 'secret' }),
    /symlink/
  );
});

test('restoring after manual change fails with conflict', async () => {
  const home = await makeHome();
  const receipt = await configureClaude({ home, shell: '/bin/zsh', serverUrl: 'https://gateway.test', apiToken: 'secret' });
  const profile = receipt.files[0].path;
  await import('node:fs/promises').then((fs) => fs.writeFile(profile, 'pwned'));
  await assert.rejects(() => restoreClient(receipt), /changed/);
});

test('Codex receipt includes managed block and model', async () => {
  const home = await makeHome();
  const receipt = await configureCodex({
    home,
    shell: '/bin/zsh',
    serverUrl: 'https://gateway.test/',
    apiToken: 'secret',
    model: 'deepseek/deepseek-v4-flash',
  });
  assert.equal(receipt.client, 'codex');
  const config = await readFile(path.join(home, '.codex', 'config.toml'), 'utf8');
  assert.ok(config.includes('model = "deepseek/deepseek-v4-flash"'));
  assert.ok(config.includes('wire_api = "responses"'));
  const profile = await readFile(path.join(home, '.zshrc'), 'utf8');
  assert.ok(profile.includes('CODEX-GATEWAY:BEGIN'));
  assert.ok(profile.includes("export CODEX_GATEWAY_KEY='secret'"));
});

test('saved receipt file is mode 0600', async () => {
  const home = await makeHome();
  await configureClaude({ home, shell: '/bin/zsh', serverUrl: 'https://gateway.test', apiToken: 'secret' });
  const receiptDir = path.join(home, '.codex-gateway', 'receipts');
  const entries = await import('node:fs/promises').then((fs) => fs.readdir(receiptDir));
  assert.equal(entries.length, 1);
  assert.equal((await stat(path.join(receiptDir, entries[0]))).mode & 0o777, 0o600);
});
