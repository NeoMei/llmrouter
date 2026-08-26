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
})
