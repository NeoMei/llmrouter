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
import { api } from '@/lib/api'

export type FineTuningJob = {
  id: string
  object: 'fine-tune'
  model: string
  status: string
  training_file: string
  validation_file?: string
  fine_tuned_model?: string
  created_at: number
  finished_at?: number
  hyperparameters?: Record<string, unknown>
  error?: unknown
}

export type FineTuningEvent = {
  object: string
  created_at: number
  level: string
  message: string
  data?: unknown
}

export async function listFineTuningJobs(): Promise<FineTuningJob[]> {
  const response = await api.get('/v1/fine-tunes')
  return (response.data?.data ?? []) as FineTuningJob[]
}

export async function createFineTuningJob(input: {
  training_file: string
  validation_file?: string
  model: string
  suffix?: string
  n_epochs?: number
}) {
  const response = await api.post('/v1/fine-tunes', input)
  return response.data as FineTuningJob
}

export async function cancelFineTuningJob(id: string) {
  const response = await api.post(
    `/v1/fine-tunes/${encodeURIComponent(id)}/cancel`
  )
  return response.data as FineTuningJob
}

export async function deleteFineTunedModel(id: string): Promise<void> {
  await api.delete(`/v1/models/${encodeURIComponent(id)}`)
}

export async function listFineTuningEvents(
  id: string
): Promise<FineTuningEvent[]> {
  const response = await api.get(
    `/v1/fine-tunes/${encodeURIComponent(id)}/events`
  )
  return sortFineTuningEvents(response.data?.data ?? [])
}

export function sortFineTuningEvents(
  events: FineTuningEvent[]
): FineTuningEvent[] {
  return [...events].sort(
    (left, right) =>
      left.created_at - right.created_at ||
      left.message.localeCompare(right.message)
  )
}

export function isFineTuningTerminal(status: string): boolean {
  return ['succeeded', 'failed', 'cancelled'].includes(status)
}
