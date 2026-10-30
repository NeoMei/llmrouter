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
import i18next from 'i18next'

import type { ApiResponse } from '@/features/auth/types'
import { api, get2FAStatus } from '@/lib/api'
import {
  buildAssertionResult,
  prepareCredentialRequestOptions,
  isPasskeySupported as detectPasskeySupport,
} from '@/lib/passkey'
import { useAuthStore } from '@/stores/auth-store'

import {
  beginPasskeyVerification,
  finishPasskeyVerification,
  getPasskeyStatus,
} from '../passkey'
import type {
  SecurityProof,
  SecurityProofScope,
  VerificationMethod,
  VerificationMethods,
} from './types'

/**
 * Fetch available verification methods for the current user.
 */
export async function checkVerificationMethods(): Promise<VerificationMethods> {
  const realm = useAuthStore.getState().auth.session?.authentication_realm
  if (realm !== 'CENTRAL' && realm !== 'LOCAL_ONLY' && realm !== 'LEGACY') {
    return {
      has2FA: false,
      hasPasskey: false,
      passkeySupported: false,
      twoFAUnavailable: true,
      passkeyUnavailable: true,
    }
  }
  const central = realm === 'CENTRAL'
  // Central verification uses only its own Passkey; its 2FA belongs to the
  // central authority and cannot be used for local security proofs.
  const [twoFA, passkey, support] = await Promise.allSettled([
    central ? Promise.resolve(null) : get2FAStatus(),
    getPasskeyStatus(),
    detectPasskeySupport(),
  ])
  const twoFAResponse = twoFA.status === 'fulfilled' ? twoFA.value : null
  const passkeyResponse = passkey.status === 'fulfilled' ? passkey.value : null
  return {
    has2FA: Boolean(twoFAResponse?.success && twoFAResponse.data?.enabled),
    hasPasskey: Boolean(
      passkeyResponse?.success && passkeyResponse.data?.enabled
    ),
    passkeySupported: support.status === 'fulfilled' && support.value,
    twoFAUnavailable: !central && (!twoFAResponse || !twoFAResponse.success),
    passkeyUnavailable: !passkeyResponse || !passkeyResponse.success,
  }
}

/**
 * Execute a verification flow based on the method type.
 */
export async function verify(
  method: VerificationMethod,
  scope: SecurityProofScope,
  code?: string
): Promise<SecurityProof> {
  switch (method) {
    case '2fa':
      return verifyTwoFA(scope, code)
    case 'passkey':
      return verifyPasskey(scope)
    default:
      throw new Error(
        i18next.t('Unsupported verification method: {{method}}', { method })
      )
  }
}

/**
 * Perform 2FA verification flow.
 */
async function verifyTwoFA(
  scope: SecurityProofScope,
  code?: string | null
): Promise<SecurityProof> {
  const trimmed = code?.trim()
  if (!trimmed) {
    throw new Error(
      i18next.t('Please enter the verification code or backup code')
    )
  }

  const res = await api.post<ApiResponse<SecurityProof>>('/api/verify', {
    method: '2fa',
    code: trimmed,
    scope,
  })

  if (!res.data?.success) {
    throw new Error(res.data?.message || i18next.t('Verification failed'))
  }
  if (!res.data.data?.proof_token) {
    throw new Error(i18next.t('Verification proof was not returned'))
  }
  return res.data.data
}

/**
 * Perform Passkey verification flow.
 */
async function verifyPasskey(
  scope: SecurityProofScope
): Promise<SecurityProof> {
  if (typeof navigator === 'undefined' || !navigator.credentials) {
    throw new Error(
      i18next.t('Passkey verification is not supported in this environment')
    )
  }

  try {
    const beginResponse = await beginPasskeyVerification(scope)
    if (!beginResponse.success) {
      throw new Error(
        beginResponse.message || i18next.t('Failed to start verification')
      )
    }

    const publicKey = prepareCredentialRequestOptions(
      beginResponse.data?.options ?? beginResponse.data
    )
    const flowToken = beginResponse.data?.flow_token
    if (!flowToken) {
      throw new Error(i18next.t('Verification flow expired'))
    }

    const credential = (await navigator.credentials.get({
      publicKey,
    })) as PublicKeyCredential | null

    if (!credential) {
      throw new Error(i18next.t('Passkey verification was cancelled'))
    }

    const assertion = buildAssertionResult(credential)
    if (!assertion) {
      throw new Error(i18next.t('Unable to build Passkey assertion'))
    }

    const finishResponse = await finishPasskeyVerification(flowToken, assertion)
    if (!finishResponse.success) {
      throw new Error(
        finishResponse.message || i18next.t('Passkey verification failed')
      )
    }

    if (!finishResponse.data?.proof_token) {
      throw new Error(i18next.t('Verification proof was not returned'))
    }
    return finishResponse.data
  } catch (error: unknown) {
    if (error instanceof DOMException && error.name === 'NotAllowedError') {
      throw new Error(
        i18next.t('Passkey verification was cancelled or timed out'),
        { cause: error }
      )
    }
    if (error instanceof DOMException && error.name === 'InvalidStateError') {
      throw new Error(
        i18next.t('Passkey verification is not available in the current state'),
        { cause: error }
      )
    }
    throw error
  }
}
