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
import { expect, test, type Page } from '@playwright/test'

const now = Math.floor(Date.now() / 1000)

type RouteRecord = {
  id: number
  public_id: string
  display_name: string
  client: string
  aliases: string[]
  metadata: string
  enabled: boolean
  targets: Array<{
    id?: number
    route_id?: number
    channel_id: number
    upstream_model: string
    billing_model: string
    preferred_wires: string
    priority: number
    weight: number
    enabled: boolean
  }>
}

type FixtureState = {
  routes: RouteRecord[]
  writes: Array<{ method: string; path: string; body?: unknown }>
}

async function installFixture(page: Page, role = 10): Promise<FixtureState> {
  const state: FixtureState = { routes: [], writes: [] }

  await page.addInitScript(() => {
    window.localStorage.setItem('setup_status_checked', 'true')
    window.localStorage.setItem(
      'status',
      JSON.stringify({ system_name: 'Codex Gateway E2E' })
    )
  })

  await page.route('**/api/**', async (route) => {
    const request = route.request()
    const path = new URL(request.url()).pathname
    const method = request.method()

    if (path === '/api/user/auth/refresh') {
      await route.fulfill({
        json: {
          success: true,
          data: {
            access_token: 'e2e-access-token',
            token_type: 'Bearer',
            access_expires_at: now + 3600,
            user: {
              id: 1,
              username: 'e2e-admin',
              display_name: 'E2E Admin',
              role,
              status: 1,
            },
            session: {
              sid: 'e2e-session',
              current: true,
              login_method: 'password',
              ip: '127.0.0.1',
              user_agent: 'Playwright',
              created_at: now,
              last_active_at: now,
              expires_at: now + 3600,
            },
          },
        },
      })
      return
    }
    if (path === '/api/status') {
      await route.fulfill({
        json: { success: true, data: { system_name: 'Codex Gateway E2E' } },
      })
      return
    }
    if (path === '/api/notice') {
      await route.fulfill({ json: { success: true, data: '' } })
      return
    }
    if (path === '/api/client_model_routes/options') {
      await route.fulfill({
        json: {
          success: true,
          data: {
            channels: [
              {
                id: 7,
                name: 'DeepSeek',
                type: 18,
                models: ['deepseek-chat', 'deepseek-reasoner'],
                wires: ['responses', 'openai_chat'],
              },
            ],
            billing_models: ['deepseek-chat', 'deepseek-reasoner'],
          },
        },
      })
      return
    }
    if (path === '/api/client_model_routes/' && method === 'GET') {
      await route.fulfill({ json: { success: true, data: state.routes } })
      return
    }
    if (path === '/api/client_model_routes/' && method === 'POST') {
      const input = request.postDataJSON() as Omit<RouteRecord, 'id'>
      state.writes.push({ method, path, body: input })
      state.routes.push({ ...input, id: 41 })
      await route.fulfill({ json: { success: true, data: state.routes[0] } })
      return
    }
    if (path === '/api/client_model_routes/' && method === 'PUT') {
      const input = request.postDataJSON() as RouteRecord
      state.writes.push({ method, path, body: input })
      state.routes = state.routes.map((item) =>
        item.id === input.id ? input : item
      )
      await route.fulfill({ json: { success: true, data: input } })
      return
    }
    if (path === '/api/client_model_routes/41' && method === 'DELETE') {
      state.writes.push({ method, path })
      state.routes = []
      await route.fulfill({ json: { success: true } })
      return
    }

    await route.fulfill({ json: { success: true, data: {} } })
  })

  return state
}

test('admin can create, edit, and delete a client model route', async ({
  page,
}) => {
  const state = await installFixture(page)

  await page.goto('/client-model-routes')
  await expect(
    page.getByRole('heading', { name: 'Client Model Routes' })
  ).toBeVisible()
  await expect(page.getByText('No client model routes')).toBeVisible()

  await page.getByLabel('Public ID').fill('deepseek/deepseek-chat')
  await page.getByLabel('Display name').fill('DeepSeek Chat')
  await page.getByLabel('Aliases (comma separated)').fill('gpt-5.6-sol')
  await page.getByLabel('Channel').selectOption('7')
  await expect(page.getByLabel('Upstream model')).toHaveValue('deepseek-chat')
  await expect(
    page.getByRole('checkbox', { name: 'Responses' })
  ).toBeChecked()
  await expect(
    page.getByRole('checkbox', { name: 'OpenAI Chat' })
  ).toBeChecked()
  await expect(
    page.getByRole('checkbox', { name: 'Anthropic Messages' })
  ).toBeDisabled()
  await page.getByLabel('Billing model').selectOption('deepseek-chat')
  await page.getByRole('button', { name: 'Save route' }).click()

  await expect(
    page.getByText('deepseek/deepseek-chat', { exact: true })
  ).toBeVisible()
  expect(state.writes[0]).toMatchObject({
    method: 'POST',
    path: '/api/client_model_routes/',
    body: {
      public_id: 'deepseek/deepseek-chat',
      aliases: ['gpt-5.6-sol'],
      targets: [
        {
          channel_id: 7,
          upstream_model: 'deepseek-chat',
          billing_model: 'deepseek-chat',
          preferred_wires: '["responses","openai_chat"]',
        },
      ],
    },
  })

  await page.getByRole('button', { name: 'Edit' }).click()
  await page.getByLabel('Display name').fill('DeepSeek Chat Updated')
  await page.getByRole('button', { name: 'Save route' }).click()
  expect(state.writes[1]).toMatchObject({
    method: 'PUT',
    path: '/api/client_model_routes/',
    body: { id: 41, display_name: 'DeepSeek Chat Updated' },
  })
  await expect(page.getByRole('button', { name: 'Save route' })).toBeEnabled()

  await page
    .getByRole('button', { name: 'Delete', exact: true })
    .filter({ visible: true })
    .click()
  await expect(page.getByRole('alertdialog')).toBeVisible()
  await expect(
    page.getByRole('heading', { name: 'Delete client model route' })
  ).toBeVisible()
  await page.getByRole('button', { name: 'Delete', exact: true }).last().click()
  await expect(page.getByText('No client model routes')).toBeVisible()
  expect(state.writes[2]).toEqual({
    method: 'DELETE',
    path: '/api/client_model_routes/41',
  })
})

test('duplicate aliases are rejected before an API write', async ({ page }) => {
  const state = await installFixture(page)

  await page.goto('/client-model-routes')
  await page.getByLabel('Public ID').fill('deepseek/deepseek-chat')
  await page
    .getByLabel('Aliases (comma separated)')
    .fill('gpt-5.6-sol, GPT-5.6-SOL')
  await page.getByRole('button', { name: 'Save route' }).click()

  await expect(page.getByRole('alert')).toContainText('Aliases must be unique')
  expect(state.writes).toEqual([])
})

test('non-admin users cannot open client model route management', async ({
  page,
}) => {
  await installFixture(page, 1)
  await page.goto('/client-model-routes')
  await expect(page).toHaveURL(/\/403$/)
})
