import assert from 'node:assert/strict';
import { chmod, mkdtemp, mkdir, symlink, writeFile } from 'node:fs/promises';
import { tmpdir } from 'node:os';
import path from 'node:path';
import test from 'node:test';

import { helperArgs, resolveOnboardHelper } from './onboard-helper.js';

test('rejects a symlinked onboard helper', async () => {
  const home = await mkdtemp(path.join(tmpdir(), 'llmapi-helper-'));
  const bin = path.join(home, '.llmapi', 'bin');
  await mkdir(bin, { recursive: true, mode: 0o700 });
  const target = path.join(home, 'target');
  await writeFile(target, '#!/bin/sh\n', { mode: 0o700 });
  await symlink(target, path.join(bin, 'llmapi-onboard'));
  assert.throws(() => resolveOnboardHelper(home), /unsafe onboarding helper/);
});

test('passes tokens only through the inherited environment', () => {
  assert.deepEqual(helperArgs('/tmp/home', 'install', ['--clients', 'codex-cli']), [
    '--home', '/tmp/home', 'install', '--clients', 'codex-cli',
  ]);
});
