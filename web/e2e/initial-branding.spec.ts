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
import { expect, test } from '@playwright/test'

test('serves LLMRouter branding before application scripts run', async ({
  page,
}) => {
  await page.route('**/*.js', (route) => route.abort())

  await page.goto('/', { waitUntil: 'domcontentloaded' })

  await expect(page).toHaveTitle('LLMRouter')
  await expect(
    page.locator('link[rel="icon"][type="image/svg+xml"]')
  ).toHaveAttribute('href', '/brand/llmrouter-confluence.svg')
  const icon = await page.request.get('/brand/llmrouter-confluence.svg')
  expect(icon.ok()).toBeTruthy()
  expect(icon.headers()['content-type']).toContain('image/svg+xml')
  await expect(page.locator('meta[name="title"]')).toHaveAttribute(
    'content',
    'LLMRouter'
  )
})
