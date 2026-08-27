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
export type AccentTone = 'emerald' | 'amber' | 'blue' | 'violet'

export interface ApiDemoConfig {
  id: string
  label: string
  method: 'POST' | 'GET'
  endpoint: string
  headers: string[]
  request: string[]
  response: string[]
  responseHighlights: string[]
  tokens: number
  latency: number
  accent: AccentTone
}

export const FEATURED_DOMESTIC_MODELS = [
  'DeepSeek',
  'GLM',
  'Kimi',
  'Qwen',
  'Doubao',
  'MiniMax',
] as const

export const HOME_STATS = [
  { end: 20, suffix: '+', labelKey: 'domestic models available' },
  { end: 6, suffix: '+', labelKey: 'domestic model brands' },
  { end: 3, suffix: '', labelKey: 'coding clients supported' },
  { end: 1, suffix: '', labelKey: 'unified API key' },
] as const

const CHAT_RESPONSE = [
  '{',
  '  "choices": [{ "message": { "content": <text> } }],',
  '  "usage": { "total_tokens": <tokens> }',
  '}',
]

function createChatDemo(
  id: string,
  label: string,
  model: string,
  accent: AccentTone,
  tokens: number,
  latency: number
): ApiDemoConfig {
  return {
    id,
    label,
    method: 'POST',
    endpoint: '/v1/chat/completions',
    headers: ['"Authorization: Bearer sk-••••"'],
    request: [
      `"model": "${model}",`,
      '"messages": [',
      '  { "role": "user", "content": "..." }',
      ']',
    ],
    response: CHAT_RESPONSE,
    responseHighlights: ['<text>', '<tokens>'],
    tokens,
    latency,
    accent,
  }
}

export const HOME_API_DEMOS = [
  createChatDemo('deepseek', 'DeepSeek', 'deepseek-chat', 'emerald', 27, 142),
  createChatDemo('glm', 'GLM', 'glm-5', 'amber', 31, 168),
  createChatDemo('kimi', 'Kimi', 'kimi-k2.7-code', 'blue', 29, 156),
  createChatDemo('qwen', 'Qwen', 'Qwen3.7-Max-A', 'violet', 25, 93),
] as const satisfies readonly ApiDemoConfig[]
