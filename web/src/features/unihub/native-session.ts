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
import { t } from 'i18next'

import { applyAuthBundle, getAuthEpoch, isAuthBundle } from '@/lib/auth-session'
import { useAuthStore, type AuthBundle } from '@/stores/auth-store'

import type { Account, Registration, User } from './sdk'
import type { ProductSession } from './session'

export async function revokeRejectedSession(bundle: AuthBundle): Promise<void> {
  const response = await fetch('/api/user/auth/logout', {
    method: 'POST',
    headers: {
      Authorization: `Bearer ${bundle.access_token}`,
      'X-Auth-Session': bundle.session.sid,
    },
    credentials: 'omit',
    redirect: 'error',
    signal: AbortSignal.timeout(5000),
  })
  if (!response.ok) throw new Error(t('Request failed'))
  const result = (await response.json()) as { success?: boolean }
  if (!result.success) throw new Error(t('Request failed'))
}

export async function authorizeProductSession(
  session: ProductSession,
  body: Registration
): Promise<{ user: User; accounts: Account[] }> {
  const authEpoch = getAuthEpoch()
  const originalSID = useAuthStore.getState().auth.session?.sid
  let bundle: AuthBundle | null = null
  try {
    const result = await session.bound(async () => {
      const tokens = await session.client.passwordLogin(body)
      session.expire(tokens.expires_in)
      const proofResponse = await fetch('/api/unihub/identity', {
        method: 'POST',
        headers: { Authorization: `Bearer ${tokens.access_token}` },
        credentials: 'omit',
        redirect: 'error',
        signal: session.signal,
      })
      if (!proofResponse.ok) {
        throw new Error(t('Product identity proof rejected'))
      }
      const proof = (await proofResponse.json()) as {
        user: User
        accounts: Account[]
      }
      const response = await fetch('/api/unihub/session', {
        method: 'POST',
        headers: { Authorization: `Bearer ${tokens.access_token}` },
        credentials: 'include',
        redirect: 'error',
        signal: session.signal,
      })
      if (!response.ok) throw new Error(t('Product identity proof rejected'))
      const payload = (await response.json()) as {
        success?: boolean
        data?: unknown
      }
      if (isAuthBundle(payload.data)) bundle = payload.data
      if (
        !payload.success ||
        !bundle ||
        bundle.user.identity_source !== 'unihub' ||
        bundle.user.unihub_user_id !== proof.user.id
      ) {
        throw new Error(t('Product identity proof rejected'))
      }
      return { proof, bundle }
    })
    if (
      authEpoch !== getAuthEpoch() ||
      originalSID !== useAuthStore.getState().auth.session?.sid
    ) {
      throw new Error(t('Product identity proof rejected'))
    }
    applyAuthBundle(result.bundle)
    bundle = null
    return result.proof
  } catch (error) {
    if (bundle) {
      try {
        await revokeRejectedSession(bundle)
      } catch {
        throw new Error(
          t('Session cleanup failed. Please sign out before retrying.')
        )
      }
    }
    throw error
  }
}
