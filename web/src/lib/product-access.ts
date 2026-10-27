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

// Effective product roles and capabilities have the same meaning for every
// authentication provider. The backend resolves their authoritative source.
export function canUseProductFeature(
  user: AuthUser | null | undefined,
  resource: string,
  action = 'read'
): boolean {
  if (!user) return false
  if (resource === 'product' && action === 'use') {
    return (
      user.role >= ROLE.USER &&
      user.permissions?.admin_permissions?.product?.use !== false
    )
  }
  if (user.role === ROLE.SUPER_ADMIN) return true
  const permissions = user.permissions?.admin_permissions
  if (permissions) return permissions[resource]?.[action] === true
  return user.role >= ROLE.ADMIN
}
