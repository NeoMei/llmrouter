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
import type { ClientModelRoute } from './api'
import { readMetadata } from './metadata'

export function validateAliases(aliases: string[]): boolean {
  const normalized = aliases
    .map((alias) => alias.trim().toLowerCase())
    .filter(Boolean)
  return new Set(normalized).size === normalized.length
}

export const WIRE_PROTOCOLS = [
  'responses',
  'openai_chat',
  'anthropic',
  'paddleocr_jobs',
  'qwen_multimodal_embedding',
] as const

export type WireProtocol = (typeof WIRE_PROTOCOLS)[number]

function readPreferredWires(raw: string): string[] | undefined {
  try {
    const value: unknown = JSON.parse(raw)
    if (!Array.isArray(value)) return undefined
    return value.map((wire: unknown) =>
      typeof wire === 'string' ? wire.trim().toLowerCase() : ''
    )
  } catch {
    return undefined
  }
}

function isWireProtocol(wire: string): wire is WireProtocol {
  return WIRE_PROTOCOLS.includes(wire as WireProtocol)
}

export function parsePreferredWires(
  raw: string,
  policy?: RouteWirePolicy
): WireProtocol[] {
  const wires = readPreferredWires(raw) ?? []
  return isLegacyVideoWire(wires, policy) ? [] : wires.filter(isWireProtocol)
}

export function serializePreferredWires(wires: WireProtocol[]): string {
  return JSON.stringify(WIRE_PROTOCOLS.filter((wire) => wires.includes(wire)))
}

// Keep this allowlist aligned with modelcatalog.OperationOnlyRoute. The server
// checks whether the selected channel supports every declared operation.
const OPERATION_ONLY = new Set([
  'images.generations.create',
  'images.edits.create',
  'dashscope.images.generate',
  'embeddings.create',
  'multimodal.embeddings.create',
  'rerank.create',
  'audio.speech.create',
  'bailian.audio.speech.create',
  'audio.transcriptions.create',
  'ocr.jobs.create',
  'tools.search',
  'tools.fetch',
  'videos.create',
])

type RouteWirePolicy =
  | 'required'
  | 'operation-only'
  | 'media-only'
  | 'video-only'

const MEDIA_OPERATIONS = new Set([
  'images.generations.create',
  'images.edits.create',
  'dashscope.images.generate',
  'videos.create',
])

export function getRouteWirePolicy(raw: string): RouteWirePolicy {
  try {
    const operations = readMetadata(raw).operations
    if (!Array.isArray(operations) || operations.length === 0) return 'required'
    const normalized = new Set(
      operations.map((op: unknown) =>
        typeof op === 'string' ? op.trim().toLowerCase() : ''
      )
    )
    if (normalized.size === 1 && normalized.has('videos.create')) {
      return 'video-only'
    }
    const names = [...normalized]
    if (names.every((op) => MEDIA_OPERATIONS.has(op))) return 'media-only'
    if (names.every((op) => OPERATION_ONLY.has(op))) return 'operation-only'
    return 'required'
  } catch {
    return 'required'
  }
}

function isLegacyVideoWire(
  wires: readonly string[],
  policy?: RouteWirePolicy
): boolean {
  return (
    policy === 'video-only' && wires.length === 1 && wires[0] === 'openai_chat'
  )
}

export function validateTargetWires(
  raw: string,
  policy: RouteWirePolicy,
  supported?: readonly WireProtocol[]
): boolean {
  const wires = readPreferredWires(raw)
  if (!wires) return false
  if (wires.length === 0) return policy !== 'required'
  if (isLegacyVideoWire(wires, policy)) return true
  return wires.every(
    (wire) => isWireProtocol(wire) && (!supported || supported.includes(wire))
  )
}

export type RouteModelMatch = 'prefix' | 'contains' | 'suffix'

export function matchRouteModel(
  route: Pick<ClientModelRoute, 'public_id'> & {
    aliases?: string[] | null
    targets?:
      | Pick<
          ClientModelRoute['targets'][number],
          'upstream_model' | 'billing_model'
        >[]
      | null
  },
  model: string,
  match?: RouteModelMatch
): boolean {
  const needle = model.trim().toLowerCase()
  const names = [
    route.public_id,
    ...(route.aliases ?? []),
    ...(route.targets ?? []).flatMap((target) => [
      target.upstream_model,
      target.billing_model,
    ]),
  ]
  return names.some((name) => {
    const normalized = name.trim().toLowerCase()
    if (match === 'prefix') return normalized.startsWith(needle)
    if (match === 'contains') return normalized.includes(needle)
    if (match === 'suffix') return normalized.endsWith(needle)
    return normalized === needle
  })
}
