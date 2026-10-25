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
/** Extract supported text chat output without rendering transport frames or hidden reasoning. */
export function chatText(value: unknown, stream: boolean): string {
  if (
    !value ||
    typeof value !== 'object' ||
    !('choices' in value) ||
    !Array.isArray(value.choices)
  ) {
    return ''
  }
  let output = ''
  for (const choice of value.choices) {
    if (!choice || typeof choice !== 'object') continue
    const message = stream ? choice.delta : choice.message
    if (
      message &&
      typeof message === 'object' &&
      'content' in message &&
      typeof message.content === 'string'
    ) {
      output += message.content
    }
  }
  return output
}
export async function consumeChat(
  response: Response,
  stream: boolean,
  onText: (text: string) => void,
  valid: () => boolean
): Promise<void> {
  if (!stream) {
    const result: unknown = await response.json()
    if (valid()) onText(chatText(result, false))
    return
  }
  const reader = response.body?.getReader()
  if (!reader) throw new Error('Gateway body unavailable')
  const decoder = new TextDecoder()
  let pending = ''
  try {
    for (;;) {
      const chunk = await reader.read()
      if (!valid()) {
        await reader.cancel()
        return
      }
      pending += decoder.decode(chunk.value ?? new Uint8Array(), {
        stream: !chunk.done,
      })
      pending = pending.replaceAll('\r\n', '\n')
      let end = pending.indexOf('\n\n')
      while (end >= 0) {
        const frame = pending.slice(0, end)
        pending = pending.slice(end + 2)
        const data = frame
          .split('\n')
          .filter((line) => line.startsWith('data:'))
          .map((line) => line.slice(5).trimStart())
          .join('\n')
        if (data && data !== '[DONE]') {
          const parsed: unknown = JSON.parse(data)
          onText(chatText(parsed, true))
        }
        end = pending.indexOf('\n\n')
      }
      if (chunk.done) break
    }
  } finally {
    reader.releaseLock()
  }
}
