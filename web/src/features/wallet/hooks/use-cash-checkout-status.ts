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
import { useEffect, useRef } from 'react'

import { getFreshAuthHeaders, refreshAuthentication } from '@/lib/auth-session'

import { cashPaymentPhase, readCashCheckout } from '../lib/cash-checkout-status'
import { CashCheckoutError } from '../lib/cash-topup'

export function useCashCheckoutStatus(props: {
  account: string
  id: string
  eligible: boolean
  assertIdentity: () => boolean
  onPaid: () => Promise<void>
}) {
  const notified = useRef('')
  const query = useQuery({
    queryKey: ['cash-checkout', props.account, props.id],
    enabled: props.eligible && !!props.id,
    gcTime: 0,
    staleTime: 0,
    retry: false,
    queryFn: ({ signal }) =>
      readCashCheckout(
        {
          fetch: (...args) => globalThis.fetch(...args),
          getAuthHeaders: getFreshAuthHeaders,
          assertIdentity: props.assertIdentity,
        },
        props.account,
        props.id,
        signal
      ).catch(async (cause: unknown) => {
        if (
          !signal.aborted &&
          cause instanceof CashCheckoutError &&
          cause.code === 'auth_retry'
        ) {
          await refreshAuthentication()
          return readCashCheckout(
            {
              fetch: (...args) => globalThis.fetch(...args),
              getAuthHeaders: getFreshAuthHeaders,
              assertIdentity: props.assertIdentity,
            },
            props.account,
            props.id,
            signal
          )
        }
        throw cause
      }),
    refetchInterval: (q) =>
      ['waiting', 'processing'].includes(cashPaymentPhase(q.state.data))
        ? 2000
        : false,
    refetchOnWindowFocus: true,
  })
  const phase = cashPaymentPhase(query.data)
  useEffect(() => {
    const key = `${props.account}:${props.id}:${phase}`
    if (
      ['success', 'processing'].includes(phase) &&
      notified.current !== key &&
      props.assertIdentity()
    ) {
      notified.current = key
      void props.onPaid()
    }
  }, [phase, props])
  return { ...query, phase }
}
