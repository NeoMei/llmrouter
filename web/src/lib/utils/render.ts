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
import DOMPurify from 'dompurify'

export function sanitizeConfiguredHtml(content: string): string {
  if (
    typeof window !== 'undefined' &&
    typeof DOMPurify.sanitize === 'function'
  ) {
    return DOMPurify.sanitize(content, {
      FORBID_TAGS: [
        'base',
        'embed',
        'iframe',
        'link',
        'meta',
        'object',
        'script',
      ],
      FORBID_ATTR: ['srcdoc'],
    })
  }

  return content
    .replaceAll(
      /<\s*(script|iframe|object|embed|base|meta|link)\b[^>]*>[\s\S]*?<\s*\/\s*\1\s*>/gi,
      ''
    )
    .replaceAll(
      /<\s*(script|iframe|object|embed|base|meta|link)\b[^>]*\/?>/gi,
      ''
    )
    .replaceAll(/\s+on[a-z]+\s*=\s*(?:"[^"]*"|'[^']*'|[^\s>]+)/gi, '')
    .replaceAll(/\s+(href|src)\s*=\s*(["'])\s*javascript:[\s\S]*?\2/gi, '')
}
