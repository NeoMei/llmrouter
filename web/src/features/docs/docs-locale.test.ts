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
  DOCS_LANGUAGE_STORAGE_KEY,
  persistDocsLanguage,
  resolveDocsLanguage,
} from './docs-locale'

describe('docs locale policy', () => {
  test('uses the explicit stored language before the browser locale', () => {
    const storage = {
      getItem: (key: string) =>
        key === DOCS_LANGUAGE_STORAGE_KEY ? 'en' : null,
      setItem() {},
    }

    assert.equal(resolveDocsLanguage(storage, 'zh-CN'), 'en')
  })

  test('defaults to Chinese for Chinese browser locales and English otherwise', () => {
    assert.equal(resolveDocsLanguage(undefined, 'zh-CN'), 'zh-CN')
    assert.equal(resolveDocsLanguage(undefined, 'zh-TW'), 'zh-CN')
    assert.equal(resolveDocsLanguage(undefined, 'en-US'), 'en')
    assert.equal(resolveDocsLanguage(undefined, undefined), 'en')
  })

  test('ignores invalid or unreadable stored values before using the browser locale', () => {
    assert.equal(
      resolveDocsLanguage({ getItem: () => 'fr', setItem() {} }, 'zh-CN'),
      'zh-CN'
    )
    assert.equal(
      resolveDocsLanguage(
        {
          getItem() {
            throw new Error('blocked')
          },
          setItem() {},
        },
        'en-US'
      ),
      'en'
    )
  })

  test('persists only explicit valid selections and safely handles storage failure', () => {
    const writes: Array<[string, string]> = []
    const storage = {
      getItem: () => null,
      setItem: (key: string, value: string) => writes.push([key, value]),
    }

    assert.equal(persistDocsLanguage('zh-CN', storage), true)
    assert.deepEqual(writes, [[DOCS_LANGUAGE_STORAGE_KEY, 'zh-CN']])
    assert.equal(
      persistDocsLanguage('en', {
        getItem: () => null,
        setItem() {
          throw new Error('blocked')
        },
      }),
      false
    )
  })
})
