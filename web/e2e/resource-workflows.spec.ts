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
import { expect, test, type Page, type TestInfo } from '@playwright/test'

const now = Math.floor(Date.now() / 1000)

type FixtureState = {
  files: Array<Record<string, unknown>>
  jobs: Array<Record<string, unknown>>
  writes: string[]
}

async function installFixture(
  page: Page,
  supported = true,
  role = 1,
  chats: Array<Record<string, string>> = []
) {
  const state: FixtureState = {
    files: [
      {
        id: 'file-training',
        object: 'file',
        bytes: 128,
        created_at: now,
        filename: 'training.jsonl',
        purpose: 'fine-tune',
        status: 'ready',
      },
    ],
    jobs: [],
    writes: [],
  }

  await page.addInitScript(() => {
    window.localStorage.setItem('setup_status_checked', 'true')
    window.localStorage.setItem(
      'status',
      JSON.stringify({ system_name: 'Codex Gateway E2E' })
    )
  })

  await page.route('**/api/**', async (route) => {
    const path = new URL(route.request().url()).pathname
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
              username: 'e2e-user',
              display_name: 'E2E User',
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
    if (path === '/api/capabilities') {
      const operations = [
        'files.create',
        'files.content',
        'files.delete',
        'fine_tuning.jobs.create',
        'fine_tuning.jobs.cancel',
        'models.delete',
      ]
      await route.fulfill({
        json: {
          success: true,
          data: {
            version: 'e2e-v1',
            capabilities: operations.map((operation) => ({
              channel_type: 1,
              channel_name: 'fixture-provider',
              protocol: 'openai',
              operation,
              supported,
            })),
          },
        },
      })
      return
    }
    if (path === '/api/status') {
      await route.fulfill({
        json: {
          success: true,
          data: { system_name: 'Codex Gateway E2E', chats },
        },
      })
      return
    }
    if (path === '/api/notice') {
      await route.fulfill({ json: { success: true, data: '' } })
      return
    }
    await route.fulfill({ json: { success: true, data: {} } })
  })

  await page.route('**/v1/**', async (route) => {
    const request = route.request()
    const path = new URL(request.url()).pathname
    const method = request.method()
    if (method !== 'GET') state.writes.push(`${method} ${path}`)

    if (path === '/v1/files' && method === 'GET') {
      await route.fulfill({ json: { object: 'list', data: state.files } })
      return
    }
    if (path === '/v1/files' && method === 'POST') {
      const uploaded = {
        id: 'file-uploaded',
        object: 'file',
        bytes: 48,
        created_at: now,
        filename: 'second.jsonl',
        purpose: 'fine-tune',
        status: 'ready',
      }
      state.files.push(uploaded)
      await route.fulfill({ json: uploaded })
      return
    }
    if (/^\/v1\/files\/[^/]+$/.test(path) && method === 'DELETE') {
      const id = path.split('/').at(-1)
      state.files = state.files.filter((file) => file.id !== id)
      await route.fulfill({ json: { deleted: true, id } })
      return
    }
    if (path === '/v1/fine-tunes' && method === 'GET') {
      await route.fulfill({ json: { object: 'list', data: state.jobs } })
      return
    }
    if (path === '/v1/fine-tunes' && method === 'POST') {
      const input = request.postDataJSON() as Record<string, unknown>
      const job = {
        id: 'ftjob-e2e',
        object: 'fine-tune',
        model: input.model,
        status: 'running',
        training_file: input.training_file,
        validation_file: input.validation_file,
        created_at: now,
      }
      state.jobs.push(job)
      await route.fulfill({ json: job })
      return
    }
    if (path === '/v1/fine-tunes/ftjob-e2e/events' && method === 'GET') {
      await route.fulfill({
        json: {
          object: 'list',
          data: [
            {
              object: 'fine-tuning-event',
              created_at: now,
              level: 'info',
              message: 'Training started',
            },
          ],
        },
      })
      return
    }
    if (path === '/v1/fine-tunes/ftjob-e2e/cancel' && method === 'POST') {
      state.jobs = state.jobs.map((job) =>
        job.id === 'ftjob-e2e' ? { ...job, status: 'cancelled' } : job
      )
      await route.fulfill({ json: state.jobs[0] })
      return
    }
    await route.fulfill({ status: 404, json: { error: { message: path } } })
  })
  return state
}

async function attachScreenshot(page: Page, testInfo: TestInfo, name: string) {
  await testInfo.attach(name, {
    body: await page.screenshot({ fullPage: true }),
    contentType: 'image/png',
  })
}

test('file and fine-tuning lifecycle is operable', async ({
  page,
}, testInfo) => {
  const consoleErrors: string[] = []
  page.on('console', (message) => {
    if (message.type() === 'error') consoleErrors.push(message.text())
  })
  const state = await installFixture(page)

  await page.goto('/files')
  await expect(page.getByRole('heading', { name: 'Files' })).toBeVisible()
  await expect(page.getByText('training.jsonl')).toBeVisible()

  await page.locator('input[type=file]').setInputFiles({
    name: 'second.jsonl',
    mimeType: 'application/jsonl',
    buffer: Buffer.from('{"messages":[]}\n'),
  })
  await expect(page.getByText('second.jsonl')).toBeVisible()
  await page.getByLabel('Delete second.jsonl').click()
  await expect(page.getByText('second.jsonl')).toBeHidden()
  await attachScreenshot(page, testInfo, 'files-lifecycle')

  await page.goto('/fine-tuning')
  await expect(page.getByRole('heading', { name: 'Fine-tuning' })).toBeVisible()
  await page.getByLabel('Training file').selectOption('file-training')
  await page.getByPlaceholder('Suffix (optional)').fill('e2e')
  await page.getByPlaceholder('Epochs (provider default)').fill('2')
  await page.getByRole('button', { name: 'Create job' }).click()
  await expect(page.getByText('ftjob-e2e')).toBeVisible()
  await expect(page.getByText('Training started')).toBeVisible()
  await page.getByRole('button', { name: 'Cancel' }).click()
  await expect(
    page.getByRole('definition').filter({ hasText: /^cancelled$/ })
  ).toBeVisible()
  await attachScreenshot(page, testInfo, 'fine-tuning-lifecycle')

  expect(state.writes).toEqual([
    'POST /v1/files',
    'DELETE /v1/files/file-uploaded',
    'POST /v1/fine-tunes',
    'POST /v1/fine-tunes/ftjob-e2e/cancel',
  ])
  expect(consoleErrors).toEqual([])
})

test('unsupported capabilities hide mutations and issue no writes', async ({
  page,
}) => {
  const state = await installFixture(page, false)
  await page.goto('/files')
  await expect(page.getByText(/Upload is unavailable/)).toBeVisible()
  await expect(page.locator('input[type=file]')).toHaveCount(0)

  await page.goto('/fine-tuning')
  await expect(page.getByText(/Job creation is unavailable/)).toBeVisible()
  await expect(page.getByRole('button', { name: 'Create job' })).toHaveCount(0)
  expect(state.writes).toEqual([])
})

test('production iframe policy rejects insecure chat presets', async ({
  page,
}) => {
  await installFixture(page, true, 1, [
    { Insecure: 'http://example.com/preview' },
  ])
  await page.goto('/chat/0')
  await expect(page.getByRole('alert')).toContainText('Unable to open chat')
  await expect(page.locator('iframe')).toHaveCount(0)
})

test('system settings require super-admin state', async ({ page }) => {
  await installFixture(page, true, 1)
  await page.goto('/system-settings/site/system-info')
  await expect(page).toHaveURL(/\/403$/)
})
