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
// Keep the existing catalog first hop alongside each valid legacy chain tail.
// Catalog candidates are already authoritative; legacy relays follow the chain.
export function getAccountPoolModels(
  models: string,
  rawMapping: string
): string[] {
  let mapping = new Map<string, string>()
  try {
    const parsed: unknown = JSON.parse(rawMapping || '{}')
    if (parsed && typeof parsed === 'object' && !Array.isArray(parsed)) {
      mapping = new Map(
        Object.entries(parsed).filter(
          (entry): entry is [string, string] => typeof entry[1] === 'string'
        )
      )
    }
  } catch {
    /* server validates configured models */
  }
  const scopes = new Set<string>()
  for (const model of models
    .split(',')
    .map((value) => value.trim())
    .filter(Boolean)) {
    const visited = new Set([model])
    let current = model
    let cycle = false
    for (;;) {
      const next = mapping.get(current)
      if (!next || next === current) break
      if (visited.has(next)) {
        cycle = true
        break
      }
      visited.add(next)
      current = next
    }
    if (!cycle) scopes.add(current)
    scopes.add(mapping.get(model) || model)
  }
  return [...scopes]
}
