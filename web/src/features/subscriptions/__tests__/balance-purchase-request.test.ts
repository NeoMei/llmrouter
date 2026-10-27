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

import { BalancePurchaseRequests } from '../balance-purchase-request'

function browserStorage() {
  const values = new Map<string, string>()
  return {
    getItem: (key: string) => values.get(key) ?? null,
    setItem: (key: string, value: string) => {
      values.set(key, value)
    },
    removeItem: (key: string) => {
      values.delete(key)
    },
  }
}

test('a retry after an unknown payment reply reuses its purchase key across reloads', () => {
  const storage = browserStorage()
  const first = new BalancePurchaseRequests(storage, () => 'purchase-one')
  assert.equal(first.key(42, 9), 'purchase-one')
  const reloaded = new BalancePurchaseRequests(storage, () => 'purchase-two')
  assert.equal(reloaded.key(42, 9), 'purchase-one')
  reloaded.responded(42, 9, 'purchase-one', {
    success: false,
    code: 'PRODUCT_PAYMENT_PENDING',
  })
  assert.equal(reloaded.key(42, 9), 'purchase-one')
})

test('a confirmed purchase or declined payment permits a new purchase action', () => {
  const storage = browserStorage()
  let next = 0
  const requests = new BalancePurchaseRequests(
    storage,
    () => `purchase-${++next}`
  )
  const first = requests.key(42, 9)
  requests.responded(42, 9, first, { success: true })
  assert.equal(requests.key(42, 9), 'purchase-2')
  requests.responded(42, 9, 'purchase-2', { success: false })
  assert.equal(requests.key(42, 9), 'purchase-3')
})

test('pending purchase keys are isolated between users and plans', () => {
  const requests = new BalancePurchaseRequests(
    browserStorage(),
    (() => {
      let n = 0
      return () => `purchase-${++n}`
    })()
  )
  assert.equal(requests.key(42, 9), 'purchase-1')
  assert.equal(requests.key(43, 9), 'purchase-2')
  assert.equal(requests.key(42, 10), 'purchase-3')
  requests.responded(42, 9, 'older-request', { success: true })
  assert.equal(requests.key(42, 9), 'purchase-1')
})
