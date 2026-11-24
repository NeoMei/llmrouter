import { expect, test } from '@playwright/test'

export function registerOwnedNetworkChecks() {
  // Page routes own every API response. Only this run's Rsbuild assets may reach
  // a socket; any missing API mock or external request fails the case.
  const ownedRequests = new Map<
    string,
    {
      requests: { method: string; origin: string; path: string }[]
      unowned: string[]
    }
  >()
  test.beforeEach(async ({ baseURL, context, page }, testInfo) => {
    if (!baseURL) throw new Error('Owned network checks require a baseURL')
    const appOrigin = new URL(baseURL).origin
    const unowned: string[] = []
    const requests: { method: string; origin: string; path: string }[] = []
    page.on('request', (request) => {
      const url = new URL(request.url())
      if (url.protocol === 'http:' || url.protocol === 'https:') {
        requests.push({
          method: request.method(),
          origin: url.origin,
          path: url.pathname,
        })
      }
    })
    await context.route('**/*', async (route) => {
      const url = new URL(route.request().url())
      if (
        url.origin === appOrigin &&
        !url.pathname.startsWith('/api/') &&
        !url.pathname.startsWith('/platform/')
      ) {
        return route.continue()
      }
      unowned.push(`${route.request().method()} ${url.origin}${url.pathname}`)
      return route.abort('blockedbyclient')
    })
    ownedRequests.set(testInfo.testId, { requests, unowned })
  })
  test.afterEach(async ({ context }, testInfo) => {
    const observed = ownedRequests.get(testInfo.testId)
    ownedRequests.delete(testInfo.testId)
    await testInfo.attach('owned-request-paths', {
      body: JSON.stringify(
        { ...observed, openPages: context.pages().length },
        null,
        2
      ),
      contentType: 'application/json',
    })
    expect(
      observed?.unowned,
      'Every API and external request must be fulfilled by an owned fixture'
    ).toEqual([])
  })
}
