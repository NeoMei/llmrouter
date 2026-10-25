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
// Vendored from UniHub packages/sdk/src/index.ts at 90a18fb (workspace-owned source).
/** UniHub JSON protocol client. No UI, storage, refresh, permissions or retries. */
export type IntegerString = string
export type BillingAsset = 'CREDIT/6' | 'CNY/6'
export type FundingMode = 'credits' | 'prepaid_cash'
export type BillingPreset = 'hybrid' | 'prepaid' | 'subscription'
export interface BillingComponent {
  enabled: boolean
}
export interface BillingProfile {
  product_id: string
  version: number
  subscription: BillingComponent
  included_credits: BillingComponent
  addons: BillingComponent
  usage: {
    enabled: boolean
    require_subscription: boolean
    funding_modes: FundingMode[]
    default_funding_mode: FundingMode | ''
  }
}
/** Presets use product_id="template" and version=0; they are not saved profiles. */
export type BillingPresets = Record<BillingPreset, BillingProfile>
export interface Product {
  id: string
  name: string
  billing_profile?: BillingProfile
}
export interface ModelCredential {
  id: string
  audience: 'llmrouter'
  actor_id: string
  account_id: string
  origin_product_id: string
  models: string[]
  task_id: string
  expires_at: string
  token: string
  funding_mode: FundingMode
  asset: BillingAsset
  billing_profile_version: number
}
export interface Registration {
  username: string
  password: string
  email?: string
  display_name?: string
}
export interface RegistrationResult {
  registration_id: string
  status: 'CREATED' | 'UNKNOWN'
  login_required?: boolean
  retry_after?: number
}
export interface TokenSet {
  access_token: string
  id_token: string
  token_type: 'Bearer'
  scope: string
  expires_in: number
}
export interface User {
  id: string
  name: string
  email: string
}
export interface Account {
  id: string
  kind: string
  name: string
  role: string
}
export interface Member {
  user_id: string
  name: string
  email: string
  role: string
}
export interface Invitation {
  id: string
  account_id: string
  recipient_id: string
  role: string
}
export interface Balance {
  product_id: string
  asset: 'CNY/6'
  amount: IntegerString
}
export interface Recharge {
  id: string
  account_id: string
  product_id: string
  amount: IntegerString
  currency: string
  merchant: string
  operation_id?: string
}
export interface Operation {
  id: string
  account_id: string
  kind: string
  status: string
  ledger_transaction_id?: string
  error_code?: string
  created_at: string
}
export interface Offer {
  id: string
  product_id: string
  name: string
  account_kind: string
  kind: string
  period: string
  price: IntegerString
  monthly_credit: IntegerString
  entitlements: Record<string, string>
  rank: number
  validity_months: number
  upgrade_rule: string
  refund_rule: string
  status: string
  sandbox: boolean
}
export interface Subscription {
  id: string
  account_id: string
  product_id: string
  offer: Offer
  version: number
  anchor: string
  paid_through: string
  status: string
}
export interface Order {
  id: string
  account_id: string
  actor_id: string
  product_id: string
  offer: Offer
  amount: IntegerString
  payment_status: string
  fulfillment_status: string
  refund_status?: string
  [key: string]: unknown
}
export interface Quote {
  id: string
  account_id: string
  subscription_id: string
  offer: Offer
  amount: IntegerString
  grant_amount: IntegerString
  expires_at: string
  [key: string]: unknown
}
export interface Authorization {
  product_id: string
  purpose: 'funding' | 'renewal'
  enabled: boolean
  version: number
  max_per_operation: IntegerString
  max_total: IntegerString
  expires_at: string
  used?: IntegerString
  actor_id?: string
}
export interface Usage {
  input: IntegerString
  output: IntegerString
  cache_read: IntegerString
  cache_write: IntegerString
  reasoning: IntegerString
}
export interface QueryScope {
  account_id: string
  origin_product_id?: string
  actor_id?: string
  visibility: 'actor' | 'account'
}
export interface CallFilter {
  from?: string
  to?: string
  model?: string
  origin_product_id?: string
  asset?: BillingAsset
}
export interface CallQuery extends CallFilter {
  limit?: number
  cursor?: string
}
export interface CallView {
  scope: QueryScope
  financial_details: boolean
  asset: BillingAsset
  call_id: string
  account_id: string
  actor_id: string
  origin_product_id: string
  model: string
  status: string
  created_at: string | null
  time_source: string
  task_id?: string
  held?: IntegerString
  charged?: IntegerString
  usage?: Usage
}
export interface CallPage {
  scope: QueryScope
  filter: CallFilter
  financial_details: boolean
  items: CallView[]
  next_cursor?: string
  has_more: boolean
  total: IntegerString
  unknown_time: IntegerString
  as_of: string
  accuracy: string
  consistency: string
}
/** mixed omits charged. All charge amounts are omitted without a finance role. */
export interface UsageSummary {
  scope: QueryScope
  filter: CallFilter
  financial_details: boolean
  calls_total: IntegerString
  settled_calls: IntegerString
  pending_calls: IntegerString
  missing_usage_calls: IntegerString
  unknown_calls: IntegerString
  rejected_calls: IntegerString
  released_calls: IntegerString
  by_status: Record<string, IntegerString>
  charged?: IntegerString
  charges_by_asset?: Partial<Record<BillingAsset, IntegerString>>
  asset: BillingAsset | 'mixed'
  usage: Usage
  usage_complete: boolean
  unknown_time: IntegerString
  as_of: string
  accuracy: string
  consistency: string
}
export interface Resources {
  account_id: string
  product_id: string
  billing_profile?: BillingProfile
  subscriptions: Subscription[]
  credits?: {
    asset: 'CREDIT/6'
    pool: 'llmrouter'
    spendable: IntegerString
    reserved: IntegerString
    frozen: IntegerString
  }
  wallets?: Balance[]
  payments: { mode: string; real_payments_configured: boolean }
  as_of: string
}
export interface ClientOptions {
  baseUrl: string
  clientId: string
  redirectUri: string
  fetch?: typeof globalThis.fetch
  crypto?: Crypto
}
export interface RequestOptions {
  body?: unknown
  idempotencyKey?: string
  signal?: AbortSignal
}
export class UniHubError extends Error {
  readonly status: number
  readonly code: string
  readonly body: unknown
  readonly headers: Headers
  constructor(
    status: number,
    code: string,
    body: unknown,
    headers = new Headers()
  ) {
    super(`UniHub ${status}: ${code}`)
    this.name = 'UniHubError'
    this.status = status
    this.code = code
    this.body = body
    this.headers = headers
  }
}
function integer(value: unknown): asserts value is string {
  if (typeof value !== 'string' || !/^(0|[1-9][0-9]*)$/.test(value)) {
    throw new TypeError(
      'Quantity must be a canonical nonnegative integer string'
    )
  }
}
function key(value: unknown): asserts value is string {
  if (
    typeof value !== 'string' ||
    value.length < 1 ||
    value.length > 128 ||
    value.trim() !== value
  ) {
    throw new TypeError('An explicit Idempotency-Key is required')
  }
}
function segment(value: string): string {
  if (typeof value !== 'string' || !value) {
    throw new TypeError('Resource ID is required')
  }
  return encodeURIComponent(value)
}
function encoded(value: Uint8Array): string {
  return btoa(String.fromCharCode(...value))
    .replaceAll('+', '-')
    .replaceAll('/', '_')
    .replace(/=+$/, '')
}
function query(path: string, values: object): string {
  const q = new URLSearchParams()
  for (const [k, v] of Object.entries(values)) {
    if (v !== undefined) q.set(k, String(v))
  }
  return q.size ? `${path}?${q}` : path
}
export const productCapabilities = Object.freeze({
  unifiedWalletRecharge: false,
  automaticFinancialRetry: false,
  refresh: false,
  serverLogout: false,
  sandboxSettlementIsRealPayment: false,
})
export class UniHubClient {
  readonly clientId: string
  readonly redirectUri: string
  readonly baseUrl: string
  private token = ''
  private send: typeof globalThis.fetch
  private cryptoAPI: Crypto
  constructor(options: ClientOptions) {
    const base = new URL(options.baseUrl)
    const loopback =
      base.hostname === 'localhost' ||
      base.hostname === '[::1]' ||
      /^127(?:\.[0-9]{1,3}){3}$/.test(base.hostname)
    if (
      (base.protocol !== 'https:' &&
        !(base.protocol === 'http:' && loopback)) ||
      base.username ||
      base.password ||
      base.search ||
      base.hash ||
      base.pathname !== '/' ||
      !options.clientId ||
      !options.redirectUri
    ) {
      throw new TypeError(
        'A service URL, fixed client ID and exact redirect URI are required'
      )
    }
    this.baseUrl = base.href.replace(/\/$/, '')
    this.clientId = options.clientId
    this.redirectUri = options.redirectUri
    this.send = options.fetch ?? globalThis.fetch.bind(globalThis)
    this.cryptoAPI = options.crypto ?? globalThis.crypto
  }
  setAccessToken(token: string): void {
    if (typeof token !== 'string' || !token || /[\r\n]/.test(token)) {
      throw new TypeError('Access token is required')
    }
    this.token = token
  }
  clearAccessToken(): void {
    this.token = ''
  }
  hasAccessToken(): boolean {
    return this.token !== ''
  }
  async request<T = Record<string, unknown>>(
    method: string,
    path: string,
    options: RequestOptions = {}
  ): Promise<T> {
    return this.perform<T>(method, path, options, true)
  }
  private async perform<T>(
    method: string,
    path: string,
    options: RequestOptions,
    authenticated: boolean
  ): Promise<T> {
    if (!path.startsWith('/v1/') || path.includes('\\')) {
      throw new TypeError('API path must start with /v1/')
    }
    const url = new URL(this.baseUrl + path)
    if (
      url.origin !== new URL(this.baseUrl).origin ||
      !url.pathname.startsWith('/v1/')
    ) {
      throw new TypeError('Cross-origin API path')
    }
    const headers = new Headers({ Accept: 'application/json' })
    if (options.body !== undefined) {
      headers.set('Content-Type', 'application/json')
    }
    if (authenticated && this.token) {
      headers.set('Authorization', `Bearer ${this.token}`)
    }
    if (options.idempotencyKey !== undefined) {
      key(options.idempotencyKey)
      headers.set('Idempotency-Key', options.idempotencyKey)
    }
    const response = await this.send(url, {
      method,
      headers,
      body:
        options.body === undefined ? undefined : JSON.stringify(options.body),
      credentials: 'omit',
      redirect: 'error',
      signal: options.signal,
    })
    const raw = await response.text()
    let body: unknown
    try {
      body = raw ? JSON.parse(raw) : undefined
    } catch {
      throw new UniHubError(
        response.status,
        'invalid_json_response',
        undefined,
        response.headers
      )
    }
    if (!response.ok) {
      const code =
        body &&
        typeof body === 'object' &&
        'error' in body &&
        typeof body.error === 'string'
          ? body.error
          : 'http_error'
      throw new UniHubError(response.status, code, body, response.headers)
    }
    return body as T
  }
  async register(
    body: Registration,
    idempotencyKey: string
  ): Promise<RegistrationResult> {
    key(idempotencyKey)
    return this.perform(
      'POST',
      '/v1/identity/registrations',
      {
        body: {
          username: body.username,
          password: body.password,
          email: body.email,
          display_name: body.display_name,
          client_id: this.clientId,
        },
        idempotencyKey,
      },
      false
    )
  }
  async passwordLogin(body: {
    username: string
    password: string
  }): Promise<TokenSet> {
    const random = () =>
      encoded(this.cryptoAPI.getRandomValues(new Uint8Array(32)))
    const verifier = random(),
      state = random(),
      nonce = random()
    const challenge = encoded(
      new Uint8Array(
        await this.cryptoAPI.subtle.digest(
          'SHA-256',
          new TextEncoder().encode(verifier)
        )
      )
    )
    const auth = await this.perform<{
      authorization_id: string
      code: string
      state: string
    }>(
      'POST',
      '/v1/identity/authorize/password',
      {
        body: {
          client_id: this.clientId,
          redirect_uri: this.redirectUri,
          scope: 'openid profile email',
          state,
          nonce,
          code_challenge_method: 'S256',
          code_challenge: challenge,
          username: body.username,
          password: body.password,
        },
      },
      false
    )
    if (
      auth?.state !== state ||
      typeof auth.authorization_id !== 'string' ||
      !/^[A-Za-z0-9_-]{43}$/.test(auth.authorization_id) ||
      typeof auth.code !== 'string' ||
      !/^[0-9a-f]{64}$/.test(auth.code)
    ) {
      throw new UniHubError(502, 'invalid_authorization_response', undefined)
    }
    const tokens = await this.perform<TokenSet>(
      'POST',
      '/v1/identity/token',
      {
        body: {
          grant_type: 'authorization_code',
          authorization_id: auth.authorization_id,
          client_id: this.clientId,
          redirect_uri: this.redirectUri,
          code: auth.code,
          code_verifier: verifier,
        },
      },
      false
    )
    if (
      typeof tokens?.access_token !== 'string' ||
      !tokens.access_token ||
      typeof tokens.id_token !== 'string' ||
      !tokens.id_token ||
      tokens.token_type !== 'Bearer' ||
      tokens.scope !== 'openid profile email' ||
      !Number.isInteger(tokens.expires_in) ||
      tokens.expires_in <= 0
    ) {
      throw new UniHubError(502, 'invalid_token_response', undefined)
    }
    this.setAccessToken(tokens.access_token)
    return tokens
  }
  private path(account: string, suffix = ''): string {
    return `/v1/accounts/${segment(account)}${suffix}`
  }
  me(): Promise<User> {
    return this.request('GET', '/v1/me')
  }
  accounts(): Promise<Account[]> {
    return this.request('GET', '/v1/accounts')
  }
  capabilities(): Promise<Record<string, unknown>> {
    return this.request('GET', '/v1/capabilities')
  }
  createTeam(name: string): Promise<Account> {
    return this.request('POST', '/v1/accounts', { body: { name } })
  }
  members(account: string): Promise<Member[]> {
    return this.request('GET', this.path(account, '/members'))
  }
  invitations(account?: string): Promise<Invitation[]> {
    return this.request(
      'GET',
      account ? this.path(account, '/invitations') : '/v1/invitations'
    )
  }
  invite(account: string, userId: string, role: string): Promise<Invitation> {
    return this.request('POST', this.path(account, '/invitations'), {
      body: { user_id: userId, role },
    })
  }
  inviteAccept(id: string): Promise<Account> {
    return this.request('POST', `/v1/invitations/${segment(id)}/accept`)
  }
  changeMemberRole(
    account: string,
    member: string,
    role: string
  ): Promise<void> {
    return this.request(
      'PATCH',
      this.path(account, `/members/${segment(member)}`),
      { body: { role } }
    )
  }
  removeMember(account: string, member: string): Promise<void> {
    return this.request(
      'DELETE',
      this.path(account, `/members/${segment(member)}`)
    )
  }
  products(): Promise<Product[]> {
    return this.request('GET', '/v1/products')
  }
  billingProfile(product: string): Promise<BillingProfile> {
    return this.request(
      'GET',
      `/v1/products/${segment(product)}/billing-profile`
    )
  }
  billingPresets(): Promise<BillingPresets> {
    return this.request('GET', '/v1/billing-presets')
  }
  catalog(): Promise<Offer[]> {
    return this.request('GET', '/v1/catalog')
  }
  subscriptions(account: string): Promise<Subscription[]> {
    return this.request('GET', this.path(account, '/subscriptions'))
  }
  wallets(account: string): Promise<Balance[]> {
    return this.request('GET', this.path(account, '/wallets'))
  }
  async createRecharge(
    account: string,
    productId: string,
    amount: IntegerString,
    idempotencyKey: string
  ): Promise<Recharge> {
    integer(amount)
    key(idempotencyKey)
    return this.request('POST', this.path(account, '/recharges'), {
      body: { product_id: productId, amount },
      idempotencyKey,
    })
  }
  getRecharge(account: string, id: string): Promise<Recharge> {
    return this.request('GET', this.path(account, `/recharges/${segment(id)}`))
  }
  async transfer(
    account: string,
    productId: string,
    amount: IntegerString,
    idempotencyKey: string
  ): Promise<Operation> {
    integer(amount)
    key(idempotencyKey)
    return this.request('POST', this.path(account, '/transfers'), {
      body: { product_id: productId, amount },
      idempotencyKey,
    })
  }
  operations(account: string): Promise<Operation[]> {
    return this.request('GET', this.path(account, '/operations'))
  }
  getOperation(account: string, id: string): Promise<Operation> {
    return this.request('GET', this.path(account, `/operations/${segment(id)}`))
  }
  orders(account: string): Promise<Order[]> {
    return this.request('GET', this.path(account, '/orders'))
  }
  purchase(
    account: string,
    offerId: string,
    idempotencyKey: string
  ): Promise<Order> {
    key(idempotencyKey)
    return this.request('POST', this.path(account, '/orders'), {
      body: { offer_id: offerId },
      idempotencyKey,
    })
  }
  quote(
    account: string,
    subscriptionId: string,
    offerId: string
  ): Promise<Quote> {
    return this.request('POST', this.path(account, '/quotes'), {
      body: { subscription_id: subscriptionId, offer_id: offerId },
    })
  }
  acceptQuote(
    account: string,
    id: string,
    idempotencyKey: string
  ): Promise<Order> {
    key(idempotencyKey)
    return this.request(
      'POST',
      this.path(account, `/quotes/${segment(id)}/accept`),
      { body: {}, idempotencyKey }
    )
  }
  refund(account: string, id: string, idempotencyKey: string): Promise<Order> {
    key(idempotencyKey)
    return this.request(
      'POST',
      this.path(account, `/orders/${segment(id)}/refund`),
      { body: {}, idempotencyKey }
    )
  }
  settings(account: string): Promise<Authorization[]> {
    return this.request('GET', this.path(account, '/settings'))
  }
  async setAuthorization(
    account: string,
    body: Authorization
  ): Promise<Authorization> {
    if (body.enabled) {
      integer(body.max_per_operation)
      integer(body.max_total)
    }
    return this.request('PUT', this.path(account, '/settings'), {
      body: {
        product_id: body.product_id,
        purpose: body.purpose,
        enabled: body.enabled,
        version: body.version,
        max_per_operation: body.max_per_operation,
        max_total: body.max_total,
        expires_at: body.expires_at,
      },
    })
  }
  resources(account: string): Promise<Resources> {
    return this.request('GET', this.path(account, '/resources'))
  }
  modelPoolCalls(account: string, filter: CallQuery = {}): Promise<CallPage> {
    return this.request(
      'GET',
      query(this.path(account, '/model-pool/calls'), filter)
    )
  }
  modelPoolCall(
    account: string,
    id: string,
    origin?: string
  ): Promise<CallView> {
    return this.request(
      'GET',
      query(this.path(account, `/model-pool/calls/${encodeURIComponent(id)}`), {
        origin_product_id: origin,
      })
    )
  }
  modelPoolUsage(
    account: string,
    filter: CallFilter = {}
  ): Promise<UsageSummary> {
    return this.request(
      'GET',
      query(this.path(account, '/model-pool/usage/summary'), filter)
    )
  }
  calls(account: string, filter: CallQuery = {}): Promise<CallPage> {
    return this.request('GET', query(this.path(account, '/calls'), filter))
  }
  getCall(
    account: string,
    id: string,
    originProductId?: string
  ): Promise<CallView> {
    return this.request(
      'GET',
      query(this.path(account, `/calls/${segment(id)}`), {
        origin_product_id: originProductId,
      })
    )
  }
  usage(account: string, filter: CallFilter = {}): Promise<UsageSummary> {
    return this.request(
      'GET',
      query(this.path(account, '/usage/summary'), filter)
    )
  }
  issueModelCredential(
    account: string,
    models: string[],
    taskId: string,
    fundingMode?: FundingMode
  ): Promise<ModelCredential> {
    return this.request('POST', this.path(account, '/model-credentials'), {
      body: { models, task_id: taskId, funding_mode: fundingMode },
    })
  }
  revokeModelCredential(account: string, id: string): Promise<void> {
    return this.request(
      'DELETE',
      this.path(account, `/model-credentials/${segment(id)}`)
    )
  }
  /** Explicit sandbox only. Never a real payment or automatically called. */
  sandboxSettleRecharge(account: string, id: string): Promise<Operation> {
    return this.request(
      'POST',
      this.path(account, `/recharges/${segment(id)}/sandbox-settle`)
    )
  }
}
