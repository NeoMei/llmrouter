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
  createCashCheckoutWithRetry,
  isWeChatQrPaymentURL,
  redirectToPayment,
  type CashCheckoutDeps,
} from '../cash-topup'

const checkoutBody = {
  id: 'ct_order1',
  payment_status: 'CREATED',
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
    const body =
      next.body === checkoutBody
        ? {
            ...checkoutBody,
            account_id: String(input).split('/').at(-2),
            amount: JSON.parse(String(init?.body)).amount,
          }
        : next.body
    return Response.json(body, { status: next.status })
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
      payerIdentity: 'payer-1',
      account: 'payer-1',
      amount: '50',
      provider: 'wechat',
      productId: 'llmrouter',
    })

    assert.equal(result.session?.url, 'https://pay.example/checkout/co_1')
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
      payerIdentity: 'payer-1',
      account: 'payer-1',
      amount: '100',
      provider: 'alipay',
    })

    const body = JSON.parse(String(requests[1].init?.body))
    assert.equal(body.product_id, '')
    assert.equal(body.amount, '100000000')
    assert.equal(body.provider, 'alipay')
  })

  test('posts checkout even when crypto.randomUUID is unavailable', async () => {
    const { deps, requests } = depsWith([
      { status: 200, body: { csrf_token: 'csrf-token' } },
      { status: 202, body: checkoutBody },
    ])
    const cryptoObj = globalThis.crypto
    const original = cryptoObj
      ? Object.getOwnPropertyDescriptor(cryptoObj, 'randomUUID')
      : undefined
    // Simulate a wallet rendered in a context that hides randomUUID.
    try {
      if (cryptoObj) {
        Object.defineProperty(cryptoObj, 'randomUUID', {
          value: undefined,
          configurable: true,
        })
      }
      const result = await createCashCheckout(deps, {
        payerIdentity: 'payer-1',
        account: 'payer-1',
        amount: '20',
        provider: 'wechat',
        productId: 'llmrouter',
      })
      assert.equal(result.session?.url, 'https://pay.example/checkout/co_1')
      assert.equal(
        requests.filter((r) => r.url.includes('cash-checkouts')).length,
        1
      )
      const headers = new Headers(requests[1].init?.headers)
      assert.match(
        headers.get('Idempotency-Key') ?? '',
        /^[A-Za-z0-9][A-Za-z0-9._:-]{0,127}$/
      )
    } finally {
      if (cryptoObj && original) {
        Object.defineProperty(cryptoObj, 'randomUUID', original)
      }
    }
  })

  for (const outcome of ['lost response', 'malformed response'] as const) {
    test(`keeps the canonical intent key after a ${outcome}`, async () => {
      const keys: (string | null)[] = []
      const input = {
        payerIdentity: outcome,
        account: 'payer-retry',
        amount: '20',
        provider: 'wechat' as const,
      }
      const deps: CashCheckoutDeps = {
        getAuthHeaders: async () => ({}),
        fetch: async (_url, init) => {
          if (init?.method !== 'POST') {
            return Response.json({ csrf_token: 'csrf-token' })
          }
          keys.push(new Headers(init.headers).get('Idempotency-Key'))
          if (keys.length === 1) {
            if (outcome === 'lost response') throw new TypeError('disconnected')
            return new Response('{"id":')
          }
          return Response.json(
            { ...checkoutBody, account_id: input.account, amount: '20000000' },
            { status: 202 }
          )
        },
      }
      await assert.rejects(() => createCashCheckout(deps, input))
      assert.equal(keys.length, 1)
      await createCashCheckout(deps, { ...input, amount: '20.00' })
      assert.ok(keys[0])
      assert.equal(keys[1], keys[0])
    })
  }

  test('does not share a pending key across authenticated payers on the same account', async () => {
    const { deps, requests } = depsWith([
      { status: 200, body: { csrf_token: 'csrf-token' } },
      { status: 500, body: {} },
      { status: 200, body: { csrf_token: 'csrf-token' } },
      { status: 202, body: checkoutBody },
      { status: 200, body: { csrf_token: 'csrf-token' } },
      { status: 202, body: checkoutBody },
    ])
    const input = {
      account: 'shared',
      amount: '20',
      provider: 'wechat',
    } as const
    await assert.rejects(() =>
      createCashCheckout(deps, { ...input, payerIdentity: 'payer-A' })
    )
    await createCashCheckout(deps, { ...input, payerIdentity: 'payer-B' })
    await createCashCheckout(deps, { ...input, payerIdentity: 'payer-A' })
    const first = new Headers(requests[1].init?.headers).get('Idempotency-Key')
    const other = new Headers(requests[3].init?.headers).get('Idempotency-Key')
    const retry = new Headers(requests[5].init?.headers).get('Idempotency-Key')
    assert.ok(first)
    assert.ok(other)
    assert.notEqual(other, first)
    assert.equal(retry, first)
  })

  test('a checkout response after an identity change cannot complete the old intent', async () => {
    let current = true
    const keys: (string | null)[] = []
    const input = {
      payerIdentity: 'payer-stale',
      account: 'payer-stale',
      amount: '20',
      provider: 'wechat',
    } as const
    const deps: CashCheckoutDeps = {
      assertIdentity: () => current,
      getAuthHeaders: async () => ({}),
      fetch: async (_url, init) => {
        if (init?.method !== 'POST') {
          return Response.json({ csrf_token: 'csrf-token' })
        }
        keys.push(new Headers(init.headers).get('Idempotency-Key'))
        if (keys.length === 1) current = false
        return Response.json(
          { ...checkoutBody, account_id: input.account, amount: '20000000' },
          { status: 202 }
        )
      },
    }
    await assert.rejects(() => createCashCheckoutWithRetry(deps, input))
    current = true
    await createCashCheckout(deps, input)
    assert.equal(keys.length, 2)
    assert.equal(keys[1], keys[0])
  })

  test('does not send a checkout if the payer changes during the CSRF request', async () => {
    let current = true
    const methods: string[] = []
    const deps: CashCheckoutDeps = {
      assertIdentity: () => current,
      getAuthHeaders: async () => ({}),
      fetch: async (_url, init) => {
        methods.push(init?.method ?? 'GET')
        current = false
        return Response.json({ csrf_token: 'csrf-token' })
      },
    }
    await assert.rejects(
      () =>
        createCashCheckout(deps, {
          payerIdentity: 'payer-preflight',
          account: 'payer-preflight',
          amount: '20',
          provider: 'wechat',
        }),
      /auth_retry/
    )
    assert.deepEqual(methods, ['GET'])
  })

  test('maps backend error statuses to distinct error codes', async () => {
    for (const [status, error, code] of [
      [400, 'invalid', 'invalid'],
      [403, 'forbidden', 'forbidden'],
      [503, 'payment_not_configured', 'payment_not_configured'],
      [503, 'finance_result_unavailable', 'request_failed'],
      [500, 'internal', 'request_failed'],
    ] as const) {
      const { deps } = depsWith([
        { status: 200, body: { csrf_token: 'csrf-token' } },
        { status, body: { error } },
      ])
      await assert.rejects(
        () =>
          createCashCheckout(deps, {
            payerIdentity: 'payer-1',
            account: 'payer-1',
            amount: '1',
            provider: 'wechat',
          }),
        (error: unknown) =>
          error instanceof CashCheckoutError && error.code === code
      )
    }
  })

  test('reports auth_retry before any network call when identity is not settled yet', async () => {
    const { deps, requests } = depsWith([])
    await assert.rejects(
      () =>
        createCashCheckout(
          { ...deps, assertIdentity: () => false },
          {
            payerIdentity: 'payer-1',
            account: 'payer-1',
            amount: '1',
            provider: 'wechat',
          }
        ),
      (error: unknown) =>
        error instanceof CashCheckoutError && error.code === 'auth_retry'
    )
    assert.equal(requests.length, 0)
  })

  test('reports auth_retry when fresh auth headers fail before any network call', async () => {
    const { deps, requests } = depsWith([])
    await assert.rejects(
      () =>
        createCashCheckout(
          {
            ...deps,
            getAuthHeaders: async () => {
              throw new Error('auth snapshot not ready')
            },
          },
          {
            payerIdentity: 'payer-1',
            account: 'payer-1',
            amount: '1',
            provider: 'wechat',
          }
        ),
      (error: unknown) =>
        error instanceof CashCheckoutError && error.code === 'auth_retry'
    )
    assert.equal(requests.length, 0)
  })

  test('refreshes auth and completes checkout after an initial unsettled-identity rejection', async () => {
    const { deps, requests } = depsWith([
      { status: 200, body: { csrf_token: 'csrf-token' } },
      { status: 202, body: checkoutBody },
    ])
    let identitySettled = false
    let refreshes = 0

    const result = await createCashCheckoutWithRetry(
      {
        ...deps,
        assertIdentity: () => identitySettled,
      },
      {
        payerIdentity: 'payer-1',
        account: 'payer-1',
        amount: '50',
        provider: 'wechat',
      },
      async () => {
        refreshes += 1
        identitySettled = true
      }
    )

    assert.equal(result.session?.url, 'https://pay.example/checkout/co_1')
    assert.equal(refreshes, 1)
    assert.equal(requests.length, 2)
  })

  test('refreshes auth and completes checkout after an initial auth-header failure', async () => {
    const { deps, requests } = depsWith([
      { status: 200, body: { csrf_token: 'csrf-token' } },
      { status: 202, body: checkoutBody },
    ])
    let headersReady = false
    let refreshes = 0

    const result = await createCashCheckoutWithRetry(
      {
        ...deps,
        getAuthHeaders: async () => {
          if (!headersReady) throw new Error('auth snapshot not ready')
          return { Authorization: 'Bearer test-token' }
        },
      },
      {
        payerIdentity: 'payer-1',
        account: 'payer-1',
        amount: '50',
        provider: 'wechat',
      },
      async () => {
        refreshes += 1
        headersReady = true
      }
    )

    assert.equal(result.session?.url, 'https://pay.example/checkout/co_1')
    assert.equal(refreshes, 1)
    assert.equal(requests.length, 2)
  })

  test('does not loop when auth stays unsettled after a refresh', async () => {
    const { deps, requests } = depsWith([])
    let refreshes = 0

    await assert.rejects(
      () =>
        createCashCheckoutWithRetry(
          { ...deps, assertIdentity: () => false },
          {
            payerIdentity: 'payer-1',
            account: 'payer-1',
            amount: '1',
            provider: 'wechat',
          },
          async () => {
            refreshes += 1
          }
        ),
      (error: unknown) =>
        error instanceof CashCheckoutError && error.code === 'auth_retry'
    )
    assert.equal(refreshes, 1)
    assert.equal(requests.length, 0)
  })

  test('does not refresh auth for a genuine backend failure', async () => {
    const { deps } = depsWith([
      { status: 200, body: { csrf_token: 'csrf-token' } },
      { status: 500, body: {} },
    ])
    let refreshes = 0

    await assert.rejects(
      () =>
        createCashCheckoutWithRetry(
          deps,
          {
            payerIdentity: 'payer-1',
            account: 'payer-1',
            amount: '1',
            provider: 'wechat',
          },
          async () => {
            refreshes += 1
          }
        ),
      (error: unknown) =>
        error instanceof CashCheckoutError && error.code === 'request_failed'
    )
    assert.equal(refreshes, 0)
  })

  test('retries a transient access preflight failure before creating an order', async () => {
    const { deps, requests } = depsWith([
      { status: 503, body: {} },
      { status: 200, body: { csrf_token: 'csrf-token' } },
      { status: 202, body: checkoutBody },
    ])
    let refreshes = 0

    const result = await createCashCheckoutWithRetry(
      deps,
      {
        payerIdentity: 'payer-1',
        account: 'payer-1',
        amount: '50',
        provider: 'alipay',
      },
      async () => {
        refreshes += 1
      }
    )

    assert.equal(result.session?.url, 'https://pay.example/checkout/co_1')
    assert.equal(refreshes, 1)
    assert.equal(requests.length, 3)
    assert.equal(
      requests.filter((r) => r.url.includes('cash-checkouts')).length,
      1
    )
  })

  test('does not retry the checkout POST after a transient access failure resolves', async () => {
    const { deps, requests } = depsWith([
      { status: 401, body: {} },
      { status: 200, body: { csrf_token: 'csrf-token' } },
      { status: 500, body: {} },
    ])
    let refreshes = 0

    await assert.rejects(
      () =>
        createCashCheckoutWithRetry(
          deps,
          {
            payerIdentity: 'payer-1',
            account: 'payer-1',
            amount: '50',
            provider: 'wechat',
          },
          async () => {
            refreshes += 1
          }
        ),
      (error: unknown) =>
        error instanceof CashCheckoutError && error.code === 'request_failed'
    )
    assert.equal(refreshes, 1)
    assert.equal(
      requests.filter((r) => r.url.includes('cash-checkouts')).length,
      1
    )
  })

  test('checkout calls fetch through a receiver-safe wrapper', async () => {
    // The card wraps globalThis.fetch in an arrow function so the native
    // receiver cannot be rebound to the deps object. A bare native fetch
    // invoked as deps.fetch(...) throws "Illegal invocation" with zero network
    // requests, which is the production failure this guards against.
    const calls: string[] = []
    const globalReceiver = globalThis as { fetch: typeof fetch }
    const originalFetch = globalReceiver.fetch
    let receiverOk = false
    globalReceiver.fetch = function (
      this: unknown,
      ...args: Parameters<typeof fetch>
    ) {
      receiverOk = this === globalThis
      calls.push(String(args[0]))
      return Promise.resolve(new Response('{}', { status: 200 }))
    } as typeof fetch
    try {
      const deps: CashCheckoutDeps = {
        fetch: (...args: Parameters<typeof fetch>) => globalThis.fetch(...args),
        getAuthHeaders: async () => ({ Authorization: 'Bearer test-token' }),
      }
      await assert.rejects(
        () =>
          createCashCheckout(deps, {
            payerIdentity: 'payer-1',
            account: 'payer-1',
            amount: '20',
            provider: 'wechat',
          }),
        (error: unknown) =>
          error instanceof Error &&
          !String(error).includes('Illegal invocation'),
        'unparsable csrf response is surfaced without Illegal invocation'
      )
      assert.ok(receiverOk, 'fetch must be invoked with the global receiver')
      assert.deepEqual(calls, ['/api/unihub/access'])
    } finally {
      globalReceiver.fetch = originalFetch
    }
  })

  test('a second transient access failure surfaces access_retry without looping', async () => {
    const { deps, requests } = depsWith([
      { status: 503, body: {} },
      { status: 503, body: {} },
    ])
    let refreshes = 0

    await assert.rejects(
      () =>
        createCashCheckoutWithRetry(
          deps,
          {
            payerIdentity: 'payer-1',
            account: 'payer-1',
            amount: '50',
            provider: 'wechat',
          },
          async () => {
            refreshes += 1
          }
        ),
      (error: unknown) =>
        error instanceof CashCheckoutError && error.code === 'access_retry'
    )
    assert.equal(refreshes, 1)
    assert.equal(requests.length, 2)
  })
})

describe('payment redirect', () => {
  test('recognises a WeChat native code_url that must be shown as a QR', () => {
    assert.equal(
      isWeChatQrPaymentURL('weixin://wxpay/bizpayurl?pr=abc123'),
      true
    )
  })

  test('does not classify other URLs as WeChat QR code URLs', () => {
    assert.equal(isWeChatQrPaymentURL('https://pay.example/co_1'), false)
    assert.equal(isWeChatQrPaymentURL('weixin://wxpay/other?pr=abc'), false)
    assert.equal(isWeChatQrPaymentURL('not a url'), false)
  })

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
