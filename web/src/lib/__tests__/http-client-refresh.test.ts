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

import axios, { AxiosError, AxiosHeaders } from 'axios'

test('after a successful refresh only explicit revocation clears the current session', async () => {
  const original = axios.defaults.adapter
  const expiry = Math.floor(Date.now() / 1000) + 3600
  const bundle = {
    access_token: 'refreshed',
    token_type: 'Bearer',
    access_expires_at: expiry,
    session: {
      sid: 'current',
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
  let finalCode = 'UNIHUB_REAUTH_REQUIRED'
  let protectedCalls = 0
  let holdRefresh = false
  let enterRefresh!: () => void
  let finishRefresh!: () => void
  const refreshStarted = new Promise<void>((resolve) => {
    enterRefresh = resolve
  })
  axios.defaults.adapter = async (config) => {
    if (config.url === '/api/user/auth/refresh') {
      if (holdRefresh) {
        enterRefresh()
        await new Promise<void>((resolve) => {
          finishRefresh = resolve
        })
      }
      return {
        status: 200,
        statusText: 'OK',
        headers: new AxiosHeaders(),
        config,
        data: { success: true, data: bundle },
      }
    }
    protectedCalls++
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
        data: {
          code: protectedCalls % 2 === 1 ? 'AUTH_TOKEN_EXPIRED' : finalCode,
        },
      }
    )
  }
  const { api } = await import('../http-client')
  const { applyAuthBundle, clearAuthentication } =
    await import('../auth-session')
  const { useAuthStore } = await import('../../stores/auth-store')
  try {
    applyAuthBundle({ ...bundle, access_token: 'expired' }, false)
    await assert.rejects(api.get('/protected', { skipErrorHandler: true }))
    assert.equal(protectedCalls, 2)
    assert.equal(useAuthStore.getState().auth.accessToken, 'refreshed')
    assert.equal(useAuthStore.getState().auth.session?.sid, 'current')
    finalCode = 'AUTH_SESSION_REVOKED'
    await assert.rejects(api.get('/protected', { skipErrorHandler: true }))
    assert.equal(protectedCalls, 4)
    assert.equal(useAuthStore.getState().auth.session, null)
    holdRefresh = true
    applyAuthBundle({ ...bundle, access_token: 'old' }, false)
    const oldRequest = api.get('/protected', { skipErrorHandler: true })
    await refreshStarted
    applyAuthBundle(
      {
        ...bundle,
        access_token: 'new',
        session: { ...bundle.session, sid: 'new' },
      },
      false
    )
    finishRefresh()
    await assert.rejects(oldRequest)
    assert.equal(
      protectedCalls,
      5,
      'the old request must not retry as the new session'
    )
    assert.equal(useAuthStore.getState().auth.session?.sid, 'new')
    assert.equal(useAuthStore.getState().auth.accessToken, 'new')
  } finally {
    axios.defaults.adapter = original
    clearAuthentication(false)
  }
})
