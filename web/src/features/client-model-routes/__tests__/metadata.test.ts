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
import test from 'node:test'

import { patchMetadata, readMetadata, validateFixedSpace } from '../metadata'

test('editing a declared field preserves unknown integers, nested values, whitespace and false/zero', () => {
  const raw =
    '{ "future":900719925474099312345, "streaming":false,"media_limits":{"future":1e+30,"video_audio":false},"zero":0 }'
  const next = patchMetadata(raw, ['media_limits', 'max_images'], 5)
  assert.equal(
    next,
    '{ "future":900719925474099312345, "streaming":false,"media_limits":{"future":1e+30,"video_audio":false,"max_images":5},"zero":0 }'
  )
  assert.equal(
    patchMetadata(next, ['streaming'], true),
    next.replace('"streaming":false', '"streaming":true')
  )
  assert.equal(
    patchMetadata('{"streaming":true}', ['streaming'], false),
    '{"streaming":false}'
  )
  assert.equal(
    patchMetadata('{}', ['media_limits', 'max_images'], 0),
    '{"media_limits":{"max_images":0}}'
  )
})

test('invalid or duplicate metadata is not normalized by typed edits', () => {
  for (const raw of [
    '[1]',
    '{broken',
    '{"a":1,"a":2}',
    '{"media_limits":{"max_images":1,"max_images":2}}',
  ]) {
    assert.throws(() => readMetadata(raw))
    assert.throws(() => patchMetadata(raw, ['streaming'], false))
  }
})

test('removing a field leaves the other exact values intact', () => {
  assert.equal(
    patchMetadata(
      '{"streaming":false, "big":9007199254740993}',
      ['streaming'],
      undefined
    ),
    '{ "big":9007199254740993}'
  )
  assert.equal(
    patchMetadata(
      '{"big":9007199254740993, "streaming":false}',
      ['streaming'],
      undefined
    ),
    '{"big":9007199254740993}'
  )
})

const space = {
  provider: 'aliyun',
  model: 'text-embedding-v4',
  version: 'v4',
  dimensions: 1024,
  input_semantics: 'text/query-document-v1',
}
test('fixed space changes and target mismatches fail while unrelated edits remain valid', () => {
  const metadata = JSON.stringify({ embedding_space: space })
  assert.equal(
    validateFixedSpace(
      metadata,
      [{ upstream_model: space.model, metadata: '' }],
      metadata
    ),
    ''
  )
  assert.ok(
    validateFixedSpace(metadata, [{ upstream_model: 'other' }], metadata)
  )
  assert.ok(
    validateFixedSpace('{}', [{ upstream_model: space.model }], metadata)
  )
  assert.ok(
    validateFixedSpace(
      JSON.stringify({ embedding_space: { ...space, dimensions: 2048 } }),
      [],
      metadata
    )
  )
  assert.ok(
    validateFixedSpace(metadata, [
      {
        upstream_model: space.model,
        metadata: JSON.stringify({
          embedding_space: { ...space, version: 'v5' },
        }),
      },
    ])
  )
})

test('nonfinite typed numeric input never becomes a silent null', () => {
  assert.throws(() =>
    patchMetadata(
      '{"future":9007199254740993}',
      ['media_limits', 'max_pixels'],
      Infinity
    )
  )
})
