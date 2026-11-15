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
import { UniHubError } from '@/features/unihub/sdk'
import type { ProductConfig } from '@/features/unihub/session'

export type SignInConfig = Pick<
  ProductConfig,
  'native_access' | 'enabled' | 'browser_sso_enabled'
>

/**
 * A public sign-in page whose primary form uses the UniHub password authority
 * still offers the local operator page as a secondary entry when native access
 * is enabled. UniHub remains the primary path; this is a deliberate secondary
 * link, not a second competing primary form.
 */
export function shouldShowLocalOperatorSignIn(
  config: SignInConfig | null | undefined
): boolean {
  return Boolean(config?.native_access)
}

/**
 * Public sign-up must land in the same identity realm as the default sign-in.
 * When the product integration (or browser SSO) is active, create the account
 * through the UniHub/Casdoor password authority unless the visitor explicitly
 * requested the native realm with ?native=1.
 */
export function shouldUseCentralSignUp(
  config: SignInConfig | null | undefined,
  nativeRequested: boolean
): boolean {
  if (nativeRequested) return false
  return Boolean(config?.enabled || config?.browser_sso_enabled)
}

/**
 * The compatibility bridge for accounts created before realm alignment: a
 * valid local (bcrypt) password has no central password credential, so the
 * central authority rejects it with 401 invalid_credentials. Only that precise
 * response is safe to retry against /api/user/login. Any other UniHub failure
 * (throttle, MFA, interaction_required, disabled client, ...) must keep its
 * own error handling and never silently downgrade to the local realm.
 */
export function shouldFallbackToNativeLogin(error: unknown): boolean {
  return (
    error instanceof UniHubError &&
    error.status === 401 &&
    error.code === 'invalid_credentials'
  )
}
