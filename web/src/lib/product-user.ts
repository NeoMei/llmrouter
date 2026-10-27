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
import { useAuthStore, type AuthUser } from '@/stores/auth-store'

import { clearAuthentication, getAuthEpoch, getFreshAuthHeaders } from './auth-session'

// Product state always comes from the original self API. Authentication
// providers may differ, but effective roles and business fields do not.
export async function refreshProductUser(): Promise<void> {
  const headers = await getFreshAuthHeaders()
  const auth = useAuthStore.getState().auth
  if (!auth.user || !auth.session) return
  const epoch = getAuthEpoch()
  const response = await fetch('/api/user/self', {
    headers,
    credentials: 'same-origin',
    cache: 'no-store',
    redirect: 'error',
    signal: AbortSignal.timeout(10000),
  })
  const payload = response.ok ? await response.json() as { success?: boolean; data?: AuthUser } : null
  const current = useAuthStore.getState().auth
  if (epoch !== getAuthEpoch() || current.session?.sid !== auth.session.sid || current.user?.id !== auth.user.id) return
  if (!response.ok) {
    if (response.status === 401 || response.status === 403) clearAuthentication()
    throw new Error('Product user refresh unavailable')
  }
  const user = payload?.data
  if (!payload?.success || !user || user.id !== auth.user.id || typeof user.username !== 'string' || !Number.isInteger(user.role)) {
    throw new Error('Product user response rejected')
  }
  current.setUser(user)
}
