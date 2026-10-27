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
export function safeProductReturn(value: string | null | undefined): string {
  if (
    !value ||
    value === '/' ||
    !value.startsWith('/') ||
    value.startsWith('//')
  ) {
    return '/dashboard'
  }
  try {
    const decoded = decodeURIComponent(value)
    if (
      decoded.startsWith('//') ||
      decoded.includes('\\') ||
      [...decoded].some((character) => character.charCodeAt(0) < 32)
    ) {
      return '/dashboard'
    }
    const parsed = new URL(value, 'https://product.invalid')
    if (parsed.origin !== 'https://product.invalid') return '/dashboard'
    return parsed.pathname + parsed.search + parsed.hash
  } catch {
    return '/dashboard'
  }
}
export function productLoginURL(returnTo: string): string {
  return `/api/unihub/login?return_to=${encodeURIComponent(safeProductReturn(returnTo))}`
}

export function markProductSignedOut(): void {
  window.sessionStorage.setItem('unihub:signed-out', '1')
}
export function startProductLogin(returnTo: string): void {
  window.sessionStorage.removeItem('unihub:signed-out')
  window.location.assign(productLoginURL(returnTo))
}
