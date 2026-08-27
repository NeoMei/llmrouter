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
import { describe, test } from 'node:test'

import {
  FEATURED_OPEN_SOURCE_MODELS,
  HOME_API_DEMOS,
  HOME_STATS,
} from '../home-content'

describe('homepage open-source model positioning', () => {
  test('features the open-source model families enabled by the service', () => {
    assert.deepEqual(FEATURED_OPEN_SOURCE_MODELS, [
      'DeepSeek',
      'GLM',
      'Kimi',
      'Qwen',
      'MiniMax',
    ])
  })

  test('reports conservative facts about the hosted service', () => {
    assert.deepEqual(HOME_STATS, [
      { end: 20, suffix: '+', labelKey: 'open-source models available' },
      { end: 6, suffix: '+', labelKey: 'open-source model families' },
      { end: 3, suffix: '', labelKey: 'coding clients supported' },
      { end: 1, suffix: '', labelKey: 'unified API key' },
    ])
  })

  test('demonstrates enabled open-source models through the unified chat route', () => {
    assert.deepEqual(
      HOME_API_DEMOS.map((demo) => ({
        label: demo.label,
        endpoint: demo.endpoint,
        modelLine: demo.request[0],
      })),
      [
        {
          label: 'DeepSeek',
          endpoint: '/v1/chat/completions',
          modelLine: '"model": "deepseek-chat",',
        },
        {
          label: 'GLM',
          endpoint: '/v1/chat/completions',
          modelLine: '"model": "glm-5",',
        },
        {
          label: 'Kimi',
          endpoint: '/v1/chat/completions',
          modelLine: '"model": "kimi-k2.7-code",',
        },
        {
          label: 'Qwen',
          endpoint: '/v1/chat/completions',
          modelLine: '"model": "Qwen3.7-Max-A",',
        },
      ]
    )
  })
})
