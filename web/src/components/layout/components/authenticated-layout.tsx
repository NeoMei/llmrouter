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
import { useEffect } from 'react'
import { Link, useLocation } from '@tanstack/react-router'
import { useTranslation } from 'react-i18next'

import { LanguageSwitcher } from '@/components/language-switcher'
import { AnimatedOutlet } from '@/components/page-transition'
import { SkipToMain } from '@/components/skip-to-main'
import { SidebarInset, SidebarProvider } from '@/components/ui/sidebar'
import { LayoutProvider } from '@/context/layout-provider'
import { SearchProvider } from '@/context/search-provider'
import { refreshProductAccess } from '@/features/unihub/session'
import { UniHub } from '@/features/unihub'
import { hasPermission } from '@/lib/admin-permissions'
import { getCookie } from '@/lib/cookies'
import { cn } from '@/lib/utils'
import { useAuthStore } from '@/stores/auth-store'

import { AppHeader } from './app-header'
import { AppSidebar } from './app-sidebar'
import { SystemBrand } from './system-brand'

type AuthenticatedLayoutProps = {
  children?: React.ReactNode
}

export function AuthenticatedLayout(props: AuthenticatedLayoutProps) {
  const { t } = useTranslation()
  const pathname = useLocation({ select: (location) => location.pathname })
  const user = useAuthStore((state) => state.auth.user)
  const projected = useAuthStore(
    (state) => state.auth.user?.identity_source === 'unihub',
  )
  useEffect(() => {
    if (!projected) return
    const refresh = () => {
      void refreshProductAccess().catch(() => {})
    }
    window.addEventListener('focus', refresh)
    const timer = window.setInterval(refresh, 60000)
    return () => {
      window.removeEventListener('focus', refresh)
      window.clearInterval(timer)
    }
  }, [projected])
  if (projected) {
    return (
      <div className='min-h-svh'>
        <header className='flex h-14 items-center justify-between border-b px-4'>
          <SystemBrand defaultName='万流 · LLMRouter' variant='inline' />
          <LanguageSwitcher />
        </header>
        <div className='flex flex-col md:flex-row'>
          <aside className='border-b p-4 md:min-h-[calc(100svh-3.5rem)] md:w-56 md:border-r'>
            <nav
              aria-label={t('UniHub account')}
              className='flex flex-wrap gap-4 md:flex-col'
            >
              <Link to='/dashboard'>{t('Console')}</Link>
              {hasPermission(user, 'channel', 'read') ? (
                <Link to='/channels'>{t('Channels')}</Link>
              ) : null}
              <Link to='/unihub'>{t('UniHub account')}</Link>
            </nav>
          </aside>
          <div className='min-w-0 flex-1'>
            {pathname.startsWith('/channels') || pathname === '/403' ? (
              (props.children ?? <AnimatedOutlet />)
            ) : (
              <UniHub />
            )}
          </div>
        </div>
      </div>
    )
  }
  const defaultOpen = getCookie('sidebar_state') !== 'false'

  return (
    <LayoutProvider>
      <SearchProvider>
        <SidebarProvider defaultOpen={defaultOpen} className='flex-col'>
          <SkipToMain />
          <AppHeader />
          <div className='flex min-h-0 w-full flex-1'>
            <AppSidebar />
            <SidebarInset
              className={cn(
                '@container/content',
                'h-[calc(100svh-var(--app-header-height,0px))]',
                'min-h-0 overflow-hidden',
                'peer-data-[variant=inset]:h-[calc(100svh-var(--app-header-height,0px)-(var(--spacing)*4))]',
              )}
            >
              {props.children ?? <AnimatedOutlet />}
            </SidebarInset>
          </div>
        </SidebarProvider>
      </SearchProvider>
    </LayoutProvider>
  )
}
