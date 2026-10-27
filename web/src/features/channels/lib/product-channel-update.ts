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
// Opaque product configuration is never read back. Submit only explicit edits;
// object configuration is sent as a patch so server-owned hidden keys survive.
export function productChannelUpdate<T extends Record<string, unknown>>(
  payload: T,
  dirty: Record<string, unknown>,
): Partial<T> {
  const result: Record<string, unknown> = { ...payload }
  const hiddenFields = [
    'type',
    'key',
    'base_url',
    'openai_organization',
    'param_override',
    'header_override',
    'other',
    'remark',
    'other_info',
  ]
  for (const field of hiddenFields) {
    if (!dirty[field]) delete result[field]
  }
  const objects: Record<string, Record<string, string>> = {
    setting: {
      force_format: 'force_format',
      thinking_to_content: 'thinking_to_content',
      proxy: 'proxy',
      pass_through_body_enabled: 'pass_through_body_enabled',
      system_prompt: 'system_prompt',
      system_prompt_override: 'system_prompt_override',
    },
    settings: { is_enterprise_account: 'openrouter_enterprise' },
  }
  for (const field of ['setting', 'settings']) {
    if (dirty[field]) continue
    const raw = result[field]
    if (typeof raw !== 'string') {
      delete result[field]
      continue
    }
    const value = JSON.parse(raw) as Record<string, unknown>
    const patch: Record<string, unknown> = {}
    for (const [key, next] of Object.entries(value)) {
      const source =
        Object.entries(objects[field]).find(([, name]) => name === key)?.[0] ??
        key
      if (dirty[source]) patch[key] = next
    }
    if (Object.keys(patch).length) result[field] = JSON.stringify(patch)
    else delete result[field]
  }
  return result as Partial<T>
}
