import { z } from 'zod'

const integer = z.string().regex(/^(0|[1-9][0-9]{0,37})$/)
const positive = integer.refine((v) => v !== '0')
export const quantitySchema = z.object({
  value: integer,
  scale: z.number().int().min(0).max(9),
})
export const policies: Record<string, readonly string[]> = {
  chat: ['provider_reported_v1'],
  responses: ['provider_reported_v1'],
  embedding: ['provider_reported_v1'],
  speech: ['provider_reported_v1', 'unicode_scalar_v1'],
  transcription: ['provider_reported_v1'],
  image_generation: ['successful_outputs_v1'],
  image_edit: ['successful_outputs_v1'],
  ocr: ['success_request'],
  video: [
    'provider_reported_v1',
    'successful_outputs_v1',
    'sora_completed_seconds_v1',
    'kling_final_units_v1',
    'doubao_completion_tokens_v1',
    'ali_video_duration_v1',
    'ali_wan26_t2v_billable_duration_v1',
    'veo_output_probe_v1',
  ],
}
export const operationUnits: Record<string, readonly string[]> = {
  chat: ['token'],
  responses: ['token'],
  embedding: ['token'],
  speech: ['character'],
  transcription: ['audio_second'],
  image_generation: ['image'],
  image_edit: ['image'],
  ocr: ['request'],
  video: ['video_second', 'provider_unit'],
}
const termSchema = z.object({
  key: z.string().trim().min(1).max(128),
  unit: z.string(),
  provider_unit: z.string().max(128).optional(),
  price: z.object({ numerator: integer, denominator: positive }),
  step: quantitySchema.refine((q) => q.value !== '0').optional(),
  minimum: quantitySchema.optional(),
})
export const priceSchema = z
  .object({
    version: z.literal('metered_v2'),
    operation: z.string(),
    policy: z.string(),
    terms: z.array(termSchema).min(1).max(64),
  })
  .superRefine((p, ctx) => {
    const keys = new Set<string>()
    if (!policies[p.operation]?.includes(p.policy)) {
      ctx.addIssue({ code: 'custom', message: 'Invalid native tariff' })
    }
    for (const term of p.terms) {
      const dimensions: Record<string, readonly string[]> = {
        token: ['input', 'output', 'cache_read', 'cache_write', 'reasoning'],
        character: ['characters'],
        audio_second: ['audio'],
        request: ['requests'],
        video_second: ['video'],
        provider_unit: ['provider'],
      }
      if (
        keys.has(term.key) ||
        !operationUnits[p.operation]?.includes(term.unit) ||
        term.unit !== p.terms[0].unit ||
        (dimensions[term.unit] && !dimensions[term.unit].includes(term.key)) ||
        (term.unit === 'provider_unit'
          ? !term.provider_unit?.trim()
          : Boolean(term.provider_unit))
      ) {
        ctx.addIssue({ code: 'custom', message: 'Invalid native tariff' })
      }
      keys.add(term.key)
      if (
        ['token', 'character', 'image', 'request'].includes(term.unit) &&
        [term.step, term.minimum].some((q) => q && q.scale !== 0)
      ) {
        ctx.addIssue({ code: 'custom', message: 'Invalid native tariff' })
      }
    }
  })
export const catalogSchema = z
  .record(z.string().trim().min(1).max(128), z.record(z.string(), priceSchema))
  .superRefine((catalog, ctx) => {
    if (Object.keys(catalog).length > 10000) {
      ctx.addIssue({ code: 'custom', message: 'Invalid native tariff' })
    }
    for (const ops of Object.values(catalog)) {
      if (Object.keys(ops).length > 16) {
        ctx.addIssue({ code: 'custom', message: 'Invalid native tariff' })
      }
      for (const [key, value] of Object.entries(ops)) {
        if (key !== value.operation) {
          ctx.addIssue({ code: 'custom', message: 'Invalid native tariff' })
        }
      }
    }
  })
export type NativePrice = z.infer<typeof priceSchema>
export type NativeQuantity = z.infer<typeof quantitySchema>
export function formatQuantity(q: NativeQuantity): string {
  quantitySchema.parse(q)
  const text = q.value.padStart(q.scale + 1, '0')
  return q.scale ? `${text.slice(0, -q.scale)}.${text.slice(-q.scale)}` : text
}
export function preview(
  price: NativePrice,
  values: Record<string, NativeQuantity>
): string {
  priceSchema.parse(price)
  let numerator = 0n,
    denominator = 1n
  for (const term of price.terms) {
    const q = quantitySchema.parse(values[term.key])
    if (
      ['token', 'character', 'image', 'request'].includes(term.unit) &&
      q.scale !== 0
    ) {
      throw new Error('Invalid native tariff')
    }
    let n = BigInt(q.value),
      d = 10n ** BigInt(q.scale)
    if (n > 0n && term.minimum) {
      const mn = BigInt(term.minimum.value),
        md = 10n ** BigInt(term.minimum.scale)
      if (n * md < mn * d) {
        n = mn
        d = md
      }
    }
    if (n > 0n && term.step) {
      const sn = BigInt(term.step.value),
        sd = 10n ** BigInt(term.step.scale)
      n = ((n * sd + d * sn - 1n) / (d * sn)) * sn
      d = sd
    }
    const tn = n * BigInt(term.price.numerator),
      td = d * BigInt(term.price.denominator)
    numerator = numerator * td + tn * denominator
    denominator *= td
  }
  let a = numerator,
    b = denominator
  while (b) {
    const r = a % b
    a = b
    b = r
  }
  const gcd = a || 1n
  return denominator / gcd === 1n
    ? String(numerator / gcd)
    : `${numerator / gcd}/${denominator / gcd}`
}
export function finalAmount(state: string, quota: string): string | undefined {
  return state === 'settled' ? quota : undefined
}
