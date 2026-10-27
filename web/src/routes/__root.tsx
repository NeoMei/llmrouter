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
import { useQueryClient, type QueryClient } from '@tanstack/react-query'
import { ReactQueryDevtools } from '@tanstack/react-query-devtools'
import {
  createRootRouteWithContext,
  Outlet,
  redirect,
  useNavigate,
} from '@tanstack/react-router'
import { TanStackRouterDevtools } from '@tanstack/react-router-devtools'
import { useEffect } from 'react'

import { NavigationProgress } from '@/components/navigation-progress'
import { Toaster } from '@/components/ui/sonner'
import { ThemeCustomizationProvider } from '@/context/theme-customization-provider'
import { saveAffiliateCode } from '@/features/auth/lib/storage'
import { GeneralError } from '@/features/errors/general-error'
import { NotFoundError } from '@/features/errors/not-found-error'
import { getSetupStatus } from '@/features/setup/api'
import { refreshProductAccess } from '@/features/unihub/session'
import { safeProductReturn } from '@/features/unihub/sso'
import { useSystemConfig } from '@/hooks/use-system-config'
import {
  bootstrapAuthentication,
  clearAuthenticatedClientState,
  clearAuthentication,
} from '@/lib/auth-session'
import { subscribeAuthSessionEvents } from '@/lib/auth-session-sync'
import { resolveLegacyRoute } from '@/lib/legacy-route'
import { canOpenProductPage, canUseProductFeature } from '@/lib/product-access'
import { useAuthStore } from '@/stores/auth-store'

function RootComponent() {
  const navigate = useNavigate()
  const queryClient = useQueryClient()

  // Load system configuration (logo, system name, etc.) from backend
  useSystemConfig({ autoLoad: true })

  useEffect(() => {
    const aff = new URLSearchParams(window.location.search).get('aff')?.trim()
    if (aff) {
      saveAffiliateCode(aff)
    }
  }, [])

  useEffect(
    () =>
      useAuthStore.subscribe((state, previousState) => {
        const sid = state.auth.session?.sid
        const previousSID = previousState.auth.session?.sid
        if (sid !== previousSID) {
          queryClient.clear()
        }
      }),
    [queryClient]
  )

  useEffect(
    () =>
      subscribeAuthSessionEvents((event) => {
        const currentSID = useAuthStore.getState().auth.session?.sid

        if (event.kind === 'authenticated') {
          if (event.sid === currentSID) return
          if (currentSID) {
            clearAuthentication(false)
          }
          window.location.reload()
          return
        }

        if (currentSID && event.sid === currentSID) {
          clearAuthenticatedClientState(queryClient, false)
          void navigate({ to: '/sign-in', replace: true })
        }
      }),
    [navigate, queryClient]
  )

  return (
    <ThemeCustomizationProvider>
      <NavigationProgress />
      <Outlet />
      <Toaster closeButton duration={5000} position='top-center' richColors />
      {import.meta.env.MODE === 'development' && (
        <>
          <ReactQueryDevtools buttonPosition='bottom-left' />
          <TanStackRouterDevtools position='bottom-right' />
        </>
      )}
    </ThemeCustomizationProvider>
  )
}

// 缓存 setup 状态检查结果，避免每次导航都重复调用 API
// 使用 localStorage 持久化，避免页面刷新后重复检查
const SETUP_CHECKED_KEY = 'setup_status_checked'

function getSetupStatusFromCache(): boolean {
  try {
    if (typeof window !== 'undefined') {
      return window.localStorage.getItem(SETUP_CHECKED_KEY) === 'true'
    }
  } catch {
    /* empty */
  }
  return false
}

function setSetupStatusCache(value: boolean): void {
  try {
    if (typeof window !== 'undefined') {
      if (value) {
        window.localStorage.setItem(SETUP_CHECKED_KEY, 'true')
      } else {
        window.localStorage.removeItem(SETUP_CHECKED_KEY)
      }
    }
  } catch {
    /* empty */
  }
}

// 内存中的标记，避免同一会话中重复检查
let setupStatusChecked = getSetupStatusFromCache()

export const Route = createRootRouteWithContext<{
  queryClient: QueryClient
}>()({
  // 应用初始化与路由解析前统一校验会话
  beforeLoad: async ({ location }) => {
    const legacyTarget = resolveLegacyRoute(location.href)
    if (legacyTarget) {
      throw redirect({ href: legacyTarget, replace: true })
    }

    const pathname = location?.pathname || ''
    if (pathname === '/unihub') {
      await bootstrapAuthentication()
      return
    }
    // The server derives this flag from the listener's local socket. Never
    // infer operator access from browser origin, stored credentials, or query.
    const productConfig = await fetch('/api/unihub/config', {
      credentials: 'omit',
      cache: 'no-store',
    })
      .then(async (response) =>
        response.ok
          ? ((await response.json()) as {
              native_access?: boolean
              enabled?: boolean
            })
          : null
      )
      .catch(() => null)
    if (!productConfig?.native_access) {
      if (
        ['/about', '/docs', '/privacy-policy', '/user-agreement'].includes(
          pathname
        )
      ) {
        return
      }
      if (productConfig?.enabled) {
        // Normalize entry routes before either restoring or preserving an SSO
        // return path, so fresh and already-authenticated visits agree.
        let destination = location.href
        if (pathname === '/') destination = '/dashboard'
        else if (pathname === '/sign-in') {
          destination = safeProductReturn(
            new URL(location.href, window.location.origin).searchParams.get(
              'redirect'
            )
          )
        }
        if (destination === '/') destination = '/dashboard'
        if (pathname === '/' || pathname === '/sign-in') {
          throw redirect({ href: destination, replace: true })
        }
        await bootstrapAuthentication()
        const auth = useAuthStore.getState().auth
        if (auth.user?.identity_source === 'unihub' && auth.accessToken) {
          try {
            await refreshProductAccess()
          } catch {
            throw redirect({ href: '/unihub?sso_error=1', replace: true })
          }
          if (
            ['/system-settings', '/system-settings/models'].includes(
              pathname
            ) &&
            canUseProductFeature(useAuthStore.getState().auth.user, 'settings')
          ) {
            throw redirect({
              href: '/system-settings/models/global',
              replace: true,
            })
          }
          if (['/wallet', '/profile', '/keys'].includes(pathname)) {
            throw redirect({ href: '/unihub', replace: true })
          }
          if (
            !canOpenProductPage(useAuthStore.getState().auth.user, pathname)
          ) {
            throw redirect({ to: '/403', replace: true })
          }
          return
        }
      }
      throw redirect({
        href: `/unihub?redirect=${encodeURIComponent(pathname === '/sign-in' ? safeProductReturn(new URL(location.href, window.location.origin).searchParams.get('redirect')) : location.href)}`,
        replace: true,
      })
    }
    const needsSetupCheck =
      !setupStatusChecked && !pathname.startsWith('/setup')
    const authBootstrap = bootstrapAuthentication()

    // 只检查 setup 状态（如果需要）
    if (needsSetupCheck) {
      const [status] = await Promise.all([
        getSetupStatus().catch((error) => {
          if (import.meta.env.DEV) {
            // eslint-disable-next-line no-console
            console.warn('[root.beforeLoad] setup status check failed', error)
          }
          return null
        }),
        authBootstrap,
      ])

      if (status?.success && status.data && !status.data.status) {
        throw redirect({ to: '/setup' })
      }
      setupStatusChecked = true
      setSetupStatusCache(true)
    } else {
      await authBootstrap
    }
  },
  component: RootComponent,
  notFoundComponent: NotFoundError,
  errorComponent: GeneralError,
})
