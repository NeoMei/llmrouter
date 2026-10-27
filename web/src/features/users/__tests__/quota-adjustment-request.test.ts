import assert from 'node:assert/strict'
import test from 'node:test'

import { QuotaAdjustmentRequests } from '../lib/quota-adjustment-request'

test('an unknown or pending quota adjustment keeps its operation across a reload', () => {
  const stored = new Map<string, string>()
  const storage = { getItem: (key: string) => stored.get(key) ?? null, setItem: (key: string, value: string) => { stored.set(key, value) }, removeItem: (key: string) => { stored.delete(key) } }
  let next = 0
  const create = () => `operation-${++next}`
  const payload = { id: 42, action: 'add_quota' as const, mode: 'override' as const, value: 400 }
  const before = new QuotaAdjustmentRequests(storage, create)
  const key = before.key(1, payload)
  before.responded(1, payload, key, { success: false, code: 'PRODUCT_CREDIT_PENDING' })
  const after = new QuotaAdjustmentRequests(storage, create)
  assert.equal(after.key(1, payload), key)
  assert.notEqual(after.key(2, payload), key)
  after.responded(1, payload, key, { success: true })
  assert.notEqual(after.key(1, payload), key)
})
