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
      data: { sessions: [{ sid: 'sid-legacy', realm: 'legacy', display_name: 'Legacy user' }], unavailable: false },
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
