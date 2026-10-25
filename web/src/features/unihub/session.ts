import { UniHubClient, UniHubError } from './sdk'

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
