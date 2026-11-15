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
import assert from 'node:assert/strict'
import { readFileSync } from 'node:fs'
import { dirname, resolve } from 'node:path'
import test from 'node:test'
import { fileURLToPath } from 'node:url'

import { AxiosHeaders } from 'axios'

import { api } from '@/lib/api'

import {
  testAllChannels,
  testChannelConnectivity,
  testChannelDeep,
} from '../api'
import { handleTestChannel } from '../lib'

const TEST_DIR = dirname(fileURLToPath(import.meta.url))
const LOCALE_DIR = resolve(TEST_DIR, '../../../i18n/locales')
const SOURCE_ROOT = resolve(TEST_DIR, '../../../')
const CHANGED_SOURCE_PATHS = [
  'features/channels/api.ts',
  'features/channels/components/channels-primary-buttons.tsx',
  'features/channels/components/data-table-row-actions.tsx',
  'features/channels/components/dialogs/channel-test-dialog.tsx',
  'features/channels/lib/channel-actions.ts',
  'features/system-settings/models/routing-reliability-section.tsx',
]

const NEW_TRANSLATION_KEYS = [
  'Scheduled checks only verify interface connectivity for non-manually-disabled channels; passive recovery only checks auto-disabled channels after real request failures. Neither generates model content, validates generation capability, or enables or disables channels.',
  'How frequently the system checks all channel interfaces with read-only requests',
  'Connectivity checks for all channels started. Please refresh to see results.',
  '{{target}} connectivity check succeeded',
  '{{target}} generation test succeeded',
  'Default checks use a read-only request and never generate model content. An explicit paid generation test requires confirmation and may incur upstream charges.',
  'Default checks only verify the channel interface; they do not test generation for each model.',
  'Check {{count}} matching models',
  'Check all {{count}} models',
  'Run paid generation tests ({{count}})',
  'Run paid generation test for {{model}}',
  'Run paid generation test',
  'Confirm paid generation test',
  'This will send {{count}} real generation requests and may incur upstream charges. Scheduled checks remain read-only.',
  'This will send a real generation request for {{model}} and may incur upstream charges. Scheduled checks remain read-only.',
  'Check channel connectivity',
  'Generation capability verified',
  'Interface reachable; generation capability not verified',
  'Read-only check unsupported; generation capability not verified',
  'Connectivity verified; generation capability not verified',
  'Scheduled checks use read-only requests, never generate model content, never verify generation capability, and do not enable or disable channels.',
  'Check all channel connectivity',
  'Scheduled connectivity checks',
  '{{target}} check failed',
  'Stopping connectivity checks...',
  'Checking channel connectivity...',
  'Connectivity checks stopped: {{completed}}/{{total}} completed, {{success}} succeeded, {{failed}} failed',
  'Connectivity checks completed: {{success}} succeeded, {{failed}} failed',
  'Connectivity checks completed: {{count}} succeeded',
  'Override the endpoint for explicit paid generation tests. Leave empty to auto detect.',
  'Enable streaming for explicit paid generation tests only.',
  'Stop connectivity checks',
  'Stopping generation tests...',
  'Running generation tests...',
  'Generation tests stopped: {{completed}}/{{total}} completed, {{success}} succeeded, {{failed}} failed',
  'Generation tests completed: {{success}} succeeded, {{failed}} failed',
  'Generation tests completed: {{count}} succeeded',
  'Stop generation tests',
  'Generation test failed',
  'Connectivity check failed',
  '{{target}} generation test failed',
  '{{target}} connectivity check failed',
]

function getChangedSourceText() {
  return CHANGED_SOURCE_PATHS.map((sourcePath) =>
    readFileSync(resolve(SOURCE_ROOT, sourcePath), 'utf8')
  ).join('\n')
}

type CapturedRequest = {
  url: string
  method?: string
  params?: Record<string, unknown>
  data?: unknown
  headers?: Record<string, unknown>
}

function captureChannelRequests(
  t: import('node:test').TestContext,
  responseData: (
    config: import('axios').InternalAxiosRequestConfig
  ) => Record<string, unknown> = (config) => ({
    success: true,
    time: 0.01,
    probe_mode: config.url?.endsWith('/deep') ? 'generation' : 'reachability',
  })
) {
  const requests: CapturedRequest[] = []
  const original = api.defaults.adapter
  api.defaults.adapter = async (config) => {
    requests.push({
      url: config.url ?? '',
      method: config.method,
      params: config.params,
      data: config.data,
      headers: config.headers,
    })
    return {
      status: 200,
      statusText: 'OK',
      headers: new AxiosHeaders(),
      config,
      data: responseData(config),
    }
  }
  t.after(() => {
    api.defaults.adapter = original
  })
  return requests
}

function failingAdapter(t: import('node:test').TestContext) {
  const original = api.defaults.adapter
  api.defaults.adapter = async () => {
    throw new Error('network unavailable')
  }
  t.after(() => {
    api.defaults.adapter = original
  })
}

test('default connectivity checks never send a generation opt-in', async (t) => {
  const requests = captureChannelRequests(t)

  await testChannelConnectivity(7)
  await handleTestChannel(8, { channelName: 'quick-check', silent: true })
  await testAllChannels()

  assert.deepEqual(
    requests.map(({ url, method, params, data }) => ({
      url,
      method,
      params,
      data,
    })),
    [
      {
        url: '/api/channel/test/7',
        method: 'get',
        params: undefined,
        data: undefined,
      },
      {
        url: '/api/channel/test/8',
        method: 'get',
        params: undefined,
        data: undefined,
      },
      {
        url: '/api/channel/test',
        method: 'get',
        params: undefined,
        data: undefined,
      },
    ]
  )
  assert.equal(
    requests.some((request) => request.url.endsWith('/deep')),
    false
  )
})

test('an explicit paid generation test requires POST, confirmation header and body', async (t) => {
  const requests = captureChannelRequests(t)

  await testChannelDeep(9, {
    model: 'qwen3-tts-flash',
    endpoint_type: 'openai',
    stream: true,
  })
  await handleTestChannel(
    10,
    {
      mode: 'generation',
      testModel: 'gpt-4o-mini',
      silent: true,
    },
    (success) => assert.equal(success, true)
  )

  assert.equal(requests.length, 2)
  for (const request of requests) {
    assert.equal(request.method, 'post')
    assert.equal(request.url.endsWith('/deep'), true)
    assert.equal(request.headers?.['X-Channel-Deep-Test'], 'confirmed')
    assert.deepEqual(JSON.parse(String(request.data)), { confirm: true })
  }
  assert.equal(requests[0].url, '/api/channel/test/9/deep')
  assert.deepEqual(requests[0].params, {
    model: 'qwen3-tts-flash',
    endpoint_type: 'openai',
    stream: true,
  })
  assert.equal(requests[1].url, '/api/channel/test/10/deep')
  assert.deepEqual(requests[1].params, { model: 'gpt-4o-mini' })
})

test('callbacks report the authoritative response probe mode', async (t) => {
  captureChannelRequests(t, (config) => ({
    success: true,
    time: 0.01,
    probe_mode: config.url?.endsWith('/deep') ? 'inconclusive' : 'reachability',
  }))
  const probeModes: unknown[] = []
  const onTestComplete = (
    _success: boolean,
    _responseTime?: number,
    _error?: string,
    _errorCode?: string,
    probeMode?: string
  ) => probeModes.push(probeMode)

  await handleTestChannel(
    11,
    {
      mode: 'generation',
      channelName: 'deep-check',
      testModel: 'model-a',
      silent: true,
    },
    onTestComplete
  )
  await handleTestChannel(
    12,
    { channelName: 'connectivity', silent: true },
    onTestComplete
  )

  assert.deepEqual(probeModes, ['inconclusive', 'reachability'])
})

test('failed checks preserve the authoritative probe mode', async (t) => {
  captureChannelRequests(t, (config) => ({
    success: false,
    message: 'upstream unavailable',
    probe_mode: config.url?.endsWith('/deep') ? 'generation' : 'reachability',
  }))
  const failures: unknown[][] = []
  const onTestComplete = (...args: unknown[]) => failures.push(args)

  await handleTestChannel(
    15,
    { mode: 'generation', channelName: 'deep-check', silent: true },
    onTestComplete
  )
  await handleTestChannel(
    16,
    { channelName: 'connectivity', silent: true },
    onTestComplete
  )

  assert.deepEqual(failures, [
    [false, undefined, 'upstream unavailable', undefined, 'generation'],
    [false, undefined, 'upstream unavailable', undefined, 'reachability'],
  ])
})

test('a missing response probe mode falls back to inconclusive', async (t) => {
  captureChannelRequests(t, () => ({ success: true, time: 0.01 }))
  let callback: unknown[] = []

  await handleTestChannel(13, { silent: true }, (...args) => {
    callback = args
  })

  assert.equal(callback[0], true)
  assert.equal(callback[4], 'inconclusive')
})

test('a request error does not fabricate a probe mode', async (t) => {
  failingAdapter(t)
  let callback: unknown[] = []

  await handleTestChannel(14, { silent: true }, (...args) => {
    callback = args
  })

  assert.equal(callback[0], false)
  assert.equal(callback.length, 5)
  assert.equal(callback[1], undefined)
  assert.equal(callback[3], undefined)
  assert.equal(callback[4], undefined)
})

test('all seven locales translate every new channel test message', () => {
  const locales = ['en', 'fr', 'ja', 'ru', 'vi', 'zh-TW', 'zh']
  const documents = locales.map((lang) => ({
    lang,
    translation: JSON.parse(
      readFileSync(resolve(LOCALE_DIR, `${lang}.json`), 'utf8')
    ).translation as Record<string, string>,
  }))
  const sourceText = getChangedSourceText()
  const placeholders = (text: string) =>
    [...text.matchAll(/\{\{\s*([A-Za-z0-9_]+)\s*\}\}/g)]
      .map((match) => match[1])
      .sort()

  for (const { lang, translation } of documents) {
    const missing = NEW_TRANSLATION_KEYS.filter(
      (key) => typeof translation[key] !== 'string' || !translation[key].trim()
    )
    assert.deepEqual(missing, [], `missing translations in ${lang}.json`)
    const placeholderMismatch = NEW_TRANSLATION_KEYS.filter(
      (key) =>
        JSON.stringify(placeholders(key)) !==
        JSON.stringify(placeholders(translation[key]))
    )
    assert.deepEqual(
      placeholderMismatch,
      [],
      `placeholder mismatch in ${lang}.json`
    )
    if (lang !== 'en') {
      const untranslated = NEW_TRANSLATION_KEYS.filter(
        (key) =>
          translation[key] === documents[0].translation[key] &&
          /[A-Za-z]{3,}/.test(key)
      )
      assert.deepEqual(
        untranslated,
        [],
        `untranslated English values in ${lang}.json`
      )
    }
  }
  for (const key of NEW_TRANSLATION_KEYS) {
    assert.equal(
      documents[0].translation[key],
      key,
      `English translation must use the source key: ${key}`
    )
    assert.equal(
      sourceText.includes(key),
      true,
      `new translation key is not used in changed sources: ${key}`
    )
  }
})
