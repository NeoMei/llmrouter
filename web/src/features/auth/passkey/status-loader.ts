import { getPasskeyStatus } from './api'
import type { ApiResponse, PasskeyStatus } from './types'

export function createPasskeyStatusLoader() {
  let latest = 0
  return async (
    fresh = false
  ): Promise<
    | { current: false }
    | { current: true; response: ApiResponse<PasskeyStatus> }
    | { current: true; error: unknown }
  > => {
    const request = ++latest
    try {
      const response = await getPasskeyStatus(fresh)
      return request === latest
        ? { current: true, response }
        : { current: false }
    } catch (error) {
      return request === latest ? { current: true, error } : { current: false }
    }
  }
}
