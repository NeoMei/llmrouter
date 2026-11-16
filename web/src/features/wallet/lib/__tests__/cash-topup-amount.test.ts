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
import { describe, test } from 'node:test'

import {
  CASH_TOPUP_TIERS_CNY,
  cnyToCashUnits,
  validateCashTopupAmount,
} from '../cash-topup'

describe('cash topup amount validation', () => {
  test('exposes exactly the fixed recharge tiers', () => {
    assert.deepEqual([...CASH_TOPUP_TIERS_CNY], [20, 50, 100, 300, 1000])
  })

  test('accepts every fixed tier', () => {
    for (const tier of CASH_TOPUP_TIERS_CNY) {
      assert.equal(validateCashTopupAmount(String(tier)), null)
    }
  })

  test('rejects nonnumeric, negative, and three-decimal input', () => {
    assert.equal(validateCashTopupAmount('abc'), 'invalidAmount')
    assert.equal(validateCashTopupAmount(''), 'invalidAmount')
    assert.equal(validateCashTopupAmount('-1'), 'invalidAmount')
    assert.equal(validateCashTopupAmount('01'), 'invalidAmount')
    assert.equal(validateCashTopupAmount('1.234'), 'invalidAmount')
  })

  test('rejects amounts outside the one yuan and 100000 yuan bounds', () => {
    assert.equal(validateCashTopupAmount('0'), 'belowMin')
    assert.equal(validateCashTopupAmount('0.99'), 'belowMin')
    assert.equal(validateCashTopupAmount('100000.01'), 'aboveMax')
    assert.equal(validateCashTopupAmount('100001'), 'aboveMax')
  })
})

describe('CNY to CNY/6 conversion', () => {
  test('converts every fixed tier to the canonical integer quantity', () => {
    assert.equal(cnyToCashUnits('20'), '20000000')
    assert.equal(cnyToCashUnits('50'), '50000000')
    assert.equal(cnyToCashUnits('100'), '100000000')
    assert.equal(cnyToCashUnits('300'), '300000000')
    assert.equal(cnyToCashUnits('1000'), '1000000000')
  })

  test('converts two-decimal CNY amounts without floating point drift', () => {
    assert.equal(cnyToCashUnits('12.34'), '12340000')
    assert.equal(cnyToCashUnits('1.05'), '1050000')
    assert.equal(cnyToCashUnits('0.01'), '10000')
  })

  test('rejects conversion of an invalid amount', () => {
    assert.throws(() => cnyToCashUnits('1.234'), TypeError)
    assert.throws(() => cnyToCashUnits('abc'), TypeError)
  })
})
