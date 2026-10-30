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
import axios, { type AxiosRequestConfig } from 'axios'
import { t } from 'i18next'
import { toast } from 'sonner'

import {
  applyAuthRotation,
  clearAuthentication,
  getAuthEpoch,
  getAuthGeneration,
  getAuthRotationEpoch,
  refreshAuthentication,
} from '@/lib/auth-session'
import { getServerErrorMessageKey } from '@/lib/server-error-message'
import { useAuthStore } from '@/stores/auth-store'

declare module 'axios' {
  export interface AxiosRequestConfig {
    skipBusinessError?: boolean
    skipErrorHandler?: boolean
    disableDuplicate?: boolean
    skipAuthRefresh?: boolean
    authRetry?: boolean
    acceptAuthRotation?: boolean
    allowNoAuthRotation?: boolean
    authRequestEpoch?: number
    authRequestSID?: string
    authRequestGeneration?: number
    authRequestRotationEpoch?: number
  }
}

export type ApiRequestConfig = AxiosRequestConfig

export const api = axios.create({
  baseURL: '',
  withCredentials: true,
  headers: {
    'Cache-Control': 'no-store',
  },
})

const inFlightGet = new Map<string, Promise<unknown>>()
const originalGet = api.get.bind(api)

api.get = ((url: string, config: ApiRequestConfig = {}) => {
  if (config.disableDuplicate) return originalGet(url, config)

  const params = config.params ? JSON.stringify(config.params) : '{}'
  const sessionSID = useAuthStore.getState().auth.session?.sid || 'anonymous'
  const key = `${getAuthGeneration()}:${sessionSID}:${url}?${params}`
  const existingRequest = inFlightGet.get(key)
  if (existingRequest) return existingRequest

  const request = originalGet(url, config).finally(() => {
    inFlightGet.delete(key)
  })
  inFlightGet.set(key, request)
  return request
}) as typeof api.get

function redirectToSignIn(): void {
  if (
    typeof window !== 'undefined' &&
    window.location.pathname !== '/sign-in'
  ) {
    window.location.replace('/sign-in')
  }
}

api.interceptors.response.use(
  (response) => {
    if (
      response.config.authRequestGeneration !== getAuthGeneration() ||
      response.config.authRequestSID !==
        useAuthStore.getState().auth.session?.sid
    ) {
      throw new Error('Authentication response was superseded')
    }
    if (response.config.acceptAuthRotation && response.data?.success === true) {
      const data = response.data.data
      if (data?.auth_rotation_required === false) {
        const session = data?.session
        if (
          !response.config.allowNoAuthRotation ||
          useAuthStore.getState().auth.session?.authentication_realm !==
            'CENTRAL' ||
          session?.authentication_realm !== 'CENTRAL' ||
          session?.sid !== response.config.authRequestSID ||
          session?.current !== true ||
          typeof data.access_token !== 'string' ||
          !data.access_token ||
          data.token_type !== 'Bearer' ||
          typeof data.access_expires_at !== 'number' ||
          !Number.isFinite(data.access_expires_at) ||
          data.access_expires_at <= 0 ||
          typeof session.login_method !== 'string' ||
          typeof session.ip !== 'string' ||
          typeof session.user_agent !== 'string' ||
          typeof session.created_at !== 'number' ||
          typeof session.last_active_at !== 'number' ||
          typeof session.expires_at !== 'number'
        ) {
          throw new Error('Invalid unchanged authentication response')
        }
        return response
      }
      if (
        response.config.authRequestRotationEpoch !== getAuthRotationEpoch() ||
        response.config.authRequestSID !==
          useAuthStore.getState().auth.session?.sid
      ) {
        throw new Error('Authentication rotation response was superseded')
      }
      applyAuthRotation(response.data.data)
    }

    if (
      !response.config.skipBusinessError &&
      typeof response.data?.success === 'boolean' &&
      !response.data.success
    ) {
      const messageKey = getServerErrorMessageKey(response.data)
      toast.error(
        messageKey
          ? t(messageKey)
          : response.data.message || t('Request failed')
      )
    }
    return response
  },
  async (error) => {
    const config = error?.config as ApiRequestConfig | undefined
    const skipErrorHandler = config?.skipErrorHandler
    const status = error?.response?.status

    if (status === 401) {
      const auth = useAuthStore.getState().auth
      if (
        config &&
        !config.authRetry &&
        !config.skipAuthRefresh &&
        error?.response?.data?.code === 'AUTH_TOKEN_EXPIRED' &&
        config.authRequestEpoch !== getAuthEpoch() &&
        config.authRequestGeneration === getAuthGeneration() &&
        config.authRequestSID === auth.session?.sid &&
        auth.accessToken &&
        config.headers?.Authorization !== `Bearer ${auth.accessToken}`
      ) {
        config.authRetry = true
        config.authRequestEpoch = getAuthEpoch()
        return api.request(config)
      }
      if (
        config?.authRequestEpoch !== getAuthEpoch() ||
        config?.authRequestSID !== useAuthStore.getState().auth.session?.sid
      ) {
        throw error
      }
      if (config && !config.skipAuthRefresh && !config.authRetry) {
        const requestEpoch = config.authRequestEpoch
        const requestSID = config.authRequestSID
        config.authRetry = true
        const outcome = await refreshAuthentication()
        if (outcome.kind === 'authenticated') {
          if (
            getAuthEpoch() !== requestEpoch + 1 ||
            outcome.bundle.session.sid !== requestSID ||
            useAuthStore.getState().auth.session?.sid !== requestSID
          ) {
            throw error
          }
          const token = useAuthStore.getState().auth.accessToken
          if (token) {
            config.headers = {
              ...config.headers,
              Authorization: `Bearer ${token}`,
            }
          }
          config.authRequestEpoch = getAuthEpoch()
          return api.request(config)
        }

        if (outcome.kind === 'anonymous' || outcome.kind === 'out_of_sync') {
          if (
            useAuthStore.getState().auth.session ||
            getAuthEpoch() > requestEpoch + 1
          ) {
            throw error
          }
          if (!skipErrorHandler) toast.error(t('Session expired!'))
          redirectToSignIn()
        }
      } else if (config?.authRetry) {
        const code = error?.response?.data?.code
        if (
          [
            'AUTH_SESSION_REVOKED',
            'AUTH_USER_DISABLED',
            'AUTH_USER_VERSION_REVOKED',
          ].includes(code)
        ) {
          clearAuthentication(false)
          if (!skipErrorHandler) toast.error(t('Session expired!'))
          redirectToSignIn()
        }
      } else if (!skipErrorHandler) {
        toast.error(t('Session expired!'))
      }
    } else if (!skipErrorHandler) {
      const messageKey = getServerErrorMessageKey(error)
      const message = messageKey
        ? t(messageKey)
        : error?.response?.data?.error?.message ||
          error?.response?.data?.message ||
          error?.message ||
          t('Request failed')
      toast.error(message)
    }
    throw error
  }
)

api.interceptors.request.use(
  (config) => {
    const auth = useAuthStore.getState().auth
    if (
      config.authRetry &&
      config.authRequestEpoch !== undefined &&
      (config.authRequestEpoch !== getAuthEpoch() ||
        config.authRequestSID !== auth.session?.sid)
    ) {
      throw new Error('Authentication retry was superseded')
    }
    config.authRequestEpoch = getAuthEpoch()
    config.authRequestGeneration = getAuthGeneration()
    config.authRequestRotationEpoch = getAuthRotationEpoch()
    config.authRequestSID = auth.session?.sid
    const accessToken = auth.accessToken
    if (accessToken) {
      config.headers.Authorization = `Bearer ${accessToken}`
    }
    return config
  },
  (error) => {
    throw error
  },
  { synchronous: true }
)
