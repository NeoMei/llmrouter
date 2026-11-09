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

import type { AuthUser, LoginSession } from '@/stores/auth-store'

import { isCentralUserDirectory } from '../lib/central-user-view'

const user = (realm?: AuthUser['authentication_realm'], source?: string) =>
  ({
    id: 1,
    username: 'admin',
    role: 100,
    authentication_realm: realm,
    identity_source: source,
  }) as AuthUser
const session = (realm?: LoginSession['authentication_realm']) =>
  ({ authentication_realm: realm }) as LoginSession

test('a verified central UniHub session uses the authoritative user directory', () => {
  assert.equal(
    isCentralUserDirectory(user('CENTRAL', 'unihub'), session('CENTRAL')),
    true
  )
  assert.equal(
    isCentralUserDirectory(user(undefined, 'unihub'), session('CENTRAL')),
    true
  )
})

test('native and unverified UniHub identities keep the original user management view', () => {
  assert.equal(
    isCentralUserDirectory(user('LOCAL_ONLY', 'unihub'), session('LOCAL_ONLY')),
    false
  )
  assert.equal(
    isCentralUserDirectory(user('CENTRAL'), session('CENTRAL')),
    false
  )
  assert.equal(
    isCentralUserDirectory(user(undefined, 'unihub'), session('LEGACY')),
    false
  )
  assert.equal(isCentralUserDirectory(null, null), false)
})
