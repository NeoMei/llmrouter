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

import React from 'react'
import { renderToStaticMarkup } from 'react-dom/server'

import { ProfileSecurityCard } from '../components/profile-security-card'
import { TwoFACard } from '../components/two-fa-card'
import type { UserProfile } from '../types'

Object.assign(globalThis, { React })

const profile = { id: 1, username: 'user', role: 1 } as UserProfile

test('central security settings retain the access token but do not offer local account changes', () => {
  const html = renderToStaticMarkup(
    React.createElement(ProfileSecurityCard, {
      profile,
      loading: false,
      central: true,
    })
  )
  assert.match(html, /Access Token/)
  assert.doesNotMatch(html, /Change Password/)
  assert.doesNotMatch(html, /Delete Account/)
  assert.match(html, /managed by UniHub/)
})

test('local security settings continue to offer password, token, and deletion', () => {
  const html = renderToStaticMarkup(
    React.createElement(ProfileSecurityCard, {
      profile,
      loading: false,
      central: false,
    })
  )
  assert.match(html, /Change Password/)
  assert.match(html, /Access Token/)
  assert.match(html, /Delete Account/)
})

test('central two-factor security is identified as centrally managed without a guessed status or controls', () => {
  const html = renderToStaticMarkup(
    React.createElement(TwoFACard, { loading: false, central: true })
  )
  assert.match(html, /Two-factor authentication is managed by UniHub/)
  assert.doesNotMatch(html, /Disabled|Enable|Disable 2FA/)
})
