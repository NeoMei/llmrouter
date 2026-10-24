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

const owner = '11111111-1111-4111-8111-111111111111'
const generation = '22222222-2222-4222-8222-222222222222'
const pool = '33333333-3333-4333-8333-333333333333'
const space = {
  provider: 'aliyun',
  model: 'text-embedding-v4',
  version: 'v4',
  dimensions: 1024,
  input_semantics: 'text/query-document-v1',
}
async function fixture(
  page: Page,
  options: {
    read?: boolean
    operate?: boolean
    write?: boolean
    gateway?: boolean
  } = {}
) {
  const state = {
    writes: [] as { action: string; body: Record<string, unknown> }[],
    reads: [] as string[],
    fail: '',
    failRead: false,
    empty: false,
    holdRead: undefined as Promise<void> | undefined,
    holdWrite: undefined as Promise<void> | undefined,
    member: {
      member_id: owner,
      generation_id: generation as string | undefined,
      indices: [0, 2],
      legacy_disabled_indices: [2],
      quota_group: 'shared-funds',
      state: 'paused',
      paused: true,
      isolated: false,
      legacy_blocked: false,
      blockers: [{ reason: 'manual_pause', scope: 'account' }],
    },
    routes: [
      {
        id: 41,
        public_id: 'fixed-text',
        display_name: 'Fixed text',
        client: 'all',
        aliases: ['alias'],
        metadata: `{ "future":900719925474099312345,"streaming":false,"zero":0,"embedding_space":${JSON.stringify(
          space
        )}}`,
        enabled: true,
        targets: [
          {
            id: 2,
            channel_id: 7,
            upstream_model: 'text-embedding-v4',
            billing_model: 'text-embedding-v4',
            preferred_wires: '["openai_chat"]',
            priority: 0,
            weight: 1,
            enabled: true,
            metadata: `{"embedding_space":${JSON.stringify(space)}}`,
          },
        ],
      },
    ],
    routeWrites: [] as Record<string, unknown>[],
  }
  await page.addInitScript(() => {
    localStorage.setItem('setup_status_checked', 'true')
    localStorage.setItem('i18nextLng', 'en')
    localStorage.setItem(
      'status',
      JSON.stringify({ system_name: 'Isolated admin fixture' })
    )
  })
  await page.route('**/api/**', async (route) => {
    const request = route.request()
    const url = new URL(request.url())
    const path = url.pathname
    const now = Math.floor(Date.now() / 1000)
    if (path === '/api/user/auth/refresh') {
      return route.fulfill({
        json: {
          success: true,
          data: {
            access_token: 'fixture-only',
            token_type: 'Bearer',
            access_expires_at: now + 3600,
            user: {
              id: 91,
              username: 'task9-fixture',
              display_name: 'Task9 Fixture',
              role: 10,
              status: 1,
              permissions: {
                admin_permissions: {
                  channel: {
                    read: options.read !== false,
                    operate: options.operate !== false,
                    write: options.write !== false,
                  },
                },
              },
            },
            session: {
              sid: 'task9-isolated',
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
    }
    if (path === '/api/status') {
      return route.fulfill({
        json: {
          success: true,
          data: { system_name: 'Isolated admin fixture' },
        },
      })
    }
    if (path === '/api/notice') {
      return route.fulfill({ json: { success: true, data: '' } })
    }
    if (path === '/api/channel' || path === '/api/channel/') {
      return route.fulfill({
        json: {
          success: true,
          data: {
            items: [
              {
                id: 7,
                type: options.gateway ? 59 : 60,
                key: '',
                name: 'Fixture pool',
                status: 1,
                models: 'ocr-model,text-embedding-v4',
                model_mapping: '{}',
                group: 'default',
                created_time: 0,
                test_time: 0,
                response_time: 0,
                balance_updated_time: 0,
                channel_info: {
                  is_multi_key: !options.gateway,
                  multi_key_size: 3,
                  multi_key_mode: 'polling',
                },
              },
            ],
            total: 1,
            type_counts: {},
          },
        },
      })
    }
    if (
      path === '/api/channel/models' ||
      path === '/api/channel/models_enabled'
    ) {
      return route.fulfill({ json: { success: true, data: [] } })
    }
    if (path === '/api/capabilities') {
      return route.fulfill({
        json: {
          success: true,
          data: {
            capabilities: ['ocr.jobs.create', 'ocr.jobs.retrieve'].map(
              (operation) => ({
                channel_type: options.gateway ? 59 : 60,
                supported: true,
                operation,
              })
            ),
          },
        },
      })
    }
    if (path === '/api/channel/7/account-pool') {
      state.reads.push(url.search)
      if (state.holdRead) await state.holdRead
      if (state.failRead) {
        return route.fulfill({
          status: 503,
          json: {
            success: false,
            code: 'state_unavailable',
            message: 'PRIVATE raw supplier error key=SECRET',
          },
        })
      }
      const members = state.empty
        ? []
        : [
            state.member,
            {
              ...state.member,
              member_id: '44444444-4444-4444-8444-444444444444',
              generation_id: owner,
              indices: [1],
              paused: false,
              state: 'cooling',
              retry_at: '2030-01-02T03:04:05Z',
              blockers: [
                {
                  reason: 'rate_limit',
                  scope: 'account',
                  operation: url.searchParams.get('operation'),
                  model: url.searchParams.get('model'),
                  retry_at: '2030-01-02T03:04:05Z',
                },
              ],
            },
          ]
      return route.fulfill({
        json: {
          success: true,
          data: {
            server_time: new Date().toISOString(),
            channel_id: 7,
            profile_id: options.gateway ? 9 : undefined,
            pool_id: pool,
            selected_scope: {
              operation: url.searchParams.get('operation'),
              model: url.searchParams.get('model'),
            },
            quota_remaining: 'unknown',
            group_scope: 'global',
            status: state.empty ? 'empty' : 'ready',
            counts: {
              total: members.length,
              available: !state.empty && !state.member.paused ? 1 : 0,
              paused: !state.empty && state.member.paused ? 1 : 0,
              cooling: state.empty ? 0 : 1,
              isolated: 0,
              legacy_blocked: 0,
              unavailable: 0,
            },
            members,
          },
        },
      })
    }
    if (path.startsWith('/api/channel/7/account-pool/')) {
      const action = path.split('/').at(-1) || ''
      const body = request.postDataJSON()
      state.writes.push({ action, body })
      if (state.holdWrite) await state.holdWrite
      if (state.fail) {
        return route.fulfill({
          status: 409,
          json: {
            success: false,
            code: state.fail,
            message: 'PRIVATE secret key raw body',
          },
        })
      }
      if (action === 'pause') {
        state.member.paused = true
        state.member.state = 'paused'
        state.member.blockers = [{ reason: 'manual_pause', scope: 'account' }]
      }
      if (action === 'resume') {
        state.member.paused = false
        state.member.state = 'available'
        state.member.blockers = []
      }
      if (action === 'quota-group') state.member.quota_group = body.quota_group
      return route.fulfill({
        json: {
          success: true,
          data: { applied: true, refresh_required: true, action },
        },
      })
    }
    if (path === '/api/client_model_routes/options') {
      return route.fulfill({
        json: {
          success: true,
          data: {
            channels: [
              {
                id: 7,
                type: 17,
                name: 'Ali fixture',
                models: [
                  'text-embedding-v4',
                  'qwen3-vl-embedding',
                  'other-model',
                ],
                wires: ['openai_chat', 'qwen_multimodal_embedding'],
              },
              {
                id: 8,
                type: 60,
                name: 'OCR fixture',
                models: ['ocr-model'],
                wires: ['paddleocr_jobs'],
              },
            ],
            billing_models: ['text-embedding-v4'],
          },
        },
      })
    }
    if (path === '/api/client_model_routes/' && request.method() === 'GET') {
      return route.fulfill({ json: { success: true, data: state.routes } })
    }
    if (path === '/api/client_model_routes/' && request.method() === 'PUT') {
      const body = request.postDataJSON()
      state.routeWrites.push(body)
      state.routes = [body]
      return route.fulfill({ json: { success: true, data: body } })
    }
    if (path === '/api/log' || path === '/api/log/self') {
      return route.fulfill({
        json: {
          success: true,
          data: {
            total: 1,
            items: [
              {
                id: 1,
                user_id: 91,
                created_at: now,
                type: 2,
                content: 'Native fixture',
                username: 'task9-fixture',
                model_name: 'ocr-model',
                quota: 500,
                prompt_tokens: 0,
                completion_tokens: 0,
                use_time: 1,
                is_stream: false,
                channel: 7,
                token_name: 'Fixture token',
                other: JSON.stringify({
                  billing_unit: 'request',
                  request_count: 1,
                  model_price: 0.001,
                  group_ratio: 1,
                  native_usage_unit: 'characters',
                  native_usage_characters: 0,
                  native_pricing: 'configured_per_request',
                }),
              },
            ],
          },
        },
      })
    }
    if (path === '/api/log/stat' || path === '/api/log/self/stat') {
      return route.fulfill({
        json: { success: true, data: { quota: 500, rpm: 1, tpm: 0 } },
      })
    }
    if (path.includes('group')) {
      return route.fulfill({ json: { success: true, data: ['default'] } })
    }
    return route.fulfill({ json: { success: true, data: {} } })
  })
  return state
}
async function openPool(page: Page) {
  await page.goto('/channels')
  await page.getByRole('button', { name: 'Open menu' }).first().click()
  await page
    .getByRole('menuitem', { name: 'Account pool', exact: true })
    .click()
  await expect(page.getByRole('dialog')).toBeVisible()
}

test('safe native pool shows counts and expiry, retains pause after pending resume failure, and refreshes authoritatively', async ({
  page,
}) => {
  const state = await fixture(page)
  let releaseRead!: () => void
  state.holdRead = new Promise((resolve) => {
    releaseRead = resolve
  })
  await openPool(page)
  await expect(page.getByRole('dialog').getByRole('status')).toContainText(
    'Loading'
  )
  releaseRead()
  state.holdRead = undefined
  await expect(page.getByText('Paused: 1', { exact: true })).toBeVisible()
  await expect(page.getByText('Cooling: 1', { exact: true })).toBeVisible()
  await expect(
    page.getByText('Blocking expiry: 2030-01-02T03:04:05Z')
  ).toBeVisible()
  const account = page.getByRole('article', {
    name: 'Account 1, 3',
    exact: true,
  })
  state.fail = 'stale_generation'
  let releaseWrite!: () => void
  state.holdWrite = new Promise((resolve) => {
    releaseWrite = resolve
  })
  await account.getByRole('button', { name: 'Resume manual pause' }).click()
  await page.getByRole('button', { name: 'Apply', exact: true }).click()
  await expect(
    page
      .getByRole('alertdialog')
      .getByRole('button', { name: 'Apply', exact: true })
  ).toBeDisabled()
  releaseWrite()
  state.holdWrite = undefined
  await expect(account.getByRole('alert')).toContainText('stale_generation')
  await expect(
    account.getByRole('button', { name: 'Resume manual pause' })
  ).toBeDisabled()
  await expect(page.getByText('Paused: 1', { exact: true })).toBeVisible()
  await expect(page.getByRole('dialog')).not.toContainText('PRIVATE')
  state.fail = ''
  await page.getByRole('button', { name: 'Refresh scope' }).click()
  await account.getByRole('button', { name: 'Resume manual pause' }).click()
  await page.getByRole('button', { name: 'Apply', exact: true }).click()
  await expect(page.getByText('Available: 1', { exact: true })).toBeVisible()
  expect(state.writes[0]).toMatchObject({
    action: 'resume',
    body: {
      member_id: owner,
      pool_id: pool,
      target_scope: 'account_global',
      profile_id: 0,
    },
  })
})

test('Gateway pool opens without native multikey and sends current group, generation and exact selected recovery scope', async ({
  page,
}) => {
  const state = await fixture(page, { gateway: true })
  await openPool(page)
  await page.getByLabel('Diagnostic operation').fill('ocr.jobs.retrieve')
  await page.getByRole('button', { name: 'Refresh scope' }).click()
  const account = page.getByRole('article', {
    name: 'Account 1, 3',
    exact: true,
  })
  await account.getByLabel('Global quota group').fill('new-funds')
  await account.getByRole('button', { name: 'Assign quota group' }).click()
  await expect(page.getByRole('alertdialog')).toContainText(
    'global across channels'
  )
  await page.getByRole('button', { name: 'Apply', exact: true }).click()
  await expect(account.getByLabel('Global quota group')).toHaveValue(
    'new-funds'
  )
  await expect(
    account.getByRole('button', { name: 'Assign quota group' })
  ).toBeDisabled()
  expect(state.writes[0]).toMatchObject({
    action: 'quota-group',
    body: {
      expected_quota_group: 'shared-funds',
      quota_group: 'new-funds',
      profile_id: 9,
      target_scope: 'account_global',
    },
  })
  await account.getByLabel('Recovery scope').selectOption('operation')
  await account
    .getByRole('button', { name: 'Recover current generation' })
    .click()
  await page.getByRole('button', { name: 'Apply', exact: true }).click()
  await expect(page.getByRole('alertdialog')).not.toBeVisible()
  expect(state.writes[1]).toMatchObject({
    action: 'recover',
    body: {
      generation_id: generation,
      target_scope: 'operation',
      operation: 'ocr.jobs.retrieve',
      model: 'ocr-model',
    },
  })
  await account
    .getByRole('button', { name: 'Recover shared quota group' })
    .click()
  await page.getByRole('button', { name: 'Apply', exact: true }).click()
  await expect(page.getByRole('alertdialog')).not.toBeVisible()
  expect(state.writes[2]).toMatchObject({
    action: 'recover-group',
    body: { quota_group: 'new-funds' },
  })
})

test('read-only pool disables mutations and renders empty and safe error states', async ({
  page,
}) => {
  const state = await fixture(page, { operate: false, write: false })
  await openPool(page)
  await expect(
    page.getByRole('button', { name: 'Resume manual pause' })
  ).toBeDisabled()
  await expect(
    page.getByRole('button', { name: 'Recover current generation' }).first()
  ).toBeDisabled()
  await expect(page.getByLabel('Global quota group').first()).toBeDisabled()
  state.empty = true
  await page.getByRole('button', { name: 'Refresh scope' }).click()
  await expect(page.getByText('No accounts in this pool')).toBeVisible()
  state.failRead = true
  await page.getByRole('button', { name: 'Refresh scope' }).click()
  await expect(page.getByRole('dialog').getByRole('alert')).toContainText(
    'Actions are disabled'
  )
  await expect(page.getByRole('dialog')).not.toContainText('PRIVATE')
  expect(state.writes).toEqual([])
})

test('metadata fields preserve unknown large integers, false and zero; invalid JSON and changed fixed identity block writes', async ({
  page,
}) => {
  const state = await fixture(page)
  await page.goto('/client-model-routes')
  await page.getByRole('button', { name: 'Edit', exact: true }).click()
  const editor = page.getByRole('region', {
    name: 'Model capabilities',
    exact: true,
  })
  await expect(editor.getByLabel('streaming', { exact: true })).toHaveValue(
    'false'
  )
  await editor.getByLabel('async', { exact: true }).selectOption('false')
  await page.getByRole('button', { name: 'Save route' }).click()
  await expect(page.getByRole('button', { name: 'Save route' })).toBeEnabled()
  expect(state.routeWrites[0].metadata).toContain(
    '"future":900719925474099312345'
  )
  expect(state.routeWrites[0].metadata).toContain('"zero":0')
  expect(state.routeWrites[0].metadata).toContain('"async":false')
  const targets = state.routeWrites[0].targets as { metadata: string }[]
  expect(targets[0].metadata).toEqual(state.routes[0].targets[0].metadata)
  await page.getByRole('button', { name: 'Edit', exact: true }).click()
  await editor
    .getByRole('textbox', { name: 'Metadata (JSON)', exact: true })
    .fill('{invalid')
  await expect(editor.getByLabel('streaming', { exact: true })).toBeDisabled()
  await page.getByRole('button', { name: 'Save route' }).click()
  await expect(page.getByRole('alert').last()).toContainText('valid JSON')
  await editor
    .getByRole('textbox', { name: 'Metadata (JSON)', exact: true })
    .fill(JSON.stringify({ embedding_space: { ...space, dimensions: 2048 } }))
  await page.getByRole('button', { name: 'Save route' }).click()
  await expect(page.getByRole('alert').last()).toContainText('new public route')
  await editor
    .getByRole('textbox', { name: 'Metadata (JSON)', exact: true })
    .fill(JSON.stringify({ embedding_space: space }))
  await page
    .getByRole('combobox', { name: 'Upstream model', exact: true })
    .selectOption('other-model')
  await page.getByRole('button', { name: 'Save route' }).click()
  await expect(page.getByRole('alert').last()).toContainText(
    'Target model must match'
  )
  expect(state.routeWrites).toHaveLength(1)
  await page
    .getByRole('combobox', { name: 'Channel', exact: true })
    .selectOption('8')
  await expect(
    page.getByRole('checkbox', { name: 'PaddleOCR Jobs' })
  ).toBeChecked()
  await expect(
    page.getByRole('checkbox', { name: 'Qwen Multimodal Embedding' })
  ).toBeDisabled()
})

test('missing current credentials disable recovery; stale quota membership is not retried optimistically', async ({
  page,
}) => {
  const state = await fixture(page, { gateway: true })
  state.member.generation_id = undefined
  state.member.state = 'unavailable'
  await openPool(page)
  const account = page.getByRole('article', {
    name: 'Account 1, 3',
    exact: true,
  })
  await expect(
    account.getByRole('button', { name: 'Recover current generation' })
  ).toBeDisabled()
  state.fail = 'stale_quota_group'
  await account.getByLabel('Global quota group').fill('new-group')
  await account.getByRole('button', { name: 'Assign quota group' }).click()
  await page.getByRole('button', { name: 'Apply', exact: true }).click()
  await expect(account.getByRole('alert')).toContainText('stale_quota_group')
  expect(state.member.quota_group).toBe('shared-funds')
  await expect(
    account.getByRole('button', { name: 'Assign quota group' })
  ).toBeDisabled()
})

test('channel read denial disables the account pool entry without requesting a snapshot', async ({
  page,
}) => {
  const state = await fixture(page, { read: false })
  await page.goto('/channels')
  await page.getByRole('button', { name: 'Open menu' }).first().click()
  await expect(
    page.getByRole('menuitem', { name: 'Account pool', exact: true })
  ).toBeDisabled()
  expect(state.reads).toEqual([])
})

test('existing consumption details show native requests and explicit zero characters alongside charged zero-token requests', async ({
  page,
}) => {
  await fixture(page)
  await page.goto('/usage-logs/common')
  await page.getByTitle('Click to view full details').first().click()
  const dialog = page.getByRole('dialog')
  await expect(dialog.getByText('Native usage', { exact: true })).toBeVisible()
  await expect(dialog.getByText('Requests', { exact: true })).toBeVisible()
  await expect(dialog.getByText('Characters', { exact: true })).toBeVisible()
  await expect(dialog).toContainText('Zero tokens do not imply a free request')
  await expect(dialog).toContainText('configured_per_request')
})

test('pause and global health recovery keep manual pause distinct from scoped recovery', async ({
  page,
}) => {
  const state = await fixture(page)
  state.member.paused = false
  state.member.state = 'available'
  await openPool(page)
  const account = page.getByRole('article', {
    name: 'Account 1, 3',
    exact: true,
  })
  await account.getByRole('button', { name: 'Pause new admissions' }).click()
  await page.getByRole('button', { name: 'Apply', exact: true }).click()
  await expect(
    account.getByRole('button', { name: 'Resume manual pause' })
  ).toBeEnabled()
  expect(state.writes[0]).toMatchObject({
    action: 'pause',
    body: { target_scope: 'account_global' },
  })
  for (const scope of ['account_global', 'model', 'selected']) {
    await account.getByLabel('Recovery scope').selectOption(scope)
    await account
      .getByRole('button', { name: 'Recover current generation' })
      .click()
    await page.getByRole('button', { name: 'Apply', exact: true }).click()
    await expect(page.getByRole('alertdialog')).not.toBeVisible()
    expect(state.writes.at(-1)).toMatchObject({
      action: 'recover',
      body: { target_scope: scope, generation_id: generation },
    })
    await expect(
      account.getByRole('button', { name: 'Resume manual pause' })
    ).toBeEnabled()
  }
})

test('padded fixed identities allow unrelated saves and equivalent whitespace edits without rewriting other metadata', async ({
  page,
}) => {
  const state = await fixture(page)
  const padded = {
    ...space,
    provider: ' aliyun ',
    model: ' text-embedding-v4 ',
    version: '\u0085v4\u3000',
    input_semantics: ' text/query-document-v1 ',
  }
  const raw = `{ "future":900719925474099312345,"zero":0,"embedding_space":${JSON.stringify(padded)}}`
  const targetRaw = JSON.stringify({
    embedding_space: { ...padded, model: '\ttext-embedding-v4\n' },
  })
  state.routes[0].metadata = raw
  state.routes[0].targets[0].metadata = targetRaw
  await page.goto('/client-model-routes')
  await page.getByRole('button', { name: 'Edit', exact: true }).click()
  const editor = page.getByRole('region', {
    name: 'Model capabilities',
    exact: true,
  })
  await editor.getByLabel('streaming', { exact: true }).selectOption('false')
  await page.getByRole('button', { name: 'Save route' }).click()
  await expect(page.getByRole('button', { name: 'Save route' })).toBeEnabled()
  expect(state.routeWrites[0].metadata).toBe(
    `${raw.slice(0, -1)},"streaming":false}`
  )
  expect(
    (state.routeWrites[0].targets as { metadata: string }[])[0].metadata
  ).toBe(targetRaw)
  await page.getByRole('button', { name: 'Edit', exact: true }).click()
  const normalized = `{ "future":900719925474099312345,"zero":0,"embedding_space":${JSON.stringify(space)},"streaming":false}`
  await editor
    .getByRole('textbox', { name: 'Metadata (JSON)', exact: true })
    .fill(normalized)
  await page.getByRole('button', { name: 'Save route' }).click()
  await expect(page.getByRole('button', { name: 'Save route' })).toBeEnabled()
  expect(state.routeWrites[1].metadata).toBe(normalized)
  expect(
    (state.routeWrites[1].targets as { metadata: string }[])[0].metadata
  ).toBe(targetRaw)
})
