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

import type { AuthUser } from '@/stores/auth-store'

import { canOpenProductPage, canUseProductFeature } from './product-access'

const member: AuthUser = {
  id: 1,
  username: 'member',
  role: 1,
  identity_source: 'unihub',
  permissions: { admin_permissions: { product: { use: true } } },
}

test('product members access product pages, while identities and financial settings stay central', () => {
  for (const path of [
    '/dashboard/overview',
    '/dashboard/models',
    '/dashboard/flow',
    '/usage-logs/common',
    '/usage-logs/task',
    '/pricing',
  ]) {
    assert.equal(canOpenProductPage(member, path), true, path)
  }
  for (const path of [
    '/keys',
    '/profile',
    '/wallet',
    '/users',
    '/models/deployments',
    '/system-settings/auth/login',
    '/system-settings/billing/general',
    '/system-settings/models/native-pricing',
  ]) {
    assert.equal(canOpenProductPage(member, path), false, path)
  }
})

test('only explicit product capabilities open technical management pages', () => {
  const viewer = {
    ...member,
    permissions: {
      admin_permissions: {
        channel: { read: true },
        catalog: { read: true },
        routing: { read: true },
        settings: { read: true },
      },
    },
  }
  for (const path of [
    '/channels',
    '/models/metadata',
    '/client-model-routes',
    '/system-settings/models/global',
    '/system-settings/models/routing-reliability',
  ]) {
    assert.equal(canOpenProductPage(member, path), false, path)
    assert.equal(canOpenProductPage(viewer, path), true, path)
  }
  assert.equal(canUseProductFeature(viewer, 'settings', 'write'), false)
})

test('a native numeric role never substitutes for current product grants', () => {
  const inconsistent = { ...member, role: 100 }
  assert.equal(canUseProductFeature(inconsistent, 'routing'), false)
  assert.equal(
    canOpenProductPage(inconsistent, '/system-settings/models/global'),
    false
  )
  assert.equal(
    canUseProductFeature(
      { ...member, identity_source: undefined, role: 10 },
      'routing',
      'write'
    ),
    true
  )
})
