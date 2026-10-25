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
