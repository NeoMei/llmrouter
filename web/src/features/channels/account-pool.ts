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
import { getCapabilities } from '@/features/capabilities/api'
import { api } from '@/lib/api'

export type PoolScope = { operation: string; model: string }
export type PoolBlocker = {
  reason: string
  scope: string
  operation?: string
  model?: string
  retry_at?: string
}
export type PoolMember = {
  member_id: string
  generation_id?: string
  indices: number[]
  legacy_disabled_indices?: number[]
  quota_group: string
  state: string
  paused: boolean
  isolated: boolean
  legacy_blocked: boolean
  retry_at?: string
  blockers: PoolBlocker[]
}
export type PoolSnapshot = {
  server_time: string
  channel_id: number
  profile_id?: number
  pool_id: string
  selected_scope: PoolScope
  quota_remaining: 'unknown'
  group_scope: 'global'
  status: string
  retry_at?: string
  counts: Record<
    | 'total'
    | 'available'
    | 'cooling'
    | 'isolated'
    | 'paused'
    | 'legacy_blocked'
    | 'unavailable',
    number
  >
  members: PoolMember[]
}
export type PoolAction =
  | 'pause'
  | 'resume'
  | 'recover'
  | 'recover-group'
  | 'quota-group'
export type RecoveryScope =
  | 'account_global'
  | 'selected'
  | 'operation'
  | 'model'
export type PoolMutation = PoolScope & {
  profile_id: number
  pool_id: string
  member_id: string
  generation_id?: string
  target_scope: RecoveryScope
  quota_group?: string
  expected_quota_group?: string
}
const config = { skipBusinessError: true, skipErrorHandler: true }
const safeCodes = new Set([
  'not_pool',
  'wrong_pool',
  'wrong_profile',
  'model_not_in_channel',
  'pool_too_large',
  'credential_unavailable',
  'channel_not_found',
  'stale_member',
  'stale_generation',
  'stale_quota_group',
  'state_unavailable',
  'configuration_unavailable',
  'invalid_channel',
  'invalid_member',
  'invalid_scope',
  'invalid_request',
  'invalid upstream quota group',
])
// Never retain an Axios error: it may contain response bodies and request data.
function safePoolError(error: unknown): Error {
  if (!error || typeof error !== 'object') return new Error('state_unavailable')
  const candidate = error as {
    response?: { data?: { code?: unknown }; status?: number }
    code?: unknown
  }
  const code = candidate.response?.data?.code ?? candidate.code
  if (typeof code === 'string' && safeCodes.has(code)) return new Error(code)
  if (
    candidate.response?.status === 403 ||
    candidate.response?.status === 401
  ) {
    return new Error('permission_denied')
  }
  return new Error('state_unavailable')
}
export async function getAccountPool(
  channelID: number,
  scope: PoolScope
): Promise<PoolSnapshot> {
  try {
    const response = await api.get(`/api/channel/${channelID}/account-pool`, {
      ...config,
      params: scope,
    })
    if (!response.data.success) throw { code: response.data.code }
    return response.data.data as PoolSnapshot
  } catch (error) {
    throw safePoolError(error)
  }
}
export async function mutateAccountPool(
  channelID: number,
  action: PoolAction,
  body: PoolMutation
): Promise<void> {
  try {
    const response = await api.post(
      `/api/channel/${channelID}/account-pool/${action}`,
      body,
      config
    )
    if (!response.data.success || response.data.data?.applied !== true) {
      throw { code: response.data.code }
    }
  } catch (error) {
    throw safePoolError(error)
  }
}

export async function getPoolOperations(
  channelType: number
): Promise<string[]> {
  try {
    const snapshot = await getCapabilities(config)
    const values = snapshot.capabilities
    return values
      .filter(
        (value) =>
          value.channel_type === channelType &&
          value.supported &&
          !value.operation.startsWith('models.') &&
          value.operation !== 'ocr.resources.retrieve'
      )
      .map((value) => value.operation)
  } catch {
    throw new Error('configuration_unavailable')
  }
}
