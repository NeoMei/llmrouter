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

import { getSupportingCapabilities, hasCapability } from './types'

test('capability helper hides unsupported actions', () => {
  const snapshot = {
    version: 'v1',
    capabilities: [
      {
        channel_type: 1,
        channel_name: 'x',
        protocol: 'openai',
        operation: 'files.create',
        supported: false,
      },
    ],
  }
  assert.equal(hasCapability(snapshot, 'files.create'), false)
  assert.equal(hasCapability(snapshot, 'fine_tuning.jobs.create'), false)
})

test('capability helper returns a stable provider declaration list', () => {
  const snapshot = {
    version: 'v1',
    capabilities: [
      {
        channel_type: 2,
        channel_name: 'Zulu',
        protocol: 'openai',
        operation: 'fine_tuning.jobs.create',
        supported: true,
      },
      {
        channel_type: 1,
        channel_name: 'Alpha',
        protocol: 'openai',
        operation: 'fine_tuning.jobs.create',
        supported: true,
      },
    ],
  }
  assert.deepEqual(
    getSupportingCapabilities(snapshot, 'fine_tuning.jobs.create').map(
      (capability) => capability.channel_name
    ),
    ['Alpha', 'Zulu']
  )
})
