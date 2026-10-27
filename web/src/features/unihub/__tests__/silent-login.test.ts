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

import { silentProductLoginURL } from '../sso'

function storage() {
  const values = new Map<string, string>()
  return {
    getItem: (key: string) => values.get(key) ?? null,
    setItem: (key: string, value: string) => {
      values.set(key, value)
    },
  }
}

test('an enabled product tries silent SSO once and retains the original product destination', () => {
  const state = storage()
  assert.equal(
    silentProductLoginURL(true, '', '/users?filter=active', state),
    '/api/unihub/login?prompt=none&return_to=%2Fusers%3Ffilter%3Dactive'
  )
  assert.equal(silentProductLoginURL(true, '', '/users', state), null)
})

test('silent failure and explicit sign-out leave the original password form usable without a loop', () => {
  const failed = storage()
  assert.equal(
    silentProductLoginURL(true, '?unihub_silent=done', '/keys', failed),
    null
  )
  assert.equal(silentProductLoginURL(true, '', '/keys', failed), null)
  const signedOut = storage()
  signedOut.setItem('unihub:signed-out', '1')
  assert.equal(silentProductLoginURL(true, '', '/keys', signedOut), null)
  assert.equal(silentProductLoginURL(false, '', '/keys', storage()), null)
})

test('silent SSO does not accept foreign or API destinations', () => {
  for (const value of [
    'https://other.invalid',
    '//other.invalid',
    '/api/user/self',
    '/%2f%2fevil',
    '/dashboard/%2e%2e/%2e%2e/api/user/self',
  ]) {
    assert.equal(
      silentProductLoginURL(true, '', value, storage()),
      '/api/unihub/login?prompt=none&return_to=%2Fdashboard'
    )
  }
})
