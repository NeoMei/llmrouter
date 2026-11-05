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

import {
  clearAuthentication,
  getAuthEpoch,
  getAuthGeneration,
  getFreshAuthHeaders,
} from '@/lib/auth-session'
import { useAuthStore } from '@/stores/auth-store'

const payerID = /^[A-Za-z0-9_-]{1,128}$/
const accountSchema = z.array(
  z.object({ id: z.string(), name: z.string(), role: z.string() })
)
const walletSchema = z.array(
  z.object({
    product_id: z.string(),
    asset: z.string(),
    amount: z.string().regex(/^(0|[1-9][0-9]*)$/),
  })
)

/** Public destination hints never grant authority or carry a user credential. */
export function centralWalletURL(
  origin: string,
  account: string,
  wallet: 'unified' | 'llmrouter'
): string {
  const url = new URL(origin)
  const loopback = ['localhost', '127.0.0.1', '[::1]'].includes(url.hostname)
  if (
    (url.protocol !== 'https:' && !(loopback && url.protocol === 'http:')) ||
    url.username ||
    url.password ||
    url.pathname !== '/' ||
    url.search ||
    url.hash ||
    (origin !== url.origin && origin !== `${url.origin}/`) ||
    !payerID.test(account)
  ) {
    throw new Error('Invalid central wallet destination')
  }
  url.searchParams.set('view', 'wallets')
  url.searchParams.set('account', account)
  url.searchParams.set(wallet === 'unified' ? 'wallet' : 'product', wallet)
  return url.toString()
}

export function useCentralCashWallet(props: {
  identity: string
  account?: string
  ready: boolean
  revision: number
}) {
  const centralSession = useAuthStore(
    (state) =>
      state.auth.user?.identity_source === 'unihub' &&
      (state.auth.session?.login_method === 'unihub' ||
        state.auth.session?.login_method === 'product_broker')
  )
  const eligible =
    centralSession && !!props.account && payerID.test(props.account)
  const query = useQuery({
    queryKey: [
      'central-cash-wallet',
      props.identity,
      props.account,
      props.revision,
    ],
    enabled: eligible && props.ready,
    retry: false,
    gcTime: 0,
    staleTime: 0,
    queryFn: async ({ signal }) => {
      const generation = getAuthGeneration()
      const previous = useAuthStore.getState().auth
      const headers = await getFreshAuthHeaders()
      const current = useAuthStore.getState().auth
      if (
        signal.aborted ||
        generation !== getAuthGeneration() ||
        previous.user?.id !== current.user?.id ||
        previous.user?.unihub_user_id !== current.user?.unihub_user_id ||
        previous.session?.sid !== current.session?.sid ||
        previous.session?.login_method !== current.session?.login_method ||
        previous.user?.role !== current.user?.role ||
        previous.user?.authentication_realm !==
          current.user?.authentication_realm ||
        previous.session?.authentication_realm !==
          current.session?.authentication_realm ||
        current.user?.identity_source !== 'unihub' ||
        (current.session?.login_method !== 'unihub' &&
          current.session?.login_method !== 'product_broker')
      ) {
        throw new Error('Wallet identity changed')
      }
      // Same-session refresh rotates the epoch without changing the payer identity.
      const epoch = getAuthEpoch()
      const read = async (
        path: string,
        authenticated = true
      ): Promise<unknown> => {
        if (signal.aborted || epoch !== getAuthEpoch()) {
          throw new Error('Wallet identity changed')
        }
        const response = await fetch(path, {
          headers: authenticated ? headers : undefined,
          credentials: authenticated ? 'same-origin' : 'omit',
          cache: 'no-store',
          redirect: 'error',
          signal,
        })
        if (signal.aborted || epoch !== getAuthEpoch()) {
          throw new Error('Wallet identity changed')
        }
        if (response.status === 401 && authenticated) clearAuthentication()
        if (!response.ok) throw new Error('Central wallet unavailable')
        const result: unknown = await response.json()
        if (signal.aborted || epoch !== getAuthEpoch()) {
          throw new Error('Wallet identity changed')
        }
        return result
      }
      const [rawConfig, rawAccounts] = await Promise.all([
        read('/api/unihub/config', false),
        read('/api/unihub/bff/v1/accounts'),
      ])
      const config = z
        .object({
          enabled: z.literal(true),
          public_origin: z.string(),
          product_id: z.literal('llmrouter'),
        })
        .parse(rawConfig)
      const matches = accountSchema
        .parse(rawAccounts)
        .filter((account) => account.id === props.account)
      if (
        matches.length !== 1 ||
        !['owner', 'billing_admin'].includes(matches[0].role)
      ) {
        throw new Error('Funding payer unavailable')
      }
      const account = matches[0]
      const rechargeURL = centralWalletURL(
        config.public_origin,
        account.id,
        'unified'
      )
      const productURL = centralWalletURL(
        config.public_origin,
        account.id,
        'llmrouter'
      )
      const wallets = walletSchema.parse(
        await read(
          `/api/unihub/bff/v1/accounts/${encodeURIComponent(account.id)}/wallets`
        )
      )
      const product = wallets.filter(
        (wallet) =>
          wallet.product_id === 'llmrouter' && wallet.asset === 'CNY/6'
      )
      if (product.length !== 1) throw new Error('Managed wallet unavailable')
      return { account, product: product[0], rechargeURL, productURL }
    },
  })
  const data =
    eligible && props.ready && query.isSuccess && !query.isFetching
      ? query.data
      : undefined
  return {
    data,
    loading: !props.ready || (eligible && query.isFetching),
    canRetry: eligible && props.ready,
    retry: query.refetch,
  }
}
