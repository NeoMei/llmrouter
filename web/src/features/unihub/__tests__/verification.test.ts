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

import { api } from '../../../lib/http-client'
import { useAuthStore } from '../../../stores/auth-store'
import { checkVerificationMethods } from '../../auth/secure-verification/api'

test('product identity never queries native password verification methods', async () => {
  const previous = api.defaults.adapter
  const paths: string[] = []
  api.defaults.adapter = async (config) => {
    paths.push(config.url ?? '')
    return {
      data: { success: true, data: { enabled: true } },
      status: 200,
      statusText: 'OK',
      headers: {},
      config,
    }
  }
  try {
    useAuthStore.getState().auth.setUser({
      id: 1,
      username: 'projection',
      role: 1,
      identity_source: 'unihub',
    })
    const methods = await checkVerificationMethods()
    assert.deepEqual(paths, [])
    assert.equal(methods.has2FA, false)
    assert.equal(methods.hasPasskey, false)
    useAuthStore
      .getState()
      .auth.setUser({ id: 2, username: 'native', role: 10 })
    const native = await checkVerificationMethods()
    assert.equal(native.has2FA, true)
    assert.equal(native.hasPasskey, true)
    assert.deepEqual(paths.sort(), [
      '/api/user/2fa/status',
      '/api/user/passkey',
    ])
  } finally {
    api.defaults.adapter = previous
    useAuthStore.getState().auth.reset()
  }
})
