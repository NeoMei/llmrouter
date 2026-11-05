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

import { exactAmount } from '../../unihub/session'
import { userSchema } from '../types'

const user = {
  id: 42,
  username: 'cash-user',
  display_name: 'Cash User',
  role: 1,
  status: 1,
  group: 'default',
  quota: 5000000,
  used_quota: 500000,
  request_count: 20,
}
const funding = {
  mode: 'unified_cash',
  account_id: 'payer-1',
  asset: 'CNY/6',
  unit_version: 'lr-cny-v1',
  available: '9007199254740993123',
  held: '0',
  consumed: '1',
}

test('user decoding retains exact shared cash separately from product quota', () => {
  assert.deepEqual(userSchema.parse({ ...user, funding }), { ...user, funding })
  assert.deepEqual(userSchema.parse(user), user)
  const { account_id: _account, ...oldFunding } = funding
  assert.deepEqual(userSchema.parse({ ...user, funding: oldFunding }), {
    ...user,
    funding: oldFunding,
  })
})

test('user decoding rejects noncanonical cash quantities and unsupported money units', () => {
  for (const invalid of [
    { ...funding, available: 9007199254740992 },
    { ...funding, available: '01' },
    { ...funding, held: '-1' },
    { ...funding, consumed: '1.5' },
    { ...funding, asset: 'LR_QUOTA/6' },
    { ...funding, unit_version: 'unknown' },
  ]) {
    assert.equal(
      userSchema.safeParse({ ...user, funding: invalid }).success,
      false
    )
  }
})

test('cash display keeps zero, micro-CNY and amounts above safe integer precision exact', () => {
  for (const [amount, expected] of [
    ['0', '0.000000 CNY'],
    ['1', '0.000001 CNY'],
    ['1200001', '1.200001 CNY'],
    ['9007199254740993123', '9007199254740.993123 CNY'],
  ]) {
    assert.equal(exactAmount(amount, 'CNY/6'), expected)
  }
})
