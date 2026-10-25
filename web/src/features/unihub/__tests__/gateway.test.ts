import assert from 'node:assert/strict'
import { test } from 'node:test'

import { consumeChat } from '../gateway'

test('ordinary chat displays assistant text and omits protocol metadata', async () => {
  let output = ''
  await consumeChat(
    new Response(
      JSON.stringify({
        choices: [
          { message: { content: 'ordinary text', reasoning: 'private' } },
        ],
        model: 'internal',
      })
    ),
    false,
    (text) => {
      output += text
    },
    () => true
  )
  assert.equal(output, 'ordinary text')
})
test('stream chunks split inside SSE frame combine exact assistant text', async () => {
  const chunks = [
    'data: {"choices":[{"delta":{"con',
    'tent":"你"}}]}\n\ndata: {"choices":[{"delta":{"content":"好"}}]}\n\n',
    'data: [DONE]\n\n',
  ]
  const body = new ReadableStream({
    start(controller) {
      for (const value of chunks) {
        controller.enqueue(new TextEncoder().encode(value))
      }
      controller.close()
    },
  })
  let output = ''
  await consumeChat(
    new Response(body),
    true,
    (text) => {
      output += text
    },
    () => true
  )
  assert.equal(output, '你好')
})
test('account switch discards late streamed output', async () => {
  let output = ''
  await consumeChat(
    new Response('data: {"choices":[{"delta":{"content":"old payer"}}]}\n\n'),
    true,
    (text) => {
      output += text
    },
    () => false
  )
  assert.equal(output, '')
})
