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

import type { WeChatNativeOrder, WeChatNativeOrderResponse } from '../../types'
import {
  isTerminalWeChatOrderStatus,
  pollWeChatOrder,
} from '../use-wechat-native-payment'

function orderWithStatus(
  status: WeChatNativeOrder['status'],
  creditStatus?: 'pending' | 'applied'
): WeChatNativeOrderResponse {
  return {
    success: true,
    data: {
      trade_no: 'WXN0123456789abcdef0123456789abc',
      code_url: 'weixin://wxpay/bizpayurl?pr=test',
      amount: '50.00',
      currency: 'CNY',
      credited_quota: creditStatus ? 0 : 3424658,
      expires_at: 1788189000,
      status,
      ...(creditStatus && {
        funding_asset: 'CNY/6',
        funding_unit_version: 'lr-cny-v1',
        credit_amount: '50000000',
        credit_status: creditStatus,
      }),
    },
  }
}

describe('WeChat order polling', () => {
  test('cash payment confirmation keeps reading the same order until ledger credit is applied', async () => {
    const responses = [
      orderWithStatus('pending', 'pending'),
      orderWithStatus('success', 'pending'),
      orderWithStatus('success', 'pending'),
      orderWithStatus('success', 'applied'),
    ]
    const updates: string[] = []
    let fetches = 0
    let waits = 0

    const result = await pollWeChatOrder({
      tradeNo: 'WXN0123456789abcdef0123456789abc',
      signal: new AbortController().signal,
      fetchOrder: async (tradeNo) => {
        assert.equal(tradeNo, 'WXN0123456789abcdef0123456789abc')
        const response = responses[fetches++]
        assert.ok(response)
        return response
      },
      wait: async () => {
        waits++
      },
      onUpdate: (order) =>
        updates.push(`${order.status}:${order.credit_status}`),
    })

    assert.equal(result?.credit_status, 'applied')
    assert.equal(fetches, 4)
    assert.equal(waits, 3)
    assert.deepEqual(updates, [
      'pending:pending',
      'success:pending',
      'success:pending',
      'success:applied',
    ])
  })

  test('cash payment confirmation without a credit status waits for explicit ledger delivery', async () => {
    const confirmed = orderWithStatus('success')
    assert.ok(confirmed.data)
    confirmed.data.funding_asset = 'CNY/6'
    confirmed.data.funding_unit_version = 'lr-cny-v1'
    confirmed.data.credit_amount = '50000000'
    confirmed.data.credited_quota = 0
    const responses = [confirmed, orderWithStatus('success', 'applied')]
    let fetches = 0

    const result = await pollWeChatOrder({
      tradeNo: 'WXN0123456789abcdef0123456789abc',
      signal: new AbortController().signal,
      fetchOrder: async () => {
        const response = responses[fetches++]
        assert.ok(response)
        return response
      },
      wait: async () => undefined,
      onUpdate: () => {},
    })

    assert.equal(result?.credit_status, 'applied')
    assert.equal(fetches, 2)
  })

  test('stops immediately after the server reports a terminal status', async () => {
    const responses = [
      orderWithStatus('pending'),
      orderWithStatus('pending'),
      orderWithStatus('success'),
    ]
    const updates: string[] = []
    let fetches = 0
    let waits = 0

    const result = await pollWeChatOrder({
      tradeNo: 'WXN0123456789abcdef0123456789abc',
      signal: new AbortController().signal,
      fetchOrder: async () => {
        const response = responses[fetches]
        fetches++
        assert.ok(response)
        return response
      },
      wait: async () => {
        waits++
      },
      onUpdate: (order) => updates.push(order.status),
    })

    assert.equal(result?.status, 'success')
    assert.equal(fetches, 3)
    assert.equal(waits, 2)
    assert.deepEqual(updates, ['pending', 'pending', 'success'])
  })

  test('stops without another request when the dialog subscription is cancelled', async () => {
    const controller = new AbortController()
    let fetches = 0

    const result = await pollWeChatOrder({
      tradeNo: 'WXN0123456789abcdef0123456789abc',
      signal: controller.signal,
      fetchOrder: async () => {
        fetches++
        return orderWithStatus('pending')
      },
      wait: async () => undefined,
      onUpdate: () => controller.abort(),
    })

    assert.equal(result?.status, 'pending')
    assert.equal(fetches, 1)
  })

  test('classifies every local terminal status', () => {
    assert.equal(isTerminalWeChatOrderStatus('success'), true)
    assert.equal(isTerminalWeChatOrderStatus('failed'), true)
    assert.equal(isTerminalWeChatOrderStatus('expired'), true)
    assert.equal(isTerminalWeChatOrderStatus('pending'), false)
  })
})
