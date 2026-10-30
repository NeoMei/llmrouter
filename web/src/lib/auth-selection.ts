// The SID is a selector, not a credential. The server still requires the
// matching HttpOnly cookie and validates its refresh secret.
const selectionKey = 'new-api:auth:selected-sid:v1'
const recentKey = 'new-api:auth:recent-sids:v1'
const returnParameter = '__auth_session'
const validSID = (sid: string) => /^[A-Za-z0-9_-]{1,128}$/.test(sid)

export function listSelectableSessions(): string[] {
  if (typeof window === 'undefined') return []
  try {
    const value: unknown = JSON.parse(
      window.localStorage.getItem(recentKey) ?? '[]'
    )
    return Array.isArray(value)
      ? [
          ...new Set(
            value.filter(
              (sid): sid is string => typeof sid === 'string' && validSID(sid)
            )
          ),
        ].slice(0, 2)
      : []
  } catch {
    return []
  }
}

export function selectSession(sid: string, share = true): void {
  if (typeof window === 'undefined' || !validSID(sid)) return
  try {
    window.sessionStorage.setItem(selectionKey, sid)
  } catch {
    /* unavailable */
  }
  if (share) {
    try {
      const recent = [
        sid,
        ...listSelectableSessions().filter((item) => item !== sid),
      ].slice(0, 2)
      window.localStorage.setItem(selectionKey, sid)
      window.localStorage.setItem(recentKey, JSON.stringify(recent))
    } catch {
      /* unavailable */
    }
  }
}

export function readSelectedSession(): string | undefined {
  if (typeof window === 'undefined') return undefined
  try {
    const tab = window.sessionStorage.getItem(selectionKey)
    if (tab !== null) return validSID(tab) ? tab : undefined
  } catch {
    /* use last explicit browser selection */
  }
  try {
    const saved = window.localStorage.getItem(selectionKey)
    if (saved && validSID(saved)) {
      try {
        window.sessionStorage.setItem(selectionKey, saved)
      } catch {
        /* unavailable */
      }
      return saved
    }
    return undefined
  } catch {
    return undefined
  }
}

// The empty per-tab value is an explicit sign-out, not an unknown selector.
// Never let a bare refresh choose some other realm's remaining HttpOnly cookie.
export function isSessionSelectionCleared(): boolean {
  if (typeof window === 'undefined') return false
  try {
    return window.sessionStorage.getItem(selectionKey) === ''
  } catch {
    // Without per-tab storage we cannot distinguish an explicit sign-out
    // from a fresh tab, so do not silently restore another realm's cookie.
    return true
  }
}

export function clearSelectedSession(sid: string): void {
  if (typeof window === 'undefined' || !validSID(sid)) return
  try {
    if (window.sessionStorage.getItem(selectionKey) === sid) {
      // A signed-out tab must not silently adopt another tab's account.
      window.sessionStorage.setItem(selectionKey, '')
    }
  } catch {
    /* unavailable */
  }
  try {
    const recent = listSelectableSessions().filter((item) => item !== sid)
    window.localStorage.setItem(recentKey, JSON.stringify(recent))
    if (window.localStorage.getItem(selectionKey) === sid) {
      window.localStorage.removeItem(selectionKey)
    }
  } catch {
    /* unavailable */
  }
}

export function consumeReturnedSession(): string | undefined {
  if (typeof window === 'undefined') return undefined
  const url = new URL(window.location.href)
  const values = url.searchParams.getAll(returnParameter)
  if (values.length === 0) return undefined
  url.searchParams.delete(returnParameter)
  window.history.replaceState(
    window.history.state,
    '',
    `${url.pathname}${url.search}${url.hash}`
  )
  if (values.length !== 1 || !validSID(values[0])) return undefined
  selectSession(values[0])
  return values[0]
}
