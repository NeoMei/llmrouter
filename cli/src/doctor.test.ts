import assert from 'node:assert/strict';
import test from 'node:test';
import { createProgram } from './index.js';
test('doctor is registered without a token command option', () => {
  const doctor = createProgram().commands.find((command) => command.name() === 'doctor');
  assert.ok(doctor);
  assert.equal(doctor.options.some((option) => option.long === '--token'), false);
});
