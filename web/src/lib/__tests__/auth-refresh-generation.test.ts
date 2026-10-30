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

import axios, { AxiosHeaders } from 'axios'

test('refresh requests deduplicate per authentication generation', async () => {
  const original = axios.defaults.adapter
  const originalLocks = Object.getOwnPropertyDescriptor(navigator, 'locks')
  Object.defineProperty(navigator, 'locks', {
    configurable: true,
    value: undefined,
  })
  const expiry = Math.floor(Date.now() / 1000) + 3600
  const bundle = (sid: string) => ({
    access_token: sid,
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
  })
  const pending = new Map<string, () => void>()
  const entered: string[] = []
  let newEntered!: () => void
  let oldEntered!: () => void
  let lockedOldEntered!: () => void
  let lockedNewEntered!: () => void
  const newStarted = new Promise<void>((resolve) => {
    newEntered = resolve
  })
  const oldStarted = new Promise<void>((resolve) => {
    oldEntered = resolve
  })
  const lockedOldStarted = new Promise<void>((resolve) => {
    lockedOldEntered = resolve
  })
  const lockedNewStarted = new Promise<void>((resolve) => {
    lockedNewEntered = resolve
  })
  axios.defaults.adapter = (config) =>
    new Promise((resolve) => {
      const sid = String(config.headers['X-Auth-Session'])
      entered.push(sid)
      if (sid === 'old') oldEntered()
      if (sid === 'new') newEntered()
      if (sid === 'locked-old') lockedOldEntered()
      if (sid === 'locked-new') lockedNewEntered()
      pending.set(sid, () =>
        resolve({
          status: 200,
          statusText: 'OK',
          headers: new AxiosHeaders(),
          config,
          data: { success: true, data: bundle(sid) },
        })
      )
    })
  const { applyAuthBundle, clearAuthentication, refreshAuthentication } =
    await import('../auth-session')
  const { useAuthStore } = await import('../../stores/auth-store')
  try {
    applyAuthBundle(bundle('old'), false)
    const old = refreshAuthentication()
    assert.equal(refreshAuthentication(), old)
    await oldStarted
    applyAuthBundle(bundle('new'), false)
    const current = refreshAuthentication()
    assert.notEqual(current, old)
    await newStarted
    assert.deepEqual(entered, ['old', 'new'])
    assert.ok(pending.has('old'))
    pending.get('old')?.()
    assert.equal((await old).kind, 'transient_error')
    assert.equal(
      refreshAuthentication(),
      current,
      'old finalizer must not clear the new in-flight refresh'
    )
    assert.deepEqual(entered, ['old', 'new'])
    assert.ok(pending.has('new'))
    pending.get('new')?.()
    assert.equal((await current).kind, 'authenticated')
    assert.equal(useAuthStore.getState().auth.session?.sid, 'new')
    if (originalLocks) Object.defineProperty(navigator, 'locks', originalLocks)
    else Reflect.deleteProperty(navigator, 'locks')
    assert.ok(
      navigator.locks,
      'browser lock must be available for the locked case'
    )
    applyAuthBundle(bundle('locked-old'), false)
    const lockedOld = refreshAuthentication()
    await lockedOldStarted
    applyAuthBundle(bundle('locked-new'), false)
    const lockedNew = refreshAuthentication()
    assert.notEqual(lockedOld, lockedNew)
    assert.equal(refreshAuthentication(), lockedNew)
    assert.deepEqual(entered, ['old', 'new', 'locked-old'])
    assert.ok(pending.has('locked-old'))
    pending.get('locked-old')?.()
    assert.equal((await lockedOld).kind, 'transient_error')
    await lockedNewStarted
    assert.equal(refreshAuthentication(), lockedNew)
    assert.ok(pending.has('locked-new'))
    pending.get('locked-new')?.()
    assert.equal((await lockedNew).kind, 'authenticated')
    assert.equal(useAuthStore.getState().auth.session?.sid, 'locked-new')
  } finally {
    axios.defaults.adapter = original
    if (originalLocks) Object.defineProperty(navigator, 'locks', originalLocks)
    else Reflect.deleteProperty(navigator, 'locks')
    clearAuthentication(false)
  }
})
