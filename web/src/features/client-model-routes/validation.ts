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

export function parsePreferredWires(raw: string): WireProtocol[] {
  try {
    const value: unknown = JSON.parse(raw)
    if (!Array.isArray(value)) return []
    return value.filter(
      (wire): wire is WireProtocol =>
        typeof wire === 'string' &&
        WIRE_PROTOCOLS.includes(wire as WireProtocol)
    )
  } catch {
    return []
  }
}

export function serializePreferredWires(wires: WireProtocol[]): string {
  return JSON.stringify(WIRE_PROTOCOLS.filter((wire) => wires.includes(wire)))
}
