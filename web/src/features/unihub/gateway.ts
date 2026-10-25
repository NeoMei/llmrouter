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
