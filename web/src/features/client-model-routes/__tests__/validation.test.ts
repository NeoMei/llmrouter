/*
Copyright (C) 2023-2026 QuantumNous

This program is free software: you can redistribute it and/or modify
it under the terms of the GNU Affero General Public License as
published by the Free Software Foundation, either version 3 of the
License, or (at your option) any later version.

This program is distributed in the hope that it will be useful,
but WITHOUT ANY WARRANTY; without even the implied warranty of
MERCHANTABILITY or FITNESS FOR A PARTICULAR PURPOSE. See the
GNU Affero General Public License for more details.

You should have received a copy of the GNU Affero General Public License
along with this program. If not, see <https://www.gnu.org/licenses/>.

For commercial licensing, please contact support@quantumnous.com
*/
import assert from 'node:assert/strict'
import test from 'node:test'

import { getClientModelRouteErrorMessage } from '../api'
import {
  parsePreferredWires,
  serializePreferredWires,
  validateAliases,
} from '../validation'

test('duplicate aliases are rejected case-insensitively', () => {
  assert.equal(validateAliases(['gpt-5.6-sol', 'deepseek-v4']), true)
  assert.equal(validateAliases(['gpt-5.6-sol', ' GPT-5.6-SOL ']), false)
})

test('wire preferences ignore invalid values and keep the supported order', () => {
  assert.deepEqual(parsePreferredWires('["anthropic","future","responses"]'), [
    'anthropic',
    'responses',
  ])
  assert.deepEqual(parsePreferredWires('not-json'), [])
  assert.equal(
    serializePreferredWires(['anthropic', 'responses']),
    '["responses","anthropic"]'
  )
})

test('server validation message takes precedence over the generic HTTP error', () => {
  const error = {
    message: 'Request failed with status code 409',
    response: { data: { message: 'alias "shared" is already in use' } },
  }

  assert.equal(
    getClientModelRouteErrorMessage(error, 'Failed to save route'),
    'alias "shared" is already in use'
  )
})

test('unknown API failures use the localized fallback', () => {
  assert.equal(
    getClientModelRouteErrorMessage({ response: { data: {} } }, 'Try again'),
    'Try again'
  )
})
