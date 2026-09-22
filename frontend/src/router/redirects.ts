export function safePostLoginRedirect(raw: unknown): string | null {
  const value = Array.isArray(raw) ? raw[0] : raw
  if (typeof value !== 'string') return null
  if (!value.startsWith('/') || value.startsWith('//')) return null
  if (
    value === '/login' ||
    value.startsWith('/login?') ||
    value.startsWith('/login#') ||
    value.startsWith('/login/')
  ) {
    return null
  }
  // Agent Mode query values include principal-authorized project, lane and
  // delivery identities. A session-expiry redirect can be completed by a
  // different user while the old View is unmounted, so no live watcher can
  // scrub that URL. Return to the bare surface and let the new principal's
  // first response establish its own selection/filter vocabulary.
  try {
    const parsed = new URL(value, 'http://paimos.invalid')
    if (/^\/agent-mode\/?$/i.test(parsed.pathname)) return '/agent-mode'
  } catch {
    return null
  }
  return value
}

export function postLoginRedirectOrFallback(raw: unknown, fallback = '/'): string {
  return safePostLoginRedirect(raw) ?? fallback
}

// Keep restricted human navigation on the same internal UI, with a closed
// read-only route vocabulary. New pages require a deliberate reviewer audit.
export function reviewerRouteAllowed(path: string): boolean {
  return path === '/' || path === '/projects' || path === '/issues' ||
    /^\/projects\/[1-9]\d*(?:\/issues\/[1-9]\d*)?$/.test(path) ||
    /^\/issues\/[1-9]\d*$/.test(path)
}
