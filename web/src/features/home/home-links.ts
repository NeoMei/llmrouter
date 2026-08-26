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
export const HERO_DOCS_PATH = '/docs' as const

export type HeroPrimaryAction =
  | { readonly labelKey: 'Get API Key'; readonly to: '/keys' }
  | { readonly labelKey: 'Get Started'; readonly to: '/sign-up' }

export function getHeroPrimaryAction(
  isAuthenticated: boolean
): HeroPrimaryAction {
  if (isAuthenticated) {
    return {
      labelKey: 'Get API Key',
      to: '/keys',
    } as const
  }

  return {
    labelKey: 'Get Started',
    to: '/sign-up',
  } as const
}

export const SUPPORTED_CODING_APPS = [
  {
    id: 'claude-code',
    name: 'Claude Code',
    href: 'https://claude.com/product/claude-code',
  },
  {
    id: 'codex',
    name: 'Codex',
    href: 'https://openai.com/codex/',
  },
  {
    id: 'open-code',
    name: 'OpenCode',
    href: 'https://opencode.ai/',
  },
] as const
