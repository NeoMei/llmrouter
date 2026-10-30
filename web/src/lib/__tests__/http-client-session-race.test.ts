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

import { AxiosError, AxiosHeaders } from 'axios'

import { useAuthStore } from '../../stores/auth-store'
import { applyAuthBundle, clearAuthentication } from '../auth-session'
import { api } from '../http-client'

function login(sid: string, token = sid) {
  const expiry = Math.floor(Date.now() / 1000) + 3600
  applyAuthBundle(
    {
      access_token: token,
      token_type: 'Bearer',
      access_expires_at: expiry,
      session: {
        sid,
        current: true,
        login_method: 'password',
        ip: '',
        user_agent: '',
        created_at: 1,
        last_active_at: 1,
        expires_at: expiry,
      },
      user: { id: 7, username: sid, role: 1 },
    },
    false
  )
}

test('a late 401 from an old retried request cannot clear the newly selected session', async () => {
  const original = api.defaults.adapter
  let rejectRequest!: () => void
  let entered!: () => void
  const started = new Promise<void>((resolve) => {
    entered = resolve
  })
  api.defaults.adapter = (config) =>
    new Promise((_resolve, reject) => {
      rejectRequest = () =>
        reject(
          new AxiosError(
            'unauthorized',
            'ERR_BAD_REQUEST',
            config,
            {},
            {
              status: 401,
              statusText: 'Unauthorized',
              headers: new AxiosHeaders(),
              config,
              data: { code: 'AUTH_SESSION_REVOKED' },
            }
          )
        )
      entered()
    })
  try {
    login('old')
    const pending = api.get('/protected', {
      authRetry: true,
      skipErrorHandler: true,
    })
    await started
    login('new')
    rejectRequest()
    await assert.rejects(pending)
    assert.equal(useAuthStore.getState().auth.session?.sid, 'new')
  } finally {
    api.defaults.adapter = original
    clearAuthentication(false)
  }
})

test('an expired response from before sign-out cannot retry after sign-in with the same SID', async () => {
  const original = api.defaults.adapter
  let rejectRequest!: () => void
  let entered!: () => void
  let calls = 0
  const started = new Promise<void>((resolve) => {
    entered = resolve
  })
  api.defaults.adapter = (config) =>
    new Promise((_resolve, reject) => {
      calls++
      rejectRequest = () =>
        reject(
          new AxiosError(
            'expired',
            'ERR_BAD_REQUEST',
            config,
            {},
            {
              status: 401,
              statusText: 'Unauthorized',
              headers: new AxiosHeaders(),
              config,
              data: { code: 'AUTH_TOKEN_EXPIRED' },
            }
          )
        )
      entered()
    })
  try {
    login('same')
    const pending = api.get('/protected', { skipErrorHandler: true })
    await started
    clearAuthentication(false)
    login('same', 'new-token')
    rejectRequest()
    await assert.rejects(pending)
    assert.equal(calls, 1)
    assert.equal(useAuthStore.getState().auth.session?.sid, 'same')
    assert.equal(useAuthStore.getState().auth.accessToken, 'new-token')
  } finally {
    api.defaults.adapter = original
    clearAuthentication(false)
  }
})

test('a retried 401 for the current revoked session clears that session', async () => {
  const original = api.defaults.adapter
  api.defaults.adapter = async (config) => {
    throw new AxiosError(
      'unauthorized',
      'ERR_BAD_REQUEST',
      config,
      {},
      {
        status: 401,
        statusText: 'Unauthorized',
        headers: new AxiosHeaders(),
        config,
        data: { code: 'AUTH_SESSION_REVOKED' },
      }
    )
  }
  try {
    login('current')
    await assert.rejects(
      api.get('/protected', { authRetry: true, skipErrorHandler: true })
    )
    assert.equal(useAuthStore.getState().auth.session, null)
  } finally {
    api.defaults.adapter = original
    clearAuthentication(false)
  }
})

test('a retried authority error does not revoke the current session', async () => {
  const original = api.defaults.adapter
  api.defaults.adapter = async (config) => {
    throw new AxiosError(
      'unauthorized',
      'ERR_BAD_REQUEST',
      config,
      {},
      {
        status: 401,
        statusText: 'Unauthorized',
        headers: new AxiosHeaders(),
        config,
        data: { code: 'UNIHUB_REAUTH_REQUIRED' },
      }
    )
  }
  try {
    login('current')
    await assert.rejects(
      api.get('/protected', { authRetry: true, skipErrorHandler: true })
    )
    assert.equal(useAuthStore.getState().auth.session?.sid, 'current')
  } finally {
    api.defaults.adapter = original
    clearAuthentication(false)
  }
})

test('a late rotation response cannot replace a newer session for the same SID', async () => {
  const original = api.defaults.adapter
  let finish!: () => void
  let entered!: () => void
  const started = new Promise<void>((resolve) => {
    entered = resolve
  })
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
              access_token: 'old-rotation',
              token_type: 'Bearer',
              access_expires_at: Math.floor(Date.now() / 1000) + 3600,
              session: useAuthStore.getState().auth.session,
            },
          },
        })
      entered()
    })
  try {
    login('same')
    const pending = api.post('/rotate', {}, { acceptAuthRotation: true })
    await started
    login('same')
    finish()
    await assert.rejects(pending)
    assert.equal(useAuthStore.getState().auth.accessToken, 'same')
  } finally {
    api.defaults.adapter = original
    clearAuthentication(false)
  }
})
