/*
Copyright (C) 2023-2026 QuantumNous

This program is free software: you can redistribute it and/or modify
it under the terms of the GNU Affero General Public License
as published by the Free Software Foundation, either version 3 of the
License, or (at your option) any later version.

This program is distributed in the hope that it will be useful,
but WITHOUT ANY WARRANTY; without even the implied warranty of
MERCHANTABILITY or FITNESS FOR A PARTICULAR PURPOSE. See the
GNU Affero General Public License for more details.

You should have received a copy of the GNU Affero General Public License
along with this program. If not, see <https://www.gnu.org/licenses/>.

For commercial licensing, please contact support@quantumnous.com
*/
import assert from 'node:assert/strict'
import { test, type TestContext } from 'node:test'

import { AxiosHeaders } from 'axios'

import { api } from '@/lib/http-client'

import {
  getFlowQuotaDates,
  getUserQuotaDataByUsers,
  getUserQuotaDates,
} from '../api'
import { processChartData } from '../lib/charts'
import { buildDashboardFlowData } from '../lib/flow'
import { calculateDashboardStats } from '../lib/stats'
import type { FlowQuotaDataItem, QuotaDataItem } from '../types'

const range = { start_timestamp: 1_793_980_000, end_timestamp: 1_794_070_000 }

function historicalRows(): Array<QuotaDataItem & FlowQuotaDataItem> {
  return [
    {
      user_id: 19,
      username: 'fixture',
      created_at: 1_793_991_600,
      token_id: 1,
      use_group: 'default',
      model_name: 'unihub-text-embedding-v4',
      count: 1,
      token_used: 8,
      quota: 2,
    },
    {
      user_id: 19,
      username: 'fixture',
      created_at: 1_793_991_600,
      token_id: 1,
      use_group: 'default',
      model_name: 'text-embedding-v4',
      count: 2,
      token_used: 16,
      quota: 3,
    },
    {
      user_id: 19,
      username: 'fixture',
      created_at: 1_793_991_600,
      token_id: 1,
      use_group: 'default',
      model_name: 'unihub-custom-model',
      count: 7,
      token_used: 21,
      quota: 11,
    },
  ]
}

function mockResponse(context: TestContext, response: unknown) {
  const original = api.defaults.adapter
  context.after(() => {
    api.defaults.adapter = original
  })
  api.defaults.adapter = async (config) => ({
    status: 200,
    statusText: 'OK',
    headers: new AxiosHeaders(),
    config,
    data: response,
  })
}

test('dashboard reads display the retired embedding name canonically for user and admin endpoints', async (context) => {
  const rows = historicalRows()
  const original = structuredClone(rows)
  rows.forEach(Object.freeze)
  Object.freeze(rows)
  mockResponse(
    context,
    Object.freeze({ success: true, data: rows, message: 'fixture' })
  )

  const responses = await Promise.all([
    getUserQuotaDates(range),
    getUserQuotaDates(range, true),
    getUserQuotaDataByUsers(range),
    getFlowQuotaDates(range),
    getFlowQuotaDates(range, true),
  ])

  for (const response of responses) {
    assert.equal(response.success, true)
    assert.deepEqual(
      response.data?.map((row) => row.model_name),
      ['text-embedding-v4', 'text-embedding-v4', 'unihub-custom-model']
    )
  }
  assert.deepEqual(rows, original)
})

test('historical and canonical embedding chart rows merge without changing requests, tokens or quota', async (context) => {
  mockResponse(context, { success: true, data: historicalRows() })
  const response = await getUserQuotaDates(range)
  const charts = processChartData(response.data, 'hour')

  assert.deepEqual(charts.spec_pie.data[0].values, [
    { type: 'unihub-custom-model', value: 7 },
    { type: 'text-embedding-v4', value: 3 },
  ])
  const consumed = charts.spec_line.data[0].values.filter(
    (row: { rawQuota: number }) => row.rawQuota > 0
  )
  assert.deepEqual(
    new Map(
      consumed.map((row: { Model: string; rawQuota: number }) => [
        row.Model,
        row.rawQuota,
      ])
    ),
    new Map([
      ['text-embedding-v4', 5],
      ['unihub-custom-model', 11],
    ])
  )
  assert.deepEqual(calculateDashboardStats(response.data), {
    totalCount: 10,
    totalTokens: 45,
    totalQuota: 16,
  })
  for (const spec of Object.values(charts)) {
    assert.equal(
      JSON.stringify(spec).includes('unihub-text-embedding-v4'),
      false
    )
  }
})

test('flow nodes and canonical model filters include historical embedding usage', async (context) => {
  mockResponse(context, { success: true, data: historicalRows() })
  const response = await getFlowQuotaDates(range)
  const result = buildDashboardFlowData(response.data ?? [], 'requests', {
    role: 'user',
    selectedNodes: [{ kind: 'model', id: 'model:text-embedding-v4' }],
  })

  assert.deepEqual(result.summary, { requests: 3, tokens: 24, quota: 5 })
  assert.deepEqual(
    result.flow.nodes
      .filter((node) => node.kind === 'model')
      .map((node) => [node.id, node.label, node.requests]),
    [['model:text-embedding-v4', 'text-embedding-v4', 3]]
  )
  assert.deepEqual(
    result.filterOptions.nodes
      .filter((node) => node.kind === 'model')
      .map((node) => [node.value, node.label]),
    [
      ['model:unihub-custom-model', 'unihub-custom-model'],
      ['model:text-embedding-v4', 'text-embedding-v4'],
    ]
  )
})

test('resolved dashboard failure and missing-data envelopes survive all read adapters unchanged', async (context) => {
  let response: unknown
  context.mock.method(api, 'get', async () => ({ data: response }))
  const readers = [
    () => getUserQuotaDates(range),
    () => getUserQuotaDates(range, true),
    () => getUserQuotaDataByUsers(range),
    () => getFlowQuotaDates(range),
    () => getFlowQuotaDates(range, true),
  ]

  for (const envelope of [
    { success: false, message: 'usage unavailable' },
    { success: false, message: 'usage unavailable', data: null },
    { success: true },
    { success: true, data: null },
    { success: true, data: [] },
  ]) {
    response = Object.freeze(envelope)
    for (const read of readers) {
      assert.deepEqual(await read(), envelope)
    }
  }
})

test('dashboard read adapters preserve request failures', async (context) => {
  const failure = new Error('usage request failed')
  context.mock.method(api, 'get', async () => {
    throw failure
  })
  for (const read of [
    () => getUserQuotaDates(range),
    () => getUserQuotaDates(range, true),
    () => getUserQuotaDataByUsers(range),
    () => getFlowQuotaDates(range),
    () => getFlowQuotaDates(range, true),
  ]) {
    await assert.rejects(read(), failure)
  }
})

test('unrecognized embedding names and other vendor models retain their original IDs', async (context) => {
  const rows = [
    { created_at: 1, model_name: 'unihub-text-embedding-v4-preview' },
    { created_at: 1, model_name: 'openai/text-embedding-3-small' },
    { created_at: 1, model_name: 'text-embedding-v4' },
    { created_at: 1 },
  ]
  mockResponse(context, { success: true, data: rows })
  assert.deepEqual((await getUserQuotaDates(range)).data, rows)
})
