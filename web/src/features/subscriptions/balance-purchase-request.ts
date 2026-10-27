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
interface PurchaseStorage {
  getItem(key: string): string | null
  setItem(key: string, value: string): void
  removeItem(key: string): void
}

interface PurchaseResponse {
  success?: boolean
  code?: string
}

// A purchase key belongs to a user's action, not an HTTP attempt. Keep it
// through unknown replies and reloads until the server confirms an outcome.
export class BalancePurchaseRequests {
  private readonly memory = new Map<string, string>()

  constructor(
    private readonly storage: PurchaseStorage,
    private readonly createKey: () => string,
  ) {}

  private scope(userId: number, planId: number): string {
    return `subscription-balance-purchase:v1:${userId}:${planId}`
  }

  key(userId: number, planId: number): string {
    const scope = this.scope(userId, planId)
    let current = this.memory.get(scope)
    try { current = this.storage.getItem(scope) || current } catch { /* Storage may be disabled. */ }
    if (current) return current
    current = this.createKey()
    this.memory.set(scope, current)
    try { this.storage.setItem(scope, current) } catch { /* In-memory retries remain stable. */ }
    return current
  }

  responded(userId: number, planId: number, key: string, response: PurchaseResponse): void {
    if (typeof response.success !== 'boolean' || response.code === 'PRODUCT_PAYMENT_PENDING') return
    if (this.key(userId, planId) !== key) return
    const scope = this.scope(userId, planId)
    this.memory.delete(scope)
    try { this.storage.removeItem(scope) } catch { /* In-memory state was cleared. */ }
  }
}

export const balancePurchaseRequests = new BalancePurchaseRequests({
  getItem: (key) => sessionStorage.getItem(key),
  setItem: (key, value) => { sessionStorage.setItem(key, value) },
  removeItem: (key) => { sessionStorage.removeItem(key) },
}, () => crypto.randomUUID())
