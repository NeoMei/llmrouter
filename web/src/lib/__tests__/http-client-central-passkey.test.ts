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

import { AxiosHeaders } from 'axios'

import { useAuthStore } from '../../stores/auth-store'
import { applyAuthBundle, clearAuthentication } from '../auth-session'
import { api } from '../http-client'

function loginCentral(sid: string) {
  const expiry = Math.floor(Date.now() / 1000) + 3600
  const session = {
    authentication_realm: 'CENTRAL' as const,
    sid,
    current: true,
    login_method: 'product_broker',
    ip: '',
    user_agent: '',
    created_at: 1,
    last_active_at: 1,
    expires_at: expiry,
  }
  applyAuthBundle(
    {
      access_token: 'original-token',
      token_type: 'Bearer',
      access_expires_at: expiry,
      session,
      user: { id: 7, username: 'fixture', role: 1 },
    },
    false
  )
  return session
}

test('concurrent central Passkey successes preserve the current token and both business results', async () => {
  const original = api.defaults.adapter
  const session = loginCentral('fixture-sid')
  const pending: Array<() => void> = []
  api.defaults.adapter = (config) =>
    new Promise((resolve) => {
      pending.push(() =>
        resolve({
          status: 200,
          statusText: 'OK',
          headers: new AxiosHeaders(),
          config,
          data: {
            success: true,
            data: {
              auth_rotation_required: false,
              access_token: 'replacement-token',
              token_type: 'Bearer',
              access_expires_at: session.expires_at,
              session,
            },
          },
        })
      )
    })
  try {
    const config = { acceptAuthRotation: true, allowNoAuthRotation: true }
    const a = api.delete('/api/user/passkey', config)
    const b = api.post('/api/user/passkey/register/finish', {}, config)
    assert.equal(pending.length, 2)
    pending[1]()
    assert.equal((await b).data.success, true)
    pending[0]()
    assert.equal((await a).data.success, true)
    assert.equal(useAuthStore.getState().auth.accessToken, 'original-token')
  } finally {
    api.defaults.adapter = original
    clearAuthentication(false)
  }
})

test('legacy rotation responses still reject a superseded token for the same SID', async () => {
  const original = api.defaults.adapter
  const session = loginCentral('fixture-sid')
  const pending: Array<() => void> = []
  api.defaults.adapter = (config) =>
    new Promise((resolve) => {
      pending.push(() =>
        resolve({
          status: 200,
          statusText: 'OK',
          headers: new AxiosHeaders(),
          config,
          data: {
            success: true,
            data: {
              access_token: 'replacement-token',
              token_type: 'Bearer',
              access_expires_at: session.expires_at,
              session,
            },
          },
        })
      )
    })
  try {
    const config = { acceptAuthRotation: true, allowNoAuthRotation: true }
    const a = api.delete('/api/user/passkey', config)
    const b = api.post('/api/user/passkey/register/finish', {}, config)
    pending[1]()
    await b
    pending[0]()
    await assert.rejects(a, /rotation response was superseded/)
    assert.equal(useAuthStore.getState().auth.accessToken, 'replacement-token')
  } finally {
    api.defaults.adapter = original
    clearAuthentication(false)
  }
})

test('no-rotation marker cannot accept a mismatched session', async () => {
  const original = api.defaults.adapter
  const session = loginCentral('fixture-sid')
  api.defaults.adapter = async (config) => ({
    status: 200,
    statusText: 'OK',
    headers: new AxiosHeaders(),
    config,
    data: {
      success: true,
      data: {
        auth_rotation_required: false,
        access_token: 'other-token',
        token_type: 'Bearer',
        access_expires_at: session.expires_at,
        session: { ...session, sid: 'other' },
      },
    },
  })
  try {
    await assert.rejects(
      api.delete('/api/user/passkey', {
        acceptAuthRotation: true,
        allowNoAuthRotation: true,
      })
    )
    assert.equal(useAuthStore.getState().auth.accessToken, 'original-token')
  } finally {
    api.defaults.adapter = original
    clearAuthentication(false)
  }
})

test('an unchanged central response is rejected after switching sessions', async () => {
  const original = api.defaults.adapter
  const session = loginCentral('fixture-sid')
  let finish!: () => void
  api.defaults.adapter = (config) =>
    new Promise((resolve) => {
      finish = () =>
        resolve({
          status: 200,
          statusText: 'OK',
          headers: new AxiosHeaders(),
          config,
          data: {
            success: true,
            data: {
              auth_rotation_required: false,
              access_token: 'replacement-token',
              token_type: 'Bearer',
              access_expires_at: session.expires_at,
              session,
            },
          },
        })
    })
  try {
    const pending = api.delete('/api/user/passkey', {
      acceptAuthRotation: true,
      allowNoAuthRotation: true,
    })
    loginCentral('different-sid')
    finish()
    await assert.rejects(pending, /response was superseded/)
    assert.equal(useAuthStore.getState().auth.session?.sid, 'different-sid')
  } finally {
    api.defaults.adapter = original
    clearAuthentication(false)
  }
})

test('an incomplete unchanged central response fails closed', async () => {
  const original = api.defaults.adapter
  const session = loginCentral('fixture-sid')
  api.defaults.adapter = async (config) => ({
    status: 200,
    statusText: 'OK',
    headers: new AxiosHeaders(),
    config,
    data: { success: true, data: { auth_rotation_required: false, session } },
  })
  try {
    await assert.rejects(
      api.delete('/api/user/passkey', {
        acceptAuthRotation: true,
        allowNoAuthRotation: true,
      }),
      /Invalid unchanged authentication response/
    )
    assert.equal(useAuthStore.getState().auth.accessToken, 'original-token')
  } finally {
    api.defaults.adapter = original
    clearAuthentication(false)
  }
})
