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
import { useQuery } from '@tanstack/react-query'
import { z } from 'zod'

import { getFreshAuthHeaders } from '@/lib/auth-session'
import { useAuthStore } from '@/stores/auth-store'

const cashTopupAccessSchema = z.object({
  finance_access: z
    .object({
      available: z.literal(true),
      cash_topup_available: z.literal(true).optional(),
      transport: z.string().optional(),
      central_user_id: z.string().optional(),
      expires_at: z.number().optional(),
    })
    .optional(),
})

/**
 * Cash topup eligibility comes from the backend finance grant, not from the
 * browser login method. A normal password/LEGACY LLMRouter session is a valid
 * payer; UniHub is backend support and is never a user step.
 */
export function useCashTopupEligibility(props: {
  identity: string
  account?: string
  ready: boolean
  revision: number
}): boolean {
  const unihubUser = useAuthStore(
    (state) => state.auth.user?.identity_source === 'unihub'
  )
  const accountPattern = /^[A-Za-z0-9_-]{1,128}$/
  const eligibleIdentity =
    unihubUser && !!props.account && accountPattern.test(props.account)
  const query = useQuery({
    queryKey: ['cash-topup-eligibility', props.identity, props.revision],
    enabled: eligibleIdentity && props.ready,
    retry: false,
    gcTime: 0,
    staleTime: 0,
    queryFn: async () => {
      const response = await fetch('/api/unihub/access', {
        headers: await getFreshAuthHeaders(),
        credentials: 'same-origin',
        cache: 'no-store',
        redirect: 'error',
      })
      if (!response.ok) throw new Error('Finance access unavailable')
      const data = cashTopupAccessSchema.parse(await response.json())
      const grant = data.finance_access
      if (!grant) return false
      const auth = useAuthStore.getState().auth
      if (
        grant.central_user_id &&
        grant.central_user_id !== auth.user?.unihub_user_id
      ) {
        return false
      }
      if (
        typeof grant.expires_at === 'number' &&
        grant.expires_at <= Date.now() / 1000
      ) {
        return false
      }
      // OIDC sessions predate the explicit flag; runtime-actor sessions must
      // carry it and an unexpired central identity to expose the topup card.
      if (grant.transport === 'oidc') return true
      return (
        grant.transport === 'runtime_actor' &&
        grant.cash_topup_available === true
      )
    },
  })
  return (
    eligibleIdentity && props.ready && query.isSuccess && query.data === true
  )
}
