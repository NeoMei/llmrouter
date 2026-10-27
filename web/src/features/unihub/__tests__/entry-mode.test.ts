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

import { shouldUseUnifiedEntry } from '../entry-mode'

test('native compatibility retains default unified entry and existing native sessions', () => {
  const config = { enabled: true, native_access: true }
  assert.equal(shouldUseUnifiedEntry(config, '/dashboard', '', null), true)
  assert.equal(shouldUseUnifiedEntry(config, '/sign-in', '', null), true)
  assert.equal(
    shouldUseUnifiedEntry(config, '/sign-in', '?native=1', null),
    false
  )
  assert.equal(
    shouldUseUnifiedEntry(config, '/dashboard', '', { identity_source: '' }),
    false
  )
  assert.equal(
    shouldUseUnifiedEntry(config, '/keys', '', { identity_source: 'unihub' }),
    true
  )
  assert.equal(
    shouldUseUnifiedEntry(
      { enabled: true, native_access: false },
      '/sign-in',
      '?native=1',
      null
    ),
    true
  )
  assert.equal(
    shouldUseUnifiedEntry(
      { enabled: false, native_access: true },
      '/dashboard',
      '',
      null
    ),
    false
  )
})

test('native provider callbacks finish before unified entry routing without opening nested OAuth paths', () => {
  const config = { enabled: true, native_access: true }
  for (const path of [
    '/oauth/discord',
    '/oauth/oidc',
    '/oauth/custom-provider/',
  ]) {
    assert.equal(
      shouldUseUnifiedEntry(
        config,
        path,
        '?code=provider-code&state=flow',
        null
      ),
      false,
      path
    )
    assert.equal(
      shouldUseUnifiedEntry(
        { ...config, native_access: false },
        path,
        '',
        null
      ),
      true,
      path
    )
  }
  for (const path of [
    '/oauth/provider/nested',
    '/oauth//',
    '/oauthish/provider',
  ]) {
    assert.equal(shouldUseUnifiedEntry(config, path, '', null), true, path)
  }
})
