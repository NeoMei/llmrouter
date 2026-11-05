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
import { test, expect } from '@playwright/test'

import { fixture, bundle } from '../../wallet/__tests__/settings-fixture'

test('native receipt business failure cannot masquerade as no records', async ({
  page,
}) => {
  await fixture(page)
  await page.route('**/api/native-billing/self', (route) =>
    route.fulfill({ json: { success: false, data: [] } })
  )
  await page.goto('/usage-logs/common')
  await expect(
    page.getByRole('alert').filter({ hasText: 'Native receipts unavailable' })
  ).toBeVisible()
  await expect(
    page.getByText('No native billing records', { exact: true })
  ).toHaveCount(0)
})

test('native measured zero stays separate from confirmed UniHub cash and private IDs are disclosed', async ({
  page,
}) => {
  await fixture(page)
  await page.route('**/api/native-billing/self', (route) =>
    route.fulfill({
      json: {
        success: true,
        data: [
          {
            id: 'private-native-id-A',
            state: 'settled',
            platform: true,
            reserved: '5',
            final: '0',
          },
        ],
      },
    })
  )
  await page.goto('/usage-logs/common')
  await expect(
    page.getByText('Final product measurement: 0 LR_QUOTA', { exact: true })
  ).toBeVisible()
  await expect(
    page.getByText('private-native-id-A', { exact: true })
  ).toBeHidden()
  await page.getByText('Measurement details', { exact: true }).click()
  await expect(
    page.getByText('private-native-id-A', { exact: true })
  ).toBeVisible()
  await page.route('**/api/native-billing/self', (route) =>
    route.fulfill({ json: { success: false, data: [] } })
  )
  await page.evaluate(
    (value) => window.settingsFixture.signIn(value),
    bundle(99)
  )
  await expect(
    page.getByRole('alert').filter({ hasText: 'Native receipts unavailable' })
  ).toBeVisible()
  await expect(
    page.getByText('private-native-id-A', { exact: true })
  ).toHaveCount(0)
})
