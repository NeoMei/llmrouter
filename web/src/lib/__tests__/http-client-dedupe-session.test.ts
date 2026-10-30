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

import {
  applyAuthBundle,
  applyAuthRotation,
  clearAuthentication,
} from '../auth-session'
import { api } from '../http-client'

test('GET responses and deduplication stay within the selected authentication generation', async () => {
  const original = api.defaults.adapter
  const expiry = Math.floor(Date.now() / 1000) + 3600
  const bundle = {
    access_token: 'old',
    token_type: 'Bearer',
    access_expires_at: expiry,
    session: {
      sid: 'same',
      current: true,
      login_method: 'password',
      ip: '',
      user_agent: '',
      created_at: 1,
      last_active_at: 1,
      expires_at: expiry,
    },
    user: { id: 7, username: 'user', role: 1 },
  }
  let entered!: () => void
  let finish!: () => void
  const started = new Promise<void>((resolve) => {
    entered = resolve
  })
  let calls = 0
  api.defaults.adapter = async (config) => {
    calls++
    if (config.headers.Authorization === 'Bearer old') {
      entered()
      await new Promise<void>((resolve) => {
        finish = resolve
      })
    }
    return {
      status: 200,
      statusText: 'OK',
      headers: new AxiosHeaders(),
      config,
      data: { token: config.headers.Authorization },
    }
  }
  try {
    applyAuthBundle(bundle, false)
    const old = api.get('/session-probe')
    await started
    clearAuthentication(false)
    applyAuthBundle({ ...bundle, access_token: 'new' }, false)
    const current = api.get('/session-probe')
    finish()
    const [oldResult, currentResult] = await Promise.allSettled([old, current])
    assert.equal(oldResult.status, 'rejected')
    assert.equal(currentResult.status, 'fulfilled')
    if (currentResult.status === 'fulfilled') {
      assert.equal(currentResult.value.data.token, 'Bearer new')
    }
    assert.equal(calls, 2)
  } finally {
    api.defaults.adapter = original
    clearAuthentication(false)
  }
})

test('a request retains the account selected when it was sent', async () => {
  const original = api.defaults.adapter
  const expiry = Math.floor(Date.now() / 1000) + 3600
  const bundle = {
    access_token: 'old',
    token_type: 'Bearer',
    access_expires_at: expiry,
    session: {
      sid: 'old',
      current: true,
      login_method: 'password',
      ip: '',
      user_agent: '',
      created_at: 1,
      last_active_at: 1,
      expires_at: expiry,
    },
    user: { id: 7, username: 'user', role: 1 },
  }
  let sentToken: unknown
  api.defaults.adapter = async (config) => {
    sentToken = config.headers.Authorization
    return {
      status: 200,
      statusText: 'OK',
      headers: new AxiosHeaders(),
      config,
      data: { success: true },
    }
  }
  try {
    applyAuthBundle(bundle, false)
    const request = api.post('/mutate')
    applyAuthBundle(
      {
        ...bundle,
        access_token: 'new',
        session: { ...bundle.session, sid: 'new' },
      },
      false
    )
    await assert.rejects(request)
    assert.equal(sentToken, 'Bearer old')
  } finally {
    api.defaults.adapter = original
    clearAuthentication(false)
  }
})

test('same-session token rotation does not discard an already completed operation', async () => {
  const original = api.defaults.adapter
  const expiry = Math.floor(Date.now() / 1000) + 3600
  const bundle = {
    access_token: 'old',
    token_type: 'Bearer',
    access_expires_at: expiry,
    session: {
      sid: 'same',
      current: true,
      login_method: 'password',
      ip: '',
      user_agent: '',
      created_at: 1,
      last_active_at: 1,
      expires_at: expiry,
    },
    user: { id: 7, username: 'user', role: 1 },
  }
  let finish!: () => void
  let entered!: () => void
  const started = new Promise<void>((resolve) => {
    entered = resolve
  })
  api.defaults.adapter = async (config) => {
    entered()
    await new Promise<void>((resolve) => {
      finish = resolve
    })
    return {
      status: 200,
      statusText: 'OK',
      headers: new AxiosHeaders(),
      config,
      data: { success: true },
    }
  }
  try {
    applyAuthBundle(bundle, false)
    const request = api.post('/mutate')
    await started
    applyAuthRotation({ ...bundle, access_token: 'rotated' })
    finish()
    assert.equal((await request).status, 200)
  } finally {
    api.defaults.adapter = original
    clearAuthentication(false)
  }
})

test('security rotation remains usable after a concurrent refresh of the same session', async () => {
  const original = api.defaults.adapter
  const expiry = Math.floor(Date.now() / 1000) + 3600
  const bundle = {
    access_token: 'old',
    token_type: 'Bearer',
    access_expires_at: expiry,
    session: {
      sid: 'same',
      current: true,
      login_method: 'password',
      ip: '',
      user_agent: '',
      created_at: 1,
      last_active_at: 1,
      expires_at: expiry,
    },
    user: { id: 7, username: 'user', role: 1 },
  }
  let finish!: () => void
  let entered!: () => void
  const started = new Promise<void>((resolve) => {
    entered = resolve
  })
  api.defaults.adapter = async (config) => {
    entered()
    await new Promise<void>((resolve) => {
      finish = resolve
    })
    return {
      status: 200,
      statusText: 'OK',
      headers: new AxiosHeaders(),
      config,
      data: { success: true, data: { ...bundle, access_token: 'security' } },
    }
  }
  try {
    applyAuthBundle(bundle, false)
    const request = api.post('/security', {}, { acceptAuthRotation: true })
    await started
    applyAuthBundle({ ...bundle, access_token: 'refreshed' }, false, true)
    finish()
    assert.equal((await request).status, 200)
    const { useAuthStore } = await import('../../stores/auth-store')
    assert.equal(useAuthStore.getState().auth.accessToken, 'security')
  } finally {
    api.defaults.adapter = original
    clearAuthentication(false)
  }
})
