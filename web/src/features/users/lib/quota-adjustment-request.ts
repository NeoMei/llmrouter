import type { ManageUserQuotaPayload } from '../types'

interface RequestStorage {
  getItem(key: string): string | null
  setItem(key: string, value: string): void
  removeItem(key: string): void
}

export class QuotaAdjustmentRequests {
  private readonly memory = new Map<string, string>()

  constructor(private readonly storage: RequestStorage, private readonly createKey: () => string) {}

  private scope(actor: number, payload: ManageUserQuotaPayload): string {
    return `quota-adjustment:v1:${actor}:${payload.id}:${payload.mode}:${payload.value}`
  }

  key(actor: number, payload: ManageUserQuotaPayload): string {
    const scope = this.scope(actor, payload)
    let key = this.memory.get(scope)
    try { key = this.storage.getItem(scope) || key } catch { /* Storage may be unavailable. */ }
    if (!key) {
      key = this.createKey()
      this.memory.set(scope, key)
      try { this.storage.setItem(scope, key) } catch { /* Retain memory retry state. */ }
    }
    return key
  }

  responded(actor: number, payload: ManageUserQuotaPayload, key: string, response: { success?: boolean; code?: string }): void {
    if (typeof response.success !== 'boolean' || response.code === 'PRODUCT_CREDIT_PENDING' || this.key(actor, payload) !== key) return
    const scope = this.scope(actor, payload)
    this.memory.delete(scope)
    try { this.storage.removeItem(scope) } catch { /* Memory state has been cleared. */ }
  }
}

export const quotaAdjustmentRequests = new QuotaAdjustmentRequests({
  getItem: (key) => sessionStorage.getItem(key),
  setItem: (key, value) => { sessionStorage.setItem(key, value) },
  removeItem: (key) => { sessionStorage.removeItem(key) },
}, () => crypto.randomUUID())
