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

export const CASH_TOPUP_MIN_CNY = 1
export const CASH_TOPUP_MAX_CNY = 100000

/** Fixed recharge tiers exposed by the wallet UI, in whole CNY. */
export const CASH_TOPUP_TIERS_CNY = [20, 50, 100, 300, 1000] as const

const cnyPattern = /^(0|[1-9]\d*)(?:\.(\d{1,2}))?$/
const accountPattern = /^[A-Za-z0-9_-]{1,128}$/

export type CashTopupProvider = 'wechat' | 'alipay'
export type CashTopupAmountError = 'invalidAmount' | 'belowMin' | 'aboveMax'
export type CashCheckoutErrorCode =
  | 'invalid'
  | 'forbidden'
  | 'payment_not_configured'
  | 'request_failed'

export class CashCheckoutError extends Error {
  readonly code: CashCheckoutErrorCode | 'missing_url'
  constructor(code: CashCheckoutErrorCode | 'missing_url') {
    super(code)
    this.name = 'CashCheckoutError'
    this.code = code
  }
}

const checkoutSchema = z.object({
  id: z.string().min(1),
  session: z.object({
    kind: z.string(),
    url: z.string().min(1),
  }),
})

export type CashCheckout = z.infer<typeof checkoutSchema>

export function validateCashTopupAmount(
  amount: string
): CashTopupAmountError | null {
  const match = cnyPattern.exec(amount.trim())
  if (!match) return 'invalidAmount'
  const yuan = Number(match[1])
  if (!Number.isSafeInteger(yuan)) return 'invalidAmount'
  const cents = match[2] === undefined ? 0 : Number(match[2])
  const value = yuan + cents / 100
  if (value < CASH_TOPUP_MIN_CNY) return 'belowMin'
  if (value > CASH_TOPUP_MAX_CNY) return 'aboveMax'
  return null
}

/** Converts a CNY amount to the canonical CNY/6 integer quantity (yuan * 1e6). */
export function cnyToCashUnits(amount: string): string {
  const match = cnyPattern.exec(amount.trim())
  if (!match) throw new TypeError('Invalid CNY amount')
  const yuan = BigInt(match[1])
  const cents = BigInt(match[2] === undefined ? '0' : match[2].padEnd(2, '0'))
  return (yuan * 1000000n + cents * 10000n).toString()
}

export interface CashCheckoutDeps {
  fetch: typeof fetch
  getAuthHeaders: () => Promise<Record<string, string>>
  assertIdentity?: () => boolean
  accessURL?: string
  bffBase?: string
}

export interface CashCheckoutInput {
  account: string
  amount: string
  provider: CashTopupProvider
  productId?: string
}

export async function createCashCheckout(
  deps: CashCheckoutDeps,
  input: CashCheckoutInput
): Promise<CashCheckout> {
  if (!accountPattern.test(input.account)) {
    throw new CashCheckoutError('invalid')
  }
  if (validateCashTopupAmount(input.amount) !== null) {
    throw new CashCheckoutError('invalid')
  }

  const authHeaders = await deps.getAuthHeaders()
  if (deps.assertIdentity && !deps.assertIdentity()) {
    throw new CashCheckoutError('request_failed')
  }

  const access = await deps.fetch(deps.accessURL ?? '/api/unihub/access', {
    headers: authHeaders,
    credentials: 'same-origin',
    cache: 'no-store',
    redirect: 'error',
  })
  if (!access.ok) throw new CashCheckoutError('request_failed')
  const csrf = z
    .object({ csrf_token: z.string().min(1) })
    .safeParse(await access.json())
  if (!csrf.success) throw new CashCheckoutError('request_failed')

  const base = deps.bffBase ?? '/api/unihub/bff'
  const path = `${base}/v1/accounts/${encodeURIComponent(input.account)}/cash-checkouts`
  const response = await deps.fetch(path, {
    method: 'POST',
    headers: {
      ...authHeaders,
      'X-UniHub-CSRF': csrf.data.csrf_token,
      'Idempotency-Key': crypto.randomUUID(),
    },
    body: JSON.stringify({
      provider: input.provider,
      product_id: input.productId ?? '',
      amount: cnyToCashUnits(input.amount),
    }),
    credentials: 'same-origin',
    cache: 'no-store',
    redirect: 'error',
  })
  if (!response.ok) {
    if (response.status === 400) throw new CashCheckoutError('invalid')
    if (response.status === 403) throw new CashCheckoutError('forbidden')
    if (response.status === 503) {
      throw new CashCheckoutError('payment_not_configured')
    }
    throw new CashCheckoutError('request_failed')
  }

  const parsed = checkoutSchema.safeParse(await response.json())
  if (!parsed.success) throw new CashCheckoutError('request_failed')
  return parsed.data
}

export function isNavigablePaymentURL(url: string): boolean {
  try {
    return ['https:', 'http:', 'weixin:', 'alipay:'].includes(
      new URL(url).protocol
    )
  } catch {
    return false
  }
}

export function redirectToPayment(
  url: string,
  target: { assign: (url: string) => void }
): void {
  if (!isNavigablePaymentURL(url)) throw new CashCheckoutError('missing_url')
  target.assign(url)
}
