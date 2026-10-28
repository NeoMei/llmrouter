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
import type { BillingAsset, FundingMode, UniHubClient } from './sdk'

export interface ManagedKey {
  id: string
  credential_kind: 'api_key'
  name: string
  actor_id: string
  account_id: string
  origin_product_id: string
  instance_id: string
  models: string[]
  operations?: Record<string, string[]>
  funding_mode: FundingMode
  asset: BillingAsset
  created_at: string
  expires_at?: string | null
  revoked_at?: string | null
  max_total: string
  max_per_call: string
  held: string
  used: string
  remaining?: string
}
export interface KeyRequest {
  name: string
  models: string[]
  operations?: Record<string, string[]>
  funding_mode: FundingMode
  expires_at?: string
  max_total: string
  max_per_call: string
}
export interface UsageBudget {
  account_id: string
  origin_product_id: string
  instance_id: string
  asset: BillingAsset
  funding_mode: FundingMode
  version: number
  max_total: string
  max_per_call: string
  held: string
  used: string
  remaining?: string
}
export function limitAmount(value: string): string {
  if (value === '') return ''
  if (!/^(0|[1-9][0-9]*)(\.[0-9]{1,6})?$/.test(value)) {
    throw new TypeError(
      'Enter a nonnegative amount with up to six decimal places'
    )
  }
  const [whole, fraction = ''] = value.split('.')
  return (whole + fraction.padEnd(6, '0')).replace(/^0+(?=\d)/, '')
}
export class ManagedAccess {
  constructor(
    readonly client: UniHubClient,
    readonly account: string,
    readonly product: string,
    readonly instance: string
  ) {}
  private path(resource: string): string {
    return `/v1/accounts/${encodeURIComponent(this.account)}/${resource}`
  }
  private query(mode?: FundingMode): string {
    const query = new URLSearchParams({
      product_id: this.product,
      instance_id: this.instance,
    })
    if (mode) query.set('funding_mode', mode)
    return query.toString()
  }
  keys(): Promise<ManagedKey[]> {
    return this.client.request(
      'GET',
      `${this.path('api-keys')}?${this.query()}`
    )
  }
  createKey(body: KeyRequest): Promise<ManagedKey & { token: string }> {
    return this.client.request('POST', this.path('api-keys'), {
      body: {
        ...body,
        origin_product_id: this.product,
        instance_id: this.instance,
      },
    })
  }
  revokeKey(id: string): Promise<void> {
    return this.client.request(
      'DELETE',
      `${this.path('api-keys')}/${encodeURIComponent(id)}?${this.query()}`
    )
  }
  budget(mode: FundingMode): Promise<UsageBudget> {
    return this.client.request(
      'GET',
      `${this.path('usage-budget')}?${this.query(mode)}`
    )
  }
  setBudget(
    mode: FundingMode,
    body: Pick<UsageBudget, 'version' | 'max_total' | 'max_per_call'>
  ): Promise<UsageBudget> {
    return this.client.request(
      'PUT',
      `${this.path('usage-budget')}?${this.query(mode)}`,
      { body }
    )
  }
}
