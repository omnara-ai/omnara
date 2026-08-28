export function safeReturnTo(value: string | null): string {
  if (!value) return '/'
  try {
    const target = new URL(value, window.location.origin)
    if (target.origin !== window.location.origin) return '/'
    const path = `${target.pathname}${target.search}${target.hash}`
    if (path.startsWith('/login')) return '/'
    return path
  } catch {
    return '/'
  }
}
