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
import { useAuthStore } from '@/stores/auth-store'

import { UniHubClient, UniHubError, type User, type Account } from './sdk'

export interface ProductConfig {
  enabled: boolean
  public_origin: string
  client_id: string
  redirect_uri: string
  product_id: string
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
    const result = await fetch(input, { ...init, signal: session.signal })
    if (result.status === 401 && epoch === session.epoch) session.clear()
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
