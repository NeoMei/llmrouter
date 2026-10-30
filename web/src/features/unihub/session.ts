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
  financeWriteScopes: string[] = []
  financeExpiresAt = 0
  financeTransport: '' | 'oidc' | 'runtime_actor' = ''
  restorePromise: Promise<void> | null = null
  readonly client: UniHubClient
  private controller = new AbortController()
  private timer: ReturnType<typeof setTimeout> | undefined
  onClear = () => {}
  onForbidden = (_reason: string) => {}
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
    this.csrfToken = ''
    this.financeExpiresAt = 0
    this.financeTransport = ''
    this.financeWriteScopes = []
    this.restorePromise = null
    this.select('')
    this.onClear()
  }
  expire(seconds: number): void {
    clearTimeout(this.timer)
    this.timer = setTimeout(() => this.clear(), seconds * 1000)
  }
  get canWrite(): boolean {
    return (
      this.financeTransport === 'oidc' &&
      useAuthStore.getState().auth.session?.login_method === 'unihub' &&
      (this.financeExpiresAt === 0 || this.financeExpiresAt > Date.now() / 1000)
    )
  }
  canFinanceWrite(scope: string, account = this.account): boolean {
    const selected = this.accounts.find((item) => item.id === account)
    if (!selected || account !== this.account) return false
    if (
      scope !== 'model-credentials:write' &&
      !['owner', 'billing_admin'].includes(selected.role)
    ) {
      return false
    }
    return (
      this.canWrite ||
      (this.financeTransport === 'runtime_actor' &&
        this.financeExpiresAt > Date.now() / 1000 &&
        this.financeWriteScopes.includes(scope))
    )
  }
  canFinanceRequest(method: string, path: string): boolean {
    const match = /^\/v1\/accounts\/([A-Za-z0-9_-]+)\/(.+)$/.exec(path)
    if (!match) return false
    const [, account, resource] = match
    let scope = ''
    if (
      (method === 'POST' && resource === 'api-keys') ||
      (method === 'DELETE' && /^api-keys\/[A-Za-z0-9_-]+$/.test(resource))
    ) {
      scope = 'model-credentials:write'
    }
    if (method === 'PUT' && resource === 'usage-budget') {
      scope = 'settings:write'
    }
    if (
      method === 'POST' &&
      (resource === 'recharges' ||
        /^recharges\/[A-Za-z0-9_-]+\/sandbox-settle$/.test(resource))
    ) {
      scope = 'recharges:write'
    }
    return scope !== '' && this.canFinanceWrite(scope, account)
  }
  acceptFinanceAccess(data: ProductAccessSnapshot): void {
    const auth = useAuthStore.getState().auth
    const proof = data.finance_access
    if (!proof && auth.session?.login_method === 'unihub') {
      // Older deployments already validate the OIDC-bound session on every BFF request.
      this.financeWriteScopes = []
      this.financeTransport = 'oidc'
      this.financeExpiresAt = 0
      return
    }
    const now = Date.now() / 1000
    if (
      !proof ||
      proof.available !== true ||
      (proof.transport !== 'runtime_actor' && proof.transport !== 'oidc') ||
      (proof.transport === 'oidc' && auth.session?.login_method !== 'unihub') ||
      proof.central_user_id !== auth.user?.unihub_user_id ||
      proof.central_user_id !== data.access.subject ||
      proof.central_user_id !== data.access.user.id ||
      typeof proof.expires_at !== 'number' ||
      !Number.isFinite(proof.expires_at) ||
      proof.expires_at <= now
    ) {
      this.clear()
      throw new Error(
        'UniHub account access is unavailable for this browser session. Contact the administrator.'
      )
    }
    this.financeWriteScopes = Array.isArray(proof.write_scopes)
      ? proof.write_scopes.filter((scope) =>
          [
            'model-credentials:write',
            'settings:write',
            'recharges:write',
          ].includes(scope)
        )
      : []
    this.financeTransport = proof.transport
    this.financeExpiresAt = proof.expires_at
    clearTimeout(this.timer)
    this.timer = setTimeout(
      () => {
        this.intents.clear()
        this.csrfToken = ''
        this.financeExpiresAt = 0
        this.financeTransport = ''
        this.financeWriteScopes = []
        this.accounts = []
        this.select('')
        this.onForbidden(
          'UniHub account access has expired. Refresh accounts and roles.'
        )
      },
      Math.max(0, (proof.expires_at - now) * 1000)
    )
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
        error.status === 403 &&
        epoch === this.epoch
      ) {
        this.intents.clear()
        this.accounts = []
        this.csrfToken = ''
        this.select('')
        this.onForbidden(error.message)
      }
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
let activeProductConfig = ''

// These claims are used only to invalidate client state, never to authorize a
// request. The server verifies each token and all financial permissions.
function tokenGeneration(token: string | null): string {
  if (!token) return ''
  try {
    const payload = token.split('.')[1]
    const claims = JSON.parse(
      atob(payload.replaceAll('-', '+').replaceAll('_', '/'))
    ) as {
      sv?: unknown
      uv?: unknown
    }
    if (Number.isSafeInteger(claims.sv) && Number.isSafeInteger(claims.uv)) {
      return `${claims.sv}:${claims.uv}`
    }
  } catch {
    // Unknown token formats are conservatively invalidated on any rotation.
  }
  return token
}

export function productAuthorityKey(
  auth: ReturnType<typeof useAuthStore.getState>['auth']
): string {
  return JSON.stringify([
    auth.user?.id,
    auth.user?.identity_source,
    auth.user?.unihub_user_id,
    auth.session?.sid,
    auth.session?.login_method,
    tokenGeneration(auth.accessToken),
  ])
}

function productConfigKey(config: ProductConfig): string {
  return JSON.stringify([
    config.public_origin,
    config.client_id,
    config.product_id,
    config.instance_id ?? 'default',
  ])
}

useAuthStore.subscribe((state, previous) => {
  if (productAuthorityKey(state.auth) !== productAuthorityKey(previous.auth)) {
    activeProductSession?.clear()
  }
})

export function getActiveProductSession(
  config?: ProductConfig
): ProductSession | null {
  if (config && activeProductConfig !== productConfigKey(config)) {
    activeProductSession?.clear()
    activeProductSession = null
  }
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
      const method = (init?.method ?? 'GET').toUpperCase()
      if (
        !session.canWrite &&
        method !== 'GET' &&
        !session.canFinanceRequest(method, source.pathname)
      ) {
        throw new Error(
          'UniHub account access is read only for this browser session.'
        )
      }
      if (!session.canWrite && session.financeExpiresAt <= Date.now() / 1000) {
        throw new Error(
          'UniHub account access is unavailable for this browser session. Contact the administrator.'
        )
      }
      const native = await getFreshAuthHeaders()
      if (epoch !== session.epoch) {
        throw new Error('UniHub session or payer changed')
      }
      if (
        !session.canWrite &&
        method !== 'GET' &&
        !session.canFinanceRequest(method, source.pathname)
      ) {
        throw new Error(
          'UniHub account access is read only for this browser session.'
        )
      }
      headers.set('Authorization', native.Authorization ?? '')
      if (method !== 'GET') {
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
  activeProductConfig = productConfigKey(config)
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
  finance_access?: {
    available: boolean
    transport?: string
    central_user_id?: string
    expires_at?: number
    write_scopes?: string[]
  }
}
export async function refreshProductAccess(): Promise<ProductAccessSnapshot> {
  const auth = useAuthStore.getState().auth
  if (auth.user?.identity_source !== 'unihub') {
    throw new Error('Product identity proof rejected')
  }
  const authority = productAuthorityKey(auth)
  const response = await fetch('/api/unihub/access', {
    headers: await getFreshAuthHeaders(),
    credentials: 'same-origin',
    cache: 'no-store',
    redirect: 'error',
    signal: AbortSignal.timeout(10000),
  })
  if (authority !== productAuthorityKey(useAuthStore.getState().auth)) {
    throw new Error('UniHub session or payer changed')
  }
  if (!response.ok) {
    if (response.status === 401 || response.status === 403) {
      clearAuthentication()
      throw new Error('Product identity proof rejected')
    }
    const current = useAuthStore.getState().auth
    if (productAuthorityKey(current) === authority) {
      current.setUser({ ...auth.user, permissions: { admin_permissions: {} } })
    }
    throw new Error('Product identity proof rejected')
  }
  const data = (await response.json()) as ProductAccessSnapshot
  if (
    data.access.subject !== auth.user.unihub_user_id ||
    authority !== productAuthorityKey(useAuthStore.getState().auth)
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
  if (!session.restorePromise) {
    const promise = restoreProductSessionOnce(session).finally(() => {
      if (session.restorePromise === promise) session.restorePromise = null
    })
    session.restorePromise = promise
  }
  return session.restorePromise
}
async function restoreProductSessionOnce(
  session: ProductSession
): Promise<void> {
  if (useAuthStore.getState().auth.user?.identity_source !== 'unihub') return
  const epoch = session.epoch
  let data: ProductAccessSnapshot
  try {
    data = await refreshProductAccess()
  } catch (error) {
    if (!session.canWrite && epoch === session.epoch) session.clear()
    throw error
  }
  if (epoch !== session.epoch) {
    throw new Error('UniHub session or payer changed')
  }
  session.acceptFinanceAccess(data)
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
