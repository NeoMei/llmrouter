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

import { centralWalletURL } from '../hooks/use-central-cash-wallet'

test('central wallet destinations retain the exact payer and an explicit distinct funding target', () => {
  assert.equal(
    centralWalletURL('https://hub.example', 'payer-1', 'unified'),
    'https://hub.example/?view=wallets&account=payer-1&wallet=unified'
  )
  assert.equal(
    centralWalletURL('https://hub.example/', 'payer-1', 'llmrouter'),
    'https://hub.example/?view=wallets&account=payer-1&product=llmrouter'
  )
})

test('central wallet links reject noncanonical origins and account injection', () => {
  for (const origin of [
    'javascript:alert(1)',
    'http://hub.example',
    'https://name:secret@hub.example',
    'https://hub.example/path',
    'https://hub.example/?token=secret',
    'https://hub.example/#fragment',
    ' https://hub.example',
    'https://hub.example/\n',
  ]) {
    assert.throws(() => centralWalletURL(origin, 'payer', 'unified'), origin)
  }
  for (const account of [
    '',
    '../other',
    'payer&account=other',
    'payer/other',
  ]) {
    assert.throws(() =>
      centralWalletURL('https://hub.example', account, 'unified')
    )
  }
})
