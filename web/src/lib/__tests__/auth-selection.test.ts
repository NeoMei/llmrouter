import assert from 'node:assert/strict'
import { afterEach, test } from 'node:test'

import { logout } from '../../features/auth/api'
import { useAuthStore } from '../../stores/auth-store'
import { api } from '../api'
import {
  clearSelectedSession,
  consumeReturnedSession,
  readSelectedSession,
  listSelectableSessions,
  selectSession,
} from '../auth-selection'
import { bootstrapAuthentication, createRefreshRunner, refreshAuthentication } from '../auth-session'

const originalWindow = globalThis.window

function storage(): Storage {
  const values = new Map<string, string>()
  return {
    get length() {
      return values.size
    },
    clear: () => values.clear(),
    getItem: (key) => values.get(key) ?? null,
    setItem: (key, value) => {
      values.set(key, value)
    },
    removeItem: (key) => {
      values.delete(key)
    },
    key: (index) => [...values.keys()][index] ?? null,
  }
}

function browser(
  local = storage(),
  tab = storage(),
  href = 'https://router.test/dashboard'
) {
  const location = new URL(href)
  let replaced = ''
  Object.defineProperty(globalThis, 'window', {
    configurable: true,
    value: {
      localStorage: local,
      sessionStorage: tab,
      location,
      history: {
        replaceState: (_state: unknown, _title: string, path: string) => {
          replaced = path
        },
      },
    },
  })
  return { local, tab, replaced: () => replaced }
}

afterEach(() => {
  useAuthStore.getState().auth.reset('idle')
  Object.defineProperty(globalThis, 'window', {
    configurable: true,
    value: originalWindow,
  })
})

test('an explicitly signed-out tab stays anonymous after reload, even if another session cookie remains', async () => {
  const { local, tab } = browser()
  selectSession('sid-native')
  browser(local)
  selectSession('sid-central')
  browser(local, tab)
  clearSelectedSession('sid-native')
  useAuthStore.getState().auth.reset('idle')
  // Simulate a reload in the same tab. The remaining central HttpOnly cookie
  // is invisible to JS and must not be selected by an unqualified refresh.
  browser(local, tab)
  assert.deepEqual(await bootstrapAuthentication(), { kind: 'anonymous' })
  assert.deepEqual(await refreshAuthentication(), { kind: 'anonymous' })
  const originalPost = api.post
  let requests = 0
  api.post = (async () => {
    requests += 1
    return { data: { success: true, message: '' } }
  }) as typeof api.post
  try {
    assert.deepEqual(await logout(), { success: true, message: '' })
    assert.equal(
      requests,
      0,
      'repeated logout must not revoke the other cookie'
    )
  } finally {
    api.post = originalPost
  }
  assert.deepEqual(listSelectableSessions(), ['sid-central'])
})

test('a temporary authority failure after reload preserves the explicit selection for recovery', async () => {
  const { local, tab } = browser()
  selectSession('sid-central')
  browser(local, tab)
  useAuthStore.getState().auth.reset('idle')
  const expected: Array<string | undefined> = []
  const refresh = createRefreshRunner({
    request: async (sid) => {
      expected.push(sid)
      return { status: 503 }
    },
    getExpectedSID: readSelectedSession,
    parseBundle: () => null,
    acceptBundle: () => undefined,
    clear: () => clearSelectedSession('sid-central'),
    markTransient: () => undefined,
    wait: async () => undefined,
  })
  assert.equal((await refresh()).kind, 'transient_error')
  assert.equal(readSelectedSession(), 'sid-central')
  browser(local, tab)
  assert.equal((await refresh()).kind, 'transient_error')
  assert.deepEqual(expected, ['sid-central', 'sid-central'])
  assert.equal(readSelectedSession(), 'sid-central')
})

test('unavailable tab storage cannot trigger an unqualified cookie refresh', async () => {
  browser()
  Object.defineProperty(window, 'sessionStorage', {
    configurable: true,
    get: () => {
      throw new Error('storage disabled')
    },
  })
  useAuthStore.getState().auth.reset('idle')
  assert.deepEqual(await bootstrapAuthentication(), { kind: 'anonymous' })
})

test('reload and a new tab retain an explicit SID without storing credentials', () => {
  const { local } = browser()
  selectSession('sid-native')
  assert.equal(readSelectedSession(), 'sid-native')
  assert.equal(local.getItem('new-api:auth:selected-sid:v1'), 'sid-native')
  assert.equal(local.getItem('new-api:auth:recent-sids:v1'), '["sid-native"]')
  assert.equal(local.length, 2)
  browser(local)
  assert.equal(readSelectedSession(), 'sid-native')
})

test('a tab switch does not overwrite another tab selection and sign-out clears only a match', () => {
  const { local, tab } = browser()
  selectSession('sid-native')
  browser(local)
  selectSession('sid-central')
  browser(local, tab)
  assert.equal(readSelectedSession(), 'sid-native')
  clearSelectedSession('sid-native')
  assert.equal(
    readSelectedSession(),
    undefined,
    'sign-out must not silently switch realms'
  )
  assert.equal(local.getItem('new-api:auth:selected-sid:v1'), 'sid-central')
  assert.deepEqual(listSelectableSessions(), ['sid-central'])
})

test('only explicitly selected recent sessions are offered for switching', () => {
  browser()
  selectSession('sid-native')
  selectSession('sid-central')
  assert.deepEqual(listSelectableSessions(), ['sid-central', 'sid-native'])
  clearSelectedSession('sid-native')
  assert.deepEqual(listSelectableSessions(), ['sid-central'])
})

test('central redirect selects the returned SID and removes it from browser URL', () => {
  const state = browser(
    storage(),
    storage(),
    'https://router.test/profile?x=1&__auth_session=sid-central#view'
  )
  assert.equal(consumeReturnedSession(), 'sid-central')
  assert.equal(readSelectedSession(), 'sid-central')
  assert.equal(state.replaced(), '/profile?x=1#view')
})

test('invalid or repeated returned selectors cannot choose an arbitrary session', () => {
  const state = browser(
    storage(),
    storage(),
    'https://router.test/profile?__auth_session=first&__auth_session=second'
  )
  assert.equal(consumeReturnedSession(), undefined)
  assert.equal(readSelectedSession(), undefined)
  assert.equal(state.replaced(), '/profile')
})
