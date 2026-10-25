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

import {
  catalogSchema,
  priceSchema,
  preview,
  finalAmount,
  formatQuantity,
  setReservationLimit,
  reservationLimitsSchema,
} from '../contract'

const price = {
  version: 'metered_v2',
  operation: 'speech',
  policy: 'unicode_scalar_v1',
  terms: [
    {
      key: 'characters',
      unit: 'character',
      price: { numerator: '1', denominator: '1000' },
    },
  ],
} as const
test('character quantities and money serialize exactly beyond Number precision', () => {
  const parsed = priceSchema.parse(price)
  assert.equal(
    preview(parsed, { characters: { value: '1500', scale: 0 } }),
    '3/2'
  )
  assert.equal(
    formatQuantity({ value: '9007199254740993', scale: 0 }),
    '9007199254740993'
  )
  assert.equal(
    JSON.parse(JSON.stringify(parsed)).terms[0].price.denominator,
    '1000'
  )
})
test('zero remains zero with minimum and step while pending has no final charge', () => {
  const parsed = priceSchema.parse({
    ...price,
    terms: [
      {
        ...price.terms[0],
        minimum: { value: '1000', scale: 0 },
        step: { value: '1000', scale: 0 },
      },
    ],
  })
  assert.equal(preview(parsed, { characters: { value: '0', scale: 0 } }), '0')
  assert.equal(
    preview(parsed, { characters: { value: '1001', scale: 0 } }),
    '2'
  )
  assert.equal(finalAmount('measurement_pending', '0'), undefined)
  assert.equal(finalAmount('settled', '0'), '0')
})
test('invalid unit, duplicate tier, zero denominator and operation mismatch reject publication', () => {
  assert.equal(
    priceSchema.safeParse({
      ...price,
      terms: [{ ...price.terms[0], unit: 'token' }],
    }).success,
    false
  )
  assert.equal(
    priceSchema.safeParse({ ...price, terms: [price.terms[0], price.terms[0]] })
      .success,
    false
  )
  assert.equal(
    priceSchema.safeParse({
      ...price,
      terms: [
        { ...price.terms[0], price: { numerator: '1', denominator: '0' } },
      ],
    }).success,
    false
  )
  assert.equal(
    catalogSchema.safeParse({ model: { ocr: price } }).success,
    false
  )
})

test('reservation limits serialize independently and preserve other operations', () => {
  const saved = setReservationLimit(
    '{"m":{"video":{"value":"10","scale":0}}}',
    'm',
    'speech',
    '2147483647'
  )
  assert.deepEqual(JSON.parse(saved), {
    m: {
      speech: { value: '2147483647', scale: 0 },
      video: { value: '10', scale: 0 },
    },
  })
  assert.equal(
    reservationLimitsSchema.safeParse({
      m: { speech: { value: '0', scale: 0 } },
    }).success,
    false
  )
  assert.equal(
    reservationLimitsSchema.safeParse({
      m: { speech: { value: '2147483648', scale: 0 } },
    }).success,
    false
  )
  assert.equal(
    reservationLimitsSchema.safeParse({
      m: { video: { value: '1', scale: 1 } },
    }).success,
    false
  )
  assert.equal(
    reservationLimitsSchema.safeParse({ m: { ocr: { value: '1', scale: 0 } } })
      .success,
    false
  )
})

test('image tiers require exclusive images or canonical bounded dimensions', () => {
  const image = {
    version: 'metered_v2',
    operation: 'image_generation',
    policy: 'successful_outputs_v1',
    terms: [
      {
        key: '1024x1024',
        unit: 'image',
        price: { numerator: '1', denominator: '1' },
      },
    ],
  }
  assert.equal(priceSchema.safeParse(image).success, true)
  for (const key of ['01024x1024', '1024x0', '2147483648x1024', 'large']) {
    assert.equal(
      priceSchema.safeParse({ ...image, terms: [{ ...image.terms[0], key }] })
        .success,
      false
    )
  }
  assert.equal(
    priceSchema.safeParse({
      ...image,
      terms: [image.terms[0], { ...image.terms[0], key: 'images' }],
    }).success,
    false
  )
  assert.equal(
    priceSchema.safeParse({
      ...image,
      terms: [{ ...image.terms[0], key: 'images' }],
    }).success,
    true
  )
})
test('server audio measurement is publishable but unimplemented generic video policies are rejected', () => {
  const audio = {
    version: 'metered_v2',
    operation: 'transcription',
    policy: 'server_audio_measurement_v1',
    terms: [
      {
        key: 'audio',
        unit: 'audio_second',
        price: { numerator: '1', denominator: '1' },
      },
    ],
  }
  assert.equal(priceSchema.safeParse(audio).success, true)
  for (const policy of [
    'provider_reported_v1',
    'successful_outputs_v1',
    'veo_output_probe_v1',
  ]) {
    assert.equal(
      priceSchema.safeParse({
        ...audio,
        operation: 'video',
        policy,
        terms: [{ ...audio.terms[0], key: 'video', unit: 'video_second' }],
      }).success,
      false
    )
  }
})
