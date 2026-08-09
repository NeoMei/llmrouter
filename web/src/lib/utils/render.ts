import DOMPurify from 'dompurify'

export function sanitizeConfiguredHtml(content: string): string {
  if (typeof window !== 'undefined' && typeof DOMPurify.sanitize === 'function') {
    return DOMPurify.sanitize(content, {
      FORBID_TAGS: ['base', 'embed', 'iframe', 'link', 'meta', 'object', 'script'],
      FORBID_ATTR: ['srcdoc'],
    })
  }

  return content
    .replaceAll(/<\s*(script|iframe|object|embed|base|meta|link)\b[^>]*>[\s\S]*?<\s*\/\s*\1\s*>/gi, '')
    .replaceAll(/<\s*(script|iframe|object|embed|base|meta|link)\b[^>]*\/?>/gi, '')
    .replaceAll(/\s+on[a-z]+\s*=\s*(?:"[^"]*"|'[^']*'|[^\s>]+)/gi, '')
    .replaceAll(/\s+(href|src)\s*=\s*(["'])\s*javascript:[\s\S]*?\2/gi, '')
}
