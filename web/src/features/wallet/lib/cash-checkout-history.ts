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
import { z } from 'zod'

import { getFreshAuthHeaders } from '@/lib/auth-session'
import { useAuthStore } from '@/stores/auth-store'

import type { TopupRecord, TopupStatus } from '../types'
import { cashCheckoutID } from './cash-checkout-status'

const historySchema = z.array(
  z.object({
    id: z.string().regex(cashCheckoutID),
    account_id: z.string(),
    product_id: z.literal('llmrouter'),
    amount: z.string().regex(/^[1-9]\d{0,11}$/),
    currency: z.literal('CNY'),
    provider: z.enum(['wechat', 'alipay']),
    payment_status: z.enum([
      'CREATED',
      'PENDING',
      'UNKNOWN',
      'PAID',
      'REVIEW',
      'CLOSED',
      'FAILED',
    ]),
    credit_status: z.string().optional(),
    wallet_target: z.enum(['unified', 'product']).optional(),
    created_at: z.iso.datetime({ offset: true }),
    expires_at: z.iso.datetime({ offset: true }),
  })
)

export async function readCashCheckoutHistory(
  account: string,
  signal: AbortSignal
): Promise<TopupRecord[]> {
  const auth = useAuthStore.getState().auth
  const assertAccount = () => {
    const current = useAuthStore.getState().auth
    if (
      signal.aborted ||
      !/^[A-Za-z0-9_-]{1,128}$/.test(account) ||
      current.session?.sid !== auth.session?.sid ||
      current.user?.id !== auth.user?.id ||
      current.user?.funding?.account_id !== account
    ) {
      throw new Error('Cash checkout account changed')
    }
  }
  const headers = await getFreshAuthHeaders()
  assertAccount()
  const response = await fetch(
    `/api/unihub/bff/v1/accounts/${account}/cash-checkouts`,
    {
      headers,
      credentials: 'same-origin',
      cache: 'no-store',
      redirect: 'error',
      signal,
    }
  )
  if (!response.ok) throw new Error('Unable to load cash checkouts')
  const orders = historySchema.parse(await response.json())
  assertAccount()
  if (orders.some((order) => order.account_id !== account)) {
    throw new Error('Unexpected cash checkout account')
  }
  return orders.map((order) => {
    let status: TopupStatus = 'pending'
    if (order.payment_status === 'PAID') status = 'success'
    else if (order.payment_status === 'REVIEW') status = 'review'
    else if (['CLOSED', 'FAILED'].includes(order.payment_status))
      status = 'failed'
    else if (
      order.payment_status === 'CREATED' &&
      Date.parse(order.expires_at) <= Date.now()
    )
      status = 'expired'
    return {
      id: order.id,
      trade_no: order.id,
      user_id: auth.user?.id ?? 0,
      amount: 0,
      money: 0,
      funding_asset: 'CNY/6',
      credit_amount: order.amount,
      create_time: Math.floor(Date.parse(order.created_at) / 1000),
      status,
      credit_status:
        status === 'success' ? creditStatus(order.credit_status) : undefined,
      payment_method: order.provider,
      wallet_target: order.wallet_target ?? 'product',
    }
  })
}

function creditStatus(status?: string): TopupRecord['credit_status'] {
  if (status === 'APPLIED') return 'applied'
  if (status === 'REJECTED' || status === 'CANCELED') return 'failed'
  return 'pending'
}
