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
// Match Go strings.TrimSpace (Unicode White Space), which differs from JS trim for BOM/NEL.
const goWhitespaceOnly = /^\p{White_Space}*$/u
const utf8Length = (value: string): number =>
  new TextEncoder().encode(value).length
const literalIdentity = z
  .string()
  .refine((value) => utf8Length(value) <= 128 && !goWhitespaceOnly.test(value))
const operationUnits: Record<string, readonly string[]> = {
  chat: ['token', 'request'],
  responses: ['token'],
  embedding: ['token'],
  rerank: ['token'],
  multimodal_embedding: ['token'],
  speech: ['character', 'provider_unit'],
  transcription: ['audio_second'],
  image_generation: ['image'],
  image_edit: ['image'],
  ocr: ['request', 'token'],
  video: ['video_second', 'provider_unit'],
}
const measurementSchema = z
  .object({
    key: literalIdentity,
    unit: z.enum([
      'request',
      'token',
      'character',
      'image',
      'audio_second',
      'video_second',
      'provider_unit',
    ]),
    provider_unit: z
      .string()
      .refine((value) => utf8Length(value) <= 128)
      .optional(),
    quantity: quantitySchema.strict(),
  })
  .strict()
  .superRefine((m, ctx) => {
    const keys: Record<string, string[]> = {
      request: ['requests'],
      token: [
        'input',
        'output',
        'cache_read',
        'cache_write',
        'reasoning',
        'text',
        'visual',
      ],
      character: ['characters'],
      image: ['images'],
      audio_second: ['audio'],
      video_second: ['video', 'video_720p', 'video_1080p'],
      provider_unit: ['provider'],
    }
    if (
      (m.unit !== 'image' && !keys[m.unit].includes(m.key)) ||
      (['request', 'token', 'character', 'image'].includes(m.unit) &&
        m.quantity.scale !== 0) ||
      (m.unit === 'provider_unit'
        ? m.provider_unit === undefined ||
          goWhitespaceOnly.test(m.provider_unit)
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
  text: 'Text tokens',
  visual: 'Visual tokens',
  video_720p: 'Video seconds (720p)',
  video_1080p: 'Video seconds (1080p)',
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
  multimodal_embedding: 'Multimodal embedding',
  rerank: 'Reranking',
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
      const key = m.key
      if (seen.has(key)) return true
      seen.add(key)
      return false
    })
  if (other.native_measurements !== undefined && !parsed.success) {
    state = 'Usage evidence is invalid'
  }
  if (parsed.success && other.operation) {
    const op = other.operation
    const compatible = parsed.data.every((m) => {
      if (!operationUnits[op]?.includes(m.unit)) return false
      if (m.unit !== 'token') return true
      if (op === 'multimodal_embedding') {
        return m.key === 'text' || m.key === 'visual'
      }
      if (op === 'rerank') return m.key === 'input'
      return m.key !== 'text' && m.key !== 'visual'
    })
    if (!compatible) state = 'Usage evidence is invalid'
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
