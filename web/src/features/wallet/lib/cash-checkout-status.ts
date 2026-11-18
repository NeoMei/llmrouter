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

import {
  cashCheckoutID,
  cashCheckoutSchema,
  CashCheckoutError,
  type CashCheckout,
  type CashCheckoutDeps,
} from './cash-topup'

export { cashCheckoutID } from './cash-topup'

const identifier = /^[A-Za-z0-9_-]{1,128}$/

export type CashCheckoutStatus = CashCheckout & {
  credited: boolean
}
export type CashPaymentPhase =
  | 'waiting'
  | 'processing'
  | 'success'
  | 'closed'
  | 'review'
  | 'expired'

export function cashPaymentPhase(
  status?: CashCheckoutStatus
): CashPaymentPhase {
  if (!status) return 'waiting'
  if (status.payment_status === 'PAID') {
    if (
      status.credit_status === 'REJECTED' ||
      status.credit_status === 'CANCELED'
    ) {
      return 'review'
    }
    return status.credited ? 'success' : 'processing'
  }
  if (status.payment_status === 'REVIEW') return 'review'
  if (['CLOSED', 'FAILED'].includes(status.payment_status)) return 'closed'
  if (status.expires_at && Date.parse(status.expires_at) <= Date.now()) {
    return 'expired'
  }
  return 'waiting'
}

export async function readCashCheckout(
  deps: CashCheckoutDeps,
  account: string,
  id: string,
  signal?: AbortSignal,
  reconcile = false
): Promise<CashCheckoutStatus> {
  if (!identifier.test(account) || !cashCheckoutID.test(id)) {
    throw new CashCheckoutError('invalid')
  }
  const headers = await deps.getAuthHeaders()
  const assertIdentity = () => {
    if (signal?.aborted || (deps.assertIdentity && !deps.assertIdentity())) {
      throw new CashCheckoutError('auth_retry')
    }
  }
  assertIdentity()
  const read = async (path: string, init?: RequestInit) => {
    assertIdentity()
    const response = await deps.fetch(path, {
      ...init,
      headers: { ...headers, ...init?.headers },
      credentials: 'same-origin',
      cache: 'no-store',
      redirect: 'error',
      signal,
    })
    assertIdentity()
    if (!response.ok) throw new CashCheckoutError('request_failed')
    const body: unknown = await response.json()
    assertIdentity()
    return body
  }
  const base = `${deps.bffBase ?? '/api/unihub/bff'}/v1/accounts/${encodeURIComponent(account)}`
  let init: RequestInit | undefined
  if (reconcile) {
    const access = z
      .object({ csrf_token: z.string().min(1) })
      .parse(await read(deps.accessURL ?? '/api/unihub/access'))
    init = { method: 'POST', headers: { 'X-UniHub-CSRF': access.csrf_token } }
  }
  const status = cashCheckoutSchema.parse(
    await read(
      `${base}/cash-checkouts/${id}${reconcile ? '/reconcile' : ''}`,
      init
    )
  )
  if (status.id !== id || status.account_id !== account) {
    throw new CashCheckoutError('forbidden')
  }
  // Reconciliation returns the canonical checkout. Passive reads keep the
  // legacy operation lookup for orders that predate the credit status field.
  if (reconcile || status.wallet_target || status.credit_status !== undefined) {
    return { ...status, credited: status.credit_status === 'APPLIED' }
  }
  let credited = false
  if (
    status.payment_status === 'PAID' &&
    status.operation_id &&
    identifier.test(status.operation_id)
  ) {
    const op = z
      .object({ id: z.string(), status: z.string() })
      .parse(await read(`${base}/operations/${status.operation_id}`))
    credited = op.id === status.operation_id && op.status === 'APPLIED'
  }
  return { ...status, credited }
}
