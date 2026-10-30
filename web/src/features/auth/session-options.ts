import { z } from 'zod'

import { api } from '@/lib/api'

const sessionOptionSchema = z.object({
  sid: z.string().regex(/^[A-Za-z0-9_-]{1,128}$/),
  realm: z.enum(['native', 'central', 'legacy']),
  display_name: z.string().max(256),
})
const sessionOptionsResponse = z.object({
  success: z.literal(true),
  data: z.object({
    sessions: z.array(sessionOptionSchema).max(2),
    unavailable: z.boolean(),
  }),
})

export type SessionOption = z.infer<typeof sessionOptionSchema>
export type SessionOptions = z.infer<typeof sessionOptionsResponse>['data']

export function parseSessionOptions(value: unknown): SessionOptions {
  const result = sessionOptionsResponse.parse(value).data
  if (
    new Set(result.sessions.map((session) => session.sid)).size !==
    result.sessions.length
  ) {
    throw new Error('Duplicate session options')
  }
  return result
}

export async function fetchSessionOptions(): Promise<SessionOptions> {
  const response = await api.get('/api/user/auth/session-options', {
    skipAuthRefresh: true,
    skipErrorHandler: true,
  })
  return parseSessionOptions(response.data)
}
