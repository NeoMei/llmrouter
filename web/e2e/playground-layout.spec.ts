import { expect, test } from '@playwright/test'

import { mockDesktopSession } from './desktop-session'

test('keeps one open model picker and its search when crossing the narrow breakpoint', async ({
  page,
}) => {
  await page.setViewportSize({ width: 1440, height: 900 })
  await mockDesktopSession(page)
  await page.route('**/api/user/models?*', (route) =>
    route.fulfill({
      json: { success: true, data: ['fixture-default', 'fixture-alternate'] },
    })
  )
  await page.goto('/playground')

  const originalModel = page.getByRole('combobox').filter({
    hasText: 'fixture-default',
  })
  await originalModel.click()
  const search = page.getByPlaceholder('Search models...')
  await search.fill('fixture-alternate')

  await page.setViewportSize({ width: 767, height: 768 })
  await expect(originalModel).toHaveAttribute('aria-expanded', 'true')
  await expect(search).toHaveCount(1)
  await expect(search).toHaveValue('fixture-alternate')
  await page.getByRole('option', { name: /fixture-alternate/ }).click()
  await expect(
    page.getByRole('combobox').filter({ hasText: 'fixture-alternate' })
  ).toHaveAttribute('aria-expanded', 'false')
})
