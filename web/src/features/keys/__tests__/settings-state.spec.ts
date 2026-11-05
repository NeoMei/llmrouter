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

import {
  fixture,
  screenshot,
  bundle,
} from '../../wallet/__tests__/settings-fixture'

for (const disclosure of [false, true]) {
  test(`UniHub signed-in native key default payload survives advanced disclosure ${disclosure}`, async ({
    page,
  }) => {
    const { writes } = await fixture(page, true)
    await page.goto('/keys')
    await page
      .getByRole('button', { name: 'Create API Key', exact: true })
      .click()
    await page
      .getByRole('textbox', { name: 'Name', exact: true })
      .fill('native-fixture')
    await expect(page.getByText('Model Limits', { exact: true })).toBeVisible()
    await expect(
      page.getByRole('spinbutton', { name: 'Quantity', exact: true })
    ).toHaveCount(0)
    const advanced = page.getByRole('button', { name: /Advanced Settings/ })
    await expect(advanced).toHaveAttribute('aria-expanded', 'false')
    await screenshot(page, 'native-key-drawer')
    if (disclosure) {
      await advanced.press('Enter')
      await expect(advanced).toHaveAttribute('aria-expanded', 'true')
      await expect(
        page.getByRole('spinbutton', { name: 'Quantity', exact: true })
      ).toHaveValue('1')
      await advanced.press('Enter')
    }
    await page
      .getByRole('button', { name: 'Save changes', exact: true })
      .press('Enter')
    await expect.poll(() => writes.length).toBe(1)
    expect(writes).toEqual([
      {
        path: '/api/token/',
        body: {
          name: 'native-fixture',
          remain_quota: 0,
          expired_time: -1,
          unlimited_quota: true,
          model_limits_enabled: false,
          model_limits: '',
          allow_ips: '',
          group: 'auto',
          cross_group_retry: true,
        },
      },
    ])
  })
}

test('Chinese native-key drawer labels identity and keeps model access outside advanced disclosure', async ({
  page,
}) => {
  await fixture(page, true, 'zh')
  await page.goto('/keys')
  await expect(
    page.getByRole('heading', { name: 'LLMRouter 产品 API Key', exact: true })
  ).toBeVisible()
  await page.getByRole('button', { name: '创建 API Key', exact: true }).click()
  await page
    .getByRole('textbox', { name: '名称', exact: true })
    .fill('原生测试 Key')
  await expect(page.getByText('模型权限', { exact: true })).toBeVisible()
  await expect(page.getByRole('button', { name: /高级设置/ })).toHaveAttribute(
    'aria-expanded',
    'false'
  )
  await screenshot(page, 'native-key-drawer-zh')
})

test('identity switch clears prior native-key budget rows and an unfinished key draft', async ({
  page,
}) => {
  const { writes } = await fixture(page)
  await page.route('**/api/token/?*', (route) => {
    const previous =
      route.request().headers()['authorization'] === 'Bearer fixture-only-42'
    return route.fulfill({
      json: {
        success: true,
        data: {
          items: previous
            ? [
                {
                  id: 7,
                  name: 'private-key-A',
                  key: 'synthetic-masked',
                  status: 1,
                  remain_quota: 123,
                  used_quota: 5,
                  unlimited_quota: false,
                  expired_time: -1,
                  created_time: 1,
                  accessed_time: 1,
                  group: 'default',
                  cross_group_retry: false,
                  model_limits_enabled: false,
                  model_limits: '',
                  allow_ips: '',
                },
              ]
            : [],
          total: previous ? 1 : 0,
        },
      },
    })
  })
  await page.goto('/keys')
  await expect(page.getByText('private-key-A', { exact: true })).toBeVisible()
  await page
    .getByRole('button', { name: 'Create API Key', exact: true })
    .click()
  await page
    .getByRole('textbox', { name: 'Name', exact: true })
    .fill('unfinished-for-A')
  await page.evaluate(
    (value) => window.settingsFixture.signIn(value),
    bundle(99)
  )
  await expect(
    page.getByRole('textbox', { name: 'Name', exact: true })
  ).toHaveCount(0)
  await expect(page.getByText('private-key-A', { exact: true })).toHaveCount(0)
  expect(writes).toEqual([])
})
