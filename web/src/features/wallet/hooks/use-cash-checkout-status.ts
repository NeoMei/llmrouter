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
import { useQuery, useQueryClient } from '@tanstack/react-query'
import { useEffect, useRef, useState } from 'react'

import { getFreshAuthHeaders, refreshAuthentication } from '@/lib/auth-session'

import { cashPaymentPhase, readCashCheckout } from '../lib/cash-checkout-status'
import { CashCheckoutError, type CashCheckout } from '../lib/cash-topup'

export function useCashCheckoutStatus(props: {
  payerIdentity: string
  account: string
  id: string
  checkout: CashCheckout | null
  eligible: boolean
  assertIdentity: () => boolean
  onPaid: () => Promise<void>
}) {
  const queryClient = useQueryClient()
  const queryKey = [
    'cash-checkout',
    props.payerIdentity,
    props.account,
    props.id,
  ]
  const notified = useRef('')
  const activeCheck = useRef<AbortController | null>(null)
  const [checkState, setCheckState] = useState<{
    id: string
    status: 'checking' | 'failed'
  }>()
  const checking =
    checkState?.id === props.id && checkState.status === 'checking'
  const deps = {
    fetch: (...args: Parameters<typeof fetch>) => globalThis.fetch(...args),
    getAuthHeaders: getFreshAuthHeaders,
    assertIdentity: props.assertIdentity,
  }
  const query = useQuery({
    queryKey,
    enabled: props.eligible && !!props.id,
    gcTime: 0,
    staleTime: 0,
    retry: false,
    queryFn: ({ signal }) =>
      readCashCheckout(deps, props.account, props.id, signal).catch(
        async (cause: unknown) => {
          if (
            !signal.aborted &&
            cause instanceof CashCheckoutError &&
            cause.code === 'auth_retry'
          ) {
            await refreshAuthentication()
            return readCashCheckout(deps, props.account, props.id, signal)
          }
          throw cause
        }
      ),
    refetchInterval: (q) =>
      !checking &&
      ['waiting', 'processing'].includes(cashPaymentPhase(q.state.data))
        ? 2000
        : false,
    refetchOnWindowFocus: !checking,
    refetchOnReconnect: !checking,
  })
  const data =
    query.data ??
    (props.checkout?.id === props.id
      ? {
          ...props.checkout,
          credited: props.checkout.credit_status === 'APPLIED',
        }
      : undefined)
  const phase = cashPaymentPhase(data)

  useEffect(() => {
    return () => {
      activeCheck.current?.abort()
      activeCheck.current = null
    }
  }, [props.payerIdentity, props.account, props.id])

  const reconcile = async () => {
    if (
      activeCheck.current ||
      !props.eligible ||
      !props.id ||
      !props.assertIdentity()
    ) {
      return
    }
    const check = new AbortController()
    activeCheck.current = check
    setCheckState({ id: props.id, status: 'checking' })
    let failed = false
    try {
      // A GET started before reconciliation must not replace its result.
      await queryClient.cancelQueries({ queryKey, exact: true })
      const result = await readCashCheckout(
        deps,
        props.account,
        props.id,
        check.signal,
        true
      )
      if (!check.signal.aborted && props.assertIdentity()) {
        queryClient.setQueryData(queryKey, result)
      }
    } catch {
      failed = true
    } finally {
      if (activeCheck.current === check) {
        activeCheck.current = null
        setCheckState(failed ? { id: props.id, status: 'failed' } : undefined)
      }
    }
  }

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
  return {
    data,
    phase,
    checking,
    checkError:
      !checking &&
      (query.isError ||
        (checkState?.id === props.id && checkState.status === 'failed')),
    reconcile,
  }
}
