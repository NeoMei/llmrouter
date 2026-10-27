import assert from 'node:assert/strict'
import test from 'node:test'

import { BalancePurchaseRequests } from '../balance-purchase-request'

function browserStorage() {
  const values = new Map<string, string>()
  return {
    getItem: (key: string) => values.get(key) ?? null,
    setItem: (key: string, value: string) => { values.set(key, value) },
    removeItem: (key: string) => { values.delete(key) },
  }
}

test('a retry after an unknown payment reply reuses its purchase key across reloads', () => {
  const storage = browserStorage()
  const first = new BalancePurchaseRequests(storage, () => 'purchase-one')
  assert.equal(first.key(42, 9), 'purchase-one')
  const reloaded = new BalancePurchaseRequests(storage, () => 'purchase-two')
  assert.equal(reloaded.key(42, 9), 'purchase-one')
  reloaded.responded(42, 9, 'purchase-one', { success: false, code: 'PRODUCT_PAYMENT_PENDING' })
  assert.equal(reloaded.key(42, 9), 'purchase-one')
})

test('a confirmed purchase or declined payment permits a new purchase action', () => {
  const storage = browserStorage()
  let next = 0
  const requests = new BalancePurchaseRequests(storage, () => `purchase-${++next}`)
  const first = requests.key(42, 9)
  requests.responded(42, 9, first, { success: true })
  assert.equal(requests.key(42, 9), 'purchase-2')
  requests.responded(42, 9, 'purchase-2', { success: false })
  assert.equal(requests.key(42, 9), 'purchase-3')
})

test('pending purchase keys are isolated between users and plans', () => {
  const requests = new BalancePurchaseRequests(browserStorage(), (() => { let n = 0; return () => `purchase-${++n}` })())
  assert.equal(requests.key(42, 9), 'purchase-1')
  assert.equal(requests.key(43, 9), 'purchase-2')
  assert.equal(requests.key(42, 10), 'purchase-3')
  requests.responded(42, 9, 'older-request', { success: true })
  assert.equal(requests.key(42, 9), 'purchase-1')
})
