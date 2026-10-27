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

import { useAuthStore } from '../../stores/auth-store'
import { refreshProductUser } from '../product-user'

function login(source?: string, sid = 'current') {
  const expiry = Math.floor(Date.now() / 1000) + 3600
  useAuthStore.getState().auth.setBundle({
    access_token: 'session-proof', token_type: 'Bearer', access_expires_at: expiry,
    session: { sid, current: true, login_method: source ?? 'password', ip: '', user_agent: '', created_at: 1, last_active_at: 1, expires_at: expiry },
    user: { id: 7, username: 'user', role: 100, identity_source: source },
  })
}

test('all login providers refresh effective roles and balance from the original product self endpoint', async () => {
  const original = globalThis.fetch
  try {
    for (const source of [undefined, 'unihub']) {
      login(source)
      globalThis.fetch = async (url) => {
        assert.equal(url, '/api/user/self')
        return Response.json({ success: true, data: { id: 7, username: 'user', role: 1, identity_source: source, quota: 120, used_quota: 30, request_count: 4 } })
      }
      await refreshProductUser()
      assert.equal(useAuthStore.getState().auth.user?.role, 1)
      assert.equal(useAuthStore.getState().auth.user?.quota, 120)
    }
  } finally {
    globalThis.fetch = original
    useAuthStore.getState().auth.reset()
  }
})

test('a late previous-session self response cannot replace the current user', async () => {
  const original = globalThis.fetch
  login()
  let resolve!: (value: Response) => void
  let enter!: () => void
  const entered = new Promise<void>(r => { enter = r })
  globalThis.fetch = () => { enter(); return new Promise<Response>(r => { resolve = r }) }
  try {
    const request = refreshProductUser()
    await entered
    login('unihub', 'newer')
    resolve(Response.json({ success: true, data: { id: 7, username: 'old', role: 1 } }))
    await request
    assert.equal(useAuthStore.getState().auth.user?.role, 100)
    assert.equal(useAuthStore.getState().auth.session?.sid, 'newer')
  } finally {
    globalThis.fetch = original
    useAuthStore.getState().auth.reset()
  }
})

test('temporary self endpoint failure preserves authentication while definitive denial clears it', async () => {
  const original = globalThis.fetch
  login()
  try {
    globalThis.fetch = async () => new Response(null, { status: 503 })
    await assert.rejects(refreshProductUser())
    assert.equal(useAuthStore.getState().auth.user?.id, 7)
    globalThis.fetch = async () => new Response(null, { status: 403 })
    await assert.rejects(refreshProductUser())
    assert.equal(useAuthStore.getState().auth.user, null)
  } finally {
    globalThis.fetch = original
    useAuthStore.getState().auth.reset()
  }
})
