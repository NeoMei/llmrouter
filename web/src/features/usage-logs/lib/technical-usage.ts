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
import { z } from 'zod'

import { quantitySchema, formatQuantity } from '../../native-metering/contract'
import type { LogOtherData } from '../types'

export function isCentralTechnical(other: LogOtherData | null): boolean {
  return other?.funding_owner === 'unihub' && other.product_runtime !== true
}
export function hasFinancialDetails(other: LogOtherData | null): boolean {
  return !isCentralTechnical(other) && other?.financial_details !== false
}
const measurementSchema = z
  .object({
    key: z.string().min(1).max(128),
    unit: z.enum([
      'request',
      'token',
      'character',
      'image',
      'audio_second',
      'video_second',
      'provider_unit',
    ]),
    provider_unit: z.string().min(1).max(128).optional(),
    quantity: quantitySchema.strict(),
  })
  .strict()
  .superRefine((m, ctx) => {
    const keys: Record<string, string[]> = {
      request: ['requests'],
      token: ['input', 'output', 'cache_read', 'cache_write', 'reasoning'],
      character: ['characters'],
      image: ['images'],
      audio_second: ['audio'],
      video_second: ['video'],
      provider_unit: ['provider'],
    }
    const dimensions = /^([1-9][0-9]{0,9})x([1-9][0-9]{0,9})$/.exec(m.key)
    const validImage =
      m.unit === 'image' &&
      dimensions &&
      BigInt(dimensions[1]) <= 2147483647n &&
      BigInt(dimensions[2]) <= 2147483647n
    if (
      (!keys[m.unit].includes(m.key) && !validImage) ||
      (['request', 'token', 'character', 'image'].includes(m.unit) &&
        m.quantity.scale !== 0) ||
      (m.unit === 'provider_unit'
        ? !m.provider_unit?.trim()
        : !!m.provider_unit)
    ) {
      ctx.addIssue({ code: 'custom', message: 'Invalid measurement' })
    }
  })
export const measurementLabels: Record<string, string> = {
  requests: 'Requests',
  input: 'Input Tokens',
  output: 'Output Tokens',
  cache_read: 'Cache read tokens',
  cache_write: 'Cache write tokens',
  reasoning: 'Reasoning tokens',
  characters: 'Characters',
  images: 'Images',
  audio: 'Audio seconds',
  video: 'Video seconds',
  provider: 'Provider units',
}
export const operationLabels: Record<string, string> = {
  chat: 'Chat',
  responses: 'Responses',
  embedding: 'Embedding',
  speech: 'Speech',
  transcription: 'Transcription',
  image_generation: 'Image generation',
  image_edit: 'Image editing',
  ocr: 'OCR',
  video: 'Video',
}
export function technicalUsage(other: LogOtherData, hasRecordedTokens = false) {
  const states: Record<string, string> = {
    confirmed: 'Usage confirmed',
    unverifiable: 'Usage cannot be verified',
    invalid: 'Usage evidence is invalid',
    conflict: 'Usage evidence conflicts',
    missing: 'Usage evidence is missing',
  }
  let state = states[other.usage_state ?? ''] ?? 'Usage evidence is missing'
  const parsed = z
    .array(measurementSchema)
    .min(1)
    .max(64)
    .safeParse(other.native_measurements)
  const seen = new Set<string>()
  const duplicate =
    parsed.success &&
    parsed.data.some((m) => {
      const key = `${m.key}/${m.unit}/${m.provider_unit ?? ''}`
      if (seen.has(key)) return true
      seen.add(key)
      return false
    })
  if (other.native_measurements !== undefined && !parsed.success) {
    state = 'Usage evidence is invalid'
  }
  if (duplicate) state = 'Usage evidence conflicts'
  if (
    other.usage_state === 'confirmed' &&
    other.native_measurements === undefined &&
    !hasRecordedTokens
  ) {
    state = 'Usage evidence is missing'
  }
  const measurements =
    state === 'Usage confirmed' && parsed.success && !duplicate
      ? parsed.data.map((m) => ({
          ...m,
          formatted: formatQuantity(m.quantity),
        }))
      : []
  return { state, measurements }
}
