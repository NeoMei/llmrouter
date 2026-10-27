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
import type { AuthUser } from '@/stores/auth-store'

import { ROLE } from './roles'

// Product capabilities are live central grants. The native role stays unchanged.
export function canUseProductFeature(
  user: AuthUser | null | undefined,
  resource: string,
  action = 'read'
): boolean {
  return user?.identity_source === 'unihub'
    ? user.permissions?.admin_permissions?.[resource]?.[action] === true
    : (user?.role ?? 0) >= ROLE.ADMIN
}

export function canOpenProductPage(
  user: AuthUser | null | undefined,
  pathname: string
): boolean {
  if (['/unihub', '/403'].includes(pathname)) return true
  if (pathname === '/keys') return canUseProductFeature(user, 'product', 'use')
  if (/^\/dashboard(?:\/(?:overview|models|flow))?\/?$/.test(pathname)) {
    return canUseProductFeature(user, 'product', 'use')
  }
  if (pathname === '/dashboard/users') {
    return canUseProductFeature(user, 'log', 'read')
  }
  if (/^\/usage-logs(?:\/(?:common|drawing|task))?\/?$/.test(pathname)) {
    return canUseProductFeature(user, 'product', 'use')
  }
  if (pathname === '/pricing') {
    return canUseProductFeature(user, 'product', 'use')
  }
  if (/^\/channels\/?$/.test(pathname)) {
    return canUseProductFeature(user, 'channel', 'read')
  }
  if (/^\/models(?:\/metadata)?\/?$/.test(pathname)) {
    return (
      canUseProductFeature(user, 'catalog', 'read') ||
      canUseProductFeature(user, 'channel', 'read')
    )
  }
  if (pathname === '/client-model-routes') {
    return canUseProductFeature(user, 'routing', 'read')
  }
  if (
    /^\/system-settings\/models\/(?:global|routing-reliability)\/?$/.test(
      pathname
    )
  ) {
    return canUseProductFeature(user, 'settings', 'read')
  }
  return false
}
