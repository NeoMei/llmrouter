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
  status: WeChatNativeOrder['status']
): WeChatNativeOrderResponse {
  return {
    success: true,
    data: {
      trade_no: 'WXN0123456789abcdef0123456789abc',
      code_url: 'weixin://wxpay/bizpayurl?pr=test',
      amount: '50.00',
      currency: 'CNY',
      credited_quota: 3424658,
      expires_at: 1788189000,
      status,
    },
  }
}

describe('WeChat order polling', () => {
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
