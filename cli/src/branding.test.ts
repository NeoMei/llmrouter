import assert from 'node:assert/strict';
import { mkdtemp, symlink, writeFile } from 'node:fs/promises';
import { tmpdir } from 'node:os';
import path from 'node:path';
import test from 'node:test';

import { createProgram, isMainModule } from './index.js';

test('public CLI is LLMAPI branded and exposes reversible commands', () => {
  const program = createProgram();

  assert.equal(program.name(), 'llmapi');
  assert.deepEqual(
    program.commands.map((command) => command.name()).sort(),
    ['doctor', 'init', 'restore', 'status', 'uninstall']
  );
});

test('CLI entrypoint recognizes paths that require URL encoding', () => {
  assert.equal(
    isMainModule('file:///tmp/opencodex%20%E7%A7%BB%E6%A4%8D/dist/index.js', '/tmp/opencodex 移植/dist/index.js'),
    true
  );
});

test('CLI entrypoint recognizes an npm-style symlink alias', async () => {
  const dir = await mkdtemp(path.join(tmpdir(), 'llmapi-bin-'));
  const target = path.join(dir, 'index.js');
  const alias = path.join(dir, 'llmapi');
  await writeFile(target, '');
  await symlink(target, alias);
  assert.equal(isMainModule(new URL(`file://${target}`).href, alias), true);
});
