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

import { cashPaymentPhase, readCashCheckout } from '../cash-checkout-status'

const paid = {
  id: 'ct_paid1',
  account_id: 'payer',
  amount: '20000000',
  payment_status: 'PAID',
  operation_id: 'op_paid1',
} as const

test('payment confirmation waits for the ledger operation before reporting updated funds', async () => {
  const responses = [
    paid,
    { id: 'op_paid1', status: 'CREATED' },
    paid,
    { id: 'op_paid1', status: 'APPLIED' },
  ]
  const deps = {
    getAuthHeaders: async () => ({}),
    fetch: async () => new Response(JSON.stringify(responses.shift())),
  }
  assert.equal(
    cashPaymentPhase(await readCashCheckout(deps, 'payer', 'ct_paid1')),
    'processing'
  )
  assert.equal(
    cashPaymentPhase(await readCashCheckout(deps, 'payer', 'ct_paid1')),
    'success'
  )
})

test('a checkout from another payer or ID cannot be accepted as payment confirmation', async () => {
  for (const body of [
    { ...paid, account_id: 'other' },
    { ...paid, id: 'ct_other' },
  ]) {
    const deps = {
      getAuthHeaders: async () => ({}),
      fetch: async () => new Response(JSON.stringify(body)),
    }
    await assert.rejects(
      () => readCashCheckout(deps, 'payer', 'ct_paid1'),
      /forbidden/
    )
  }
})

test('a delayed credit remains processing after QR expiry and review never becomes success', () => {
  assert.equal(
    cashPaymentPhase({
      ...paid,
      expires_at: '2000-01-01T00:00:00Z',
      credited: false,
    }),
    'processing'
  )
  assert.equal(
    cashPaymentPhase({ ...paid, payment_status: 'REVIEW', credited: false }),
    'review'
  )
  assert.equal(
    cashPaymentPhase({
      ...paid,
      payment_status: 'CREATED',
      expires_at: '2000-01-01T00:00:00Z',
      credited: false,
    }),
    'expired'
  )
})
