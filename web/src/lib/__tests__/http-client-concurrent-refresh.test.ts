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

test('late expired request retries after another request refreshes the same session', async () => {
  const original = axios.defaults.adapter
  const expiry = Math.floor(Date.now() / 1000) + 3600
  const bundle = {
    access_token: 'fresh',
    token_type: 'Bearer',
    access_expires_at: expiry,
    session: {
      sid: 'sid',
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
  let release!: () => void
  const started = new Promise<void>((resolve) => {
    entered = resolve
  })
  let refreshes = 0
  axios.defaults.adapter = async (config) => {
    if (config.url === '/api/user/auth/refresh') {
      refreshes++
      return {
        status: 200,
        statusText: 'OK',
        headers: new AxiosHeaders(),
        config,
        data: { success: true, data: bundle },
      }
    }
    if (
      config.url === '/slow' &&
      config.headers.Authorization === 'Bearer old'
    ) {
      entered()
      await new Promise<void>((resolve) => {
        release = resolve
      })
    }
    if (config.headers.Authorization === 'Bearer old') {
      throw new AxiosError(
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
    }
    return {
      status: 200,
      statusText: 'OK',
      headers: new AxiosHeaders(),
      config,
      data: { success: true },
    }
  }
  const { api } = await import('../http-client')
  const { applyAuthBundle, clearAuthentication } =
    await import('../auth-session')
  try {
    applyAuthBundle({ ...bundle, access_token: 'old' }, false)
    const slow = api.get('/slow', { skipErrorHandler: true })
    await started
    await api.get('/fast', { skipErrorHandler: true })
    release()
    assert.equal((await slow).status, 200)
    assert.equal(refreshes, 1)
  } finally {
    axios.defaults.adapter = original
    clearAuthentication(false)
  }
})
