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
    <ProfileSecurityCard profile={profile} loading={false} central />
  )
  assert.match(html, /Access Token/)
  assert.doesNotMatch(html, /Change Password/)
  assert.doesNotMatch(html, /Delete Account/)
  assert.match(html, /managed by UniHub/)
})

test('local security settings continue to offer password, token, and deletion', () => {
  const html = renderToStaticMarkup(
    <ProfileSecurityCard profile={profile} loading={false} central={false} />
  )
  assert.match(html, /Change Password/)
  assert.match(html, /Access Token/)
  assert.match(html, /Delete Account/)
})

test('central two-factor security is identified as centrally managed without a guessed status or controls', () => {
  const html = renderToStaticMarkup(
    <TwoFACard loading={false} central />
  )
  assert.match(html, /Two-factor authentication is managed by UniHub/)
  assert.doesNotMatch(html, /Disabled|Enable|Disable 2FA/)
})
