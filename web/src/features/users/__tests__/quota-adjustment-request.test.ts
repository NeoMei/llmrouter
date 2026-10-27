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

import { QuotaAdjustmentRequests } from '../lib/quota-adjustment-request'

test('an unknown or pending quota adjustment keeps its operation across a reload', () => {
  const stored = new Map<string, string>()
  const storage = {
    getItem: (key: string) => stored.get(key) ?? null,
    setItem: (key: string, value: string) => {
      stored.set(key, value)
    },
    removeItem: (key: string) => {
      stored.delete(key)
    },
  }
  let next = 0
  const create = () => `operation-${++next}`
  const payload = {
    id: 42,
    action: 'add_quota' as const,
    mode: 'override' as const,
    value: 400,
  }
  const before = new QuotaAdjustmentRequests(storage, create)
  const key = before.key(1, payload)
  before.responded(1, payload, key, {
    success: false,
    code: 'PRODUCT_CREDIT_PENDING',
  })
  const after = new QuotaAdjustmentRequests(storage, create)
  assert.equal(after.key(1, payload), key)
  assert.notEqual(after.key(2, payload), key)
  after.responded(1, payload, key, { success: true })
  assert.notEqual(after.key(1, payload), key)
})
