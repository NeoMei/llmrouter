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
