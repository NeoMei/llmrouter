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
import test from 'node:test'

import { productChannelUpdate } from '../lib/product-channel-update'

test('a product routing edit omits hidden configuration defaults even for sensitive editors', () => {
  const updated = productChannelUpdate(
    {
      id: 1,
      name: 'after',
      models: 'a,b',
      base_url: '',
      header_override: '',
      param_override: '',
      setting: '{"proxy":"","force_format":false}',
      settings: '{"allow_service_tier":false}',
      other: '',
      remark: '',
    },
    { name: true, models: true },
  )
  assert.deepEqual(updated, { id: 1, name: 'after', models: 'a,b' })
})
test('explicit product credential edit sends changed config only, preserving unknown saved config', () => {
  const updated = productChannelUpdate(
    {
      id: 1,
      header_override: '{"Authorization":"new-value"}',
      base_url: '',
      setting: '{"proxy":"http://new-proxy","force_format":false}',
      settings: '{"allow_service_tier":false,"disable_store":false}',
    },
    { header_override: true, proxy: true, allow_service_tier: true },
  )
  assert.deepEqual(updated, {
    id: 1,
    header_override: '{"Authorization":"new-value"}',
    setting: '{"proxy":"http://new-proxy"}',
    settings: '{"allow_service_tier":false}',
  })
})
