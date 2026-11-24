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

import { getClientModelRouteErrorMessage } from '../api'
import {
  getRouteWirePolicy,
  matchRouteModel,
  parsePreferredWires,
  serializePreferredWires,
  validateAliases,
  validateTargetWires,
} from '../validation'

test('duplicate aliases are rejected case-insensitively', () => {
  assert.equal(validateAliases(['gpt-5.6-sol', 'deepseek-v4']), true)
  assert.equal(validateAliases(['gpt-5.6-sol', ' GPT-5.6-SOL ']), false)
})

test('wire preferences ignore invalid values and keep the supported order', () => {
  assert.deepEqual(parsePreferredWires('["anthropic","future","responses"]'), [
    'anthropic',
    'responses',
  ])
  assert.deepEqual(parsePreferredWires('not-json'), [])
  assert.equal(
    serializePreferredWires(['anthropic', 'responses']),
    '["responses","anthropic"]'
  )
})

test('server validation message takes precedence over the generic HTTP error', () => {
  const error = {
    message: 'Request failed with status code 409',
    response: { data: { message: 'alias "shared" is already in use' } },
  }

  assert.equal(
    getClientModelRouteErrorMessage(error, 'Failed to save route'),
    'alias "shared" is already in use'
  )
})

test('unknown API failures use the localized fallback', () => {
  assert.equal(
    getClientModelRouteErrorMessage({ response: { data: {} } }, 'Try again'),
    'Try again'
  )
})

test('explicit image and video operations allow an empty protocol list', () => {
  for (const operation of ['images.generations.create', 'videos.create']) {
    const policy = getRouteWirePolicy(
      JSON.stringify({ operations: [operation] })
    )
    assert.equal(validateTargetWires('[]', policy, []), true)
    assert.equal(validateTargetWires('not-json', policy, []), false)
    assert.equal(validateTargetWires('["future"]', policy, []), false)
  }
})

test('only an explicit video-only route accepts the old chat placeholder', () => {
  const videoOnly = getRouteWirePolicy('{"operations":["videos.create"]}')
  const mixed = getRouteWirePolicy(
    '{"operations":["videos.create","chat.completions.create"]}'
  )
  const imageOnly = getRouteWirePolicy(
    '{"operations":["images.generations.create"]}'
  )

  assert.equal(validateTargetWires('["openai_chat"]', videoOnly, []), true)
  assert.equal(
    validateTargetWires('["openai_chat", "responses"]', videoOnly, []),
    false
  )
  assert.equal(validateTargetWires('["openai_chat"]', imageOnly, []), false)
  assert.equal(validateTargetWires('[]', mixed, ['openai_chat']), false)
  assert.equal(
    validateTargetWires('["openai_chat"]', mixed, ['openai_chat']),
    true
  )
  assert.equal(
    validateTargetWires('[]', getRouteWirePolicy('{}'), ['openai_chat']),
    false
  )
})

test('an existing multimodal embedding protocol remains valid', () => {
  const policy = getRouteWirePolicy(
    '{"operations":["multimodal.embeddings.create"]}'
  )
  assert.equal(
    validateTargetWires('["qwen_multimodal_embedding"]', policy, [
      'qwen_multimodal_embedding',
    ]),
    true
  )
})

test('model tracing matches public IDs, aliases, upstream models and billing keys', () => {
  const route = {
    public_id: 'public-model',
    aliases: ['alternate'],
    targets: [{ upstream_model: 'provider-model', billing_model: 'price-key' }],
  }

  for (const model of [
    'PUBLIC-MODEL',
    'alternate',
    'provider-model',
    'price-key',
  ]) {
    assert.equal(matchRouteModel(route, model), true)
  }
  assert.equal(matchRouteModel(route, 'provider'), false)
  assert.equal(matchRouteModel(route, 'provider', 'prefix'), true)
})

test('the video-only editor hides the legacy chat selection', () => {
  const policy = getRouteWirePolicy('{"operations":["videos.create"]}')
  assert.deepEqual(parsePreferredWires('[" OPENAI_CHAT "]', policy), [])
  assert.deepEqual(
    parsePreferredWires('["openai_chat"]', getRouteWirePolicy('{}')),
    ['openai_chat']
  )
})
