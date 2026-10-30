import assert from 'node:assert/strict'
import { test } from 'node:test'

import { AxiosHeaders } from 'axios'

import { applyAuthBundle, clearAuthentication } from '@/lib/auth-session'
import { api } from '@/lib/http-client'

import { getPasskeyStatus } from '../api'

function login() {
  const expiry = Math.floor(Date.now() / 1000) + 3600
  applyAuthBundle(
    {
      access_token: 'fixture',
      token_type: 'Bearer',
      access_expires_at: expiry,
      session: {
        sid: 'fixture',
        current: true,
        login_method: 'password',
        ip: '',
        user_agent: '',
        created_at: 1,
        last_active_at: 1,
        expires_at: expiry,
      },
      user: { id: 7, username: 'fixture', role: 1 },
    },
    false
  )
}

test('post-mutation Passkey status bypasses an earlier in-flight GET', async () => {
  const original = api.defaults.adapter
  const pending: Array<(enabled: boolean) => void> = []
  login()
  api.defaults.adapter = (config) =>
    new Promise((resolve) =>
      pending.push((enabled) =>
        resolve({
          status: 200,
          statusText: 'OK',
          headers: new AxiosHeaders(),
          config,
          data: { success: true, data: { enabled } },
        })
      )
    )
  try {
    const old = getPasskeyStatus()
    const fresh = getPasskeyStatus(true)
    assert.equal(pending.length, 2)
    pending[1](false)
    assert.equal((await fresh).data?.enabled, false)
    pending[0](true)
    assert.equal((await old).data?.enabled, true)
  } finally {
    api.defaults.adapter = original
    clearAuthentication(false)
  }
})

test('a delayed pre-mutation status cannot overwrite the newer result', async () => {
  const { createPasskeyStatusLoader } = await import('../status-loader')
  const original = api.defaults.adapter
  const pending: Array<(enabled: boolean) => void> = []
  login()
  api.defaults.adapter = (config) =>
    new Promise((resolve) =>
      pending.push((enabled) =>
        resolve({
          status: 200,
          statusText: 'OK',
          headers: new AxiosHeaders(),
          config,
          data: { success: true, data: { enabled } },
        })
      )
    )
  try {
    const load = createPasskeyStatusLoader()
    const old = load()
    const fresh = load(true)
    assert.equal(pending.length, 2)
    pending[1](false)
    assert.deepEqual(await fresh, {
      current: true,
      response: { success: true, data: { enabled: false } },
    })
    pending[0](true)
    assert.deepEqual(await old, { current: false })
  } finally {
    api.defaults.adapter = original
    clearAuthentication(false)
  }
})
