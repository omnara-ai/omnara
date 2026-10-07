const RELOAD_AT_KEY = 'omnara:stale-chunk-reload-at'
const RELOAD_COOLDOWN_MS = 60_000

const staleChunkMessages = [
  'Failed to fetch dynamically imported module',
  'error loading dynamically imported module',
  'Importing a module script failed',
  'Unable to preload CSS',
]

type StorageOwner = Pick<Window, 'sessionStorage'>

export function staleChunkReloadDue(error: Error, target: StorageOwner, now = Date.now()): boolean {
  if (!staleChunkMessages.some((message) => error.message.startsWith(message))) return false
  return now - Number(target.sessionStorage.getItem(RELOAD_AT_KEY)) >= RELOAD_COOLDOWN_MS
}

export function markStaleChunkReload(target: StorageOwner, now = Date.now()): void {
  target.sessionStorage.setItem(RELOAD_AT_KEY, String(now))
}
