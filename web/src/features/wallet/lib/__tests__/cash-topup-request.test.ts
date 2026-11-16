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
  CashCheckoutError,
  createCashCheckout,
  redirectToPayment,
  type CashCheckoutDeps,
} from '../cash-topup'

const checkoutBody = {
  id: 'co_1',
  session: { kind: 'redirect', url: 'https://pay.example/checkout/co_1' },
}

function depsWith(responses: Array<{ status: number; body: unknown }>): {
  deps: CashCheckoutDeps
  requests: Array<{ url: string; init?: RequestInit }>
} {
  const requests: Array<{ url: string; init?: RequestInit }> = []
  let index = 0
  const fetchStub = async (input: RequestInfo | URL, init?: RequestInit) => {
    requests.push({ url: String(input), init })
    const next = responses[index++]
    assert.ok(next, 'unexpected extra fetch')
    return new Response(JSON.stringify(next.body), { status: next.status })
  }
  return {
    requests,
    deps: {
      fetch: fetchStub as typeof fetch,
      getAuthHeaders: async () => ({ Authorization: 'Bearer test-token' }),
    },
  }
}

describe('cash checkout request', () => {
  test('posts the canonical request shape with auth, CSRF, and an idempotency key', async () => {
    const { deps, requests } = depsWith([
      { status: 200, body: { csrf_token: 'csrf-token' } },
      { status: 202, body: checkoutBody },
    ])

    const result = await createCashCheckout(deps, {
      account: 'payer-1',
      amount: '50',
      provider: 'wechat',
      productId: 'llmrouter',
    })

    assert.equal(result.session.url, 'https://pay.example/checkout/co_1')
    assert.equal(requests.length, 2)
    assert.equal(requests[0].url, '/api/unihub/access')
    assert.equal(
      new Headers(requests[0].init?.headers).get('Authorization'),
      'Bearer test-token'
    )
    assert.equal(
      requests[1].url,
      '/api/unihub/bff/v1/accounts/payer-1/cash-checkouts'
    )
    const headers = new Headers(requests[1].init?.headers)
    assert.equal(headers.get('Authorization'), 'Bearer test-token')
    assert.equal(headers.get('X-UniHub-CSRF'), 'csrf-token')
    assert.match(
      headers.get('Idempotency-Key') ?? '',
      /^[A-Za-z0-9][A-Za-z0-9._:-]{0,127}$/
    )
    assert.deepEqual(JSON.parse(String(requests[1].init?.body)), {
      provider: 'wechat',
      product_id: 'llmrouter',
      amount: '50000000',
    })
  })

  test('uses an empty product id for unified cash when none is supplied', async () => {
    const { deps, requests } = depsWith([
      { status: 200, body: { csrf_token: 'csrf-token' } },
      { status: 202, body: checkoutBody },
    ])

    await createCashCheckout(deps, {
      account: 'payer-1',
      amount: '100',
      provider: 'alipay',
    })

    const body = JSON.parse(String(requests[1].init?.body))
    assert.equal(body.product_id, '')
    assert.equal(body.amount, '100000000')
    assert.equal(body.provider, 'alipay')
  })

  test('maps backend error statuses to distinct error codes', async () => {
    for (const [status, code] of [
      [400, 'invalid'],
      [403, 'forbidden'],
      [503, 'payment_not_configured'],
      [500, 'request_failed'],
    ] as const) {
      const { deps } = depsWith([
        { status: 200, body: { csrf_token: 'csrf-token' } },
        { status, body: {} },
      ])
      await assert.rejects(
        () =>
          createCashCheckout(deps, {
            account: 'payer-1',
            amount: '1',
            provider: 'wechat',
          }),
        (error: unknown) =>
          error instanceof CashCheckoutError && error.code === code
      )
    }
  })

  test('rejects the request before any network call when identity changed', async () => {
    const { deps, requests } = depsWith([])
    await assert.rejects(
      () =>
        createCashCheckout(
          { ...deps, assertIdentity: () => false },
          { account: 'payer-1', amount: '1', provider: 'wechat' }
        ),
      (error: unknown) =>
        error instanceof CashCheckoutError && error.code === 'request_failed'
    )
    assert.equal(requests.length, 0)
  })
})

describe('payment redirect', () => {
  test('redirects to an https payment URL', () => {
    const urls: string[] = []
    redirectToPayment('https://pay.example/co_1', {
      assign: (url) => urls.push(url),
    })
    assert.deepEqual(urls, ['https://pay.example/co_1'])
  })

  test('accepts a weixin native payment URL', () => {
    const urls: string[] = []
    redirectToPayment('weixin://wxpay/bizpayurl?pr=abc', {
      assign: (url) => urls.push(url),
    })
    assert.deepEqual(urls, ['weixin://wxpay/bizpayurl?pr=abc'])
  })

  test('rejects an unsafe redirect target', () => {
    assert.throws(
      () =>
        redirectToPayment('javascript:alert(1)', {
          assign: () => {},
        }),
      (error: unknown) =>
        error instanceof CashCheckoutError && error.code === 'missing_url'
    )
  })
})
