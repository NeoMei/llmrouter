import assert from 'node:assert/strict'
import test from 'node:test'

import { resolveIframePolicy } from './iframe-policy'

test('iframe policy rejects active and insecure production URLs', () => {
  assert.equal(
    resolveIframePolicy('javascript:alert(1)', {}, { production: true })
      .allowed,
    false
  )
  assert.equal(
    resolveIframePolicy('http://example.com', {}, { production: true })
      .allowed,
    false
  )
})

test('iframe policy only permits localhost HTTP during development', () => {
  assert.equal(
    resolveIframePolicy('http://localhost:5173', {}, { production: false })
      .allowed,
    true
  )
  assert.equal(
    resolveIframePolicy('http://192.168.1.4', {}, { production: false })
      .allowed,
    false
  )
})

test('iframe policy never combines scripts and same-origin for untrusted origins', () => {
  const policy = resolveIframePolicy(
    'https://untrusted.example/app',
    { allowScripts: true },
    { production: true, trustedPreviewOrigin: 'https://preview.example' }
  )
  assert.equal(policy.allowed, true)
  assert.equal(
    policy.sandbox.includes('allow-same-origin') &&
      policy.sandbox.includes('allow-scripts'),
    false
  )
  assert.equal(policy.allow, '')
})

test('trusted isolated preview origin may combine scripts and same-origin', () => {
  const policy = resolveIframePolicy(
    'https://preview.example/app',
    { allowScripts: true, allowCamera: true },
    { production: true, trustedPreviewOrigin: 'https://preview.example' }
  )
  assert.match(policy.sandbox, /allow-scripts/)
  assert.match(policy.sandbox, /allow-same-origin/)
  assert.equal(policy.allow, 'camera')
})
