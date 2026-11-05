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
import { test, expect, type Page } from '@playwright/test'

import { fixture } from '../../wallet/__tests__/settings-fixture'

const evidence =
  '/Users/neomei/项目/codexprojects/U-L-S 综合验收/artifacts/remaining-gaps-20261106/lr-usage-presentation'
const request = {
  key: 'requests',
  unit: 'request',
  quantity: { value: '1', scale: 0 },
}
async function logFixture(
  page: Page,
  other: Record<string, unknown>,
  tokens = [0, 0]
) {
  await fixture(page)
  await page.route('**/api/log/self?**', (route) =>
    route.fulfill({
      json: {
        success: true,
        data: {
          total: 1,
          items: [
            {
              id: 1,
              user_id: 42,
              created_at: 1793923200,
              type: 2,
              content: 'Technical fixture details',
              model_name: 'technical-fixture',
              quota: 777777,
              prompt_tokens: tokens[0],
              completion_tokens: tokens[1],
              other: JSON.stringify(other),
            },
          ],
        },
      },
    })
  )
  await page.goto('/usage-logs/common')
  await expect(
    page.getByText('technical-fixture', { exact: true }).first()
  ).toBeVisible()
}
for (const width of [1440, 390]) {
  test(`central technical request has exact usage and no financial billing at width ${width}`, async ({
    page,
  }) => {
    await page.setViewportSize({ width, height: 1000 })
    await logFixture(page, {
      funding_owner: 'unihub',
      usage_state: 'confirmed',
      operation: 'chat',
      request_count: 1,
      native_measurements: [request],
      model_price: 999,
      fee_quota: 888,
      central_charged: 'secret-financial-amount',
    })
    await expect(
      page.getByText('Requests: 1', { exact: true }).first()
    ).toBeVisible()
    await expect(
      page.getByText('Cash settlement managed by UniHub').first()
    ).toBeVisible()
    await expect(
      page.getByText('Provider token usage was not recorded.').first()
    ).toBeVisible()
    await expect(page.getByText('secret-financial-amount')).toHaveCount(0)
    await expect(page.getByText(/Per-call/)).toHaveCount(0)
    await page.screenshot({
      path: `${evidence}/request-${width}.png`,
      fullPage: true,
      animations: 'disabled',
    })
    await page
      .getByTitle('Click to view full details')
      .filter({ visible: true })
      .click()
    const dialog = page.getByRole('dialog')
    await expect(dialog.getByText('Requests: 1', { exact: true })).toBeVisible()
    await expect(
      dialog.getByText('Cash settlement managed by UniHub')
    ).toBeVisible()
    await expect(
      dialog.getByText('Billing Details', { exact: true })
    ).toHaveCount(0)
    await expect(dialog.getByText('Per-token', { exact: true })).toHaveCount(0)
    await expect(dialog.getByText('Total Cost', { exact: true })).toHaveCount(0)
    await page.screenshot({
      path: `${evidence}/request-details-${width}.png`,
      fullPage: true,
      animations: 'disabled',
    })
  })
}
for (const state of ['unverifiable', 'invalid', 'conflict', 'missing']) {
  test(`historical ${state} call count is not a validated request`, async ({
    page,
  }) => {
    await logFixture(page, {
      funding_owner: 'unihub',
      usage_state: state,
      request_count: 1,
    })
    await expect(page.getByText('Requests: 1', { exact: true })).toHaveCount(0)
    await expect(
      page.getByText('Cash settlement managed by UniHub').first()
    ).toBeVisible()
    const labels: Record<string, string> = {
      unverifiable: 'Usage cannot be verified',
      invalid: 'Usage evidence is invalid',
      conflict: 'Usage evidence conflicts',
      missing: 'Usage evidence is missing',
    }
    await expect(
      page.getByText(labels[state], { exact: true }).first()
    ).toBeVisible()
  })
}
test('native token quantities preserve precision in the real table and detail dialog', async ({
  page,
}) => {
  await logFixture(page, {
    funding_owner: 'unihub',
    usage_state: 'confirmed',
    operation: 'chat',
    native_measurements: [
      {
        key: 'input',
        unit: 'token',
        quantity: { value: '90071992547409931234', scale: 0 },
      },
    ],
  })
  await expect(
    page
      .getByText('Input Tokens: 90071992547409931234', { exact: true })
      .first()
  ).toBeVisible()
  await page
    .getByTitle('Click to view full details')
    .filter({ visible: true })
    .click()
  await expect(
    page.getByRole('dialog').getByText('Input Tokens: 90071992547409931234')
  ).toBeVisible()
})
for (const other of [{}, { funding_owner: 'unihub', product_runtime: true }]) {
  test(`ordinary product billing stays visible ${JSON.stringify(other)}`, async ({
    page,
  }) => {
    await logFixture(page, other, [21, 7])
    await page
      .getByTitle('Click to view full details')
      .filter({ visible: true })
      .click()
    await expect(
      page.getByRole('dialog').getByText('Billing Details', { exact: true })
    ).toBeVisible()
    await expect(
      page.getByRole('dialog').getByText('Total Cost', { exact: true })
    ).toBeVisible()
  })
}
test('financial details false suppresses both the cost cell and financial detail breakdown', async ({
  page,
}) => {
  await logFixture(
    page,
    {
      financial_details: false,
      model_price: 999,
      billing_source: 'subscription',
      subscription_consumed: 777,
      native_usage_unit: 'characters',
      native_pricing: 'secret-native-price',
    },
    [21, 7]
  )
  await expect(page.getByText('Finance access required').first()).toBeVisible()
  await page
    .getByTitle('Click to view full details')
    .filter({ visible: true })
    .click()
  await expect(
    page.getByRole('dialog').getByText('Billing Details', { exact: true })
  ).toHaveCount(0)
  await expect(
    page.getByRole('dialog').getByText('Total Cost', { exact: true })
  ).toHaveCount(0)
})

test('hidden finance cannot leak subscription or native pricing details', async ({
  page,
}) => {
  await logFixture(page, {
    financial_details: false,
    billing_source: 'subscription',
    subscription_consumed: 777,
    native_usage_unit: 'characters',
    native_pricing: 'secret-native-price',
  })
  await page
    .getByTitle('Click to view full details')
    .filter({ visible: true })
    .click()
  await expect(
    page.getByRole('dialog').getByText('Subscription Billing', { exact: true })
  ).toHaveCount(0)
  await expect(
    page.getByRole('dialog').getByText('secret-native-price')
  ).toHaveCount(0)
})
