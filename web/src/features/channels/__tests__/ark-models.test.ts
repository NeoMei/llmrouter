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
import { test } from 'node:test'

import { filterComboboxOptions } from '../../../components/ui/combobox-input'
import { CHANNEL_TYPE_OPTIONS } from '../constants'
import {
  getChannelAdapterLabel,
  getChannelSupplierFamily,
} from '../lib/channel-utils'

test('ByteDance VolcEngine and Doubao video are visible channel options', () => {
  assert.equal(
    CHANNEL_TYPE_OPTIONS.find((option) => option.value === 45)?.label,
    'ByteDance VolcEngine Ark'
  )
  assert.equal(
    CHANNEL_TYPE_OPTIONS.find((option) => option.value === 54)?.label,
    'Doubao Video'
  )
})

test('Each Ark channel type appears once', () => {
  assert.equal(
    new Set(CHANNEL_TYPE_OPTIONS.map((option) => option.value)).size,
    CHANNEL_TYPE_OPTIONS.length
  )
})

test('Chinese and English provider aliases find the Ark channel', () => {
  const option = {
    value: '45',
    label: '火山方舟（字节跳动）',
    keywords: ['VolcEngine', 'Doubao', '豆包', '火山引擎'],
  }
  for (const search of ['字节', '火山引擎', '豆包', 'doubao', 'volcengine']) {
    assert.deepEqual(filterComboboxOptions([option], search), [option])
  }
})

test('Ark adapters have distinct names and the same stable supplier family', () => {
  assert.equal(getChannelAdapterLabel(45), 'VolcEngine Ark')
  assert.equal(getChannelAdapterLabel(54), 'Doubao Video')
  assert.equal(getChannelSupplierFamily(45), 'ByteDance')
  assert.equal(getChannelSupplierFamily(54), 'ByteDance')
})

test('compatible and custom proxies do not infer an upstream supplier', () => {
  for (const type of [1, 8, 20, 58]) {
    assert.equal(getChannelSupplierFamily(type), undefined)
  }
})
