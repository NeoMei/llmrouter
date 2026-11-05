import assert from 'node:assert/strict'
import { test } from 'node:test'

import i18n from 'i18next'
import { renderToStaticMarkup } from 'react-dom/server'
import { initReactI18next } from 'react-i18next'

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
import parityCases from '../../../../../pkg/metering/testdata/technical-presentation.json'
import zeroLegacy from '../../../../../service/testdata/central-zero-legacy.json'
import { TechnicalUsage } from '../components/technical-usage'
import { usageLogSchema } from '../data/schema'
import {
  technicalUsage,
  hasFinancialDetails,
  isCentralTechnical,
} from '../lib/technical-usage'

await i18n
  .use(initReactI18next)
  .init({ lng: 'en', resources: {}, fallbackLng: 'en' })
const log = usageLogSchema.parse({
  id: 1,
  user_id: 42,
  created_at: 1,
  type: 2,
  content: '',
})
const measurement = (key: string, unit: string, value: string, scale = 0) => ({
  key,
  unit,
  quantity: { value, scale },
})
const central = { funding_owner: 'unihub', operation: 'chat', request_count: 1 }

test('historical logical request count cannot become validated native usage', () => {
  const html = renderToStaticMarkup(
    <TechnicalUsage
      log={log}
      other={{ ...central, usage_state: 'unverifiable' }}
    />
  )
  assert.match(html, /Usage cannot be verified/)
  assert.doesNotMatch(html, /Requests: 1/)
  assert.match(html, /Provider token usage was not recorded/)
})
test('native tokens preserve exact integers beyond JavaScript precision', () => {
  const html = renderToStaticMarkup(
    <TechnicalUsage
      log={log}
      other={{
        ...central,
        usage_state: 'confirmed',
        native_measurements: [
          measurement('input', 'token', '90071992547409931234'),
        ],
      }}
    />
  )
  assert.match(html, /Input Tokens: 90071992547409931234/)
  assert.doesNotMatch(html, /not recorded/)
})
test('recorded fractional audio retains scale', () => {
  assert.equal(
    technicalUsage({
      usage_state: 'confirmed',
      native_measurements: [measurement('audio', 'audio_second', '12300', 3)],
    }).measurements[0].formatted,
    '12.300'
  )
})
test('malformed quantities and unknown dimensions fail safely', () => {
  for (const m of [
    measurement('input', 'token', '-1'),
    measurement('input', 'token', '01'),
    measurement('input', 'token', '1', 1),
    measurement('unknown', 'request', '1'),
    measurement('audio', 'audio_second', '1', 10),
    { ...measurement('requests', 'request', '1'), charged: 'secret' },
  ]) {
    const result = technicalUsage({
      usage_state: 'confirmed',
      native_measurements: [m],
    })
    assert.equal(result.state, 'Usage evidence is invalid')
    assert.deepEqual(result.measurements, [])
  }
})
test('duplicate dimensions are conflicting and not promoted', () => {
  const m = measurement('requests', 'request', '1')
  assert.equal(
    technicalUsage({ usage_state: 'confirmed', native_measurements: [m, m] })
      .state,
    'Usage evidence conflicts'
  )
})
test('confirmed without observations is missing and invalid/conflict stay explicit', () => {
  assert.equal(
    technicalUsage({ usage_state: 'confirmed' }).state,
    'Usage evidence is missing'
  )
  assert.equal(
    technicalUsage({ usage_state: 'invalid' }).state,
    'Usage evidence is invalid'
  )
  assert.equal(
    technicalUsage({ usage_state: 'conflict' }).state,
    'Usage evidence conflicts'
  )
})
test('legacy technical tokens remain recorded without promoting request count', () => {
  const html = renderToStaticMarkup(
    <TechnicalUsage
      log={{ ...log, prompt_tokens: 21, completion_tokens: 7 }}
      other={{ ...central, usage_state: 'unverifiable' }}
    />
  )
  assert.match(html, /Recorded input \/ output tokens.*21.*7/)
  assert.doesNotMatch(html, /Requests: 1|not recorded/)
})
test('central finance is hidden while ordinary and product runtime billing survive', () => {
  assert.equal(hasFinancialDetails(central), false)
  assert.equal(hasFinancialDetails({ ...central, product_runtime: true }), true)
  assert.equal(isCentralTechnical({ ...central, product_runtime: true }), false)
  assert.equal(hasFinancialDetails({}), true)
  assert.equal(hasFinancialDetails({ financial_details: false }), false)
})

test('confirmed legacy token evidence keeps its confirmed state without native dimensions', () => {
  const html = renderToStaticMarkup(
    <TechnicalUsage
      log={{ ...log, prompt_tokens: 21 }}
      other={{ ...central, usage_state: 'confirmed' }}
    />
  )
  assert.match(html, /Usage confirmed/)
  assert.doesNotMatch(html, /Usage evidence is missing/)
})

for (const fixture of parityCases) {
  test(`Go native producer parity: ${fixture.name}`, () => {
    const result = technicalUsage({
      operation: fixture.usage.operation,
      usage_state: 'confirmed',
      native_measurements: fixture.usage.measurements,
    })
    assert.equal(
      result.state,
      fixture.valid ? 'Usage confirmed' : 'Usage evidence is invalid'
    )
    if (fixture.valid) {
      assert.deepEqual(
        result.measurements.map((m) => ({
          key: m.key,
          unit: m.unit,
          provider_unit: m.provider_unit,
          quantity: m.quantity,
        })),
        fixture.usage.measurements.map((m) => ({
          ...m,
          provider_unit: 'provider_unit' in m ? m.provider_unit : undefined,
        }))
      )
    } else assert.deepEqual(result.measurements, [])
  })
}

test('producer-valid dimensions render their identities and do not fabricate token aggregates', () => {
  for (const fixture of parityCases.filter((f) => f.valid)) {
    const html = renderToStaticMarkup(
      <TechnicalUsage
        log={log}
        other={{
          ...central,
          operation: fixture.usage.operation,
          usage_state: 'confirmed',
          native_measurements: fixture.usage.measurements,
        }}
      />
    )
    assert.match(html, /Usage confirmed/)
    if (fixture.usage.operation === 'multimodal_embedding') {
      assert.match(html, /Multimodal embedding/)
      assert.match(html, /Text tokens.*12300/)
      assert.match(html, /Visual tokens.*12300/)
      assert.doesNotMatch(html, /not recorded|Recorded input|24600/)
    }
    if (fixture.name === 'literal image tier') {
      assert.ok(html.includes('( custom-tier )'))
    }
    if (fixture.name.startsWith('video_')) {
      assert.ok(
        html.includes(
          fixture.name.endsWith('720p')
            ? 'Video seconds (720p)'
            : 'Video seconds (1080p)'
        )
      )
    }
    assert.doesNotMatch(html, /Total Cost|Per-token/)
  }
})

test('new operation and measurement labels translate into Chinese', async () => {
  const locale = await import('../../../i18n/locales/zh.json')
  i18n.addResourceBundle('zh', 'translation', locale.default.translation)
  await i18n.changeLanguage('zh')
  try {
    for (const fixture of parityCases.filter((f) =>
      [
        'multimodal text and visual',
        'rerank input',
        'video_720p',
        'video_1080p',
      ].includes(f.name)
    )) {
      const html = renderToStaticMarkup(
        <TechnicalUsage
          log={log}
          other={{
            ...central,
            operation: fixture.usage.operation,
            usage_state: 'confirmed',
            native_measurements: fixture.usage.measurements,
          }}
        />
      )
      assert.doesNotMatch(
        html,
        /Other operation|Text tokens|Visual tokens|Video seconds|Multimodal embedding|Reranking/
      )
      if (fixture.name === 'multimodal text and visual') {
        assert.match(html, /多模态向量化/)
        assert.match(html, /文本 Token/)
        assert.match(html, /视觉 Token/)
      }
      if (fixture.name === 'rerank input') assert.match(html, /重排序/)
      if (fixture.name.startsWith('video_')) assert.match(html, /视频秒数/)
    }
  } finally {
    await i18n.changeLanguage('en')
  }
})

test('literal image keys resembling token dimensions keep their image identity', () => {
  for (const key of ['input', 'text', 'visual']) {
    const html = renderToStaticMarkup(
      <TechnicalUsage
        log={log}
        other={{
          ...central,
          operation: 'image_generation',
          usage_state: 'confirmed',
          native_measurements: [measurement(key, 'image', '1')],
        }}
      />
    )
    assert.match(html, /Images/)
    assert.ok(html.includes(` (${key})`))
    assert.doesNotMatch(html, /Input Tokens|Text tokens|Visual tokens/)
  }
})

test('producer-confirmed all-zero legacy evidence remains recorded zero', () => {
  const html = renderToStaticMarkup(
    <TechnicalUsage
      log={{
        ...log,
        prompt_tokens: zeroLegacy.prompt_tokens,
        completion_tokens: zeroLegacy.completion_tokens,
      }}
      other={zeroLegacy.other}
    />
  )
  assert.match(html, /Usage confirmed/)
  assert.match(html, /Recorded input \/ output tokens: 0 \/ 0/)
  assert.doesNotMatch(html, /missing|not recorded|Total Cost|Per-token/)
})
test('default zero fields never become recorded tokens for unconfirmed or request-only evidence', () => {
  for (const state of ['missing', 'unverifiable', 'invalid', 'conflict']) {
    const html = renderToStaticMarkup(
      <TechnicalUsage
        log={log}
        other={{ ...zeroLegacy.other, usage_state: state }}
      />
    )
    assert.doesNotMatch(html, /Recorded input/)
  }
  const html = renderToStaticMarkup(
    <TechnicalUsage
      log={log}
      other={{
        ...zeroLegacy.other,
        native_measurements: [measurement('requests', 'request', '1')],
      }}
    />
  )
  assert.doesNotMatch(html, /Recorded input/)
  assert.match(html, /not recorded/)
})
