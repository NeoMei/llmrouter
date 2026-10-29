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
import { clearAuthentication, getFreshAuthHeaders } from '@/lib/auth-session'
import { useAuthStore, type AuthUser } from '@/stores/auth-store'

import { UniHubClient, UniHubError, type User, type Account } from './sdk'

export interface ProductConfig {
  native_access?: boolean
  browser_sso_enabled?: boolean
  enabled: boolean
  public_origin: string
  client_id: string
  redirect_uri: string
  product_id: string
  instance_id?: string
  models: string[]
}
export function exactAmount(value: string, asset: string): string {
  if (!/^(0|[1-9][0-9]*)$/.test(value)) {
    throw new TypeError('Invalid canonical quantity')
  }
  const padded = value.padStart(7, '0')
  return `${padded.slice(0, -6)}.${padded.slice(-6)} ${asset.split('/')[0]}`
}
export class FinancialIntent {
  readonly key = crypto.randomUUID()
  readonly account: string
  readonly kind: string
  readonly payload: Readonly<Record<string, string>>
  constructor(account: string, kind: string, payload: Record<string, string>) {
    this.account = account
    this.kind = kind
    this.payload = Object.freeze({ ...payload })
  }
  assertAccount(account: string): void {
    if (this.account !== account) {
      throw new Error(
        'Original payer differs; select the original account to query or retry'
      )
    }
  }
}
export class ProductSession {
  readonly intents = new Map<string, FinancialIntent>()
  user: User | null = null
  accounts: Account[] = []
  account = ''
  epoch = 0
  csrfToken = ''
  restorePromise: Promise<void> | null = null
  readonly client: UniHubClient
  private controller = new AbortController()
  private timer: ReturnType<typeof setTimeout> | undefined
  onClear = () => {}
  constructor(client: UniHubClient) {
    this.client = client
  }
  get signal(): AbortSignal {
    return this.controller.signal
  }
  select(account: string): void {
    this.controller.abort()
    this.controller = new AbortController()
    this.epoch++
    this.account = account
  }
  clear(): void {
    clearTimeout(this.timer)
    this.client.clearAccessToken()
    this.intents.clear()
    this.user = null
    this.accounts = []
    this.select('')
    this.onClear()
  }
  expire(seconds: number): void {
    clearTimeout(this.timer)
    this.timer = setTimeout(() => this.clear(), seconds * 1000)
  }
  async bound<T>(work: () => Promise<T>): Promise<T> {
    const epoch = this.epoch
    try {
      const result = await work()
      if (epoch !== this.epoch) {
        throw new Error('UniHub session or payer changed')
      }
      return result
    } catch (error) {
      if (
        error instanceof UniHubError &&
        error.status === 401 &&
        epoch === this.epoch
      ) {
        this.clear()
      }
      throw error
    }
  }
}
let activeProductSession: ProductSession | null = null

useAuthStore.subscribe((state, previous) => {
  const identityChanged =
    state.auth.user?.id !== previous.auth.user?.id ||
    state.auth.user?.identity_source !== previous.auth.user?.identity_source ||
    state.auth.user?.unihub_user_id !== previous.auth.user?.unihub_user_id
  if (identityChanged && activeProductSession?.user) {
    activeProductSession.clear()
  }
})

export function getActiveProductSession(): ProductSession | null {
  return activeProductSession
}

export function createProductSession(config: ProductConfig): ProductSession {
  const send: typeof fetch = async (input, init) => {
    const epoch = session.epoch
    const projected =
      useAuthStore.getState().auth.user?.identity_source === 'unihub'
    let result: Response
    if (projected) {
      const source = new URL(String(input))
      if (
        source.origin !== new URL(config.public_origin).origin ||
        !source.pathname.startsWith('/v1/')
      ) {
        throw new Error('Cross-origin product request')
      }
      const headers = new Headers(init?.headers)
      const native = await getFreshAuthHeaders()
      headers.set('Authorization', native.Authorization ?? '')
      if (init?.method !== 'GET') {
        headers.set('X-UniHub-CSRF', session.csrfToken)
      }
      result = await fetch(
        `/api/unihub/bff${source.pathname}${source.search}`,
        {
          ...init,
          headers,
          credentials: 'same-origin',
          redirect: 'error',
          signal: session.signal,
        }
      )
    } else {
      result = await fetch(input, { ...init, signal: session.signal })
    }

    if (result.status === 401 && epoch === session.epoch) {
      session.clear()
      if (projected) clearAuthentication()
    }
    return result
  }
  const session = new ProductSession(
    new UniHubClient({
      baseUrl: config.public_origin,
      clientId: config.client_id,
      redirectUri: config.redirect_uri,
      fetch: send,
    })
  )
  activeProductSession = session
  return session
}

export function message(error: unknown): string {
  return error instanceof Error ? error.message : 'UniHub request failed'
}
export function cnyMillionths(value: string): string {
  if (!/^(0|[1-9][0-9]*)(\.[0-9]{1,6})?$/.test(value)) {
    throw new TypeError('Enter a CNY amount with up to six decimal places')
  }
  const [whole, fraction = ''] = value.split('.')
  const amount = (whole + fraction.padEnd(6, '0')).replace(/^0+(?=\d)/, '')
  if (amount === '0') throw new TypeError('Recharge amount must be positive')
  return amount
}

export interface ProductAccessSnapshot {
  access: {
    subject: string
    user: User
    roles: string[]
    permissions: string[]
    expires_at: number
  }
  permissions: NonNullable<
    ReturnType<typeof useAuthStore.getState>['auth']['user']
  >['permissions']
  user?: AuthUser
  csrf_token: string
}
export async function refreshProductAccess(): Promise<ProductAccessSnapshot> {
  const auth = useAuthStore.getState().auth
  if (auth.user?.identity_source !== 'unihub') {
    throw new Error('Product identity proof rejected')
  }
  const sid = auth.session?.sid
  const response = await fetch('/api/unihub/access', {
    headers: await getFreshAuthHeaders(),
    credentials: 'same-origin',
    cache: 'no-store',
    redirect: 'error',
    signal: AbortSignal.timeout(10000),
  })
  if (!response.ok) {
    if (response.status === 401 || response.status === 403) {
      clearAuthentication()
      throw new Error('Product identity proof rejected')
    }
    const current = useAuthStore.getState().auth
    if (current.user?.id === auth.user.id) {
      current.setUser({ ...auth.user, permissions: { admin_permissions: {} } })
    }
    throw new Error('Product identity proof rejected')
  }
  const data = (await response.json()) as ProductAccessSnapshot
  if (
    data.access.subject !== auth.user.unihub_user_id ||
    sid !== useAuthStore.getState().auth.session?.sid
  ) {
    throw new Error('UniHub session or payer changed')
  }
  if (
    data.user &&
    (data.user.id !== auth.user.id ||
      data.user.identity_source !== 'unihub' ||
      data.user.unihub_user_id !== auth.user.unihub_user_id)
  ) {
    clearAuthentication()
    throw new Error('Product identity proof rejected')
  }
  useAuthStore.getState().auth.setUser({
    ...(data.user ?? auth.user),
    permissions: data.permissions,
  })
  return data
}
// Hydrate through the session-authenticated BFF. A restored identity cannot
// silently replace an existing payer or a pending financial intent.
export function restoreProductSession(session: ProductSession): Promise<void> {
  session.restorePromise ??= restoreProductSessionOnce(session).finally(() => {
    session.restorePromise = null
  })
  return session.restorePromise
}
async function restoreProductSessionOnce(
  session: ProductSession
): Promise<void> {
  if (useAuthStore.getState().auth.user?.identity_source !== 'unihub') return
  const epoch = session.epoch
  const data = await refreshProductAccess()
  const accounts = await session.bound(() => session.client.accounts())
  if (epoch !== session.epoch) {
    throw new Error('UniHub session or payer changed')
  }
  session.csrfToken = data.csrf_token
  session.user = data.access.user
  session.accounts = accounts
  if (!session.account && session.intents.size === 0) {
    const personal = accounts
      .filter((account) => account.kind === 'personal')
      .sort((a, b) => a.id.localeCompare(b.id))[0]
    if (personal) session.select(personal.id)
  }
}
