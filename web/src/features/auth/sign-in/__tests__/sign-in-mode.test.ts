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
import { mock, test } from 'node:test'

import { authorizeProductSession } from '@/features/unihub/native-session'
import { UniHubError } from '@/features/unihub/sdk'

import {
  shouldFallbackToNativeLogin,
  shouldShowLocalOperatorSignIn,
  shouldUseCentralSignUp,
} from '../sign-in-mode'

const central = {
  native_access: true,
  enabled: true,
  browser_sso_enabled: true,
}

test('a UniHub-enabled sign-in keeps the local operator entry as a secondary path', () => {
  assert.equal(shouldShowLocalOperatorSignIn(central), true)
  assert.equal(
    shouldShowLocalOperatorSignIn({
      native_access: true,
      enabled: false,
      browser_sso_enabled: true,
    }),
    true
  )
  assert.equal(
    shouldShowLocalOperatorSignIn({
      native_access: false,
      enabled: false,
      browser_sso_enabled: false,
    }),
    false
  )
  assert.equal(shouldShowLocalOperatorSignIn(null), false)
})

test('standalone native deployments retain the local operator entry', () => {
  assert.equal(
    shouldShowLocalOperatorSignIn({
      native_access: true,
      enabled: false,
      browser_sso_enabled: false,
    }),
    true
  )
})

test('public sign-up uses the central realm by default', () => {
  assert.equal(shouldUseCentralSignUp(central, false), true)
  assert.equal(
    shouldUseCentralSignUp(
      { enabled: false, browser_sso_enabled: true, native_access: true },
      false
    ),
    true
  )
  assert.equal(
    shouldUseCentralSignUp(
      { enabled: false, browser_sso_enabled: false, native_access: true },
      false
    ),
    false
  )
  assert.equal(shouldUseCentralSignUp(null, false), false)
  assert.equal(shouldUseCentralSignUp(undefined, false), false)
})

test('an explicit native request keeps sign-up in the local realm', () => {
  assert.equal(shouldUseCentralSignUp(central, true), false)
})

test('only a central invalid-credentials response unlocks the local fallback', () => {
  assert.equal(
    shouldFallbackToNativeLogin(
      new UniHubError(401, 'invalid_credentials', {})
    ),
    true
  )
  assert.equal(
    shouldFallbackToNativeLogin(new UniHubError(401, 'other', {})),
    false
  )
  assert.equal(
    shouldFallbackToNativeLogin(
      new UniHubError(429, 'invalid_credentials', {})
    ),
    false
  )
  assert.equal(
    shouldFallbackToNativeLogin(
      new UniHubError(409, 'interaction_required', {})
    ),
    false
  )
  assert.equal(shouldFallbackToNativeLogin(new Error('network')), false)
  assert.equal(shouldFallbackToNativeLogin(null), false)
})

test('a handshake failure after central password authorization never unlocks the local fallback', async () => {
  const proofResponse = {
    ok: false,
    json: async () => ({ error: 'no_identity_overrides' }),
  }
  const fetchMock = mock.method(globalThis, 'fetch', async () => proofResponse)
  const session = {
    epoch: 1,
    bound: async (run: () => Promise<unknown>) => run(),
    client: {
      passwordLogin: async () => ({
        access_token: 'central-token',
        expires_in: 3600,
      }),
    },
    expire: () => {},
    signal: new AbortController().signal,
  }
  await assert.rejects(
    authorizeProductSession(
      session as never,
      { username: 'native-account', password: 'unused' },
      { authEpoch: 1, originalSID: undefined }
    ),
    (error: unknown) => {
      assert.ok(error instanceof Error)
      assert.equal(shouldFallbackToNativeLogin(error), false)
      return true
    }
  )
  fetchMock.mock.restore()
})
