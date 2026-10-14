const RELOAD_AT_KEY = 'omnara:stale-chunk-reload-at'
const RELOAD_COOLDOWN_MS = 60_000

const staleChunkMessages = [
  'Failed to fetch dynamically imported module',
  'error loading dynamically imported module',
  'Importing a module script failed',
  'Unable to preload CSS',
]

export function claimStaleChunkReload(
  error: Error,
  target: Pick<Window, 'sessionStorage'>,
  now = Date.now(),
): boolean {
  if (!staleChunkMessages.some((message) => error.message.startsWith(message))) return false
  try {
    const storage = target.sessionStorage
    if (now - Number(storage.getItem(RELOAD_AT_KEY)) < RELOAD_COOLDOWN_MS) return false
    storage.setItem(RELOAD_AT_KEY, String(now))
    return true
  } catch {
    return false
  }
}
