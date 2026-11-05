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

test('statistics failure never becomes confirmed zero and retry displays successful zero', async ({
  page,
}) => {
  const { writes } = await fixture(page)
  let failed = true
  await page.route('**/api/log/self/stat**', (route) =>
    route.fulfill({
      json: failed
        ? { success: false }
        : { success: true, data: { quota: 0, rpm: 0, tpm: 0, token: 0 } },
    })
  )
  await page.goto('/usage-logs/common')
  await expect(
    page.getByRole('alert').filter({ hasText: 'Usage statistics unavailable' })
  ).toBeVisible()
  await expect(page.getByText('RPM', { exact: true })).toHaveCount(0)
  failed = false
  await page
    .getByRole('button', { name: 'Retry statistics', exact: true })
    .click()
  await expect(page.getByText('RPM', { exact: true })).toBeVisible()
  expect(writes).toEqual([])
})

test('late old-user statistics stay hidden after identity changes', async ({
  page,
}) => {
  await fixture(page)
  let release!: () => void
  const gate = new Promise<void>((resolve) => {
    release = resolve
  })
  let started!: () => void
  const pending = new Promise<void>((resolve) => {
    started = resolve
  })
  await page.route('**/api/log/self/stat**', async (route) => {
    if (
      route.request().headers()['authorization'] === 'Bearer fixture-only-42'
    ) {
      started()
      await gate
      await route.fulfill({
        json: { success: true, data: { quota: 777, rpm: 777, tpm: 777 } },
      })
      return
    }
    await route.fulfill({ json: { success: false } })
  })
  await page.goto('/usage-logs/common')
  await pending
  await page.evaluate(
    (value) => window.settingsFixture.signIn(value),
    bundle(99)
  )
  await expect(
    page.getByRole('alert').filter({ hasText: 'Usage statistics unavailable' })
  ).toBeVisible()
  release()
  await expect(page.getByText('777', { exact: true })).toHaveCount(0)
  await expect(page.getByText('RPM', { exact: true })).toHaveCount(0)
})

test('failed statistics refresh retains labeled values rather than zero', async ({
  page,
}) => {
  await fixture(page)
  let failed = false
  await page.route('**/api/log/self/stat**', (route) =>
    route.fulfill({
      json: failed
        ? { success: false }
        : { success: true, data: { quota: 5, rpm: 7, tpm: 11, token: 11 } },
    })
  )
  await page.goto('/usage-logs/common')
  await expect(page.getByText('RPM', { exact: true })).toBeVisible()
  failed = true
  await page
    .getByRole('button', { name: 'Refresh statistics', exact: true })
    .click()
  await expect(
    page.getByText('Showing previous statistics. Refresh failed.', {
      exact: true,
    })
  ).toBeVisible()
  await expect(page.getByText('7', { exact: true })).toBeVisible()
})

for (const malformed of [
  { name: 'missing metrics', body: '{"success":true,"data":{}}' },
  {
    name: 'wrong metric type',
    body: '{"success":true,"data":{"quota":0,"rpm":0,"tpm":"0"}}',
  },
  {
    name: 'nonfinite metric',
    body: '{"success":true,"data":{"quota":1e999,"rpm":0,"tpm":0}}',
  },
]) {
  test(`malformed statistics success with ${malformed.name} stays unavailable and safe retry accepts zero`, async ({
    page,
  }) => {
    const { writes } = await fixture(page)
    let valid = false
    await page.route('**/api/log/self/stat**', (route) =>
      route.fulfill({
        contentType: 'application/json',
        body: valid
          ? '{"success":true,"data":{"quota":0,"rpm":0,"tpm":0}}'
          : malformed.body,
      })
    )
    await page.goto('/usage-logs/common')
    await expect(
      page
        .getByRole('alert')
        .filter({ hasText: 'Usage statistics unavailable' })
    ).toBeVisible()
    await expect(page.getByText('RPM', { exact: true })).toHaveCount(0)
    valid = true
    await page
      .getByRole('button', { name: 'Retry statistics', exact: true })
      .click()
    await expect(page.getByText('RPM', { exact: true })).toBeVisible()
    await expect(
      page
        .getByRole('alert')
        .filter({ hasText: 'Usage statistics unavailable' })
    ).toHaveCount(0)
    expect(writes).toEqual([])
  })
}

test('malformed statistics refresh retains previously confirmed metrics with a stale label', async ({
  page,
}) => {
  const { writes } = await fixture(page)
  let malformed = false
  await page.route('**/api/log/self/stat**', (route) =>
    route.fulfill({
      json: {
        success: true,
        data: malformed ? {} : { quota: 5, rpm: 7, tpm: 11 },
      },
    })
  )
  await page.goto('/usage-logs/common')
  await expect(page.getByText('7', { exact: true })).toBeVisible()
  malformed = true
  await page
    .getByRole('button', { name: 'Refresh statistics', exact: true })
    .click()
  await expect(
    page.getByText('Showing previous statistics. Refresh failed.', {
      exact: true,
    })
  ).toBeVisible()
  await expect(page.getByText('7', { exact: true })).toBeVisible()
  await expect(page.getByText('11', { exact: true })).toBeVisible()
  expect(writes).toEqual([])
})
