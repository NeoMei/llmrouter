import { expect, test } from '@playwright/test'

test('keeps pricing filters inline and usable at a narrow width', async ({
  page,
}) => {
  await page.setViewportSize({ width: 560, height: 800 })
  await page.addInitScript(() => {
    localStorage.setItem('setup_status_checked', 'true')
    localStorage.setItem('i18nextLng', 'en')
  })
  await page.route('**/api/pricing', (route) =>
    route.fulfill({
      json: {
        success: true,
        vendors: [],
        data: [
          {
            id: 1,
            model_name: 'fixture-token-model',
            quota_type: 0,
            model_ratio: 1,
            completion_ratio: 1,
            enable_groups: ['default'],
          },
          {
            id: 2,
            model_name: 'fixture-request-model',
            quota_type: 1,
            model_price: 0.25,
            enable_groups: ['default'],
          },
        ],
      },
    })
  )
  await page.goto('/pricing')

  const tokenModel = page.getByText('fixture-token-model', { exact: true })
  const requestModel = page.getByText('fixture-request-model', { exact: true })
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
