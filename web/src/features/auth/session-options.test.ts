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
import { test } from 'node:test'

import { parseSessionOptions } from './session-options'

test('accepts only cookie-verified central and native session options', () => {
  assert.deepEqual(
    parseSessionOptions({
      success: true,
      data: {
        sessions: [
          { sid: 'sid-native', realm: 'native', display_name: 'Local owner' },
          {
            sid: 'sid-central',
            realm: 'central',
            display_name: 'Central user',
          },
        ],
        unavailable: false,
      },
    }),
    {
      sessions: [
        { sid: 'sid-native', realm: 'native', display_name: 'Local owner' },
        { sid: 'sid-central', realm: 'central', display_name: 'Central user' },
      ],
      unavailable: false,
    }
  )
  assert.equal(
    parseSessionOptions({
      success: true,
      data: {
        sessions: [
          { sid: 'sid-legacy', realm: 'legacy', display_name: 'Legacy user' },
        ],
        unavailable: false,
      },
    }).sessions[0].realm,
    'legacy'
  )
  assert.throws(() =>
    parseSessionOptions({
      success: true,
      data: {
        sessions: [
          { sid: 'sid-native', realm: 'unknown', display_name: 'user' },
        ],
        unavailable: false,
      },
    })
  )
  assert.throws(() =>
    parseSessionOptions({
      success: true,
      data: {
        sessions: [
          { sid: 'sid-native', realm: 'native', display_name: 'user' },
          { sid: 'sid-native', realm: 'central', display_name: 'user' },
        ],
        unavailable: false,
      },
    })
  )
})
