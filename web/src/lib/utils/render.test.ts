import assert from 'node:assert/strict'
import test from 'node:test'

import { sanitizeConfiguredHtml } from './render'

test('sanitize configured html removes executable content', () => {
  const sanitized = sanitizeConfiguredHtml(
    '<img src=x onerror=alert(1)><script>alert(2)</script><a href="javascript:alert(3)">x</a>'
  )
  assert.equal(sanitized.includes('onerror'), false)
  assert.equal(sanitized.includes('<script'), false)
  assert.equal(sanitized.toLowerCase().includes('javascript:'), false)
})
