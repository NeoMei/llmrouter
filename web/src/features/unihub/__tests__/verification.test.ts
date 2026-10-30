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

function sessionRealm(realm: 'CENTRAL' | 'LOCAL_ONLY' | 'LEGACY' | 'UNKNOWN') {
  useAuthStore.setState((state) => ({
    auth: {
      ...state.auth,
      session: {
        sid: 'sid',
        current: true,
        login_method: 'password',
        ip: '',
        user_agent: '',
        created_at: 1,
        last_active_at: 1,
        expires_at: 2,
        authentication_realm: realm,
      },
    },
  }))
}

test('central uses its Passkey and local retains both configured verification methods', async () => {
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
    sessionRealm('CENTRAL')
    const methods = await checkVerificationMethods()
    assert.deepEqual(paths, ['/api/user/passkey'])
    assert.equal(methods.has2FA, false)
    assert.equal(methods.hasPasskey, true)
    paths.length = 0
    useAuthStore
      .getState()
      .auth.setUser({ id: 2, username: 'native', role: 10 })
    sessionRealm('LOCAL_ONLY')
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

test('central Passkey remains available without calling local 2FA status', async () => {
  const previous = api.defaults.adapter
  const originalWindow = globalThis.window
  Object.defineProperty(globalThis, 'window', {
    configurable: true,
    value: { PublicKeyCredential: class {} },
  })
  api.defaults.adapter = async (config) => {
    if (config.url === '/api/user/2fa/status') {
      throw new Error('2FA unavailable')
    }
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
      username: 'central',
      role: 1,
      identity_source: 'unihub',
    })
    sessionRealm('CENTRAL')
    const methods = await checkVerificationMethods()
    assert.equal(methods.has2FA, false)
    assert.equal(methods.hasPasskey, true)
    assert.equal(methods.passkeySupported, true)
  } finally {
    api.defaults.adapter = previous
    useAuthStore.getState().auth.reset()
    Object.defineProperty(globalThis, 'window', {
      configurable: true,
      value: originalWindow,
    })
  }
})

test('local 2FA lookup failure stays unknown and does not hide Passkey', async () => {
  const previous = api.defaults.adapter
  api.defaults.adapter = async (config) => {
    if (config.url === '/api/user/2fa/status') throw new Error('unavailable')
    return {
      data: { success: true, data: { enabled: true } },
      status: 200,
      statusText: 'OK',
      headers: {},
      config,
    }
  }
  try {
    useAuthStore.getState().auth.setUser({ id: 2, username: 'native', role: 1 })
    sessionRealm('LOCAL_ONLY')
    const methods = await checkVerificationMethods()
    assert.equal(methods.twoFAUnavailable, true)
    assert.equal(methods.hasPasskey, true)
    assert.equal(methods.has2FA, false)
  } finally {
    api.defaults.adapter = previous
    useAuthStore.getState().auth.reset()
  }
})

test('a failed Passkey status does not hide an available local 2FA method', async () => {
  const previous = api.defaults.adapter
  api.defaults.adapter = async (config) => {
    if (config.url === '/api/user/passkey') throw new Error('unavailable')
    return {
      data: { success: true, data: { enabled: true } },
      status: 200,
      statusText: 'OK',
      headers: {},
      config,
    }
  }
  try {
    useAuthStore.getState().auth.setUser({ id: 2, username: 'native', role: 1 })
    sessionRealm('LEGACY')
    const methods = await checkVerificationMethods()
    assert.equal(methods.has2FA, true)
    assert.equal(methods.passkeyUnavailable, true)
    assert.equal(methods.hasPasskey, false)
  } finally {
    api.defaults.adapter = previous
    useAuthStore.getState().auth.reset()
  }
})

test('legacy projected account keeps 2FA and unknown realm fails closed', async () => {
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
      username: 'legacy',
      role: 1,
      identity_source: 'unihub',
    })
    sessionRealm('LEGACY')
    const legacy = await checkVerificationMethods()
    assert.equal(legacy.has2FA, true)
    assert.ok(paths.includes('/api/user/2fa/status'))
    paths.length = 0
    sessionRealm('UNKNOWN')
    const unknown = await checkVerificationMethods()
    assert.equal(unknown.twoFAUnavailable, true)
    assert.equal(unknown.passkeyUnavailable, true)
    assert.deepEqual(paths, [])
  } finally {
    api.defaults.adapter = previous
    useAuthStore.getState().auth.reset()
  }
})
