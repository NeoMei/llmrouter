import { expect, test } from '@playwright/test'

import { mockDesktopSession } from './desktop-session'

test('keeps desktop header controls inside a 1024px window', async ({
  page,
}) => {
  await page.setViewportSize({ width: 1024, height: 768 })
  await mockDesktopSession(page)
  await page.goto('/keys')

  const header = page.getByRole('banner')
  await expect(
    header.getByRole('button', { name: 'Change language' })
  ).toBeInViewport({ ratio: 1 })
  expect(
    await page.evaluate(
      () => document.documentElement.scrollWidth <= window.innerWidth
    )
  ).toBe(true)
})

test('keeps desktop layout settings available after narrowing the window', async ({
  page,
}) => {
  await page.setViewportSize({ width: 1440, height: 900 })
  await mockDesktopSession(page)
  await page.goto('/keys')

  const themeSettings = page.getByRole('button', {
    name: 'Open theme settings',
  })
  await expect(themeSettings).toBeInViewport({ ratio: 1 })
  await page.setViewportSize({ width: 767, height: 768 })
  await themeSettings.click({ timeout: 3000 })

  const settings = page.getByRole('dialog', { name: 'Theme Settings' })
  await expect(settings).toBeVisible()
  for (const name of [
    'Select sidebar style',
    'Select layout style',
    'Select content width',
  ]) {
    await expect(settings.getByRole('radiogroup', { name })).toBeVisible()
  }
})
