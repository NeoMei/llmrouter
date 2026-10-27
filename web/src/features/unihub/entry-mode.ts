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
export function shouldUseUnifiedEntry(
  config: { enabled?: boolean; native_access?: boolean } | null,
  pathname: string,
  search: string,
  user: { identity_source?: string } | null
): boolean {
  if (!config?.enabled) return !config?.native_access
  if (!config.native_access) return true
  if (user) return user.identity_source === 'unihub'
  if (
    pathname === '/sign-in' &&
    new URLSearchParams(search).get('native') === '1'
  ) {
    return false
  }
  if (
    ['/forgot-password', '/reset', '/user/reset', '/otp', '/oauth'].includes(
      pathname
    ) ||
    /^\/oauth\/[^/]+\/?$/.test(pathname)
  ) {
    return false
  }
  return true
}
