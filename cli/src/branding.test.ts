import assert from 'node:assert/strict';
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
