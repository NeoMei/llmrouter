import { UniHubError } from './sdk'

/** Only service responses that establish no unresolved creation unlock a new intent. */
export function registrationDefinitivelyRejected(error: unknown): boolean {
  if (!(error instanceof UniHubError)) return false
  if (error.status === 400)
    return ['invalid_request', 'invalid_password'].includes(error.code)
  if (error.status === 403)
    return ['client_not_enabled', 'origin_not_allowed'].includes(error.code)
  return (
    error.status === 409 &&
    ['registration_conflict', 'interaction_required'].includes(error.code)
  )
}
