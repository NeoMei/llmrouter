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
// Patch only the requested JSON value. Unrelated lexemes (including integers
// outside JavaScript's precision) never pass through JSON.stringify.
type FieldSpan = { key: string; start: number; valueStart: number; end: number }

function objectFields(raw: string): FieldSpan[] {
  const parsed: unknown = JSON.parse(raw)
  if (!parsed || typeof parsed !== 'object' || Array.isArray(parsed)) {
    throw new Error('Metadata must be a JSON object')
  }
  const fields: FieldSpan[] = []
  const seen = new Set<string>()
  let i = raw.indexOf('{') + 1
  while (i < raw.length) {
    while (/\s/.test(raw[i] ?? '') && i < raw.length) i++
    if (raw[i] === '}') break
    const start = i
    i++
    while (i < raw.length && raw[i] !== '"') {
      if (raw[i] === '\\') i++
      i++
    }
    const key = JSON.parse(raw.slice(start, ++i)) as string
    if (seen.has(key)) throw new Error('Duplicate metadata field')
    seen.add(key)
    while (raw[i] !== ':') i++
    i++
    while (/\s/.test(raw[i] ?? '') && i < raw.length) i++
    const valueStart = i
    let depth = 0
    let quoted = false
    for (; i < raw.length; i++) {
      const c = raw[i]
      if (quoted) {
        if (c === '\\') i++
        else if (c === '"') quoted = false
      } else if (c === '"') quoted = true
      else if (c === '[' || c === '{') depth++
      else if ((c === '}' || c === ',') && depth === 0) break
      else if (c === ']' || c === '}') depth--
    }
    let end = i
    while (/\s/.test(raw[end - 1] ?? '')) end--
    const value = raw.slice(valueStart, end)
    if (value.startsWith('{')) objectFields(value)
    fields.push({ key, start, valueStart, end })
    if (raw[i] === ',') i++
  }
  return fields
}

export function readMetadata(raw: string): Record<string, unknown> {
  const source = raw.trim() ? raw : '{}'
  objectFields(source)
  return JSON.parse(source) as Record<string, unknown>
}

export function patchMetadata(
  raw: string,
  path: string[],
  value: unknown
): string {
  if (typeof value === 'number' && !Number.isFinite(value)) {
    throw new Error('Metadata numbers must be finite')
  }
  const source = raw.trim() ? raw : '{}'
  const fields = objectFields(source)
  const index = fields.findIndex((field) => field.key === path[0])
  const field = fields[index]
  let replacement: string | undefined
  if (path.length > 1) {
    replacement = patchMetadata(
      field ? source.slice(field.valueStart, field.end) : '{}',
      path.slice(1),
      value
    )
  } else if (value !== undefined) replacement = JSON.stringify(value)
  if (field && replacement !== undefined) {
    return (
      source.slice(0, field.valueStart) + replacement + source.slice(field.end)
    )
  }
  if (field) {
    if (fields.length === 1) {
      return source.slice(0, field.start) + source.slice(field.end)
    }
    if (index < fields.length - 1) {
      return (
        source.slice(0, field.start) +
        source.slice(source.indexOf(',', field.end) + 1)
      )
    }
    return (
      source.slice(0, source.lastIndexOf(',', field.start)) +
      source.slice(field.end)
    )
  }
  if (replacement === undefined) return source
  const end = source.lastIndexOf('}')
  return `${
    source.slice(0, end) + (fields.length ? ',' : '') + JSON.stringify(path[0])
  }:${replacement}${source.slice(end)}`
}

const SPACE_FIELDS = [
  'provider',
  'model',
  'version',
  'dimensions',
  'input_semantics',
] as const

// Match Go strings.TrimSpace on copies only: Unicode White_Space includes
// U+0085, but excludes U+FEFF (unlike JavaScript String.trim).
function normalizedEmbeddingSpace(
  value: Record<string, unknown> | undefined
): Record<string, unknown> | undefined {
  if (!value) return value
  const normalized = { ...value }
  for (const key of SPACE_FIELDS) {
    if (key !== 'dimensions' && typeof normalized[key] === 'string') {
      normalized[key] = normalized[key].replaceAll(
        /^\p{White_Space}+|\p{White_Space}+$/gu,
        ''
      )
    }
  }
  return normalized
}

export function validateFixedSpace(
  raw: string,
  targets: { upstream_model: string; metadata?: string }[],
  original?: string
): string {
  try {
    const metadata = readMetadata(raw)
    const space = normalizedEmbeddingSpace(
      metadata.embedding_space as Record<string, unknown> | undefined
    )
    const previous = normalizedEmbeddingSpace(
      original
        ? (readMetadata(original).embedding_space as
            | Record<string, unknown>
            | undefined)
        : undefined
    )
    if (
      previous &&
      SPACE_FIELDS.some((key) => previous[key] !== space?.[key])
    ) {
      return 'Changed vector spaces require a new public route and disjoint aliases'
    }
    if (
      space &&
      (SPACE_FIELDS.some(
        (key) =>
          key !== 'dimensions' &&
          (typeof space[key] !== 'string' || !space[key])
      ) ||
        !Number.isInteger(space.dimensions) ||
        Number(space.dimensions) < 1 ||
        Number(space.dimensions) > 65536)
    ) {
      return 'Complete the fixed embedding identity'
    }
    for (const target of targets) {
      const targetSpace = normalizedEmbeddingSpace(
        readMetadata(target.metadata || '').embedding_space as
          | Record<string, unknown>
          | undefined
      )
      if (space && target.upstream_model !== space.model) {
        return 'Target model must match the fixed embedding identity'
      }
      if (
        targetSpace &&
        (!space || SPACE_FIELDS.some((key) => targetSpace[key] !== space[key]))
      ) {
        return 'Target embedding identity must match the route'
      }
    }
    return ''
  } catch {
    return 'Metadata must be valid JSON with unique fields'
  }
}
