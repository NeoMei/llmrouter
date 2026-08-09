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

import { isFineTuningTerminal, sortFineTuningEvents } from './api'

test('fine-tuning events render in stable order', () => {
  const events = sortFineTuningEvents([
    { object: 'event', created_at: 2, level: 'info', message: 'done' },
    { object: 'event', created_at: 1, level: 'info', message: 'queued' },
  ])
  assert.deepEqual(
    events.map((event) => event.message),
    ['queued', 'done']
  )
})

test('fine-tuning terminal states disable cancellation', () => {
  assert.equal(isFineTuningTerminal('running'), false)
  assert.equal(isFineTuningTerminal('succeeded'), true)
  assert.equal(isFineTuningTerminal('failed'), true)
  assert.equal(isFineTuningTerminal('cancelled'), true)
})
