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
import { api, type ApiRequestConfig } from '@/lib/api'

const clientModelRouteRequestConfig = {
  skipBusinessError: true,
  skipErrorHandler: true,
} satisfies ApiRequestConfig

export type ClientModelTarget = {
  id?: number
  route_id?: number
  channel_id: number
  upstream_model: string
  billing_model: string
  preferred_wires: string
  priority: number
  weight: number
  enabled: boolean
}

export type ClientModelRoute = {
  id: number
  public_id: string
  display_name: string
  client: string
  aliases: string[]
  metadata?: string
  enabled: boolean
  targets: ClientModelTarget[]
}

export type ClientModelRouteInput = Omit<ClientModelRoute, 'id' | 'targets'> & {
  id?: number
  targets: ClientModelTarget[]
}

export type ClientModelRouteChannelOption = {
  id: number
  name: string
  type: number
  models: string[]
  wires: WireProtocol[]
}

export type WireProtocol = 'responses' | 'openai_chat' | 'anthropic'

export type ClientModelRouteOptions = {
  channels: ClientModelRouteChannelOption[]
  billing_models: string[]
}

export async function getClientModelRoutes(): Promise<{
  success: boolean
  data: ClientModelRoute[]
  message?: string
}> {
  const response = await api.get(
    '/api/client_model_routes/',
    clientModelRouteRequestConfig
  )
  return response.data
}

export async function getClientModelRouteOptions(): Promise<{
  success: boolean
  data?: ClientModelRouteOptions
  message?: string
}> {
  const response = await api.get(
    '/api/client_model_routes/options',
    clientModelRouteRequestConfig
  )
  return response.data
}

export async function createClientModelRoute(
  input: ClientModelRouteInput
): Promise<{ success: boolean; data?: ClientModelRoute; message?: string }> {
  const response = await api.post(
    '/api/client_model_routes/',
    input,
    clientModelRouteRequestConfig
  )
  return response.data
}

export async function updateClientModelRoute(
  input: ClientModelRouteInput & { id: number }
): Promise<{ success: boolean; data?: ClientModelRoute; message?: string }> {
  const response = await api.put(
    '/api/client_model_routes/',
    input,
    clientModelRouteRequestConfig
  )
  return response.data
}

export async function deleteClientModelRoute(
  id: number
): Promise<{ success: boolean; message?: string }> {
  const response = await api.delete(
    `/api/client_model_routes/${id}`,
    clientModelRouteRequestConfig
  )
  return response.data
}

export function getClientModelRouteErrorMessage(
  error: unknown,
  fallback: string
): string {
  if (!error || typeof error !== 'object') return fallback

  const response = Reflect.get(error, 'response')
  if (response && typeof response === 'object') {
    const data = Reflect.get(response, 'data')
    if (data && typeof data === 'object') {
      const serverMessage = Reflect.get(data, 'message')
      if (typeof serverMessage === 'string' && serverMessage.trim()) {
        return serverMessage
      }
    }
  }

  const message = Reflect.get(error, 'message')
  return typeof message === 'string' && message.trim() ? message : fallback
}
