import assert from 'node:assert/strict';
import { existsSync } from 'node:fs';
import { mkdtemp, readFile, readdir, stat } from 'node:fs/promises';
import { tmpdir } from 'node:os';
import path from 'node:path';
import test from 'node:test';

import { configureClaude, configureCodex, loadReceipt, restoreClient, sha256, storeBackup, uninstallClient } from './client-state.js';

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
  assert.equal(profile.match(/LLMAPI:BEGIN claude/g)?.length, 1);
});

test('configure removes every stale duplicate managed block', async () => {
  const home = await makeHome();
  const profile = path.join(home, '.zshrc');
  const stale = '# LLMAPI:BEGIN claude\nexport ANTHROPIC_API_KEY="${LLMAPI_TOKEN}"\n# LLMAPI:END claude\n';
  await import('node:fs/promises').then((fs) => fs.writeFile(profile, `${stale}export KEEP=1\n${stale}`, { mode: 0o600 }));

  await configureClaude({ home, shell: '/bin/zsh', serverUrl: 'https://gateway.test', apiToken: 'secret' });
  const configured = await readFile(profile, 'utf8');

  assert.equal(configured.match(/LLMAPI:BEGIN claude/g)?.length, 1);
  assert.match(configured, /export KEEP=1/);
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
  assert.ok(config.includes('model_provider = "llmapi"'));
  assert.ok(config.includes('env_key = "LLMAPI_TOKEN"'));
  assert.equal(existsSync(path.join(home, '.zshrc')), false);
});

test('saved receipt file is mode 0600', async () => {
  const home = await makeHome();
  await configureClaude({ home, shell: '/bin/zsh', serverUrl: 'https://gateway.test', apiToken: 'secret' });
  const receiptDir = path.join(home, '.llmapi', 'receipts');
  const entries = await import('node:fs/promises').then((fs) => fs.readdir(receiptDir));
  assert.equal(entries.length, 1);
  assert.equal((await stat(path.join(receiptDir, entries[0]))).mode & 0o777, 0o600);
});

test('receipt never serializes environment values', async () => {
  const home = await makeHome();
  await configureClaude({ home, shell: '/bin/zsh', serverUrl: 'https://gateway.test', apiToken: 'super-secret' });

  const receiptDir = path.join(home, '.llmapi', 'receipts');
  const [name] = await readdir(receiptDir);
  const receipt = await readFile(path.join(receiptDir, name), 'utf8');

  assert.doesNotMatch(receipt, /super-secret|ANTHROPIC_API_KEY.*:/);
});

test('Codex configuration preserves unrelated bytes and converges on one LLMAPI provider', async () => {
  const home = await makeHome();
  const configPath = path.join(home, '.codex', 'config.toml');
  const original = '# keep this comment\nmodel = "native"\nextra = "unchanged"\n\n[model_providers.other]\nname = "Other"\n';
  await import('node:fs/promises').then((fs) => fs.mkdir(path.dirname(configPath), { recursive: true, mode: 0o700 }));
  await import('node:fs/promises').then((fs) => fs.writeFile(configPath, original, { mode: 0o600 }));

  await configureCodex({
    home,
    shell: '/bin/zsh',
    serverUrl: 'https://gateway.test',
    apiToken: 'never-write-me',
    model: 'model-a',
  });
  const configured = await readFile(configPath, 'utf8');
  await configureCodex({
    home,
    shell: '/bin/zsh',
    serverUrl: 'https://gateway.test',
    apiToken: 'never-write-me',
    model: 'model-a',
  });

  assert.match(configured, /# keep this comment/);
  assert.match(configured, /extra = "unchanged"/);
  assert.match(configured, /\[model_providers.other\]/);
  assert.match(configured, /# LLMAPI:BEGIN codex/);
  assert.match(configured, /model_provider = "llmapi"/);
  assert.match(configured, /\[model_providers.llmapi\]/);
  assert.doesNotMatch(configured, /never-write-me/);
  assert.equal(await readFile(configPath, 'utf8'), configured);
});

test('uninstall removes only matching LLMAPI configuration and restore reverses it', async () => {
  const home = await makeHome();
  const receipt = await configureClaude({
    home,
    shell: '/bin/zsh',
    serverUrl: 'https://gateway.test',
    apiToken: 'secret',
  });
  const configured = await readFile(receipt.files[0].path, 'utf8');

  const uninstallReceipt = await uninstallClient(home, 'claude');
  const uninstalled = await readFile(receipt.files[0].path, 'utf8');
  assert.doesNotMatch(uninstalled, /LLMAPI:BEGIN claude/);

  await restoreClient(uninstallReceipt);
  assert.equal(await readFile(receipt.files[0].path, 'utf8'), configured);
});

test('legacy Codex Gateway state is detected but remains unchanged', async () => {
  const home = await makeHome();
  const legacyReceipt = path.join(home, '.codex-gateway', 'receipts', 'legacy.json');
  await import('node:fs/promises').then((fs) => fs.mkdir(path.dirname(legacyReceipt), { recursive: true, mode: 0o700 }));
  await import('node:fs/promises').then((fs) => fs.writeFile(legacyReceipt, '{"legacy":true}', { mode: 0o600 }));

  const receipt = await configureClaude({
    home,
    shell: '/bin/zsh',
    serverUrl: 'https://gateway.test',
    apiToken: 'secret',
  });

  assert.equal(receipt.legacyStateDetected, true);
  assert.equal(await readFile(legacyReceipt, 'utf8'), '{"legacy":true}');
});

test('uninstall refuses to remove a manually changed file', async () => {
  const home = await makeHome();
  const receipt = await configureClaude({
    home,
    shell: '/bin/zsh',
    serverUrl: 'https://gateway.test',
    apiToken: 'secret',
  });
  await import('node:fs/promises').then((fs) => fs.writeFile(receipt.files[0].path, 'manual change', { mode: 0o600 }));

  await assert.rejects(() => uninstallClient(home, 'claude'), /changed/);
});

test('restore receipt selection uses modification time rather than filename order', async () => {
  const home = await makeHome();
  const profile = path.join(home, '.zshrc');
  const configured = '# LLMAPI:BEGIN claude\n# LLMAPI:END claude\n';
  await import('node:fs/promises').then((fs) => fs.writeFile(profile, configured, { mode: 0o600 }));
  const backup = storeBackup(home, '');
  const receiptDir = path.join(home, '.llmapi', 'receipts');
  const receipt = (serverUrl: string) => ({
    version: 1,
    client: 'claude',
    home,
    serverUrl,
    files: [{ path: profile, originalHash: sha256(''), configuredHash: sha256(configured), backupPath: backup }],
    shellProfile: profile,
    envVars: ['LLMAPI_TOKEN'],
  });
  await import('node:fs/promises').then((fs) => fs.mkdir(receiptDir, { recursive: true, mode: 0o700 }));
  await import('node:fs/promises').then((fs) => fs.writeFile(path.join(receiptDir, 'z-old.json'), JSON.stringify(receipt('https://old.test'))));
  await new Promise((resolve) => setTimeout(resolve, 20));
  await import('node:fs/promises').then((fs) => fs.writeFile(path.join(receiptDir, 'a-new.json'), JSON.stringify(receipt('https://new.test'))));

  assert.equal(loadReceipt(home).serverUrl, 'https://new.test');
});
