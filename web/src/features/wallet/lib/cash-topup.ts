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
  | 'auth_retry'
  | 'access_retry'

export class CashCheckoutError extends Error {
  readonly code: CashCheckoutErrorCode | 'missing_url'
  constructor(code: CashCheckoutErrorCode | 'missing_url') {
    super(code)
    this.name = 'CashCheckoutError'
    this.code = code
  }
}

/**
 * Access preflight failures that are frequently transient: an access token
 * expiring between render and click, a briefly unsettled identity handoff,
 * or a short upstream/proxy outage. These all happen before any order is
 * created, so a single bounded retry is safe. Never apply this to the
 * checkout POST, whose Idempotency-Key is regenerated on every attempt.
 */
function isRetryableAccessStatus(status: number): boolean {
  return status === 401 || status === 408 || status === 429 || status >= 500
}

const checkoutSchema = z.object({
  id: z.string().min(1),
  session: z.object({
    kind: z.string(),
    url: z.string().min(1),
  }),
})

export type CashCheckout = z.infer<typeof checkoutSchema>

/**
 * Idempotency keys must be unique per attempt, but crypto.randomUUID() is not
 * available in every embedded/mobile webview that can render the wallet. When
 * it is missing we still need a collision-resistant key: the checkout POST
 * must go through instead of throwing a bare TypeError before any request.
 */
function createIdempotencyKey(): string {
  const webcrypto = globalThis.crypto
  if (webcrypto && typeof webcrypto.randomUUID === 'function') {
    return webcrypto.randomUUID()
  }
  if (webcrypto && typeof webcrypto.getRandomValues === 'function') {
    const bytes = new Uint8Array(16)
    webcrypto.getRandomValues(bytes)
    const hex = Array.from(bytes, (b) => b.toString(16).padStart(2, '0')).join('')
    return `${hex.slice(0, 8)}-${hex.slice(8, 12)}-${hex.slice(12, 16)}-${hex.slice(16, 20)}-${hex.slice(20)}`
  }
  // Last-resort fallback keeps the key non-empty and shaped like a UUID so
  // the backend accepts it. getRandomValues is present in every secure
  // context; this only guards against a broken crypto implementation.
  return `${Date.now().toString(16)}-${Math.random().toString(16).slice(2)}`
}

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

  let authHeaders: Record<string, string>
  try {
    authHeaders = await deps.getAuthHeaders()
  } catch {
    // A fresh login or silent UniHub handoff can leave the auth snapshot
    // unsettled for a moment. This happens before any request is sent, so it
    // is safe to retry rather than surfacing it as a failed recharge.
    throw new CashCheckoutError('auth_retry')
  }
  if (deps.assertIdentity && !deps.assertIdentity()) {
    throw new CashCheckoutError('auth_retry')
  }

  const access = await deps.fetch(deps.accessURL ?? '/api/unihub/access', {
    headers: authHeaders,
    credentials: 'same-origin',
    cache: 'no-store',
    redirect: 'error',
  })
  if (!access.ok) {
    if (isRetryableAccessStatus(access.status)) {
      throw new CashCheckoutError('access_retry')
    }
    throw new CashCheckoutError('request_failed')
  }
  const csrf = z
    .object({ csrf_token: z.string().min(1) })
    .safeParse(await access.json())
  if (!csrf.success) throw new CashCheckoutError('access_retry')

  const base = deps.bffBase ?? '/api/unihub/bff'
  const path = `${base}/v1/accounts/${encodeURIComponent(input.account)}/cash-checkouts`
  const response = await deps.fetch(path, {
    method: 'POST',
    headers: {
      ...authHeaders,
      'X-UniHub-CSRF': csrf.data.csrf_token,
      'Idempotency-Key': createIdempotencyKey(),
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

/**
 * A freshly logged-in or silently handed-off session can be settled a beat
 * after the wallet card first renders. In that window getAuthHeaders() or the
 * identity assertion briefly disagree with the account captured from the
 * wallet query, and createCashCheckout() reports 'auth_retry' before any
 * network call. Refreshing auth rewrites the store snapshot from the backend
 * (including funding/identity_source), so one refresh + retry turns that
 * transient race into a normal checkout instead of a red error on first click.
 * The retry is bounded to a single attempt: a second 'auth_retry' is surfaced
 * to the caller as-is and never loops.
 * A transient access preflight failure (401/503 or an unparsable grant) gets
 * the same single retry after refreshing auth. The checkout POST is never
 * retried: a fresh Idempotency-Key there could create a duplicate order.
 */
export async function createCashCheckoutWithRetry(
  deps: CashCheckoutDeps,
  input: CashCheckoutInput,
  refreshAuth?: () => Promise<unknown>
): Promise<CashCheckout> {
  try {
    return await createCashCheckout(deps, input)
  } catch (cause) {
    if (
      cause instanceof CashCheckoutError &&
      (cause.code === 'auth_retry' || cause.code === 'access_retry') &&
      refreshAuth
    ) {
      await refreshAuth()
      return await createCashCheckout(deps, input)
    }
    throw cause
  }
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

/**
 * WeChat native checkout returns a `weixin://wxpay/bizpayurl?pr=...` string.
 * That is the WeChat Pay "code_url": a QR-code payload, not a normal page
 * URL. Navigating to it launches the WeChat app, which is wrong for desktop
 * and browser users. Detect it here so the wallet can render it as a
 * scannable QR code on-page instead.
 */
export function isWeChatQrPaymentURL(url: string): boolean {
  try {
    const parsed = new URL(url)
    return (
      parsed.protocol === 'weixin:' &&
      parsed.hostname === 'wxpay' &&
      parsed.pathname === '/bizpayurl'
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
