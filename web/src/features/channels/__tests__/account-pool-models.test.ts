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

import { getAccountPoolModels } from '../account-pool-models'

test('chain terminals and catalog first hops share deduplicated selectable scopes', () => {
  assert.deepEqual(
    getAccountPoolModels(
      'alias,second,distinct,self,cycle-a',
      JSON.stringify({
        alias: 'middle',
        middle: 'canonical',
        second: 'canonical',
        self: 'self',
        'cycle-a': 'cycle-b',
        'cycle-b': 'cycle-a',
      })
    ),
    ['canonical', 'middle', 'distinct', 'self', 'cycle-b']
  )
})
test('absent, empty and self mappings keep the configured provider scope', () => {
  for (const mapping of ['', '{}', '{"model":""}', '{"model":"model"}']) {
    assert.deepEqual(getAccountPoolModels(' model,model,toString ', mapping), [
      'model',
      'toString',
    ])
  }
})
test('malformed mappings remain subject to server validation', () => {
  assert.deepEqual(getAccountPoolModels('model', '{'), ['model'])
})
