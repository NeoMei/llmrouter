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
import { UniHubError } from './sdk'

/** Only service responses that establish no unresolved creation unlock a new intent. */
export function registrationDefinitivelyRejected(error: unknown): boolean {
  if (!(error instanceof UniHubError)) return false
  if (error.status === 400) {
    return ['invalid_request', 'invalid_password'].includes(error.code)
  }
  if (error.status === 403) {
    return ['client_not_enabled', 'origin_not_allowed'].includes(error.code)
  }
  return (
    error.status === 409 &&
    ['registration_conflict', 'interaction_required'].includes(error.code)
  )
}

/** A throttle response alone never resolves an earlier uncertain creation. */
export function registrationThrottle(
  error: unknown,
  now = Date.now()
): { notStarted: boolean; retryAfterMs: number } | null {
  if (
    !(error instanceof UniHubError) ||
    error.status !== 429 ||
    error.code !== 'rate_limited'
  ) {
    return null
  }
  const body = error.body && typeof error.body === 'object' ? error.body : {}
  const header = error.headers.get('Retry-After')?.trim() ?? ''
  let delay = Number.NaN
  if (/^\d+$/.test(header)) delay = Number(header) * 1000
  else if (
    /^[A-Za-z]{3}, \d{2} [A-Za-z]{3} \d{4} \d{2}:\d{2}:\d{2} GMT$/.test(header)
  ) {
    delay = Date.parse(header) - now
  }
  if (!Number.isSafeInteger(delay) || delay < 0) {
    delay =
      'retry_after' in body &&
      typeof body.retry_after === 'number' &&
      Number.isSafeInteger(body.retry_after) &&
      body.retry_after >= 0
        ? body.retry_after * 1000
        : 60000
  }
  if (!Number.isSafeInteger(delay) || !Number.isSafeInteger(now + delay)) {
    delay = 60000
  }
  return {
    notStarted:
      'registration_not_started' in body &&
      body.registration_not_started === true,
    retryAfterMs: Math.max(1000, delay),
  }
}
