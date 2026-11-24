import { expect, test, type Page } from '@playwright/test'

const tokenModelFixture = {
  id: 1,
  model_name: 'gpt-4.1-mini',
  quota_type: 0,
  model_ratio: 50,
  completion_ratio: 1,
  enable_groups: ['default'],
}
const requestModelFixture = {
  id: 2,
  model_name: 'request-model',
  quota_type: 1,
  model_price: 0.25,
  enable_groups: ['default'],
}

async function mockPricing(
  page: Page,
  models: (typeof tokenModelFixture | typeof requestModelFixture)[]
) {
  await page.addInitScript(() => {
    localStorage.setItem('setup_status_checked', 'true')
    localStorage.setItem('i18nextLng', 'en')
  })
  await page.route('**/api/user/auth/refresh', (route) =>
    route.fulfill({ status: 401 })
  )
  await page.route('**/api/status', (route) =>
    route.fulfill({
      json: {
        success: true,
        data: { system_name: 'LLMRouter Desktop Fixture' },
      },
    })
  )
  await page.route('**/api/pricing', (route) =>
    route.fulfill({
      json: { success: true, vendors: [], data: models },
    })
  )
}

test('keeps pricing filters inline and usable at a narrow width', async ({
  page,
}) => {
  await page.setViewportSize({ width: 560, height: 800 })
  await mockPricing(page, [tokenModelFixture, requestModelFixture])
  await page.goto('/pricing')

  const tokenModel = page.getByText(tokenModelFixture.model_name, {
    exact: true,
  })
  const requestModel = page.getByText(requestModelFixture.model_name, {
    exact: true,
  })
  await expect(tokenModel).toBeVisible()
  await expect(requestModel).toBeVisible()
  await expect(
    page.getByRole('button', { name: 'Filter', exact: true })
  ).toHaveCount(0)

  await page
    .locator('aside')
    .getByRole('button', { name: /Per Request/ })
    .click()
  await expect(tokenModel).toHaveCount(0)
  await expect(requestModel).toBeVisible()
})

test('keeps the pricing card model name and price readable beside filters at 1152px', async ({
  page,
}) => {
  await page.setViewportSize({ width: 1152, height: 768 })
  await mockPricing(page, [tokenModelFixture])
  await page.goto('/pricing?view=card')

  const name = page.getByRole('heading', { name: tokenModelFixture.model_name })
  const price = page.getByText('Input $100', { exact: true })
  const details = page.getByRole('button', { name: 'Details' })
  await expect(name).toBeVisible()
  await expect(price).toBeVisible()
  const nameFits = await name.evaluate((heading) => {
    const range = document.createRange()
    range.selectNodeContents(heading)
    return (
      range.getBoundingClientRect().right <=
      heading.getBoundingClientRect().right
    )
  })
  expect.soft(nameFits, 'model name fits without clipping').toBe(true)
  const [priceRight, detailsLeft] = await Promise.all([
    price.evaluate((element) => element.getBoundingClientRect().right),
    details.evaluate((element) => element.getBoundingClientRect().left),
  ])
  expect(priceRight).toBeLessThanOrEqual(detailsLeft)
})

test('keeps the filter reset button clear of the header when scrolling at 1152px', async ({
  page,
}) => {
  await page.setViewportSize({ width: 1152, height: 768 })
  const otherTokens = Array.from({ length: 20 }, (_, index) => ({
    ...tokenModelFixture,
    id: index + 3,
    model_name: `token-model-${index}`,
  }))
  await mockPricing(page, [
    tokenModelFixture,
    requestModelFixture,
    ...otherTokens,
  ])
  await page.goto('/pricing?view=card&quotaType=token')

  const requestModel = page.getByRole('heading', {
    name: requestModelFixture.model_name,
  })
  const reset = page.locator('aside').getByRole('button', { name: 'Reset' })
  await expect(reset).toBeEnabled()
  await expect(requestModel).toHaveCount(0)
  await page.evaluate(() => window.scrollTo({ top: 600, behavior: 'instant' }))
  await expect
    .poll(() => page.evaluate(() => window.scrollY))
    .toBeGreaterThan(400)
  await expect
    .poll(() =>
      reset.evaluate((button) => {
        const { x, y, width, height } = button.getBoundingClientRect()
        return button.contains(
          document.elementFromPoint(x + width / 2, y + height / 2)
        )
      })
    )
    .toBe(true)
  await reset.click()
  await expect(requestModel).toBeVisible()
})
