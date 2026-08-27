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
import fs from 'node:fs/promises'
import { describe, test } from 'node:test'

const DESCRIPTION_KEY =
  'Supports one-click configuration and perfectly adapts to NewAPI multi-protocol configuration.'
const LOCALES = ['en', 'zh', 'zh-TW', 'fr', 'ja', 'ru', 'vi'] as const
const POSITIONING_KEYS = [
  'Open-source AI Model Access Service',
  'One API for',
  'Leading Open-source AI Models',
  'Access DeepSeek, GLM, Kimi, Qwen, MiniMax, and more through one API. Use open-source models directly in your AI applications and coding tools.',
  'open-source models available',
  'open-source model families',
  'coding clients supported',
  'unified API key',
  'Open-source Model Access',
  'One API connects DeepSeek, GLM, Kimi, Qwen, MiniMax, and more.',
  'Unified Routing',
  'Route requests across open-source model services with consistent access and failover controls',
  'Built for open-source AI applications,',
  'ready for production workflows',
  'Usage Visibility',
  'Track model usage, costs, and request performance in one place',
  'Three steps to use open-source models',
  'Create API Key',
  'Sign up and create an API key with the access scope you need',
  'Choose an Open-source Model',
  'Select DeepSeek, GLM, Kimi, Qwen, MiniMax, or another enabled open-source model',
  'Connect and Monitor',
  'Call the unified API from your application or coding tool and track usage and costs',
  'Ready to use',
  'open-source AI models?',
  'Create an API key, choose an enabled open-source model, and start calling it from your application or coding tool.',
  'View Models',
] as const

describe('homepage copy', () => {
  test('supported-app description identifies LLM API as the multi-protocol target', async () => {
    for (const locale of LOCALES) {
      const localePath = new URL(
        `../../../i18n/locales/${locale}.json`,
        import.meta.url
      )
      const messages = JSON.parse(await fs.readFile(localePath, 'utf8'))
      const description = messages.translation[DESCRIPTION_KEY]

      assert.match(description, /LLM API/, `${locale} should name LLM API`)
      assert.doesNotMatch(
        description,
        /NewAPI/,
        `${locale} should not name NewAPI`
      )
    }

    const zhPath = new URL('../../../i18n/locales/zh.json', import.meta.url)
    const zhMessages = JSON.parse(await fs.readFile(zhPath, 'utf8'))
    assert.equal(
      zhMessages.translation[DESCRIPTION_KEY],
      '支持一键配置并完美适配 LLM API 多协议配置。'
    )
  })

  test('open-source-model positioning is translated in every supported locale', async () => {
    for (const locale of LOCALES) {
      const localePath = new URL(
        `../../../i18n/locales/${locale}.json`,
        import.meta.url
      )
      const messages = JSON.parse(await fs.readFile(localePath, 'utf8'))

      for (const key of POSITIONING_KEYS) {
        assert.equal(
          typeof messages.translation[key],
          'string',
          `${locale} should translate ${key}`
        )
        assert.notEqual(
          messages.translation[key].trim(),
          '',
          `${locale} should not leave ${key} empty`
        )
      }
    }

    const zhPath = new URL('../../../i18n/locales/zh.json', import.meta.url)
    const zhMessages = JSON.parse(await fs.readFile(zhPath, 'utf8'))
    assert.equal(
      zhMessages.translation['Open-source AI Model Access Service'],
      '开源大模型接入服务'
    )
    assert.equal(zhMessages.translation['One API for'], '一个 API，接入')
    assert.equal(
      zhMessages.translation['Leading Open-source AI Models'],
      '主流开源大模型'
    )

    const positioningText = POSITIONING_KEYS.map(
      (key) => zhMessages.translation[key]
    ).join('\n')
    assert.doesNotMatch(positioningText, /国产模型|国内模型/)
  })
})
